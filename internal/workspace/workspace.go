// Package workspace loads and edits Relay's canonical file-backed definitions.
// SQLite remains a derived index/history store; definitions here are the source
// of truth once a workspace.toml marker exists.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/google/uuid"
	"github.com/muhaymien96/relay/internal/dsl"
)

const SchemaVersion = 2

var (
	ErrConflict     = errors.New("workspace definition changed on disk")
	ErrFutureSchema = errors.New("unsupported future workspace schema")
)

type Manifest struct {
	SchemaVersion int      `toml:"schema_version"`
	ID            string   `toml:"workspace_id"`
	Collections   []string `toml:"collections"`
	Hash          string   `toml:"-"`
}
type Collection struct {
	ID            string `toml:"id"`
	SchemaVersion int    `toml:"schema_version"`
	dsl.Config
	Path string `toml:"-"`
	Hash string `toml:"-"`
}
type Folder struct {
	ID            string `toml:"id"`
	SchemaVersion int    `toml:"schema_version"`
	dsl.Config
	CollectionID string `toml:"-"`
	Path         string `toml:"-"`
	Hash         string `toml:"-"`
}
type Request struct {
	ID            string `toml:"id"`
	SchemaVersion int    `toml:"schema_version"`
	dsl.Request
	CollectionID string `toml:"-"`
	FolderID     string `toml:"-"`
	Path         string `toml:"-"`
	Hash         string `toml:"-"`
}
type Environment struct {
	ID            string `toml:"id"`
	SchemaVersion int    `toml:"schema_version"`
	Name          string `toml:"name"`
	dsl.Environment
	Path         string `toml:"-"`
	Hash         string `toml:"-"`
	CollectionID string `toml:"-"`
}
type Workspace struct {
	Root         string
	Manifest     Manifest
	Collections  []Collection
	Folders      []Folder
	Requests     []Request
	Environments []Environment
	Legacy       bool
}

var locks sync.Map // canonical root -> *sync.Mutex

func lockFor(root string) *sync.Mutex {
	v, _ := locks.LoadOrStore(root, &sync.Mutex{})
	return v.(*sync.Mutex)
}
func NewID() string { return strings.ToLower(uuid.NewString()) }

// LegacyID gives a stable UUID for one pre-v2 SQLite integer identity. The
// caller should persist the integer-to-UUID pair in migration metadata so old
// API references remain resolvable across restore and re-import.
func LegacyID(kind string, oldID int64) string {
	return strings.ToLower(uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("relay:legacy:%s:%d", kind, oldID))).String())
}

