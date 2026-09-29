package store

import (
	"crypto/sha256"
	"encoding/hex"
)

func hashRequestContent(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// RequestContentHash returns a stable hash of the persisted request JSON.
func (s *Store) RequestContentHash(id int64) (string, error) {
	var payload string
	if err := s.db.QueryRow(`SELECT spec FROM requests WHERE id=?`, id).Scan(&payload); err != nil {
		return "", err
	}
	return hashRequestContent([]byte(payload)), nil
}
