package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSeedFromDirMalformedNestedRequestDoesNotLeavePartialCollection(t *testing.T) {
	s := open(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "collection.toml"), []byte("name = \"Import me\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "01-valid.req.toml"), []byte("name = \"Valid\"\nmethod = \"GET\"\nurl = \"https://example.test\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "outer", "inner")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "02-malformed.req.toml"), []byte("name = [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SeedFromDir(root); err == nil {
		t.Fatal("expected malformed nested request to fail")
	}
	empty, err := s.Empty()
	if err != nil {
		t.Fatal(err)
	}
	if !empty {
		collections, err := s.Collections()
		if err != nil {
			t.Fatal(err)
		}
		requests, reqErr := s.Requests(collections[0].ID)
		folders, folderErr := s.Folders(collections[0].ID)
		t.Fatalf("failed seed left partial state: collections=%d requests=%d (%v) folders=%d (%v)", len(collections), len(requests), reqErr, len(folders), folderErr)
	}
}

func TestSeedFromDirDatabaseFailureRollsBackAllRows(t *testing.T) {
	s := open(t)
	root := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "01-first.req.toml"), "name = \"First\"\nmethod = \"GET\"\nurl = \"https://example.test/first\"\n")
	write(filepath.Join(root, "nested", "02-fail.req.toml"), "name = \"Fail me\"\nmethod = \"GET\"\nurl = \"https://example.test/fail\"\n")
	if _, err := s.db.Exec(`CREATE TRIGGER fail_seed_request BEFORE INSERT ON requests WHEN NEW.name = 'Fail me' BEGIN SELECT RAISE(ABORT, 'forced seed failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SeedFromDir(root); err == nil {
		t.Fatal("expected database insert trigger to fail")
	}
	empty, err := s.Empty()
	if err != nil {
		t.Fatal(err)
	}
	if !empty {
		cols, err := s.Collections()
		if err != nil {
			t.Fatal(err)
		}
		requests, reqErr := s.Requests(cols[0].ID)
		folders, folderErr := s.Folders(cols[0].ID)
		t.Fatalf("failed transaction left partial state: collections=%d requests=%d (%v) folders=%d (%v)", len(cols), len(requests), reqErr, len(folders), folderErr)
	}
}