// Open loads schema v2 when marked. An unmarked directory is read in legacy
// single-collection mode and never upgraded or rewritten by this function.
func Open(root string) (*Workspace, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := RecoverMigration(absolute); err != nil {
		return nil, err
	}
	if err := RecoverImports(absolute); err != nil {
		return nil, err
	}
	w := &Workspace{Root: absolute}
	marker := filepath.Join(absolute, "workspace.toml")
	if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
		return loadLegacy(w)
	} else if err != nil {
		return nil, err
	}
	if !within(absolute, marker) {
		return nil, fmt.Errorf("workspace manifest path escapes or traverses a symlink")
	}
	meta, err := toml.DecodeFile(marker, &w.Manifest)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", marker, err)
	}
	if err = validateMeta(meta, marker, w.Manifest.SchemaVersion); err != nil {
		return nil, err
	}
	manifestBytes, err := os.ReadFile(marker)
	if err != nil {
		return nil, err
	}
	w.Manifest.Hash = Hash(manifestBytes)
	if w.Manifest.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("%w: %d", ErrFutureSchema, w.Manifest.SchemaVersion)
	}
	if w.Manifest.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported workspace schema %d", w.Manifest.SchemaVersion)
	}
	if !validID(w.Manifest.ID) {
		return nil, fmt.Errorf("workspace.toml: invalid workspace_id %q", w.Manifest.ID)
	}
	seen := map[string]string{"workspace": w.Manifest.ID}
	for _, cid := range w.Manifest.Collections {
		if !validID(cid) {
			return nil, fmt.Errorf("workspace.toml: invalid collection ID %q", cid)
		}
		path, err := findCollection(absolute, cid)
		if err != nil {
			if restoreErr := restoreManifestCollection(absolute, cid); restoreErr != nil {
				return nil, restoreErr
			}
			path, err = findCollection(absolute, cid)
			if err != nil {
				return nil, err
			}
		}
		if !within(absolute, path) {
			return nil, fmt.Errorf("collection path escapes or traverses a symlink")
		}
		var c Collection
		meta, err := toml.DecodeFile(path, &c)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if err = validateMeta(meta, path, c.SchemaVersion); err != nil {
			return nil, err
		}
		if c.ID != cid {
			return nil, fmt.Errorf("%s: id does not match manifest collection %s", path, cid)
		}
		if err := addID(seen, c.ID, path); err != nil {
			return nil, err
		}
		c.Path = path
		rawCollection, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		c.Hash = Hash(rawCollection)
		w.Collections = append(w.Collections, c)
		base := filepath.Dir(path)
		folderByPath := map[string]string{}
		requestPaths := make([]string, 0)
		err = filepath.WalkDir(base, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("collection contains a symlink: %s", p)
			}
			if d.IsDir() {
				if p != base && (d.Name() == "environments" || strings.HasPrefix(d.Name(), ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(p, ".req.toml") {
				requestPaths = append(requestPaths, p)
			}
			if filepath.Base(p) == "folder.toml" {
				var f Folder
				meta, e := toml.DecodeFile(p, &f)
				if e != nil {
					return fmt.Errorf("%s: %w", p, e)
				}
				if e = validateMeta(meta, p, f.SchemaVersion); e != nil {
					return e
				}
				if !validID(f.ID) {
					return fmt.Errorf("%s: missing or invalid folder id %q", p, f.ID)
				}
				if e = addID(seen, f.ID, p); e != nil {
					return e
				}
				folderByPath[filepath.Dir(p)] = f.ID
				f.Path = p
				folderBytes, e := os.ReadFile(p)
				if e != nil {
					return e
				}
				f.Hash = Hash(folderBytes)
				f.CollectionID = cid
				w.Folders = append(w.Folders, f)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		loadedRequests, err := loadRequestFiles(requestPaths)
		if err != nil {
			return nil, err
		}
		for i := range loadedRequests {
			r := loadedRequests[i]
			r.CollectionID = cid
			if !validID(r.ID) {
				return nil, fmt.Errorf("%s: invalid id %q", r.Path, r.ID)
			}
			if err = addID(seen, r.ID, r.Path); err != nil {
				return nil, err
			}
			for d := filepath.Dir(r.Path); d != base && d != filepath.Dir(d); d = filepath.Dir(d) {
				if fid := folderByPath[d]; fid != "" {
					r.FolderID = fid
					break
				}
			}
			w.Requests = append(w.Requests, r)
		}
		for i := range w.Requests {
			if w.Requests[i].CollectionID != cid {
				continue
			}
			for d := filepath.Dir(w.Requests[i].Path); d != base && d != filepath.Dir(d); d = filepath.Dir(d) {
				if fid := folderByPath[d]; fid != "" {
					w.Requests[i].FolderID = fid
					break
				}
			}
		}
		envDir := filepath.Join(base, "environments")
		envs, _ := filepath.Glob(filepath.Join(envDir, "*.toml"))
		envNames := map[string]bool{}
		for _, p := range envs {
			if !within(absolute, p) {
				return nil, fmt.Errorf("environment path escapes or traverses a symlink")
			}
			var v Environment
			meta, err := toml.DecodeFile(p, &v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", p, err)
			}
			if err = validateMeta(meta, p, v.SchemaVersion); err != nil {
				return nil, err
			}
			if !validID(v.ID) {
				return nil, fmt.Errorf("%s: invalid id %q", p, v.ID)
			}
			if err = addID(seen, v.ID, p); err != nil {
				return nil, err
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				return nil, err
			}
			v.Path = p
			v.Hash = Hash(raw)
			v.Name = strings.TrimSuffix(filepath.Base(p), ".toml")
			if envNames[v.Name] {
				return nil, fmt.Errorf("duplicate environment name %q", v.Name)
			}
			envNames[v.Name] = true
			v.CollectionID = cid
			w.Environments = append(w.Environments, v)
		}
	}
	return w, nil
}

// loadRequestFiles does file I/O and TOML decoding concurrently, then returns
// results in the caller's traversal order so duplicate-ID diagnostics and UI
// ordering remain deterministic. Every request file is read and hashed on each
// Open; the worker pool changes latency only, not external-edit detection.
func loadRequestFiles(paths []string) ([]Request, error) {
	requests := make([]Request, len(paths))
	if len(paths) == 0 {
		return requests, nil
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if workers > len(paths) {
		workers = len(paths)
	}
	jobs := make(chan int)
	errs := make([]error, len(paths))
	var wg sync.WaitGroup
	wg.Add(workers)
	for n := 0; n < workers; n++ {
		go func() {
			defer wg.Done()
			for i := range jobs {
				p := paths[i]
				raw, err := os.ReadFile(p)
				if err == nil {
					var r Request
					var meta toml.MetaData
					meta, err = toml.Decode(string(raw), &r)
					if err == nil {
						err = validateMeta(meta, p, r.SchemaVersion)
					}
					if err == nil {
						r.Path = p
						r.CollectionID = ""
						r.Hash = Hash(raw)
						r.Request.Path = p
						requests[i] = r
					}
				}
				if err != nil {
					errs[i] = fmt.Errorf("%s: %w", p, err)
				}
			}
		}()
	}
	for i := range paths {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return requests, nil
}

func loadLegacy(w *Workspace) (*Workspace, error) {
	w.Legacy = true
	var c Collection
	c.ID = ""
	c.Name = filepath.Base(w.Root)
	c.Path = filepath.Join(w.Root, "collection.toml")
	if _, err := os.Stat(c.Path); err == nil {
		if _, err = toml.DecodeFile(c.Path, &c); err != nil {
			return nil, err
		}
	}
	w.Collections = []Collection{c}
	err := filepath.WalkDir(w.Root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			if p != w.Root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "environments") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".req.toml") {
			r, e := dsl.LoadRequest(p)
			if e != nil {
				return e
			}
			raw, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			w.Requests = append(w.Requests, Request{Request: *r, CollectionID: "", Path: p, Hash: Hash(raw)})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(w.Requests, func(i, j int) bool { return w.Requests[i].Path < w.Requests[j].Path })
	paths, _ := filepath.Glob(filepath.Join(w.Root, "environments", "*.toml"))
	for _, p := range paths {
		env, err := dsl.LoadEnvironment(p)
		if err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		w.Environments = append(w.Environments, Environment{Name: strings.TrimSuffix(filepath.Base(p), ".toml"), Environment: *env, Path: p, Hash: Hash(raw)})
	}
	return w, nil
}

func findCollection(root, id string) (string, error) {
	matches, _ := filepath.Glob(filepath.Join(root, "collections", "*--"+id, "collection.toml"))
	if len(matches) != 1 {
		return "", fmt.Errorf("collection %s: expected one collection.toml, found %d", id, len(matches))
	}
	return matches[0], nil
}

// A collection directory is moved to recoverable trash before workspace.toml
// is atomically updated. If the process stops between those operations, the
// manifest still references the collection; restore it before opening.
func restoreManifestCollection(root, id string) error {
	if !validID(id) {
		return fmt.Errorf("invalid collection id %q", id)
	}
	candidates, _ := filepath.Glob(filepath.Join(root, ".relay", "trash", "*--"+id))
	if len(candidates) == 0 {
		return fmt.Errorf("collection %s is listed in workspace.toml but missing from collections and recoverable trash", id)
	}
	if len(candidates) > 1 {
		return fmt.Errorf("collection %s has multiple recoverable trash copies", id)
	}
	source := candidates[0]
	if !within(root, source) {
		return fmt.Errorf("recoverable collection path escapes or traverses a symlink")
	}
	if _, err := os.Stat(filepath.Join(source, "collection.toml")); err != nil {
		return fmt.Errorf("recoverable collection %s is incomplete: %w", id, err)
	}
	base := filepath.Base(source)
	if marker := strings.Index(base, "-collection-"); marker >= 0 {
		base = base[marker+len("-collection-"):]
	}
	target := filepath.Join(root, "collections", base)
	if !within(root, target) {
		return fmt.Errorf("collection restore path escapes or traverses a symlink")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	return os.Rename(source, target)
}
func validID(id string) bool {
	u, e := uuid.Parse(id)
	return e == nil && strings.ToLower(id) == u.String()
}
func addID(seen map[string]string, id, path string) error {
	if prior, ok := seen[id]; ok {
		return fmt.Errorf("duplicate stable ID %s in %s and %s", id, prior, path)
	}
	seen[id] = path
	return nil
}
func Hash(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }

func validateMeta(meta toml.MetaData, path string, version int) error {
	if version > SchemaVersion {
		return fmt.Errorf("%s: %w: %d", path, ErrFutureSchema, version)
	}
	if len(meta.Undecoded()) > 0 {
		return fmt.Errorf("%s: unsupported fields %v; preserve the original before editing", path, meta.Undecoded())
	}
	return nil
}
