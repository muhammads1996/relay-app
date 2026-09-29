package workspace

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/muhaymien96/relay/internal/dsl"
)

// SaveRequest atomically writes a v2 request. expectedHash is the hash captured
// when the editor opened the file; an empty hash means the target must not exist.
// The new content hash is returned only after the replacement succeeds.
func (w *Workspace) SaveRequest(r Request, expectedHash string) (string, error) {
	if w.Legacy {
		return "", fmt.Errorf("legacy workspace must be explicitly upgraded before editing canonical definitions")
	}
	if !validID(r.ID) {
		return "", fmt.Errorf("request has invalid stable id %q", r.ID)
	}
	if r.URL == "" {
		return "", fmt.Errorf("request needs a url")
	}
	if r.Method == "" {
		r.Method = "GET"
	}
	if r.Name == "" {
		r.Name = "Untitled"
	}
	path := r.Path
	if path == "" {
		return "", fmt.Errorf("request path is required")
	}
	if !within(w.Root, path) {
		return "", fmt.Errorf("request path is outside workspace")
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "id = %q\n", r.ID)
	b.Write(dsl.Marshal(&r.Request))
	return w.save(path, expectedHash, b.Bytes())
}

func (w *Workspace) NewRequestPath(collectionID, folderID, name string) (string, error) {
	if w.Legacy {
		return "", fmt.Errorf("legacy collections do not have stable workspace paths")
	}
	collectionPath, err := findCollection(w.Root, collectionID)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(collectionPath)
	if folderID != "" {
		found := false
		for _, f := range w.Folders {
			if f.ID == folderID && f.CollectionID == collectionID {
				dir = filepath.Dir(f.Path)
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("folder %s does not belong to collection %s", folderID, collectionID)
		}
	}
	base := slug(name)
	if base == "" {
		base = "request"
	}
	for n := 1; n < 10000; n++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%02d-%s.req.toml", n, base))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("could not allocate request filename")
}

func (w *Workspace) TrashRequest(id, path, expectedHash string) (string, error) {
	if w.Legacy {
		return "", fmt.Errorf("legacy collection deletion is not supported by the canonical workspace writer")
	}
	if !validID(id) || !within(w.Root, path) {
		return "", fmt.Errorf("invalid request identity or path")
	}
	mu := lockFor(w.Root)
	mu.Lock()
	defer mu.Unlock()
	current, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if Hash(current) != expectedHash {
		return "", ErrConflict
	}
	trashDir := filepath.Join(w.Root, ".relay", "trash")
	if err = os.MkdirAll(trashDir, 0700); err != nil {
		return "", err
	}
	target := filepath.Join(trashDir, time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+id+"-"+filepath.Base(path))
	if err = os.Rename(path, target); err != nil {
		return "", err
	}
	return target, nil
}

func (w *Workspace) TrashEnvironment(id, path, expectedHash string) (string, error) {
	if w.Legacy {
		return "", fmt.Errorf("legacy environment deletion is not supported by the canonical workspace writer")
	}
	if !validID(id) || !within(w.Root, path) {
		return "", fmt.Errorf("invalid environment identity or path")
	}
	mu := lockFor(w.Root)
	mu.Lock()
	defer mu.Unlock()
	current, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if Hash(current) != expectedHash {
		return "", ErrConflict
	}
	trashDir := filepath.Join(w.Root, ".relay", "trash")
	if err = os.MkdirAll(trashDir, 0700); err != nil {
		return "", err
	}
	target := filepath.Join(trashDir, time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+id+"-"+filepath.Base(path))
	if err = os.Rename(path, target); err != nil {
		return "", err
	}
	return target, nil
}

// SaveEnvironment atomically writes an environment and stores secret names only.
func (w *Workspace) SaveEnvironment(e Environment, expectedHash string) (string, error) {
	if w.Legacy {
		return "", fmt.Errorf("legacy workspace must be explicitly upgraded before editing canonical definitions")
	}
	if !validID(e.ID) {
		return "", fmt.Errorf("environment has invalid stable id %q", e.ID)
	}
	if e.Name == "" || strings.ContainsAny(e.Name, "/\\") || e.Name == "." || e.Name == ".." {
		return "", fmt.Errorf("invalid environment name")
	}
	path := e.Path
	if path == "" {
		if !validID(e.CollectionID) {
			return "", fmt.Errorf("environment collection id is required")
		}
		collectionPath, err := findCollection(w.Root, e.CollectionID)
		if err != nil {
			return "", err
		}
		path = filepath.Join(filepath.Dir(collectionPath), "environments", e.Name+".toml")
	}
	if !within(w.Root, path) {
		return "", fmt.Errorf("environment path is outside workspace")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "id = %q\nname = %q\n", e.ID, e.Name)
	if len(e.Secrets) > 0 {
		fmt.Fprintf(&b, "secrets = [")
		for i, s := range e.Secrets {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", s)
		}
		b.WriteString("]\n")
	}
	secretNames := map[string]bool{}
	for _, name := range e.Secrets {
		secretNames[strings.ToLower(name)] = true
	}
	vars := map[string]string{}
	for k, v := range e.Vars {
		if !secretNames[strings.ToLower(k)] {
			vars[k] = v
		}
	}
	writeMap(&b, "vars", vars)
	return w.save(path, expectedHash, []byte(b.String()))
}

func writeMap(b *strings.Builder, name string, m map[string]string) {
	if len(m) == 0 {
		return
	}
	fmt.Fprintf(b, "\n[%s]\n", name)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "%q = %q\n", k, m[k])
	}
}
func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// SafePath reports whether path stays inside root without traversing a symlink.
// Read paths use the same guard as writes so a replaced file cannot redirect
// a targeted workspace read outside its root.
func SafePath(root, path string) bool { return within(root, path) }

