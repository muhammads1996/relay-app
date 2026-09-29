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
	"github.com/muhaymien96/relay/internal/dsl"
)

type importJournal struct {
	ID          string `json:"id"`
	Collection  string `json:"collection"`
	Stage       string `json:"stage"`
	Destination string `json:"destination"`
}

// ValidateImportDirectory performs the semantic and credential preflight used
// by bulk-import preview/commit without creating files in the destination.
func ValidateImportDirectory(sourceRoot string) error {
	source, err := Open(sourceRoot)
	if err != nil {
		return err
	}
	if !source.Legacy || len(source.Collections) != 1 {
		return fmt.Errorf("import source must be one unmarked collection directory")
	}
	if importContainsCredentials(source) {
		return fmt.Errorf("import contains literal credentials; replace them with environment secret references before importing")
	}
	folderConfigs, err := readImportFolderConfigs(sourceRoot)
	if err != nil {
		return err
	}
	for _, config := range folderConfigs {
		if hasSensitiveConfig(config.Headers, config.Vars) {
			return fmt.Errorf("import folder contains literal credentials; replace them with environment secret references before importing")
		}
	}
	return nil
}

// ImportDirectory converts an unmarked legacy collection into a stable-ID v2
// collection. It validates the staged tree, checks the manifest hash under the
// workspace lock, and uses a journal to recover the directory/manifest publish.
func (w *Workspace) ImportDirectory(sourceRoot, expectedManifestHash string) (Collection, error) {
	if w.Legacy {
		return Collection{}, fmt.Errorf("bulk imports require a marked schema v2 workspace")
	}
	source, err := Open(sourceRoot)
	if err != nil {
		return Collection{}, err
	}
	if err = ValidateImportDirectory(sourceRoot); err != nil {
		return Collection{}, err
	}
	folderConfigs, err := readImportFolderConfigs(sourceRoot)
	if err != nil {
		return Collection{}, err
	}

	root := w.Root
	opID := NewID()
	opRoot := filepath.Join(root, ".relay", "imports", opID)
	if !within(root, opRoot) {
		return Collection{}, fmt.Errorf("import staging path escapes or traverses a symlink")
	}
	if err = os.MkdirAll(opRoot, 0700); err != nil {
		return Collection{}, err
	}
	defer func() {
		// Preserve a transaction directory if its journal is still present.
		if _, e := os.Stat(importJournalPath(root, opID)); os.IsNotExist(e) {
			_ = os.RemoveAll(opRoot)
		}
	}()

	collection := Collection{ID: NewID(), Config: source.Collections[0].Config}
	if strings.TrimSpace(collection.Name) == "" {
		collection.Name = filepath.Base(sourceRoot)
	}
	collectionDirName := slug(collection.Name) + "--" + collection.ID
	stageDir := filepath.Join(opRoot, collectionDirName)
	if err = os.MkdirAll(stageDir, 0700); err != nil {
		return Collection{}, err
	}
	collection.Path = filepath.Join(stageDir, "collection.toml")
	collectionData, err := encodeCollection(collection)
	if err != nil {
		return Collection{}, err
	}
	if err = os.WriteFile(collection.Path, collectionData, 0600); err != nil {
		return Collection{}, err
	}

	folderIDs := map[string]string{}
	folderDirs := map[string]string{}
	ensureFolder := func(rel string) error {
		if rel == "." || rel == "" {
			return nil
		}
		parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
		current := ""
		for _, part := range parts {
			if part == ".." || filepath.IsAbs(part) {
				return fmt.Errorf("unsafe import folder path %q", rel)
			}
			if current == "" {
				current = part
			} else {
				current = filepath.Join(current, part)
			}
			if folderIDs[current] != "" {
				continue
			}
			id := NewID()
			dir := filepath.Join(stageDir, current)
			if !within(stageDir, dir) {
				return fmt.Errorf("import folder escapes collection")
			}
			if err := os.MkdirAll(dir, 0700); err != nil {
				return err
			}
			cfg := folderConfigs[filepath.Clean(current)]
			name := part
			if strings.TrimSpace(cfg.Name) != "" {
				name = cfg.Name
			}
			folder := Folder{ID: id, CollectionID: collection.ID, Config: dsl.Config{Name: name, Headers: cfg.Headers, Vars: cfg.Vars}}
			encoded, err := encodeFolder(folder)
			if err != nil {
				return err
			}
			if err = os.WriteFile(filepath.Join(dir, "folder.toml"), encoded, 0600); err != nil {
				return err
			}
			folderIDs[current], folderDirs[current] = id, dir
		}
		return nil
	}
	usedNames := map[string]int{}
	for i := range source.Requests {
		req := source.Requests[i]
		rel, e := filepath.Rel(sourceRoot, filepath.Dir(req.Path))
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return Collection{}, fmt.Errorf("request path is outside import source")
		}
		if e = ensureFolder(rel); e != nil {
			return Collection{}, e
		}
		dir := stageDir
		if rel != "." && rel != "" {
			dir = folderDirs[filepath.Clean(rel)]
		}
		stem := slug(req.Name)
		if stem == "" {
			stem = "request"
		}
		key := filepath.Clean(dir) + "\x00" + stem
		usedNames[key]++
		filename := fmt.Sprintf("%02d-%s.req.toml", usedNames[key], stem)
		request := Request{ID: NewID(), CollectionID: collection.ID, FolderID: folderIDs[filepath.Clean(rel)], Path: filepath.Join(dir, filename), Request: req.Request}
		var encoded bytes.Buffer
		fmt.Fprintf(&encoded, "id = %q\n", request.ID)
		encoded.Write(dsl.Marshal(&request.Request))
		if e = os.WriteFile(request.Path, encoded.Bytes(), 0600); e != nil {
			return Collection{}, e
		}
	}
	for _, env := range source.Environments {
		if !safeEnvironmentName(env.Name) {
			return Collection{}, fmt.Errorf("unsafe environment name %q", env.Name)
		}
		data, e := encodeImportedEnvironment(env)
		if e != nil {
			return Collection{}, e
		}
		envDir := filepath.Join(stageDir, "environments")
		if e = os.MkdirAll(envDir, 0700); e != nil {
			return Collection{}, e
		}
		if e = os.WriteFile(filepath.Join(envDir, env.Name+".toml"), data, 0600); e != nil {
			return Collection{}, e
		}
	}
	stageRoot := filepath.Join(opRoot, "validation")
	if err = os.MkdirAll(filepath.Join(stageRoot, "collections"), 0700); err != nil {
		return Collection{}, err
	}
	// Validation operates on a copy of the staged collection so the final
	// publish directory remains on the same volume and can be renamed atomically.
	validationCollection := filepath.Join(stageRoot, "collections", collectionDirName)
	if err = copyTree(stageDir, validationCollection); err != nil {
		return Collection{}, err
	}
	validationManifest := Manifest{SchemaVersion: SchemaVersion, ID: NewID(), Collections: []string{collection.ID}}
	var marker bytes.Buffer
	if err = toml.NewEncoder(&marker).Encode(validationManifest); err != nil {
		return Collection{}, err
	}
	if err = os.WriteFile(filepath.Join(stageRoot, "workspace.toml"), marker.Bytes(), 0600); err != nil {
		return Collection{}, err
	}
	validated, err := Open(stageRoot)
	if err != nil {
		return Collection{}, fmt.Errorf("import validation failed: %w", err)
	}
	if len(validated.Collections) != 1 || len(validated.Requests) != len(source.Requests) || len(validated.Environments) != len(source.Environments) {
		return Collection{}, fmt.Errorf("import validation count mismatch")
	}

	mu := lockFor(root)
	mu.Lock()
	defer mu.Unlock()
	manifestPath := filepath.Join(root, "workspace.toml")
	current, err := os.ReadFile(manifestPath)
	if err != nil {
		return Collection{}, err
	}
	if Hash(current) != expectedManifestHash {
		return Collection{}, ErrConflict
	}
	var manifest Manifest
	meta, err := toml.Decode(string(current), &manifest)
	if err != nil {
		return Collection{}, err
	}
	if err = validateMeta(meta, manifestPath, manifest.SchemaVersion); err != nil {
		return Collection{}, err
	}
	currentWorkspace, err := Open(root)
	if err != nil {
		return Collection{}, err
	}
	for _, incoming := range source.Environments {
		for _, existing := range currentWorkspace.Environments {
			if strings.EqualFold(incoming.Name, existing.Name) {
				return Collection{}, fmt.Errorf("environment %q already exists in this workspace; rename it before importing to avoid an index collision", incoming.Name)
			}
		}
	}
	latest, err := os.ReadFile(manifestPath)
	if err != nil {
		return Collection{}, err
	}
	if Hash(latest) != Hash(current) || Hash(latest) != expectedManifestHash {
		return Collection{}, ErrConflict
	}
	for _, id := range manifest.Collections {
		if id == collection.ID {
			return Collection{}, ErrConflict
		}
	}
	destination := filepath.Join(root, "collections", collectionDirName)
	if !within(root, stageDir) || !within(root, destination) {
		return Collection{}, fmt.Errorf("import publish path escapes or traverses a symlink")
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return Collection{}, err
	}
	if _, err = os.Lstat(destination); err == nil {
		return Collection{}, ErrConflict
	} else if !os.IsNotExist(err) {
		return Collection{}, err
	}
	manifest.Collections = append(manifest.Collections, collection.ID)
	var next bytes.Buffer
	if err = toml.NewEncoder(&next).Encode(manifest); err != nil {
		return Collection{}, err
	}
	backup := filepath.Join(root, ".relay", "backups", time.Now().UTC().Format("20060102T150405.000000000Z")+"-workspace.toml")
	if !within(root, backup) {
		return Collection{}, fmt.Errorf("manifest backup path escapes or traverses a symlink")
	}
	if err = os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
		return Collection{}, err
	}
	if err = os.WriteFile(backup, current, 0600); err != nil {
		return Collection{}, err
	}
	journal := importJournal{ID: opID, Collection: collection.ID, Stage: stageDir, Destination: destination}
	journalBytes, err := json.Marshal(journal)
	if err != nil {
		return Collection{}, err
	}
	journalPath := importJournalPath(root, opID)
	if err = os.MkdirAll(filepath.Dir(journalPath), 0700); err != nil {
		return Collection{}, err
	}
	if err = writeJournal(journalPath, journalBytes); err != nil {
		return Collection{}, err
	}
	if err = os.Rename(stageDir, destination); err != nil {
		_ = os.Remove(journalPath)
		return Collection{}, err
	}
	if err = atomicReplace(manifestPath, next.Bytes()); err != nil {
		if rollbackErr := os.Rename(destination, stageDir); rollbackErr == nil {
			_ = os.Remove(journalPath)
			return Collection{}, err
		}
		return Collection{}, fmt.Errorf("publish manifest failed; import journal retained for recovery: %w", err)
	}
	if err = os.Remove(journalPath); err != nil {
		return Collection{}, fmt.Errorf("import committed but journal cleanup failed: %w", err)
	}
	_ = os.RemoveAll(opRoot)
	collection.Path = filepath.Join(destination, "collection.toml")
	collection.Hash = Hash(collectionData)
	return collection, nil
}

