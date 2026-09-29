package workspace

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// CreateCollection adds a collection directory, then publishes its stable ID
// through workspace.toml. A failed manifest write removes the uncommitted dir.
func (w *Workspace) CreateCollection(c Collection, expectedManifestHash string) (Collection, error) {
	if w.Legacy {
		return c, fmt.Errorf("legacy workspace must be explicitly upgraded")
	}
	if c.ID == "" {
		c.ID = NewID()
	}
	if !validID(c.ID) {
		return c, fmt.Errorf("invalid collection stable ID")
	}
	if strings.TrimSpace(c.Name) == "" {
		return c, fmt.Errorf("collection needs a name")
	}
	mu := lockFor(w.Root)
	mu.Lock()
	defer mu.Unlock()
	manifestPath := filepath.Join(w.Root, "workspace.toml")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return c, err
	}
	if Hash(raw) != expectedManifestHash {
		return c, ErrConflict
	}
	for _, id := range w.Manifest.Collections {
		if id == c.ID {
			return c, fmt.Errorf("collection already exists")
		}
	}
	dir := filepath.Join(w.Root, "collections", slug(c.Name)+"--"+c.ID)
	if !within(w.Root, dir) {
		return c, fmt.Errorf("collection path escapes or traverses a symlink")
	}
	if _, err = os.Lstat(dir); err == nil {
		return c, ErrConflict
	} else if !os.IsNotExist(err) {
		return c, err
	}
	if err = os.MkdirAll(dir, 0755); err != nil {
		return c, err
	}
	c.Path = filepath.Join(dir, "collection.toml")
	data, err := encodeCollection(c)
	if err != nil {
		os.RemoveAll(dir)
		return c, err
	}
	if err = atomicReplace(c.Path, data); err != nil {
		os.RemoveAll(dir)
		return c, err
	}
	manifest := w.Manifest
	manifest.Collections = append(append([]string(nil), w.Manifest.Collections...), c.ID)
	var b bytes.Buffer
	if err = toml.NewEncoder(&b).Encode(manifest); err != nil {
		os.RemoveAll(dir)
		return c, err
	}
	if err = writeBackup(w.Root, manifestPath, raw); err != nil {
		os.RemoveAll(dir)
		return c, err
	}
	if err = atomicReplace(manifestPath, b.Bytes()); err != nil {
		os.RemoveAll(dir)
		return c, err
	}
	c.Hash = Hash(data)
	return c, nil
}

func (w *Workspace) SaveCollection(c Collection, expectedHash string) (string, error) {
	if w.Legacy {
		return "", fmt.Errorf("legacy workspace must be explicitly upgraded")
	}
	if !validID(c.ID) || strings.TrimSpace(c.Name) == "" {
		return "", fmt.Errorf("collection identity and name are required")
	}
	path, err := findCollection(w.Root, c.ID)
	if err != nil {
		return "", err
	}
	if !within(w.Root, path) {
		return "", fmt.Errorf("collection path escapes or traverses a symlink")
	}
	data, err := encodeCollection(c)
	if err != nil {
		return "", err
	}
	return w.save(path, expectedHash, data)
}

func encodeCollection(c Collection) ([]byte, error) {
	var b bytes.Buffer
	err := toml.NewEncoder(&b).Encode(struct {
		ID      string            `toml:"id"`
		Schema  int               `toml:"schema_version"`
		Name    string            `toml:"name"`
		Headers map[string]string `toml:"headers,omitempty"`
		Vars    map[string]string `toml:"vars,omitempty"`
	}{c.ID, SchemaVersion, c.Name, c.Headers, c.Vars})
	return b.Bytes(), err
}

