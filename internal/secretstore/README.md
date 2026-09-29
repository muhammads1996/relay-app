# Secret storage

`secretstore.New()` selects the platform implementation. On Windows it stores
generic credentials in the current user's Windows Credential Manager using the
`Relay/<NAME>` target prefix and local-machine persistence (available to that
account across logons). Secret names are restricted to ASCII letters, digits,
and underscores. Windows credential values are limited to 2560 bytes by the
Credential Manager API.

On non-Windows platforms `Get` reads `RELAY_SECRET_<NAME>` from the process
environment, uppercasing the name. Environment fallback is read-only: `Set`
and `Delete` return `ErrUnsupported`. This supports CI without writing secrets
to files or databases. Missing entries return `ErrNotFound`.

The store's API is available for integration, but existing Xray credentials
remain in the current SQLite table. Migrating those records needs a separate
explicit migration and recovery design; this package does not implicitly
rewrite or remove them.
