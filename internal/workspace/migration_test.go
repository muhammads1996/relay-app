package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/store"
)

func TestMigrateSQLiteExplicitlySnapshotsAndWritesStableDefinitions(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := &store.Collection{Name: "Demo", Headers: map[string]string{"Accept": "application/json"}, Vars: map[string]string{"host": "https://example.test"}}
	if err := db.CreateCollection(c); err != nil {
		t.Fatal(err)
	}
	f := &store.Folder{CollectionID: c.ID, Name: "nested/smoke", Headers: map[string]string{"X-Scope": "folder"}}
	if err := db.CreateFolder(f); err != nil {
		t.Fatal(err)
	}
	request := &store.Request{CollectionID: c.ID, FolderID: &f.ID, Spec: &dsl.Request{Name: "Health check", Method: "GET", URL: "{{host}}/health"}}
	if err := db.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertEnvironment(&store.Environment{Name: "dev", Vars: map[string]string{"host": "https://dev.test", "token": "must-not-write"}, Secrets: []string{"token"}}); err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewSQLiteMigration(db, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Blockers) != 0 || preview.Requests != 1 || preview.Folders != 1 {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	result, err := MigrateSQLite(db, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(result.Backup); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	w, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Collections) != 1 || len(w.Requests) != 1 || len(w.Environments) != 1 {
		t.Fatalf("migrated counts: collections=%d requests=%d environments=%d", len(w.Collections), len(w.Requests), len(w.Environments))
	}
	if w.Requests[0].ID != LegacyID("request", request.ID) || w.Requests[0].CollectionID != LegacyID("collection", c.ID) {
		t.Fatalf("unstable request mapping: %+v", w.Requests[0])
	}
	if w.Requests[0].FolderID != LegacyID("folder", f.ID) {
		t.Fatalf("folder link not preserved: %q", w.Requests[0].FolderID)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(w.Requests[0].Path), "folder.toml")); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(w.Environments[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(env) == "" || contains(string(env), "must-not-write") {
		t.Fatalf("environment secret leaked: %s", env)
	}
	if LegacyID("request", request.ID) != LegacyID("request", request.ID) {
		t.Fatal("legacy mapping is not deterministic")
	}
}

func TestSQLiteMigrationPreviewBlocksTestDataWithoutChangingWorkspace(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := &store.Collection{Name: "Demo"}
	if err := db.CreateCollection(c); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTestFolder(&store.TestFolder{Name: "QA"}); err != nil {
		t.Fatal(err)
	}
	p, err := PreviewSQLiteMigration(db, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Blockers) == 0 {
		t.Fatal("test data should block unsupported migration")
	}
	if _, err := MigrateSQLite(db, root); err == nil {
		t.Fatal("migration unexpectedly committed")
	}
	if _, err := os.Stat(filepath.Join(root, "workspace.toml")); !os.IsNotExist(err) {
		t.Fatalf("blocked migration wrote workspace marker: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
