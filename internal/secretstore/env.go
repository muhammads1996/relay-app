package secretstore

import (
	"fmt"
	"os"
	"strings"
)

// EnvStore resolves RELAY_SECRET_<NAME> variables and never persists writes.
// The lookup function is injectable so tests need not mutate process environment.
type EnvStore struct{ lookup func(string) (string, bool) }

func NewEnvStore() *EnvStore { return &EnvStore{lookup: os.LookupEnv} }

func (s *EnvStore) Get(name string) (string, error) {
	if !validName(name) {
		return "", fmt.Errorf("invalid secret name")
	}
	v, ok := s.lookup("RELAY_SECRET_" + strings.ToUpper(name))
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}
func (*EnvStore) Set(string, string) error { return ErrUnsupported }
func (*EnvStore) Delete(string) error      { return ErrUnsupported }
