package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MigrationSnapshot is a read-only view of authored data used by the explicit
// SQLite-to-files migration. It deliberately does not seed default test cases.
type MigrationSnapshot struct {
	Collections    []Collection
	Folders        map[int64][]Folder
	Requests       map[int64][]Request
	Environments   []Environment
	TestFolders    int
	TestCases      int
	TestSets       int
	TestExecutions int
	Presets        []Preset
}

func (s *Store) MigrationSnapshot() (*MigrationSnapshot, error) {
	out := &MigrationSnapshot{Folders: map[int64][]Folder{}, Requests: map[int64][]Request{}}
	var err error
	if out.Collections, err = s.Collections(); err != nil {
		return nil, err
	}
	for _, c := range out.Collections {
		if out.Folders[c.ID], err = s.Folders(c.ID); err != nil {
			return nil, err
		}
		if out.Requests[c.ID], err = s.Requests(c.ID); err != nil {
			return nil, err
		}
	}
	if out.Environments, err = s.Environments(); err != nil {
		return nil, err
	}
	if out.Presets, err = s.Presets(); err != nil {
		return nil, err
	}
	for table, dst := range map[string]*int{"test_folders": &out.TestFolders, "test_cases": &out.TestCases, "test_sets": &out.TestSets, "test_executions": &out.TestExecutions} {
		n, e := s.tableCountIfExists(table)
		if e != nil {
			return nil, e
		}
		*dst = n
	}
	return out, nil
}

func (s *Store) tableCountIfExists(table string) (int, error) {
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, nil
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// BackupTo uses SQLite's VACUUM INTO snapshot so committed WAL data is included.
// It refuses to overwrite an existing backup.
func (s *Store) BackupTo(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("backup already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := s.db.Ping(); err != nil {
		return err
	}
	quoted := strings.ReplaceAll(path, "'", "''")
	if _, err := s.db.Exec(`VACUUM INTO '` + quoted + `'`); err != nil {
		return fmt.Errorf("SQLite snapshot backup: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = f.Sync(); err != nil {
		return err
	}
	return nil
}
