package store

import (
	"database/sql"
	"errors"
)

// ImportSourceReport returns the retained parse report associated with an
// uploaded import source.
func (s *Store) ImportSourceReport(id string) ([]byte, error) {
	var report []byte
	err := s.db.QueryRow(`SELECT report FROM import_sources WHERE id = ?`, id).Scan(&report)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrImportSourceNotFound
	}
	return report, err
}
