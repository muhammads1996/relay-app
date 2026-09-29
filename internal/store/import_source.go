package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// ErrImportSourceNotFound is returned when a retained import source ID is
// unknown to this workspace.
var ErrImportSourceNotFound = errors.New("import source not found")

// SaveImportSource retains original import bytes in the workspace database
// and returns an opaque ID that can be used to retrieve them later.
func (s *Store) SaveImportSource(source, report []byte) (string, error) {
	var rawID [16]byte
	if _, err := rand.Read(rawID[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(rawID[:])
	_, err := s.db.Exec(`INSERT INTO import_sources(id, source, report, created) VALUES(?, ?, ?, ?)`, id, source, report, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return "", err
	}
	return id, nil
}

// ImportSource retrieves original import bytes retained in this workspace.
func (s *Store) ImportSource(id string) ([]byte, error) {
	var source []byte
	err := s.db.QueryRow(`SELECT source FROM import_sources WHERE id = ?`, id).Scan(&source)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrImportSourceNotFound
	}
	return source, err
}
