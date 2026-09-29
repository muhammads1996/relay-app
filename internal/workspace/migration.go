package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/google/uuid"
	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/store"
)

type MigrationPreview struct {
	Collections    int      `json:"collections"`
	Folders        int      `json:"folders"`
	Requests       int      `json:"requests"`
	Environments   int      `json:"environments"`
	TestFolders    int      `json:"testFolders"`
	TestCases      int      `json:"testCases"`
	TestSets       int      `json:"testSets"`
	TestExecutions int      `json:"testExecutions"`
	Presets        int      `json:"presets"`
	Blockers       []string `json:"blockers,omitempty"`
}

type MigrationResult struct {
	Preview   MigrationPreview
	Backup    string
	Workspace string
}

// PreviewSQLiteMigration is read-only. It deliberately reports test definitions
// and presets as blockers until their effective behavior has a file schema.
func PreviewSQLiteMigration(db *store.Store, root string) (MigrationPreview, error) {
	s, err := db.MigrationSnapshot()
	if err != nil {
		return MigrationPreview{}, err
	}
	p := MigrationPreview{Collections: len(s.Collections), Environments: len(s.Environments), TestFolders: s.TestFolders, TestCases: s.TestCases, TestSets: s.TestSets, TestExecutions: s.TestExecutions, Presets: len(s.Presets)}
	for _, c := range s.Collections {
		p.Folders += len(s.Folders[c.ID])
		p.Requests += len(s.Requests[c.ID])
	}
	if p.Collections == 0 {
		p.Blockers = append(p.Blockers, "database has no authored collections")
	}
	if sensitiveRequestPresent(s) {
		p.Blockers = append(p.Blockers, "request credentials or external body file references need explicit resolution before file export")
	}
	if hasPathCollisions(s) {
		p.Blockers = append(p.Blockers, "collection folder or environment names collide after safe path conversion")
	}
	if hasUnsafeEnvironmentNames(s) {
		p.Blockers = append(p.Blockers, "an environment name cannot be represented as a safe, behavior-preserving filename")
	}
	if hasDanglingFolder(s) {
		p.Blockers = append(p.Blockers, "one or more requests reference a missing folder")
	}
	if p.TestFolders+p.TestCases+p.TestSets+p.TestExecutions > 0 {
		p.Blockers = append(p.Blockers, "test folders, cases, sets, or executions are present; export is not yet supported; SQLite and the backup will remain unchanged")
	}
	if p.Presets > 0 {
		p.Blockers = append(p.Blockers, "presets and attachments are present; export is not yet supported; SQLite and the backup will remain unchanged")
	}
	if p.Collections > 1 && p.Environments > 0 {
		p.Blockers = append(p.Blockers, "environments are workspace-global in SQLite but collection-scoped in v2; multiple collections make this mapping ambiguous")
	}
	if _, err := os.Stat(filepath.Join(root, "workspace.toml")); err == nil {
		p.Blockers = append(p.Blockers, "workspace.toml already exists; refusing to migrate over canonical files")
	} else if !os.IsNotExist(err) {
		return p, err
	}
	if _, err := os.Stat(filepath.Join(root, "collections")); err == nil {
		p.Blockers = append(p.Blockers, "collections/ already exists; refusing to merge or overwrite definitions")
	} else if !os.IsNotExist(err) {
		return p, err
	}
	if _, err := os.Stat(journalPath(root)); err == nil {
		p.Blockers = append(p.Blockers, "an interrupted migration journal exists; run the workspace loader to recover it before previewing again")
	} else if !os.IsNotExist(err) {
		return p, err
	}
	return p, nil
}

