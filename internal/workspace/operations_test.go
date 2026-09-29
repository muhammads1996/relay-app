package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/muhaymien96/relay/internal/dsl"
)

func TestCanonicalCollectionFolderAndRequestOperations(t *testing.T) {
	root := t.TempDir()
	cid := "00000000-0000-4000-8000-000000000001"
	wid := "00000000-0000-4000-8000-000000000010"
	collectionDir := filepath.Join(root, "collections", "demo--"+cid)
	if err := os.MkdirAll(collectionDir, 0755); err != nil {
		t.Fatal(err)
	}
	put := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	put(filepath.Join(root, "workspace.toml"), "schema_version = 2\nworkspace_id = \""+wid+"\"\ncollections = [\""+cid+"\"]\n")
	put(filepath.Join(collectionDir, "collection.toml"), "id = \""+cid+"\"\nname = \"Demo\"\n")
	rid := "00000000-0000-4000-8000-000000000002"
	requestPath := filepath.Join(collectionDir, "01-get.req.toml")
	put(requestPath, "id = \""+rid+"\"\nname = \"Get\"\nmethod = \"GET\"\nurl = \"https://example.test\"\n")
	w, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	created, err := w.CreateCollection(Collection{ID: "00000000-0000-4000-8000-000000000003", Config: dsl.Config{Name: "Second"}}, w.Manifest.Hash)
	if err != nil {
		t.Fatal(err)
	}
	w, err = Open(root)
	if err != nil || len(w.Collections) != 2 {
		t.Fatalf("collections after create: %d err=%v", len(w.Collections), err)
	}
	if _, err = w.CreateCollection(Collection{ID: "00000000-0000-4000-8000-000000000004", Config: dsl.Config{Name: "Stale"}}, "stale"); err != ErrConflict {
		t.Fatalf("stale collection create = %v", err)
	}
	if err = w.DeleteCollection(created.ID, created.Hash, w.Manifest.Hash); err != nil {
		t.Fatal(err)
	}
	w, err = Open(root)
	if err != nil || len(w.Collections) != 1 {
		t.Fatalf("collections after delete: %d err=%v", len(w.Collections), err)
	}
	folder, err := w.CreateFolder(Folder{ID: "00000000-0000-4000-8000-000000000005", CollectionID: cid, Config: dsl.Config{Name: "nested"}})
	if err != nil {
		t.Fatal(err)
	}
	w, err = Open(root)
	if err != nil || len(w.Folders) != 1 {
		t.Fatalf("folders after create: %d err=%v", len(w.Folders), err)
	}
	folder = w.Folders[0]
	moved, err := w.MoveRequest(rid, requestPath, filepath.Dir(folder.Path), w.Requests[0].Hash)
	if err != nil {
		t.Fatal(err)
	}
	w, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Requests) != 1 || w.Requests[0].FolderID != folder.ID || w.Requests[0].Path != moved {
		t.Fatalf("request after move: %+v", w.Requests)
	}
	if _, err = w.SaveFolder(Folder{ID: folder.ID, CollectionID: cid, Config: dsl.Config{Name: "renamed", Headers: map[string]string{"X-Test": "1"}}}, folder.Hash); err != nil {
		t.Fatal(err)
	}
	w, err = Open(root)
	if err != nil || len(w.Requests) != 1 || w.Requests[0].FolderID != folder.ID {
		t.Fatalf("request after folder rename: %+v err=%v", w.Requests, err)
	}
	trash, err := w.DeleteFolder(folder.ID, w.Folders[0].Hash)
	if err != nil {
		t.Fatal(err)
	}
	w, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Requests) != 0 || len(w.Folders) != 0 {
		t.Fatalf("folder trash should remove subtree from active workspace: %+v", w)
	}
	if _, err = os.Stat(trash); err != nil {
		t.Fatalf("recoverable trash missing: %v", err)
	}
}

func TestOpenRestoresCollectionMovedBeforeManifestCommit(t *testing.T) {
	root := t.TempDir()
	id := "00000000-0000-4000-8000-000000000001"
	dir := filepath.Join(root, "collections", "demo--"+id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "workspace.toml"), []byte("schema_version = 2\nworkspace_id = \"00000000-0000-4000-8000-000000000010\"\ncollections = [\""+id+"\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "collection.toml"), []byte("id = \""+id+"\"\nname = \"Demo\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	trash := filepath.Join(root, ".relay", "trash", "20260929-collection-demo--"+id)
	if err := os.MkdirAll(filepath.Dir(trash), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, trash); err != nil {
		t.Fatal(err)
	}
	w, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Collections) != 1 {
		t.Fatalf("recovered collections %d", len(w.Collections))
	}
	if _, err = os.Stat(dir); err != nil {
		t.Fatalf("collection was not restored: %v", err)
	}
}
