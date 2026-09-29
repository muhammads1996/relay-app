package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafePathRejectsTraversalAndSymlinkFile(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "request.req.toml")
	if err := os.WriteFile(inside, []byte("name = \"inside\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !SafePath(root, inside) {
		t.Fatal("ordinary file inside root was rejected")
	}
	if SafePath(root, filepath.Join(root, "..", "outside.req.toml")) {
		t.Fatal("path traversal was accepted")
	}
	link := filepath.Join(root, "linked.req.toml")
	if err := os.Symlink(inside, link); err != nil {
		t.Skipf("host does not permit file symlinks: %v", err)
	}
	if SafePath(root, link) {
		t.Fatal("symlink file was accepted")
	}
}
