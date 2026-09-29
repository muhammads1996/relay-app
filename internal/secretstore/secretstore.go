// Package secretstore stores named application secrets outside the collection database.
// Windows uses the current user's Credential Manager vault. Other platforms read
// RELAY_SECRET_<NAME> environment variables; writes are deliberately unsupported.
package secretstore

import "errors"

var ErrNotFound = errors.New("secret not found")
var ErrUnsupported = errors.New("secret storage writes are unsupported on this platform")

// Store reads, writes, and deletes opaque secret values by application-defined name.
type Store interface {
	Get(name string) (string, error)
	Set(name, value string) error
	Delete(name string) error
}

// New returns the platform's default secret store.
func New() Store { return newPlatformStore() }