func (w *Workspace) DeleteCollection(id, expectedCollectionHash, expectedManifestHash string) error {
	if w.Legacy {
		return fmt.Errorf("legacy workspace must be explicitly upgraded")
	}
	if !validID(id) {
		return fmt.Errorf("invalid collection stable ID")
	}
	mu := lockFor(w.Root)
	mu.Lock()
	defer mu.Unlock()
	path, err := findCollection(w.Root, id)
	if err != nil {
		return err
	}
	if !within(w.Root, path) {
		return fmt.Errorf("collection path escapes or traverses a symlink")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrConflict
		}
		return err
	}
	if Hash(raw) != expectedCollectionHash {
		return ErrConflict
	}
	manifestPath := filepath.Join(w.Root, "workspace.toml")
	manifestRaw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	if Hash(manifestRaw) != expectedManifestHash {
		return ErrConflict
	}
	manifest := w.Manifest
	manifest.Collections = nil
	found := false
	for _, cid := range w.Manifest.Collections {
		if cid == id {
			found = true
			continue
		}
		manifest.Collections = append(manifest.Collections, cid)
	}
	if !found {
		return fmt.Errorf("collection is not in workspace manifest")
	}
	var b bytes.Buffer
	if err = toml.NewEncoder(&b).Encode(manifest); err != nil {
		return err
	}
	if err = writeBackup(w.Root, manifestPath, manifestRaw); err != nil {
		return err
	}
	src := filepath.Dir(path)
	trash := filepath.Join(w.Root, ".relay", "trash")
	if !within(w.Root, trash) {
		return fmt.Errorf("trash path escapes or traverses a symlink")
	}
	if err = os.MkdirAll(trash, 0700); err != nil {
		return err
	}
	dest := filepath.Join(trash, time.Now().UTC().Format("20060102T150405.000000000Z")+"-collection-"+filepath.Base(src))
	if err = os.Rename(src, dest); err != nil {
		return err
	}
	if err = atomicReplace(manifestPath, b.Bytes()); err != nil {
		_ = os.Rename(dest, src)
		return err
	}
	return nil
}

func (w *Workspace) CreateFolder(f Folder) (Folder, error) {
	if w.Legacy {
		return f, fmt.Errorf("legacy workspace must be explicitly upgraded")
	}
	if f.ID == "" {
		f.ID = NewID()
	}
	if !validID(f.ID) || strings.TrimSpace(f.Name) == "" {
		return f, fmt.Errorf("folder stable ID and name are required")
	}
	mu := lockFor(w.Root)
	mu.Lock()
	defer mu.Unlock()
	collectionPath, err := findCollection(w.Root, f.CollectionID)
	if err != nil {
		return f, err
	}
	dir, err := folderDirectory(filepath.Dir(collectionPath), f.Name)
	if err != nil {
		return f, err
	}
	if !within(w.Root, dir) {
		return f, fmt.Errorf("folder path escapes or traverses a symlink")
	}
	if _, err = os.Lstat(dir); err == nil {
		return f, ErrConflict
	} else if !os.IsNotExist(err) {
		return f, err
	}
	if err = os.MkdirAll(dir, 0755); err != nil {
		return f, err
	}
	f.Path = filepath.Join(dir, "folder.toml")
	data, err := encodeFolder(f)
	if err != nil {
		_ = os.RemoveAll(dir)
		return f, err
	}
	if err = atomicReplace(f.Path, data); err != nil {
		_ = os.RemoveAll(dir)
		return f, err
	}
	f.Hash = Hash(data)
	return f, nil
}

func (w *Workspace) RequestDirectory(collectionID, folderID string) (string, error) {
	if w.Legacy {
		return "", fmt.Errorf("legacy workspace has no stable collection paths")
	}
	path, err := findCollection(w.Root, collectionID)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if folderID != "" {
		for _, f := range w.Folders {
			if f.ID == folderID && f.CollectionID == collectionID {
				return filepath.Dir(f.Path), nil
			}
		}
		return "", fmt.Errorf("folder %s does not belong to collection %s", folderID, collectionID)
	}
	return dir, nil
}

func (w *Workspace) SaveFolder(f Folder, expectedHash string) (Folder, error) {
	if w.Legacy {
		return f, fmt.Errorf("legacy workspace must be explicitly upgraded")
	}
	if !validID(f.ID) || !validID(f.CollectionID) {
		return f, fmt.Errorf("folder and collection stable IDs are required")
	}
	mu := lockFor(w.Root)
	mu.Lock()
	defer mu.Unlock()
	var old Folder
	for _, item := range w.Folders {
		if item.ID == f.ID {
			old = item
			break
		}
	}
	if old.ID == "" {
		return f, fmt.Errorf("folder not found")
	}
	if !within(w.Root, old.Path) {
		return f, fmt.Errorf("folder path escapes or traverses a symlink")
	}
	if old.CollectionID != f.CollectionID {
		return f, fmt.Errorf("moving folders between collections is not supported")
	}
	raw, err := os.ReadFile(old.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return f, ErrConflict
		}
		return f, err
	}
	if Hash(raw) != expectedHash {
		return f, ErrConflict
	}
	collectionPath, err := findCollection(w.Root, f.CollectionID)
	if err != nil {
		return f, err
	}
	destDir, err := folderDirectory(filepath.Dir(collectionPath), f.Name)
	if err != nil {
		return f, err
	}
	srcDir := filepath.Dir(old.Path)
	f.Path = filepath.Join(destDir, "folder.toml")
	data, err := encodeFolder(f)
	if err != nil {
		return f, err
	}
	if filepath.Clean(srcDir) == filepath.Clean(destDir) {
		if err = writeBackup(w.Root, old.Path, raw); err != nil {
			return f, err
		}
		if err = atomicReplace(old.Path, data); err != nil {
			return f, err
		}
	} else {
		if _, err = os.Lstat(destDir); err == nil {
			return f, ErrConflict
		} else if !os.IsNotExist(err) {
			return f, err
		}
		if err = os.MkdirAll(filepath.Dir(destDir), 0755); err != nil {
			return f, err
		}
		if err = os.Rename(srcDir, destDir); err != nil {
			return f, err
		}
		if err = writeBackup(w.Root, f.Path, raw); err != nil {
			_ = os.Rename(destDir, srcDir)
			return f, err
		}
		if err = atomicReplace(f.Path, data); err != nil {
			_ = os.Rename(destDir, srcDir)
			return f, err
		}
	}
	f.Hash = Hash(data)
	return f, nil
}

