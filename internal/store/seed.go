package store

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/muhaymien96/relay/internal/dsl"
)

// SeedFromDir imports a file-based workspace (the .req.toml interchange
// format) into the store: the directory becomes one collection, immediate
// subdirectories become folders (deeper nesting flattens into "a/b" folder
// names), and environments/*.toml become environments.
func (s *Store) SeedFromDir(root string) (int64, error) {
	prepared, err := prepareSeed(root)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	col := prepared.collection
	res, err := tx.Exec(`INSERT INTO collections (name, headers, vars) VALUES (?, ?, ?)`, col.Name, j(col.Headers), j(col.Vars))
	if err != nil {
		return 0, err
	}
	colID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	folderIDs := make(map[string]int64, len(prepared.folders))
	for _, folder := range prepared.folders {
		res, err := tx.Exec(`INSERT INTO folders (collection_id, name, headers, vars) VALUES (?, ?, ?, ?)`, colID, folder.Name, j(folder.Headers), j(folder.Vars))
		if err != nil {
			return 0, err
		}
		folderIDs[folder.Name], err = res.LastInsertId()
		if err != nil {
			return 0, err
		}
	}
	for _, request := range prepared.requests {
		var folderID any
		if request.folder != "" {
			folderID = folderIDs[request.folder]
		}
		if _, err := tx.Exec(`INSERT INTO requests (collection_id, folder_id, name, method, url, spec, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			colID, folderID, request.spec.Name, request.spec.Method, request.spec.URL, j(request.spec), now()); err != nil {
			return 0, err
		}
	}
	for _, env := range prepared.environments {
		if _, err := tx.Exec(`INSERT INTO environments (name, vars, secrets) VALUES (?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET vars = excluded.vars, secrets = excluded.secrets`, env.Name, j(env.Vars), j(env.Secrets)); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return colID, nil
}

type preparedSeed struct {
	collection   *Collection
	folders      []*Folder
	requests     []preparedSeedRequest
	environments []Environment
}

type preparedSeedRequest struct {
	folder string
	spec   *dsl.Request
}

func prepareSeed(root string) (*preparedSeed, error) {
	prepared := &preparedSeed{collection: &Collection{Name: filepath.Base(root), Headers: map[string]string{}, Vars: map[string]string{}}}
	if cfg, err := dsl.LoadConfig(filepath.Join(root, "collection.toml")); err != nil {
		return nil, err
	} else if cfg != nil {
		if cfg.Name != "" {
			prepared.collection.Name = cfg.Name
		}
		if cfg.Headers != nil {
			prepared.collection.Headers = cfg.Headers
		}
		if cfg.Vars != nil {
			prepared.collection.Vars = cfg.Vars
		}
	}
	folders := map[string]*Folder{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "environments") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".req.toml") {
			return nil
		}
		req, err := dsl.LoadRequest(path)
		if err != nil {
			return err
		}
		if err := normalizeSpec(&Request{Spec: req}); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		if rel != "." {
			key := filepath.ToSlash(rel)
			f, ok := folders[key]
			if !ok {
				f = &Folder{Name: key, Headers: map[string]string{}, Vars: map[string]string{}}
				// Merge folder.toml configs along the nested path.
				cur := root
				for _, part := range strings.Split(rel, string(filepath.Separator)) {
					cur = filepath.Join(cur, part)
					if cfg, err := dsl.LoadConfig(filepath.Join(cur, "folder.toml")); err != nil {
						return err
					} else if cfg != nil {
						for k, v := range cfg.Headers {
							f.Headers[k] = v
						}
						for k, v := range cfg.Vars {
							f.Vars[k] = v
						}
					}
				}
				folders[key] = f
			}
			prepared.requests = append(prepared.requests, preparedSeedRequest{folder: key, spec: req})
		} else {
			prepared.requests = append(prepared.requests, preparedSeedRequest{spec: req})
		}
		req.Path = ""
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, folder := range folders {
		prepared.folders = append(prepared.folders, folder)
	}
	sort.Slice(prepared.folders, func(i, j int) bool { return prepared.folders[i].Name < prepared.folders[j].Name })
	envDir := filepath.Join(root, "environments")
	matches, err := filepath.Glob(filepath.Join(envDir, "*.toml"))
	if err != nil {
		return nil, err
	}
	for _, match := range matches {
		env, err := dsl.LoadEnvironment(match)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(filepath.Base(match), ".toml")
		if name == "" {
			return nil, fmt.Errorf("%s: environment needs a name", match)
		}
		prepared.environments = append(prepared.environments, Environment{Name: name, Vars: env.Vars, Secrets: env.Secrets})
	}
	return prepared, nil
}

// CollectionExportOptions narrows a file export. FilterRequests and
// FilterEnvironments distinguish "export none" from the zero-value behavior
// of exporting every request/environment.
type CollectionExportOptions struct {
	RequestIDs         []int64
	FilterRequests     bool
	EnvironmentNames   []string
	FilterEnvironments bool
}

// ExportCollectionDir writes a complete collection back out as the .req.toml
// interchange format, which is what the runner and the k6/Playwright/curl
// porters consume.
func (s *Store) ExportCollectionDir(collectionID int64, dir string) error {
	return s.ExportCollectionDirWithOptions(collectionID, dir, CollectionExportOptions{})
}

// ExportCollectionDirWithOptions writes a complete or selected collection.
// Preset headers attached at each level are flattened into that level's
// config, except secret-flagged ones, which never leave the store.
func (s *Store) ExportCollectionDirWithOptions(collectionID int64, dir string, opts CollectionExportOptions) error {
	col, err := s.Collection(collectionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	colHeaders := map[string]string{}
	presetHeaders, _, err := s.PresetHeadersFor(collectionID, nil)
	if err != nil {
		return err
	}
	if err := s.mergeNonSecret(colHeaders, presetHeaders, collectionID, nil); err != nil {
		return err
	}
	for k, v := range col.Headers {
		colHeaders[k] = v
	}
	if err := writeConfig(filepath.Join(dir, "collection.toml"), col.Name, colHeaders, col.Vars); err != nil {
		return err
	}

	requests, err := s.Requests(collectionID)
	if err != nil {
		return err
	}
	if opts.FilterRequests {
		wanted := make(map[int64]bool, len(opts.RequestIDs))
		for _, id := range opts.RequestIDs {
			wanted[id] = true
		}
		found := make(map[int64]bool, len(wanted))
		selected := requests[:0]
		for _, r := range requests {
			if wanted[r.ID] {
				selected = append(selected, r)
				found[r.ID] = true
			}
		}
		for _, id := range opts.RequestIDs {
			if !found[id] {
				return fmt.Errorf("request %d not found in collection %d", id, collectionID)
			}
		}
		requests = selected
	}

	folders, err := s.Folders(collectionID)
	if err != nil {
		return err
	}
	usedFolders := map[int64]bool{}
	if opts.FilterRequests {
		for _, r := range requests {
			if r.FolderID != nil {
				usedFolders[*r.FolderID] = true
			}
		}
	}
	folderDir := map[int64]string{}
	for _, f := range folders {
		if opts.FilterRequests && !usedFolders[f.ID] {
			continue
		}
		sub := filepath.Join(dir, slugPath(f.Name))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			return err
		}
		folderDir[f.ID] = sub
		fHeaders := map[string]string{}
		fid := f.ID
		pf, _, err := s.PresetHeadersFor(collectionID, &fid)
		if err != nil {
			return err
		}
		if err := s.mergeNonSecret(fHeaders, pf, collectionID, &fid); err != nil {
			return err
		}
		// PresetHeadersFor includes collection-level presets; drop the ones
		// already written at collection level.
		for k := range presetHeaders {
			if fHeaders[k] == presetHeaders[k] {
				delete(fHeaders, k)
			}
		}
		for k, v := range f.Headers {
			fHeaders[k] = v
		}
		if err := writeConfig(filepath.Join(sub, "folder.toml"), "", fHeaders, f.Vars); err != nil {
			return err
		}
	}

	counter := map[string]int{}
	for _, r := range requests {
		target := dir
		if r.FolderID != nil {
			if d, ok := folderDir[*r.FolderID]; ok {
				target = d
			}
		}
		counter[target]++
		name := fmt.Sprintf("%02d-%s.req.toml", counter[target], slug(r.Spec.Name))
		if err := os.WriteFile(filepath.Join(target, name), dsl.Marshal(r.Spec), 0o644); err != nil {
			return err
		}
	}

	envs, err := s.Environments()
	if err != nil {
		return err
	}
	if opts.FilterEnvironments {
		wanted := make(map[string]bool, len(opts.EnvironmentNames))
		for _, name := range opts.EnvironmentNames {
			wanted[name] = true
		}
		found := make(map[string]bool, len(wanted))
		selected := envs[:0]
		for _, e := range envs {
			if wanted[e.Name] {
				selected = append(selected, e)
				found[e.Name] = true
			}
		}
		for _, name := range opts.EnvironmentNames {
			if !found[name] {
				return fmt.Errorf("environment %q not found", name)
			}
		}
		envs = selected
	}
	if len(envs) > 0 {
		envDir := filepath.Join(dir, "environments")
		if err := os.MkdirAll(envDir, 0o755); err != nil {
			return err
		}
		for _, e := range envs {
			if err := writeEnvironment(filepath.Join(envDir, e.Name+".toml"), e); err != nil {
				return err
			}
		}
	}
	return nil
}

// mergeNonSecret copies preset headers into dst, skipping secret-flagged
// values so they never land in exported files.
func (s *Store) mergeNonSecret(dst, presetHeaders map[string]string, collectionID int64, folderID *int64) error {
	_, secretVals, err := s.PresetHeadersFor(collectionID, folderID)
	if err != nil {
		return err
	}
	secret := map[string]bool{}
	for _, v := range secretVals {
		secret[v] = true
	}
	for k, v := range presetHeaders {
		if !secret[v] {
			dst[k] = v
		}
	}
	return nil
}

func writeConfig(path, name string, headers, vars map[string]string) error {
	var b strings.Builder
	if name != "" {
		fmt.Fprintf(&b, "name = %q\n", name)
	}
	writeTable(&b, "headers", headers)
	writeTable(&b, "vars", vars)
	if b.Len() == 0 {
		return nil
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func writeEnvironment(path string, e Environment) error {
	return os.WriteFile(path, MarshalEnvironment(e), 0o644)
}

// MarshalEnvironment returns Relay's deterministic TOML representation of an
// environment. Secret names are included; secret values are never stored.
func MarshalEnvironment(e Environment) []byte {
	var b strings.Builder
	if len(e.Secrets) > 0 {
		names := append([]string(nil), e.Secrets...)
		sort.Strings(names)
		b.WriteString("secrets = [")
		for i, n := range names {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", n)
		}
		b.WriteString("]\n")
	}
	writeTable(&b, "vars", e.Vars)
	return []byte(b.String())
}

func writeTable(b *strings.Builder, table string, m map[string]string) {
	if len(m) == 0 {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(b, "\n[%s]\n", table)
	for _, k := range keys {
		fmt.Fprintf(b, "%q = %q\n", k, m[k])
	}
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = nonSlug.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "item"
	}
	return s
}

func slugPath(s string) string {
	parts := strings.Split(s, "/")
	for i, p := range parts {
		parts[i] = slug(p)
	}
	return filepath.Join(parts...)
}
