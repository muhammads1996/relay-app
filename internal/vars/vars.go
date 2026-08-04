// Package vars resolves {{variable}} placeholders. Resolution order follows
// the PRD: request → folder → collection → environment, with computed
// variables ({{$uuid}}, {{$timestamp}}, {{$randomInt}}) always available and
// environment secrets pulled from the process environment
// (RELAY_SECRET_<NAME>) so secret values never live in workspace files.
package vars

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/muhaymien96/relay/internal/dsl"
)

var placeholder = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

// Scope is an ordered variable lookup chain.
type Scope struct {
	layers  []map[string]string // highest precedence first
	secrets map[string]string
	Getenv  func(string) string
}

// NewScope builds a scope from highest-precedence to lowest. Nil maps are
// allowed and skipped.
func NewScope(layers ...map[string]string) *Scope {
	s := &Scope{secrets: map[string]string{}}
	for _, l := range layers {
		if l != nil {
			s.layers = append(s.layers, l)
		}
	}
	return s
}

// AddEnvironment appends an environment file's vars below existing layers
// and registers its secrets.
func (s *Scope) AddEnvironment(env *dsl.Environment, getenv func(string) string) error {
	if env == nil {
		return nil
	}
	if env.Vars != nil {
		s.layers = append(s.layers, env.Vars)
	}
	for _, name := range env.Secrets {
		v := getenv(SecretEnvVar(name))
		if v == "" {
			return fmt.Errorf("secret %q is not set: export %s", name, SecretEnvVar(name))
		}
		s.secrets[name] = v
	}
	return nil
}

// SecretEnvVar maps a secret name to its process environment variable,
// e.g. "apiKey" → "RELAY_SECRET_APIKEY".
func SecretEnvVar(name string) string {
	clean := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, name)
	return "RELAY_SECRET_" + strings.ToUpper(clean)
}

// Lookup returns the value for a variable name, checking secrets, then the
// layer chain, then computed variables.
func (s *Scope) Lookup(name string) (string, bool) {
	if v, ok := s.secrets[name]; ok {
		return v, true
	}
	for _, l := range s.layers {
		if v, ok := l[name]; ok {
			return v, true
		}
	}
	return computed(name)
}

// IsSecret reports whether a variable name is a registered secret, so
// callers (exporters, loggers) can mask it.
func (s *Scope) IsSecret(name string) bool {
	_, ok := s.secrets[name]
	return ok
}

// Interpolate replaces every {{name}} in s. Unknown variables are an error:
// a silently unresolved variable in a URL or auth header is worse than a
// failed run.
func (s *Scope) Interpolate(in string) (string, error) {
	var firstErr error
	out := placeholder.ReplaceAllStringFunc(in, func(m string) string {
		name := strings.TrimSpace(placeholder.FindStringSubmatch(m)[1])
		// Variable values may themselves contain placeholders (one level of
		// nesting, e.g. baseUrl = "{{scheme}}://{{host}}").
		if v, ok := s.Lookup(name); ok {
			nested, err := s.interpolateOnce(v)
			if err != nil && firstErr == nil {
				firstErr = err
			}
			return nested
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("unresolved variable {{%s}}", name)
		}
		return m
	})
	return out, firstErr
}

func (s *Scope) interpolateOnce(in string) (string, error) {
	var firstErr error
	out := placeholder.ReplaceAllStringFunc(in, func(m string) string {
		name := strings.TrimSpace(placeholder.FindStringSubmatch(m)[1])
		if v, ok := s.Lookup(name); ok {
			return v
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("unresolved variable {{%s}}", name)
		}
		return m
	})
	return out, firstErr
}

func computed(name string) (string, bool) {
	switch name {
	case "$uuid":
		return uuid4(), true
	case "$timestamp":
		return fmt.Sprintf("%d", time.Now().Unix()), true
	case "$isoTimestamp":
		return time.Now().UTC().Format(time.RFC3339), true
	case "$randomInt":
		n, _ := rand.Int(rand.Reader, big.NewInt(1000))
		return n.String(), true
	}
	return "", false
}

func uuid4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// MaskSecrets replaces every registered secret value in s with a mask, for
// display surfaces (UI, logs) that must never show secret material.
func (s *Scope) MaskSecrets(in string) string {
	for _, v := range s.secrets {
		if v != "" {
			in = strings.ReplaceAll(in, v, "••••••")
		}
	}
	return in
}

// SecretValues returns a copy of the registered secret name → value pairs,
// for callers that need to substitute references (e.g. curl export writing
// $RELAY_SECRET_X instead of the value).
func (s *Scope) SecretValues() map[string]string {
	out := make(map[string]string, len(s.secrets))
	for k, v := range s.secrets {
		out[k] = v
	}
	return out
}
