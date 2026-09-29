//go:build windows

package secretstore

import "testing"

func TestCredentialTargetNameValidation(t *testing.T) {
	s := NewWindowsStore()
	if _, err := s.target("xrayClientSecret"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "with space", "../secret"} {
		if _, err := s.target(name); err == nil {
			t.Errorf("target(%q) succeeded", name)
		}
	}
}
