package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/muhaymien96/relay/internal/secretstore"
)

// MigrateLegacyXrayCredentials explicitly moves the legacy plaintext SQLite
// record to the configured secret store. It requires a new backup path, makes
// a consistent full-database snapshot with VACUUM INTO, writes the secure-
// store value if absent, verifies it, then removes the plaintext row
// transactionally. An existing vault value is retained: the backup preserves
// the old SQLite value even when the user has since saved newer credentials.
// Nothing is removed if backup or vault verification fails. The backup remains
// available after success for recovery.
func (s *Store) MigrateLegacyXrayCredentials(backupPath string) error {
	if s.secretStore == nil {
		return fmt.Errorf("configure an external secret store before migrating credentials")
	}
	if strings.TrimSpace(backupPath) == "" {
		return fmt.Errorf("a database backup path is required")
	}
	if err := s.ensureTestManagement(); err != nil {
		return err
	}
	creds, err := s.legacyXrayCredentials()
	if err != nil {
		return err
	}
	if creds == (XrayCredentials{}) {
		return fmt.Errorf("no legacy Xray credentials to migrate")
	}
	key, err := s.xrayCredentialSecretKey()
	if err != nil {
		return err
	}
	existing, err := s.secretStore.Get(key)
	vaultAlreadyPresent := err == nil
	vaultCreds := creds
	if vaultAlreadyPresent {
		vaultCreds, err = unmarshalXrayCredentials(existing)
		if err != nil {
			return fmt.Errorf("decoding existing external Xray credentials: %w", err)
		}
	} else if !errors.Is(err, secretstore.ErrNotFound) {
		return fmt.Errorf("checking external credentials: %w", err)
	}

	absBackup, err := filepath.Abs(backupPath)
	if err != nil {
		return fmt.Errorf("resolving backup path: %w", err)
	}
	if _, err := os.Lstat(absBackup); err == nil {
		return fmt.Errorf("backup path already exists: %s", absBackup)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking backup path %s: %w", absBackup, err)
	}
	dbPath, err := s.databasePath()
	if err != nil {
		return err
	}
	if dbPath == "" || dbPath == ":memory:" {
		return fmt.Errorf("cannot make a file backup of an in-memory database")
	}
	absDB, err := filepath.Abs(dbPath)
	if err != nil {
		return fmt.Errorf("resolving database path: %w", err)
	}
	if same, err := samePath(absDB, absBackup); err != nil {
		return err
	} else if same {
		return fmt.Errorf("backup path must differ from the live database path")
	}
	if err := s.vacuumBackup(absBackup); err != nil {
		return fmt.Errorf("creating credential migration backup %s: %w", absBackup, err)
	}

	if !vaultAlreadyPresent {
		data, err := jsonMarshalXrayCredentials(creds)
		if err == nil {
			err = s.secretStore.Set(key, data)
		}
		if err != nil {
			return fmt.Errorf("credential migration stopped; plaintext remains in SQLite; backup: %s: %w", absBackup, err)
		}
	}
	check, err := s.secretStore.Get(key)
	if err == nil {
		var verified XrayCredentials
		verified, err = unmarshalXrayCredentials(check)
		if err == nil && verified != vaultCreds {
			err = fmt.Errorf("secure-store readback did not match expected value")
		}
	}
	if err != nil {
		return fmt.Errorf("credential migration stopped; plaintext remains in SQLite; backup: %s: secure-store verification failed: %w", absBackup, err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("secure credentials verified but SQLite cleanup could not begin; plaintext remains; backup: %s: %w", absBackup, err)
	}
	if _, err := tx.Exec(`DELETE FROM xray_credentials WHERE id = 1`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("secure credentials verified but plaintext cleanup failed; plaintext remains; backup: %s: %w", absBackup, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("secure credentials verified but plaintext cleanup commit failed; inspect database; backup: %s: %w", absBackup, err)
	}
	return nil
}

// xrayCredentialSecretKey derives an opaque, stable key from this workspace's
// database path. It keeps workspaces in the same OS credential vault isolated.
// In-memory stores use their per-process DB identity because they have no path.
func (s *Store) xrayCredentialSecretKey() (string, error) {
	dbPath, err := s.databasePath()
	if err != nil {
		return "", err
	}
	if dbPath == "" || dbPath == ":memory:" {
		dbPath = fmt.Sprintf("memory:%p", s.db)
	} else {
		dbPath, err = filepath.Abs(dbPath)
		if err != nil {
			return "", fmt.Errorf("resolving workspace database path: %w", err)
		}
		if resolved, resolveErr := filepath.EvalSymlinks(dbPath); resolveErr == nil {
			dbPath = resolved
		}
		dbPath = filepath.Clean(dbPath)
		if runtime.GOOS == "windows" {
			dbPath = strings.ToLower(dbPath)
		}
	}
	sum := sha256.Sum256([]byte(dbPath))
	return "XRAY_CREDENTIALS_" + hex.EncodeToString(sum[:]), nil
}

func (s *Store) databasePath() (string, error) {
	var seq int
	var name, path string
	rows, err := s.db.Query(`PRAGMA database_list`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&seq, &name, &path); err != nil {
			return "", err
		}
		if name == "main" {
			return path, nil
		}
	}
	return "", rows.Err()
}

func (s *Store) vacuumBackup(path string) error {
	// VACUUM INTO takes a SQL string literal; double embedded quotes.
	literal := strings.ReplaceAll(path, "'", "''")
	_, err := s.db.Exec(`VACUUM INTO '` + literal + `'`)
	return err
}

func samePath(a, b string) (bool, error) {
	aa, err := filepath.Abs(a)
	if err != nil {
		return false, err
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb)), nil
}

// Keep the encoding helpers local to the migration path so all failures can
// include the backup location before the plaintext row is touched.
func jsonMarshalXrayCredentials(v XrayCredentials) (string, error) {
	data, err := json.Marshal(v)
	return string(data), err
}
func unmarshalXrayCredentials(v string) (XrayCredentials, error) {
	var creds XrayCredentials
	err := json.Unmarshal([]byte(v), &creds)
	return creds, err
}