// MigrateSQLite performs the explicit migration after a clean preview. It keeps
// the source database, snapshots it with SQLite VACUUM INTO, stages definitions
// on the same volume, validates them with Open, and writes workspace.toml last.
func MigrateSQLite(db *store.Store, root string) (MigrationResult, error) {
	if err := RecoverMigration(root); err != nil {
		return MigrationResult{}, err
	}
	preview, err := PreviewSQLiteMigration(db, root)
	if err != nil {
		return MigrationResult{}, err
	}
	result := MigrationResult{Preview: preview, Workspace: root}
	if len(preview.Blockers) > 0 {
		return result, fmt.Errorf("migration blocked: %s", strings.Join(preview.Blockers, "; "))
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return result, err
	}
	mu := lockFor(root)
	mu.Lock()
	defer mu.Unlock()
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	backup := filepath.Join(root, ".relay", "backups", "relay-"+stamp+".db")
	if err = db.BackupTo(backup); err != nil {
		return result, err
	}
	result.Backup = backup
	backupDB, err := store.Open(backup)
	if err != nil {
		return result, fmt.Errorf("open migration snapshot %s: %w", backup, err)
	}
	defer backupDB.Close()
	backupPreview, err := PreviewSQLiteMigration(backupDB, root)
	if err != nil {
		return result, err
	}
	if !samePreview(preview, backupPreview) {
		return result, fmt.Errorf("workspace changed between preview and backup; review a fresh preview (snapshot: %s)", backup)
	}
	if len(backupPreview.Blockers) > 0 {
		return result, fmt.Errorf("migration snapshot is blocked: %s", strings.Join(backupPreview.Blockers, "; "))
	}
	stage, err := os.MkdirTemp(filepath.Dir(root), "."+filepath.Base(root)+"-v2-stage-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(stage)
	snapshot, err := backupDB.MigrationSnapshot()
	if err != nil {
		return result, err
	}
	manifest, mapping, err := stageSnapshot(stage, snapshot, StableWorkspaceID(root))
	if err != nil {
		return result, err
	}
	var stagedMarker bytes.Buffer
	if err = toml.NewEncoder(&stagedMarker).Encode(manifest); err != nil {
		return result, err
	}
	if err = atomicReplace(filepath.Join(stage, "workspace.toml"), stagedMarker.Bytes()); err != nil {
		return result, err
	}
	validated, err := Open(stage)
	if err != nil {
		return result, fmt.Errorf("staged workspace validation failed: %w", err)
	}
	if len(validated.Requests) != preview.Requests || len(validated.Environments) != preview.Environments {
		return result, fmt.Errorf("staged validation count mismatch: got %d requests/%d environments", len(validated.Requests), len(validated.Environments))
	}
	metadata := filepath.Join(root, ".relay", "migration-v2-id-map.json")
	if err = os.MkdirAll(filepath.Dir(metadata), 0700); err != nil {
		return result, err
	}
	encoded, err := json.MarshalIndent(mapping, "", "  ")
	if err != nil {
		return result, err
	}
	if err = atomicReplace(metadata, append(encoded, '\n')); err != nil {
		return result, err
	}
	collectionsStage := filepath.Join(stage, "collections")
	collectionsDest := filepath.Join(root, "collections")
	journal := journalPath(root)
	if err = os.MkdirAll(filepath.Dir(journal), 0700); err != nil {
		return result, err
	}
	journalBytes, err := json.Marshal(migrationJournal{Stage: stage, Collections: collectionsDest})
	if err != nil {
		return result, err
	}
	if err = writeJournal(journal, journalBytes); err != nil {
		return result, err
	}
	if err = os.Rename(collectionsStage, collectionsDest); err != nil {
		_ = os.Remove(journal)
		return result, err
	}
	if err = atomicReplace(filepath.Join(root, "workspace.toml"), stagedMarker.Bytes()); err != nil {
		rollbackErr := os.Rename(collectionsDest, collectionsStage)
		if rollbackErr == nil {
			_ = os.Remove(journal)
			return result, err
		}
		return result, fmt.Errorf("publish workspace marker: %w; rollback deferred to workspace recovery: %v", err, rollbackErr)
	}
	_ = os.Remove(journal)
	return result, nil
}

func samePreview(a, b MigrationPreview) bool {
	return a.Collections == b.Collections && a.Folders == b.Folders && a.Requests == b.Requests && a.Environments == b.Environments && a.TestFolders == b.TestFolders && a.TestCases == b.TestCases && a.TestSets == b.TestSets && a.TestExecutions == b.TestExecutions && a.Presets == b.Presets && strings.Join(a.Blockers, "\x00") == strings.Join(b.Blockers, "\x00")
}

type mappedID struct {
	Kind  string `json:"kind"`
	OldID int64  `json:"oldId"`
	ID    string `json:"id"`
}

// StableWorkspaceID lets a retry after a crash before marker publication use
// the same workspace identity. It is based on the canonical workspace path.
func StableWorkspaceID(root string) string {
	abs, err := filepath.Abs(root)
	if err == nil {
		root = abs
	}
	return strings.ToLower(uuid.NewSHA1(uuid.NameSpaceURL, []byte("relay:workspace:"+strings.ToLower(filepath.Clean(root)))).String())
}

func sensitiveRequestPresent(s *store.MigrationSnapshot) bool {
	for _, c := range s.Collections {
		if hasSensitiveMap(c.Headers) || hasSensitiveMap(c.Vars) {
			return true
		}
		for _, f := range s.Folders[c.ID] {
			if hasSensitiveMap(f.Headers) || hasSensitiveMap(f.Vars) {
				return true
			}
		}
	}
	for _, requests := range s.Requests {
		for _, r := range requests {
			if r.Spec == nil {
				continue
			}
			spec := r.Spec
			if a := spec.Auth; a != nil && (a.Token != "" || a.Password != "" || a.Value != "") {
				return true
			}
			if hasSensitiveMap(spec.Headers) || hasSensitiveMap(spec.Vars) {
				return true
			}
			for k := range spec.Headers {
				switch strings.ToLower(strings.TrimSpace(k)) {
				case "authorization", "proxy-authorization", "x-api-key", "api-key", "x-auth-token":
					return true
				}
			}
			for _, entry := range spec.HeaderEntries {
				if !entry.Disabled && sensitiveHeaderName(entry.Key) && strings.TrimSpace(entry.Value) != "" {
					return true
				}
			}
			for _, entry := range spec.QueryEntries {
				if !entry.Disabled && sensitiveHeaderName(entry.Key) && strings.TrimSpace(entry.Value) != "" {
					return true
				}
			}
			if spec.Body != nil && spec.Body.File != "" {
				return true
			}
			if spec.Body != nil {
				for _, field := range spec.Body.FormData {
					if field.File != "" {
						return true
					}
				}
			}
		}
	}
	return false
}

func sensitiveHeaderName(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "authorization", "proxy-authorization", "x-api-key", "api-key", "x-auth-token", "cookie", "set-cookie":
		return true
	}
	return false
}