func importJournalPath(root, id string) string {
	return filepath.Join(root, ".relay", "transactions", "imports", id+".json")
}

func readImportFolderConfigs(root string) (map[string]dsl.Config, error) {
	configs := map[string]dsl.Config{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "environments") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Base(path) != "folder.toml" {
			return nil
		}
		var cfg dsl.Config
		if _, err := toml.DecodeFile(path, &cfg); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		configs[filepath.Clean(rel)] = cfg
		return nil
	})
	return configs, err
}

func encodeImportedEnvironment(env Environment) ([]byte, error) {
	var output strings.Builder
	fmt.Fprintf(&output, "id = %q\nname = %q\n", NewID(), env.Name)
	secrets := append([]string(nil), env.Secrets...)
	vars := make(map[string]string, len(env.Vars))
	for key, value := range env.Vars {
		if isSecretName(key) {
			secrets = append(secrets, key)
			continue
		}
		vars[key] = value
	}
	if len(secrets) > 0 {
		fmt.Fprintf(&output, "secrets = [")
		for i, secret := range secrets {
			if i > 0 {
				output.WriteString(", ")
			}
			fmt.Fprintf(&output, "%q", secret)
		}
		output.WriteString("]\n")
	}
	writeMap(&output, "vars", vars)
	return []byte(output.String()), nil
}