func encodeFolder(f Folder) ([]byte, error) {
	var b bytes.Buffer
	err := toml.NewEncoder(&b).Encode(struct {
		ID      string            `toml:"id"`
		Schema  int               `toml:"schema_version"`
		Name    string            `toml:"name"`
		Headers map[string]string `toml:"headers,omitempty"`
		Vars    map[string]string `toml:"vars,omitempty"`
	}{f.ID, SchemaVersion, filepath.Base(filepath.Clean(f.Name)), f.Headers, f.Vars})
	return b.Bytes(), err
}

func (w *Workspace) DeleteFolder(id, expectedHash string) (string, error) {
	if w.Legacy {
		return "", fmt.Errorf("legacy workspace must be explicitly upgraded")
	}
	mu := lockFor(w.Root)
	mu.Lock()
	defer mu.Unlock()
	var folder *Folder
	for i := range w.Folders {
		if w.Folders[i].ID == id {
			folder = &w.Folders[i]
			break
		}
	}
	if folder == nil {
		return "", fmt.Errorf("folder not found")
	}
	if !within(w.Root, folder.Path) {
		return "", fmt.Errorf("folder path escapes or traverses a symlink")
	}
	raw, err := os.ReadFile(folder.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrConflict
		}
		return "", err
	}
	if Hash(raw) != expectedHash {
		return "", ErrConflict
	}
	src := filepath.Dir(folder.Path)
	trash := filepath.Join(w.Root, ".relay", "trash")
	if !within(w.Root, trash) {
		return "", fmt.Errorf("trash path escapes or traverses a symlink")
	}
	if err = os.MkdirAll(trash, 0700); err != nil {
		return "", err
	}
	dest := filepath.Join(trash, time.Now().UTC().Format("20060102T150405.000000000Z")+"-folder-"+id+"-"+filepath.Base(src))
	if err = os.Rename(src, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// MoveRequest renames the canonical request file atomically within this workspace.
// It preserves the complete TOML bytes and stable ID; the caller then updates only
// the derived SQLite collection/folder index.
func (w *Workspace) MoveRequest(id, source, targetDir, expectedHash string) (string, error) {
	if w.Legacy || !validID(id) || !within(w.Root, source) || !within(w.Root, targetDir) {
		return "", fmt.Errorf("invalid request move")
	}
	mu := lockFor(w.Root)
	mu.Lock()
	defer mu.Unlock()
	raw, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	if Hash(raw) != expectedHash {
		return "", ErrConflict
	}
	if err = os.MkdirAll(targetDir, 0755); err != nil {
		return "", err
	}
	dest := filepath.Join(targetDir, filepath.Base(source))
	if filepath.Clean(dest) == filepath.Clean(source) {
		return source, nil
	}
	if _, err = os.Lstat(dest); err == nil {
		return "", ErrConflict
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err = writeBackup(w.Root, source, raw); err != nil {
		return "", err
	}
	if err = os.Rename(source, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func folderDirectory(collectionRoot, name string) (string, error) {
	name = filepath.Clean(filepath.FromSlash(name))
	if name == "." || filepath.IsAbs(name) {
		return "", fmt.Errorf("invalid folder path")
	}
	dir := filepath.Join(collectionRoot, name)
	if !within(collectionRoot, dir) {
		return "", fmt.Errorf("folder path escapes collection")
	}
	return dir, nil
}
func writeBackup(root, path string, raw []byte) error {
	dir := filepath.Join(root, ".relay", "backups")
	if !within(root, dir) {
		return fmt.Errorf("backup path escapes or traverses a symlink")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	target := filepath.Join(dir, time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+filepath.Base(path))
	return os.WriteFile(target, raw, 0600)
}
