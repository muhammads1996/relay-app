package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muhaymien96/relay/internal/dsl"
)

func TestOpenLegacyDoesNotRewrite(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "a.req.toml")
	src := []byte("name = \"A\"\nurl = \"https://example.test\"\n")
	if err := os.WriteFile(p, src, 0600); err != nil {
		t.Fatal(err)
	}
	w, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	if !w.Legacy || len(w.Requests) != 1 || w.Requests[0].Hash != Hash(src) {
		t.Fatalf("unexpected legacy load: %+v", w)
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(src) {
		t.Fatal("legacy load rewrote source")
	}
}

func TestLoadRequestFilesPreservesInputOrderAndHashesDiskBytes(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "z.req.toml"), filepath.Join(dir, "a.req.toml")}
	contents := []string{
		"id = \"00000000-0000-4000-8000-000000000002\"\nname = \"Zed\"\nmethod = \"GET\"\nurl = \"https://example.test/z\"\n",
		"id = \"00000000-0000-4000-8000-000000000001\"\nname = \"Alpha\"\nmethod = \"GET\"\nurl = \"https://example.test/a\"\n",
	}
	for i, path := range paths {
		if err := os.WriteFile(path, []byte(contents[i]), 0600); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := loadRequestFiles(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != len(paths) {
		t.Fatalf("loaded %d requests, want %d", len(loaded), len(paths))
	}
	for i := range paths {
		if loaded[i].Path != paths[i] || loaded[i].Hash != Hash([]byte(contents[i])) || loaded[i].Name != []string{"Zed", "Alpha"}[i] {
			t.Fatalf("request %d lost input order or file hash: %+v", i, loaded[i])
		}
	}
}

func TestOpenRejectsFutureAndDuplicateIDs(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "workspace.toml"), []byte("schema_version = 99\nworkspace_id = \"00000000-0000-4000-8000-000000000001\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(d); !errors.Is(err, ErrFutureSchema) {
		t.Fatalf("future schema error = %v", err)
	}
	d = t.TempDir()
	id := "00000000-0000-4000-8000-000000000001"
	cid := "00000000-0000-4000-8000-000000000002"
	rid := "00000000-0000-4000-8000-000000000003"
	put := func(p, s string) {
		t.Helper()
		if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(p, []byte(s), 0600); e != nil {
			t.Fatal(e)
		}
	}
	put(filepath.Join(d, "workspace.toml"), "schema_version = 2\nworkspace_id = \""+id+"\"\ncollections = [\""+cid+"\"]\n")
	base := filepath.Join(d, "collections", "demo--"+cid)
	put(filepath.Join(base, "collection.toml"), "id = \""+cid+"\"\nname = \"Demo\"\n")
	put(filepath.Join(base, "a.req.toml"), "id = \""+rid+"\"\nname = \"A\"\nmethod = \"GET\"\nurl = \"https://example.test\"\n")
	put(filepath.Join(base, "b.req.toml"), "id = \""+rid+"\"\nname = \"B\"\nmethod = \"GET\"\nurl = \"https://example.test\"\n")
	if _, err := Open(d); err == nil || !strings.Contains(err.Error(), "duplicate stable ID") {
		t.Fatalf("duplicate id error = %v", err)
	}
}

func TestSaveRequestChecksHashAndKeepsBackup(t *testing.T) {
	d := t.TempDir()
	id := "00000000-0000-4000-8000-000000000001"
	path := filepath.Join(d, "collections", "c", "x.req.toml")
	initial := []byte("id = \"" + id + "\"\nname = \"Old\"\nmethod = \"GET\"\nurl = \"https://example.test\"\n")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, initial, 0600); err != nil {
		t.Fatal(err)
	}
	w := &Workspace{Root: d}
	r := Request{ID: id, Path: path, Request: dsl.Request{Name: "New", Method: "POST", URL: "https://example.test"}}
	if _, err := w.SaveRequest(r, "stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale write error = %v", err)
	}
	h, err := w.SaveRequest(r, Hash(initial))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if Hash(saved) != h || !strings.Contains(string(saved), "name = \"New\"") {
		t.Fatalf("saved data/hash mismatch: %s", saved)
	}
	backups, err := filepath.Glob(filepath.Join(d, ".relay", "backups", "*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup files %v, err %v", backups, err)
	}
	b, _ := os.ReadFile(backups[0])
	if string(b) != string(initial) {
		t.Fatal("backup did not preserve prior definition")
	}
}

func TestSaveEnvironmentNeverPersistsSecretValues(t *testing.T) {
	d := t.TempDir()
	e := Environment{ID: "00000000-0000-4000-8000-000000000001", Name: "dev", Path: filepath.Join(d, "e.toml"), Environment: dsl.Environment{Vars: map[string]string{"base": "x"}, Secrets: []string{"APITOKEN"}}}
	w := &Workspace{Root: d}
	if _, err := w.SaveEnvironment(e, ""); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(e.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret-value") {
		t.Fatal("secret value was written")
	}
	if !strings.Contains(string(b), "APITOKEN") {
		t.Fatalf("secret name missing: %s", b)
	}
}