func isSecretName(name string) bool {
	name = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(name, "-", ""), "_", ""))
	for _, part := range []string{"token", "secret", "password", "passwd", "credential", "apikey", "privatekey"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}

func importContainsCredentials(source *Workspace) bool {
	if hasSensitiveConfig(source.Collections[0].Headers, source.Collections[0].Vars) {
		return true
	}
	for _, request := range source.Requests {
		spec := &request.Request
		if spec.Auth != nil && ((spec.Auth.Token != "" && !looksTemplated(spec.Auth.Token)) || (spec.Auth.Password != "" && !looksTemplated(spec.Auth.Password)) || (spec.Auth.Value != "" && !looksTemplated(spec.Auth.Value))) {
			return true
		}
		if hasSensitiveConfig(spec.Headers, spec.Vars) {
			return true
		}
		if hasSensitiveConfig(nil, spec.Query) {
			return true
		}
		for _, entry := range spec.HeaderEntries {
			if !entry.Disabled && sensitiveHeaderName(entry.Key) && entry.Value != "" && !looksTemplated(entry.Value) {
				return true
			}
		}
		for _, entry := range spec.QueryEntries {
			if !entry.Disabled && sensitiveHeaderName(entry.Key) && entry.Value != "" && !looksTemplated(entry.Value) {
				return true
			}
		}
	}
	return false
}

func hasSensitiveConfig(headers, vars map[string]string) bool {
	for key, value := range headers {
		if (sensitiveHeaderName(key) || isSecretName(key)) && value != "" && !looksTemplated(value) {
			return true
		}
	}
	for key, value := range vars {
		if isSecretName(key) && value != "" && !looksTemplated(value) {
			return true
		}
	}
	return false
}

func looksTemplated(value string) bool {
	value = strings.TrimSpace(value)
	if isTemplateReference(value) {
		return true
	}
	parts := strings.Fields(value)
	return len(parts) == 2 && isTemplateReference(parts[1])
}

func isTemplateReference(value string) bool {
	if !strings.HasPrefix(value, "{{") || !strings.HasSuffix(value, "}}") || strings.Count(value, "{{") != 1 || strings.Count(value, "}}") != 1 {
		return false
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "{{"), "}}")) != ""
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported non-regular import file %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	})
}
