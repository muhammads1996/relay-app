package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrMigrationRecovered = errors.New("an interrupted workspace migration was recovered")

type migrationJournal struct {
	Stage       string `json:"stage"`
	Collections string `json:"collections"`
}

func journalPath(root string) string {
	return filepath.Join(root, ".relay", "transactions", "workspace-v2-migration.json")
}

func writeJournal(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create migration journal: %w", err)
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// RecoverMigration rolls back a publish interrupted before workspace.toml was
// committed. Generated collections are retained under .relay/recovery for review.
func RecoverMigration(root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	path := journalPath(root)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var j migrationJournal
	if err = json.Unmarshal(raw, &j); err != nil {
		return fmt.Errorf("read workspace migration journal: %w", err)
	}
	name := filepath.Base(root)
	expectedCollections := filepath.Join(root, "collections")
	stageAbs, err := filepath.Abs(j.Stage)
	if err != nil {
		return err
	}
	if filepath.Dir(stageAbs) != filepath.Dir(root) || !strings.HasPrefix(filepath.Base(stageAbs), "."+name+"-v2-stage-") || filepath.Clean(j.Collections) != filepath.Clean(expectedCollections) {
		return fmt.Errorf("unsafe workspace migration journal %s; preserve it and inspect manually", path)
	}
	marker := filepath.Join(root, "workspace.toml")
	if _, err = os.Stat(marker); err == nil {
		manifest, e := DecodeManifest(marker)
		if e != nil || !validID(manifest.ID) || manifest.SchemaVersion != SchemaVersion {
			return fmt.Errorf("migration marker is invalid; journal retained for recovery: %s", path)
		}
		_ = os.RemoveAll(stageAbs)
		if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err = os.Stat(expectedCollections); err == nil {
		recovery := filepath.Join(root, ".relay", "recovery", "migration-v2-"+time.Now().UTC().Format("20060102T150405.000000000Z"))
		if err = os.MkdirAll(filepath.Dir(recovery), 0700); err != nil {
			return err
		}
		if err = os.Mkdir(recovery, 0700); err != nil {
			return err
		}
		if err = os.Rename(expectedCollections, filepath.Join(recovery, "collections")); err != nil {
			return err
		}
		_ = os.RemoveAll(stageAbs)
		_ = os.Remove(path)
		return fmt.Errorf("%w: unpublished collections moved to %s; source database remains intact", ErrMigrationRecovered, recovery)
	} else if !os.IsNotExist(err) {
		return err
	}
	_ = os.RemoveAll(stageAbs)
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
