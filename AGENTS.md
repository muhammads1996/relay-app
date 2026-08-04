# Repository Guidelines

## Project Structure & Module Organization

Relay is a Go 1.25 project for a local-first API client and test runner. CLI entrypoints live in `cmd/relay`, while the desktop wrapper lives in `cmd/relay-app`. Core implementation packages are under `internal/`, grouped by responsibility: `dsl`, `runner`, `engine`, `store`, `ui`, `porter`, `vars`, `script`, `assert`, and external adapters such as `internal/adapters/xray`.

Example collections and environment files live in `examples/aml-demo`. Product, distribution, API, and CI notes live in `docs/`. Built artifacts are kept in `build/` and `dist/`; treat them as generated outputs unless a release task specifically requires updating them.

## Build, Test, and Development Commands

- `go test ./...` runs the full Go test suite.
- `go test ./internal/runner -run TestName` runs a focused package test.
- `go build -o relay ./cmd/relay` builds the CLI locally.
- `go build -tags desktop,production -ldflags "-s -w" -o relay-app ./cmd/relay-app` builds the desktop app.
- `go run ./cmd/relay ui examples/aml-demo --port 7717` starts the browser workbench against the demo collection.
- `go run ./cmd/relay run examples/aml-demo --env local --report junit --out report.xml` exercises the runner and writes CI-style output.

## Coding Style & Naming Conventions

Use idiomatic Go and run `gofmt` before committing. Keep package names short, lowercase, and aligned with directory purpose. Export only API needed across packages; prefer unexported helpers inside `internal` packages. Request files use the `*.req.toml` suffix, and environment files use `examples/.../environments/<name>.toml`.

## Testing Guidelines

Tests use Go's standard `testing` package and live beside source files as `*_test.go`. Add focused unit tests for parser, runner, store, exporter, and adapter changes. Prefer table-driven tests for request formats, assertions, and import/export cases. Run `go test ./...` before opening a PR.

## Commit & Pull Request Guidelines

Recent history uses concise conventional-style subjects such as `feat: add test management endpoints` and `fix(tm): resolve server-wide deadlock`. Keep commits scoped and imperative. Pull requests should include a short behavior summary, testing performed, linked issues when applicable, and screenshots or recordings for UI changes in `internal/ui` or desktop workflows.

## Security & Configuration Tips

Do not commit local databases, credentials, tokens, or generated reports. Relay secrets are read from environment variables named `RELAY_SECRET_<NAME>`, for example `RELAY_SECRET_APITOKEN`. Keep Xray/Jira credentials local and avoid hard-coding URLs or account data in examples.
