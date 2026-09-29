package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muhaymien96/relay/internal/secretstore"
)

type fakeCredentialStore struct {
	values map[string]string
	setErr error
	getErr error
}

func newFakeCredentialStore() *fakeCredentialStore {
	return &fakeCredentialStore{values: map[string]string{}}
}
func (f *fakeCredentialStore) Get(k string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	v, ok := f.values[k]
	if !ok {
		return "", secretstore.ErrNotFound
	}
	return v, nil
}
func (f *fakeCredentialStore) Set(k, v string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.values[k] = v
	return nil
}
func (f *fakeCredentialStore) Delete(k string) error { delete(f.values, k); return nil }

func openCredentialStore(t *testing.T) (*Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "workspace.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dbPath
}

func seedLegacyXray(t *testing.T, s *Store, c XrayCredentials) {
	t.Helper()
	if err := s.SaveXrayCredentials(c); err != nil {
		t.Fatal(err)
	}
}

func TestSaveXrayCredentialsUsesInjectedStoreWithoutPlaintextWrite(t *testing.T) {
	s, _ := openCredentialStore(t)
	fake := newFakeCredentialStore()
	s.SetSecretStore(fake)
	want := XrayCredentials{ClientID: "id", ClientSecret: "s3cret", JiraEmail: "a@b.test", JiraAPIKey: "jira"}
	if err := s.SaveXrayCredentials(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.XrayCredentials()
	if err != nil || got != want {
		t.Fatalf("XrayCredentials() = %#v, %v", got, err)
	}
	var count int
	err = s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='xray_credentials'`).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("vault save created the plaintext SQLite credentials table")
	}
}

func TestVaultCredentialsAreIsolatedByWorkspaceDatabase(t *testing.T) {
	first, _ := openCredentialStore(t)
	second, _ := openCredentialStore(t)
	sharedVault := newFakeCredentialStore()
	first.SetSecretStore(sharedVault)
	second.SetSecretStore(sharedVault)

	wantFirst := XrayCredentials{ClientID: "first-client", ClientSecret: "first-secret"}
	wantSecond := XrayCredentials{ClientID: "second-client", ClientSecret: "second-secret"}
	if err := first.SaveXrayCredentials(wantFirst); err != nil {
		t.Fatal(err)
	}
	if err := second.SaveXrayCredentials(wantSecond); err != nil {
		t.Fatal(err)
	}
	gotFirst, err := first.XrayCredentials()
	if err != nil || gotFirst != wantFirst {
		t.Fatalf("first workspace got %#v, %v", gotFirst, err)
	}
	gotSecond, err := second.XrayCredentials()
	if err != nil || gotSecond != wantSecond {
		t.Fatalf("second workspace got %#v, %v", gotSecond, err)
	}
	if len(sharedVault.values) != 2 {
		t.Fatalf("shared vault entries = %d, want 2", len(sharedVault.values))
	}
}

func TestMigrationRetiresLegacySQLiteAfterNewVaultCredentialsWereSaved(t *testing.T) {
	s, dbPath := openCredentialStore(t)
	legacy := XrayCredentials{ClientID: "old-id", ClientSecret: "old-secret"}
	seedLegacyXray(t, s, legacy)
	vault := newFakeCredentialStore()
	s.SetSecretStore(vault)
	current := XrayCredentials{ClientID: "new-id", ClientSecret: "new-secret"}
	if err := s.SaveXrayCredentials(current); err != nil {
		t.Fatal(err)
	}
	status, err := s.XrayCredentialStatus()
	if err != nil || status.Source != "secure store" || !status.LegacySQLitePresent {
		t.Fatalf("credential status before cleanup = %#v, %v", status, err)
	}
	backup := filepath.Join(filepath.Dir(dbPath), "legacy-credentials-backup.db")
	if err := s.MigrateLegacyXrayCredentials(backup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	got, err := s.XrayCredentials()
	if err != nil || got != current {
		t.Fatalf("vault credentials after cleanup = %#v, %v", got, err)
	}
	status, err = s.XrayCredentialStatus()
	if err != nil || status.LegacySQLitePresent {
		t.Fatalf("credential status after cleanup = %#v, %v", status, err)
	}
}

func TestMigrateLegacyXrayCredentialsBacksUpAndVerifiesBeforeRemovingPlaintext(t *testing.T) {
	s, dbPath := openCredentialStore(t)
	want := XrayCredentials{ClientID: "legacy-id", ClientSecret: "legacy-secret", JiraEmail: "qa@example.test", JiraAPIKey: "legacy-token"}
	seedLegacyXray(t, s, want)
	fake := newFakeCredentialStore()
	s.SetSecretStore(fake)
	backup := filepath.Join(filepath.Dir(dbPath), "credential-migration-backup.db")
	if err := s.MigrateLegacyXrayCredentials(backup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	got, err := s.XrayCredentials()
	if err != nil || got != want {
		t.Fatalf("migrated credentials = %#v, %v", got, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM xray_credentials WHERE id=1`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("plaintext row count = %d, err=%v", n, err)
	}
	backupDB, err := sql.Open("sqlite", backup)
	if err != nil {
		t.Fatal(err)
	}
	defer backupDB.Close()
	var raw string
	if err := backupDB.QueryRow(`SELECT data FROM xray_credentials WHERE id=1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, want.ClientSecret) {
		t.Fatal("backup did not retain the legacy plaintext record")
	}
}

func TestMigrateLegacyXrayCredentialsFailureLeavesPlaintextAndNamesBackup(t *testing.T) {
	s, dbPath := openCredentialStore(t)
	want := XrayCredentials{ClientID: "legacy-id", ClientSecret: "legacy-secret"}
	seedLegacyXray(t, s, want)
	fake := newFakeCredentialStore()
	fake.setErr = errors.New("vault unavailable")
	s.SetSecretStore(fake)
	backup := filepath.Join(filepath.Dir(dbPath), "failed-migration-backup.db")
	err := s.MigrateLegacyXrayCredentials(backup)
	if err == nil || !strings.Contains(err.Error(), backup) {
		t.Fatalf("migration error = %v; want backup path", err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("recovery backup missing: %v", err)
	}
	got, err := s.XrayCredentials()
	if err != nil || got != want {
		t.Fatalf("legacy credentials after failure = %#v, %v", got, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM xray_credentials WHERE id=1`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("legacy row count = %d, err=%v", n, err)
	}
}