func within(root, path string) bool {
	a, e := filepath.Abs(path)
	if e != nil {
		return false
	}
	rootAbs, e := filepath.Abs(root)
	if e != nil {
		return false
	}
	rel, e := filepath.Rel(rootAbs, a)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	if info, e := os.Lstat(a); e == nil && info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	for cur := filepath.Dir(a); ; cur = filepath.Dir(cur) {
		if info, e := os.Lstat(cur); e == nil && info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if cur == rootAbs || filepath.Dir(cur) == cur {
			break
		}
	}
	return true
}

func (w *Workspace) save(path, expected string, data []byte) (string, error) {
	if !within(w.Root, path) {
		return "", fmt.Errorf("workspace path escapes or traverses a symlink")
	}
	mu := lockFor(w.Root)
	mu.Lock()
	defer mu.Unlock()
	current, err := os.ReadFile(path)
	if err == nil {
		if expected == "" || Hash(current) != expected {
			return "", ErrConflict
		}
	} else if !os.IsNotExist(err) {
		return "", err
	} else if expected != "" {
		return "", ErrConflict
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	// Keep a timestamped previous copy for recovery before replacing an existing file.
	if err == nil {
		backup := filepath.Join(w.Root, ".relay", "backups", time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+filepath.Base(path))
		if !within(w.Root, backup) {
			return "", fmt.Errorf("backup path escapes or traverses a symlink")
		}
		if e := os.MkdirAll(filepath.Dir(backup), 0o755); e != nil {
			return "", e
		}
		if e := os.WriteFile(backup, current, 0o600); e != nil {
			return "", fmt.Errorf("backup existing definition: %w", e)
		}
	}
	newHash := Hash(data)
	if e := atomicReplace(path, data); e != nil {
		return "", e
	}
	return newHash, nil
}

// DecodeManifest is useful to preview a marker without loading its contents.
func DecodeManifest(path string) (Manifest, error) {
	var m Manifest
	_, e := toml.DecodeFile(path, &m)
	return m, e
}
