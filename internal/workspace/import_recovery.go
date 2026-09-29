package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RecoverImports completes imports whose manifest commit succeeded or moves
// uncommitted published files to recovery for inspection. Original import
// sources remain in SQLite, so recovery never discards the uploaded source.
func RecoverImports(root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	journalDir := filepath.Join(root, ".relay", "transactions", "imports")
	entries, err := os.ReadDir(journalDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		journalPath := filepath.Join(journalDir, entry.Name())
		raw, err := os.ReadFile(journalPath)
		if err != nil {
			return err
		}
		var journal importJournal
		if err = json.Unmarshal(raw, &journal); err != nil {
			return fmt.Errorf("read import journal %s: %w", journalPath, err)
		}
		if !validID(journal.ID) || !validID(journal.Collection) || entry.Name() != journal.ID+".json" {
			return fmt.Errorf("invalid import journal identity in %s", journalPath)
		}
		expectedStage := filepath.Join(root, ".relay", "imports", journal.ID)
		if filepath.Clean(journal.Stage) != filepath.Join(expectedStage, filepath.Base(journal.Destination)) || !within(root, journal.Stage) {
			return fmt.Errorf("unsafe staged path in import journal %s", journalPath)
		}
		if filepath.Base(journal.Destination) != filepath.Base(journal.Stage) || !strings.HasSuffix(filepath.Base(journal.Destination), "--"+journal.Collection) || !within(root, journal.Destination) || filepath.Dir(filepath.Clean(journal.Destination)) != filepath.Join(root, "collections") {
			return fmt.Errorf("unsafe destination path in import journal %s", journalPath)
		}
		manifestPath := filepath.Join(root, "workspace.toml")
		manifest, err := DecodeManifest(manifestPath)
		if err != nil {
			return fmt.Errorf("read workspace marker during import recovery: %w", err)
		}
		committed := false
		for _, id := range manifest.Collections {
			if id == journal.Collection {
				committed = true
				break
			}
		}
		_, stageErr := os.Lstat(journal.Stage)
		_, destErr := os.Lstat(journal.Destination)
		stageExists := stageErr == nil
		destExists := destErr == nil
		if stageErr != nil && !os.IsNotExist(stageErr) {
			return stageErr
		}
		if destErr != nil && !os.IsNotExist(destErr) {
			return destErr
		}
		if committed {
			if !destExists && stageExists {
				if err = os.Rename(journal.Stage, journal.Destination); err != nil {
					return fmt.Errorf("finish committed import %s: %w", journal.Collection, err)
				}
				destExists = true
			}
			if !destExists {
				return fmt.Errorf("workspace marker references imported collection %s but neither stage nor destination exists", journal.Collection)
			}
			if stageExists {
				_ = os.RemoveAll(journal.Stage)
			}
		} else if destExists {
			recovery := filepath.Join(root, ".relay", "recovery", "import-"+journal.Collection+"-"+time.Now().UTC().Format("20060102T150405.000000000Z"))
			if !within(root, recovery) {
				return fmt.Errorf("unsafe import recovery path")
			}
			if err = os.MkdirAll(filepath.Dir(recovery), 0700); err != nil {
				return err
			}
			if err = os.Rename(journal.Destination, recovery); err != nil {
				return fmt.Errorf("preserve interrupted unpublished import: %w", err)
			}
		}
		_ = os.RemoveAll(expectedStage)
		if err = os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
