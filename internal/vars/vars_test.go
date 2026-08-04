package vars

import (
	"regexp"
	"strings"
	"testing"

	"github.com/muhaymien96/relay/internal/dsl"
)

func TestScopePrecedence(t *testing.T) {
	s := NewScope(
		map[string]string{"a": "request"},
		map[string]string{"a": "folder", "b": "folder"},
	)
	if err := s.AddEnvironment(&dsl.Environment{
		Vars: map[string]string{"a": "env", "b": "env", "c": "env"},
	}, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]string{"a": "request", "b": "folder", "c": "env"} {
		got, ok := s.Lookup(name)
		if !ok || got != want {
			t.Errorf("%s: got %q ok=%v, want %q", name, got, ok, want)
		}
	}
}

func TestSecrets(t *testing.T) {
	getenv := func(k string) string {
		if k == "RELAY_SECRET_APIKEY" {
			return "s3cret"
		}
		return ""
	}
	s := NewScope()
	if err := s.AddEnvironment(&dsl.Environment{Secrets: []string{"apiKey"}}, getenv); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Lookup("apiKey")
	if !ok || got != "s3cret" {
		t.Errorf("got %q ok=%v", got, ok)
	}
	if !s.IsSecret("apiKey") {
		t.Error("apiKey should be flagged secret")
	}

	// Missing secret is a hard error with the env var name in the message.
	err := NewScope().AddEnvironment(&dsl.Environment{Secrets: []string{"dbPass"}}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "RELAY_SECRET_DBPASS") {
		t.Errorf("err = %v", err)
	}
}

func TestInterpolate(t *testing.T) {
	s := NewScope(map[string]string{
		"baseUrl": "{{scheme}}://api.example.com",
		"scheme":  "https",
		"id":      "42",
	})
	got, err := s.Interpolate("{{baseUrl}}/items/{{ id }}")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://api.example.com/items/42" {
		t.Errorf("got %q", got)
	}

	if _, err := s.Interpolate("{{missing}}"); err == nil {
		t.Error("unresolved variable should error")
	}
}

func TestComputed(t *testing.T) {
	s := NewScope()
	u1, err := s.Interpolate("{{$uuid}}")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(u1) {
		t.Errorf("not a v4 uuid: %q", u1)
	}
	u2, _ := s.Interpolate("{{$uuid}}")
	if u1 == u2 {
		t.Error("uuids should differ per evaluation")
	}
	ts, _ := s.Interpolate("{{$timestamp}}")
	if !regexp.MustCompile(`^\d{10}$`).MatchString(ts) {
		t.Errorf("timestamp = %q", ts)
	}
}

func TestSecretEnvVar(t *testing.T) {
	if got := SecretEnvVar("api-key.2"); got != "RELAY_SECRET_API_KEY_2" {
		t.Errorf("got %q", got)
	}
}
