//go:build !windows

package secretstore

import (
	"errors"
	"testing"
)

func TestEnvStoreLookupAndReadOnlyWrites(t *testing.T) {
	values := map[string]string{"RELAY_SECRET_APITOKEN": "value-from-test"}
	s := &EnvStore{lookup: func(k string) (string, bool) { v, ok := values[k]; return v, ok }}
	v, err := s.Get("apiToken")
	if err != nil || v != "value-from-test" {
		t.Fatalf("Get() = %q, %v", v, err)
	}
	if _, err := s.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Get() error = %v", err)
	}
	if err := s.Set("apiToken", "secret"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Set() error = %v", err)
	}
	if err := s.Delete("apiToken"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestEnvStoreRejectsUnsafeNames(t *testing.T) {
	s := &EnvStore{lookup: func(string) (string, bool) { t.Fatal("lookup called for invalid name"); return "", false }}
	for _, name := range []string{"", "A-B", "../TOKEN"} {
		if _, err := s.Get(name); err == nil {
			t.Errorf("Get(%q) succeeded", name)
		}
	}
}