func hasSensitiveMap(values map[string]string) bool {
	for k := range values {
		name := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(k), "-", ""), "_", ""))
		for _, part := range []string{"token", "secret", "password", "passwd", "credential", "authorization", "apikey", "privatekey"} {
			if strings.Contains(name, part) {
				return true
			}
		}
	}
	return false
}

func hasPathCollisions(s *store.MigrationSnapshot) bool {
	for _, c := range s.Collections {
		seen := map[string]bool{}
		for _, f := range s.Folders[c.ID] {
			p := strings.ToLower(filepath.Clean(safeRelativeFolder(f.Name)))
			if seen[p] {
				return true
			}
			seen[p] = true
		}
	}
	if len(s.Collections) == 1 {
		seen := map[string]bool{}
		for _, e := range s.Environments {
			p := strings.ToLower(e.Name + ".toml")
			if seen[p] {
				return true
			}
			seen[p] = true
		}
	}
	return false
}

func hasUnsafeEnvironmentNames(s *store.MigrationSnapshot) bool {
	for _, e := range s.Environments {
		if !safeEnvironmentName(e.Name) {
			return true
		}
	}
	return false
}
func safeEnvironmentName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, "/\\:*?\"<>|\x00")
}

func hasDanglingFolder(s *store.MigrationSnapshot) bool {
	for _, c := range s.Collections {
		folders := map[int64]bool{}
		for _, f := range s.Folders[c.ID] {
			folders[f.ID] = true
		}
		for _, r := range s.Requests[c.ID] {
			if r.Spec == nil || r.FolderID != nil && !folders[*r.FolderID] {
				return true
			}
		}
	}
	return false
}

