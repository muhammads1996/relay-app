package workspace

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestImportDirectoryStagesValidatesAndPublishesStableDefinitions(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "workspace.toml"), []byte("schema_version = 2\nworkspace_id = \"00000000-0000-4000-8000-000000000010\"\ncollections = []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	importRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(importRoot, "collection.toml"), []byte("name = \"Imported\"\n[vars]\nbaseUrl = \"https://example.test\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(importRoot, "Users"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(importRoot, "Users", "folder.toml"), []byte("name = \"Users\"\n[headers]\nX-Folder = \"yes\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"a.req.toml", "b.req.toml"} {
		content := "name = \"List users\"\nmethod = \"GET\"\nurl = \"{{baseUrl}}/users/" + string(rune('1'+i)) + "\"\n"
		if i == 0 {
			content += "\n[auth]\ntype = \"bearer\"\ntoken = \"{{apiToken}}\"\n"
		}
		if err := os.WriteFile(filepath.Join(importRoot, "Users", name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(importRoot, "environments"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(importRoot, "environments", "local.toml"), []byte("[vars]\nbaseUrl = \"https://example.test\"\napiToken = \"do-not-persist\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	originalMarker, err := os.ReadFile(filepath.Join(root, "workspace.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.ImportDirectory(importRoot, "stale"); err != ErrConflict {
		t.Fatalf("stale import error=%v", err)
	}
	marker, err := os.ReadFile(filepath.Join(root, "workspace.toml"))
	if err != nil || string(marker) != string(originalMarker) {
		t.Fatalf("stale import changed marker: %s err=%v", marker, err)
	}
	created, err := w.ImportDirectory(importRoot, Hash(originalMarker))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Collections) != 1 || len(opened.Folders) != 1 || len(opened.Requests) != 2 || len(opened.Environments) != 1 {
		t.Fatalf("published counts collections=%d folders=%d requests=%d envs=%d", len(opened.Collections), len(opened.Folders), len(opened.Requests), len(opened.Environments))
	}
	if opened.Collections[0].ID != created.ID || opened.Requests[0].FolderID != opened.Folders[0].ID || opened.Requests[1].FolderID != opened.Folders[0].ID {
		t.Fatalf("stable IDs and folder links: collection=%+v folders=%+v requests=%+v", opened.Collections, opened.Folders, opened.Requests)
	}
	if opened.Requests[0].ID == opened.Requests[1].ID || filepath.Base(opened.Requests[0].Path) == filepath.Base(opened.Requests[1].Path) {
		t.Fatal("duplicate names did not receive unique IDs and paths")
	}
	envBytes, err := os.ReadFile(opened.Environments[0].Path)
	if err != nil || strings.Contains(string(envBytes), "do-not-persist") || !strings.Contains(string(envBytes), "apiToken") {
		t.Fatalf("environment secret handling: %s err=%v", envBytes, err)
	}
	marker, err = os.ReadFile(filepath.Join(root, "workspace.toml"))
	if err != nil {
		t.Fatal(err)
	}
	current, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	concurrentID := "00000000-0000-4000-8000-000000000003"
	concurrentDir := filepath.Join(root, "collections", "concurrent--"+concurrentID)
	if err = os.MkdirAll(concurrentDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(concurrentDir, "collection.toml"), []byte("id = \""+concurrentID+"\"\nschema_version = 2\nname = \"Concurrent\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var changed Manifest
	if _, err = toml.Decode(string(marker), &changed); err != nil {
		t.Fatal(err)
	}
	changed.Collections = append(changed.Collections, concurrentID)
	var changedBytes bytes.Buffer
	if err = toml.NewEncoder(&changedBytes).Encode(changed); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "workspace.toml"), changedBytes.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = current.ImportDirectory(importRoot, Hash(marker)); err != ErrConflict {
		t.Fatalf("concurrent manifest change error=%v", err)
	}
	marker, err = os.ReadFile(filepath.Join(root, "workspace.toml"))
	if err != nil {
		t.Fatal(err)
	}
	current, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = current.ImportDirectory(importRoot, Hash(marker)); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate environment import error=%v", err)
	}
	markerAfter, err := os.ReadFile(filepath.Join(root, "workspace.toml"))
	if err != nil || string(markerAfter) != string(marker) {
		t.Fatalf("duplicate environment import changed manifest: %s err=%v", markerAfter, err)
	}
}

func TestRecoverImportsAroundManifestCommit(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncommitted", true: "committed"}[committed], func(t *testing.T) {
			root := t.TempDir()
			collectionID := "00000000-0000-4000-8000-000000000001"
			opID := "00000000-0000-4000-8000-000000000002"
			base := "imported--" + collectionID
			stage := filepath.Join(root, ".relay", "imports", opID, base)
			destination := filepath.Join(root, "collections", base)
			if err := os.MkdirAll(stage, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(stage, "collection.toml"), []byte("id = \""+collectionID+"\"\nschema_version = 2\nname = \"Imported\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(stage, destination); err != nil {
				t.Fatal(err)
			}
			collections := "[]"
			if committed {
				collections = "[\"" + collectionID + "\"]"
			}
			marker := "schema_version = 2\nworkspace_id = \"00000000-0000-4000-8000-000000000010\"\ncollections = " + collections + "\n"
			if err := os.WriteFile(filepath.Join(root, "workspace.toml"), []byte(marker), 0600); err != nil {
				t.Fatal(err)
			}
			journal := importJournal{ID: opID, Collection: collectionID, Stage: stage, Destination: destination}
			encoded, _ := json.Marshal(journal)
			path := importJournalPath(root, opID)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			if err := RecoverImports(root); err != nil {
				t.Fatal(err)
			}
			if committed {
				if _, err := os.Stat(filepath.Join(destination, "collection.toml")); err != nil {
					t.Fatalf("committed import not completed: %v", err)
				}
			} else {
				recovered, _ := filepath.Glob(filepath.Join(root, ".relay", "recovery", "import-"+collectionID+"-*"))
				if len(recovered) != 1 {
					t.Fatalf("uncommitted import not preserved: %v", recovered)
				}
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("journal remains: %v", err)
			}
		})
	}
}

func TestImportCredentialPreflightRejectsMixedTemplateValues(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "collection.toml"), []byte("name = \"Unsafe\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	request := "name = \"Unsafe auth\"\nmethod = \"GET\"\nurl = \"https://example.test\"\n\n[auth]\ntype = \"bearer\"\ntoken = \"Bearer literal-secret {{placeholder}}\"\n"
	if err := os.WriteFile(filepath.Join(root, "01-unsafe.req.toml"), []byte(request), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateImportDirectory(root); err == nil || !strings.Contains(err.Error(), "credential") {
		t.Fatalf("mixed literal/template credential error=%v", err)
	}
}