func stageSnapshot(stage string, s *store.MigrationSnapshot, workspaceID string) (Manifest, []mappedID, error) {
	manifest := Manifest{SchemaVersion: SchemaVersion, ID: workspaceID}
	mapping := []mappedID{{Kind: "workspace", OldID: 0, ID: manifest.ID}}
	mapID := func(kind string, old int64) string {
		id := LegacyID(kind, old)
		mapping = append(mapping, mappedID{Kind: kind, OldID: old, ID: id})
		return id
	}
	for _, c := range s.Collections {
		cid := mapID("collection", c.ID)
		manifest.Collections = append(manifest.Collections, cid)
		dir := filepath.Join(stage, "collections", slug(c.Name)+"--"+cid)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return manifest, nil, err
		}
		if err := writeTOML(filepath.Join(dir, "collection.toml"), struct {
			ID      string            `toml:"id"`
			Schema  int               `toml:"schema_version"`
			Name    string            `toml:"name"`
			Headers map[string]string `toml:"headers"`
			Vars    map[string]string `toml:"vars"`
		}{cid, SchemaVersion, c.Name, c.Headers, c.Vars}); err != nil {
			return manifest, nil, err
		}
		folderDirs := map[int64]string{}
		usedFolderPaths := map[string]int64{}
		for _, f := range s.Folders[c.ID] {
			fid := mapID("folder", f.ID)
			rel := safeRelativeFolder(f.Name)
			fd := filepath.Join(dir, rel)
			if prior, ok := usedFolderPaths[filepath.Clean(fd)]; ok && prior != f.ID {
				return manifest, nil, fmt.Errorf("folders %d and %d collide at %s after safe path conversion", prior, f.ID, rel)
			}
			usedFolderPaths[filepath.Clean(fd)] = f.ID
			if err := os.MkdirAll(fd, 0755); err != nil {
				return manifest, nil, err
			}
			folderDirs[f.ID] = fd
			if err := writeTOML(filepath.Join(fd, "folder.toml"), struct {
				ID      string            `toml:"id"`
				Schema  int               `toml:"schema_version"`
				Name    string            `toml:"name"`
				Headers map[string]string `toml:"headers"`
				Vars    map[string]string `toml:"vars"`
			}{fid, SchemaVersion, filepath.Base(rel), f.Headers, f.Vars}); err != nil {
				return manifest, nil, err
			}
		}
		counts := map[string]int{}
		for _, r := range s.Requests[c.ID] {
			if r.Spec == nil {
				return manifest, nil, fmt.Errorf("request %d has no definition", r.ID)
			}
			target := dir
			if r.FolderID != nil {
				var ok bool
				target, ok = folderDirs[*r.FolderID]
				if !ok {
					return manifest, nil, fmt.Errorf("request %d references missing folder %d", r.ID, *r.FolderID)
				}
			}
			counts[target]++
			rid := mapID("request", r.ID)
			name := fmt.Sprintf("%02d-%s.req.toml", counts[target], slug(r.Spec.Name))
			var b bytes.Buffer
			fmt.Fprintf(&b, "id = %q\n", rid)
			b.Write(dsl.Marshal(r.Spec))
			if err := os.WriteFile(filepath.Join(target, name), b.Bytes(), 0644); err != nil {
				return manifest, nil, err
			}
		}
		if len(s.Collections) == 1 {
			envDir := filepath.Join(dir, "environments")
			for _, e := range s.Environments {
				if err := os.MkdirAll(envDir, 0755); err != nil {
					return manifest, nil, err
				}
				eid := mapID("environment", e.ID)
				var b strings.Builder
				fmt.Fprintf(&b, "id = %q\nname = %q\n", eid, e.Name)
				writeStringList(&b, "secrets", e.Secrets)
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
				fileName := e.Name + ".toml"
				if err := os.WriteFile(filepath.Join(envDir, fileName), []byte(b.String()), 0644); err != nil {
					return manifest, nil, err
				}
			}
		}
	}
	return manifest, mapping, nil
}

func writeTOML(path string, value any) error {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(value); err != nil {
		return err
	}
	return os.WriteFile(path, b.Bytes(), 0644)
}
func writeStringList(b *strings.Builder, name string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(b, "%s = [", name)
	for i, v := range values {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", v)
	}
	b.WriteString("]\n")
}
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
func safeRelativeFolder(s string) string {
	parts := strings.Split(strings.ReplaceAll(s, "\\", "/"), "/")
	for i, p := range parts {
		parts[i] = slug(p)
		if parts[i] == "" {
			parts[i] = "folder"
		}
	}
	return filepath.Join(parts...)
}
