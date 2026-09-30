# Relay

**A local API workbench for repeatable testing.**

Relay brings HTTP request authoring, assertions, scripted tests, and CI reports into one Go application. Use the native desktop workbench for interactive testing and the CLI for repeatable collection runs. Both use the same HTTP engine, with no account or cloud workspace required.

> [!WARNING]
> **Beta software.** Back up the complete workspace before upgrading. Relay is not yet a complete Postman replacement. See [current limitations](#current-limitations) and the [release-readiness review](docs/REVIEW_2026-09-30.md) before adopting it for critical workflows.

## Capabilities

| Workflow | Available today |
| --- | --- |
| Explore APIs | Request tabs, collection search, environments, inherited headers, cookie sessions, bearer/basic/API-key auth |
| Inspect responses | Resizable panes, formatted and raw previews, search, headers, timing, script console, response history |
| Verify behavior | Visual assertions, pre-request and test scripts, data-driven CLI runs, cancellable requests and collection runs |
| Automate delivery | JUnit and JSON reports, configurable quality gates, k6 scripts, Playwright exports, Xray Cloud integration |
| Manage collections | Postman and OpenAPI JSON import, curl import, compatibility reports, TOML request/environment files in schema-v2 workspaces |

The desktop app uses the system webview through Wails, without Electron or a bundled Chromium runtime. Relay does not provide cloud sync or telemetry. Requests and explicitly configured integrations still make network connections.

## Install

### Windows desktop

[Download the latest Relay Windows installer](https://github.com/muhammadi1996/relay/releases).

The MSI installs for the current user and adds a Start Menu shortcut without requiring administrator access. Windows desktop operation requires Microsoft Edge WebView2. Verify the installer against its published SHA-256 checksum and read the release notes before upgrading. Uninstalling Relay preserves workspace data.

For installation details, see the [installation quick start](INSTALL_QUICK_START.md).

### CLI

```sh
go install github.com/muhaymien96/relay/cmd/relay@latest
```

Or build from source:

```sh
go build -o relay ./cmd/relay
```

Relay currently targets Go 1.25.

## CLI Usage

```text
relay send <file.req.toml> [--env NAME] [-v] [--insecure] [--timeout 30s] [--no-redirect]
relay run  <dir>           [--env NAME] [--report junit|json] [--out FILE]
                           [--data rows.csv|rows.json] [--delay 0ms] [--bail]
                           [--config relay-run.json] [--plan ID] [--execution ID]
                           [--xray-push] [--insecure] [--timeout 30s] [--no-redirect]

relay import postman <collection.json> [--out DIR]
relay import openapi <spec.json>       [--out DIR]
relay import curl '<command>'          [--out FILE]

relay export curl <file.req.toml> [--env NAME]
relay export postman <dir> [--out collection.json]
relay export openapi <dir> [--out spec.json]
relay export k6 <dir> [--env NAME] [--out script.js]
relay export playwright <dir> [--env NAME] [--out api.spec.ts]

relay pack validate <relay-dir>
relay xray import <relay-playwright-results.json> --project KEY [--plan KEY]
relay ui [dir] [--db relay.db] [--port 7717]
relay version
```

Flags can appear before or after positional arguments.

## Quick Start

Create a collection directory with a request such as `health.req.toml` and an `environments/local.toml` file. Point `baseUrl` at an API you control:

```toml
name = "Health"
method = "GET"
url = "{{baseUrl}}/health"

[[assertions]]
type = "status"
equals = 200

[[assertions]]
type = "max_ms"
max_ms = 2000
```

Environment file:

```toml
[vars]
baseUrl = "http://127.0.0.1:8080"
```

From the collection directory, send the request or run the collection:

```sh
relay send health.req.toml --env local -v
relay run . --env local --report junit --out report.xml
relay ui .
```

The workbench normally opens at `http://127.0.0.1:7717`. Collection runs execute request files in lexical order. Use `--report json` for machine-readable output. The [example collection](examples/aml-demo) demonstrates authenticated requests and additional assertions; it requires a compatible API endpoint and is not a bundled mock server.

## CI Integration

For automation repos, keep the exported Relay collection, environment files, and a `relay.ci.json` together:

```json
{
  "report": "junit",
  "out": "relay-results.xml",
  "jsonOut": "relay-results.json",
  "tags": ["regression"],
  "priorities": ["high", "critical"],
  "gates": {
    "failOnAnyFailure": true,
    "failOnXrayError": true
  },
  "xray": {
    "push": true,
    "projectKey": "AML",
    "testPlanKey": "AML-TP1",
    "summary": "Relay automated API execution"
  }
}
```

Run it locally or in Azure DevOps:

```sh
relay run collections/aml --env sit --config relay.ci.json
```

`relay run` exits with code `1` when configured quality gates fail. If tests complete but Xray Cloud import fails and `failOnXrayError` is enabled, Relay exits with code `3`.

## Environments And Secrets

Environment files live at `environments/<name>.toml`. Relay finds them by walking up from the request file or collection directory.

```toml
secrets = ["apiToken"]

[vars]
baseUrl = "https://sit.example.com"
testIdNumber = "8001015009087"
```

Secret values are read from process environment variables named `RELAY_SECRET_<NAME>`, with the name uppercased. For `apiToken`, set `RELAY_SECRET_APITOKEN` before running Relay.

Request files, recovery drafts, local databases, and backups are not encrypted by Relay. Masked inputs hide values on screen; they do not protect literal credentials at rest. Prefer secret references, keep databases and backups out of version control, and review exported artifacts before sharing. Windows Xray credentials can use Credential Manager; legacy credential migration is explicit and requires a backup.

Variable precedence is request vars, then folder vars, then collection vars, then the selected environment. Computed variables are available everywhere: `{{$uuid}}`, `{{$timestamp}}`, `{{$isoTimestamp}}`, and `{{$randomInt}}`.

## Workspace Files

Relay reads these files from a collection directory:

- `*.req.toml` for requests.
- `collection.toml` for collection-level `name`, `[headers]`, and `[vars]`.
- `folder.toml` for folder-level `name`, `[headers]`, and `[vars]`.
- `environments/<name>.toml` for environment variables and secret names.

Headers and variables inherit from collection to folder to request. A request value wins over inherited values; setting an inherited header to an empty string disables it for that request.

## Request Format

Supported request fields include:

- `name`, `method`, `url`
- `query` and `headers` tables, or ordered `query_entries` / `header_entries` rows with duplicate-key and disabled-row support
- `vars` table
- `auth` table with `bearer`, `basic`, or `apikey`
- `body` table with `json`, `xml`, `raw`, `urlencoded`, `formdata`, or `binary`
- `assertions` array
- `scripts.pre_request` and `scripts.tests`
- top-level test metadata: `tags`, `owner`, `priority`, `xray_key`, `requirements`

Supported assertions:

| Type | Fields | Checks |
| --- | --- | --- |
| `status` | `equals` | HTTP status code |
| `jsonpath` | `path`, `equals` | JSON value at a simple JSONPath |
| `header` | `name`, `equals` or `contains` | Response header |
| `contains` | `contains` | Response body substring |
| `max_ms` | `max_ms` | Total duration in milliseconds |

Scripts run in a sandboxed goja runtime. The implemented Postman-style subset includes `pm.test`, `pm.expect`, `pm.environment`, `pm.collectionVariables`, `pm.variables`, `pm.response`, and basic `console` methods.

## Data-Driven Runs

```sh
relay run my-collection --env sit --data ids.csv
```

The collection executes once per row. CSV files need a header row; JSON files must be an array of objects. Row values have the highest variable precedence and can be used in URLs, headers, bodies, and assertion expectations.

## Import And Export

```sh
relay import postman collection.json --out my-collection
relay import openapi openapi.json --out my-api
relay import curl 'curl -X POST -H "Content-Type: application/json" --data-raw "{}" https://api.example.com/x'

relay export postman my-collection --out collection.postman_collection.json
relay export openapi my-collection --out openapi.json
relay export curl my-collection/verify.req.toml --env sit
relay export k6 my-collection --env sit --out load.js
relay export playwright my-collection --env sit --out api.spec.ts
```

Postman import preserves common auth inheritance, collection/folder/request scripts, and ordered query/header rows. The supported scripting subset is not full Postman compatibility. Review the import preview and retained report for unsupported or changed behavior before relying on an imported collection. OpenAPI JSON import creates a request per operation; curl import accepts a command argument or stdin.

Exporters use `RELAY_SECRET_*` references where supported. Literal values embedded in arbitrary request bodies or scripts may remain in exports; secret handling is not a general-purpose content scrubber.

## Browser Workbench

```sh
relay ui my-collection
```

Relay serves the workbench at `http://127.0.0.1:7717`. Use `--port 0` to pick a free port, or `--db path/to/relay.db` to choose the SQLite database location.

The workbench includes:

- Collections, folders, and request CRUD.
- Structured editing for method, URL, query, headers, auth, body, vars, assertions, and scripts, including JSON/XML syntax highlighting and format/minify actions.
- Environment management.
- Header presets attachable to collections and folders.
- A Postman-style resizable request/response split with persisted sizing, maximize/restore controls, and independently scrolling long responses.
- Content-aware JSON, XML/HTML, YAML, form, and text response rendering with pretty/raw and wrap controls, plus response headers/timing, history, stats, and copy-as-curl.
- Collection/folder/request runs using the selected environment.
- Postman, OpenAPI, and curl import.
- Collection, folder, or request export to Relay zip, Postman, OpenAPI, k6, Playwright, or curl, with optional environment files and standalone environment TOML export.
- Complete Playwright project zip export for selected tests, collections, source folders, test folders, sets, and executions.
- Settings for timeout, redirects, TLS verification, and Xray Cloud.
- Test Management for request-linked tests, test folders, test sets, last runs, and Xray actions.

**Storage depends on workspace format.** In schema-v2 workspaces, identified by `workspace.toml`, request and environment edits update canonical TOML files shared with the CLI. External changes are detected, and stale saves require conflict resolution. SQLite retains history, settings, presets, Test Management data, indices, and recovery drafts.

Unmarked legacy workspaces still use SQLite for workbench edits; initial files seed a new database, but later edits do not automatically update the CLI files. Do not assume file continuity until the workspace is migrated. See the [workspace storage design](docs/WORKSPACE_STORAGE_DESIGN.md) and [client guide](docs/API_CLIENT_GUIDE.md#1-workspace-and-storage). Back up the whole workspace, not only its database.

## Test Management And Xray

The Test Management section is available in the workbench and desktop app. It creates default tests from existing requests and lets you add multiple test cases per request, organize them into test folders and test sets, run selected tests, review step results, and configure Xray traceability.

Current state:

- CLI `relay run` executes request-file assertions and script tests.
- CLI `relay run --config relay.ci.json` supports tag/priority filters, JUnit plus JSON artifacts, quality gates, and Xray Cloud Test Execution creation.
- UI Test Management test cases are stored in `relay.db`.
- Top-level request metadata (`tags`, `owner`, `priority`, `xray_key`, `requirements`) is loaded into default UI test cases when seeded.
- Xray Cloud settings and push actions are available from the UI/local API. Test Management also supports persisted executions for selected tests, folders, sets, tags, and priorities.
- Xray execution results use Xray Cloud's JSON execution-import API; test creation and grouping use the supported GraphQL issue-ID contracts, while Jira issue links use REST v3.

Generated Playwright projects write `relay-playwright-results.json`. Import it into Xray with `npm run xray:import` from the project after setting `XRAY_PROJECT`, or directly:

```sh
relay xray import relay-playwright-results.json --project AML --plan AML-P1
```

For daily UI usage, see [docs/API_CLIENT_GUIDE.md](docs/API_CLIENT_GUIDE.md). For repeatable verification and pipeline examples, see [docs/ci-cd.md](docs/ci-cd.md).

## Desktop App

The `relay-app` command wraps the same workbench in a Wails v2 native webview.

On Windows, run these commands in PowerShell from the repository root. Go 1.25 and Microsoft Edge WebView2 are required. Close any app using the output executable before rebuilding.

```powershell
go build -tags desktop,production -ldflags "-s -w -H windowsgui" -o build/relay-app.exe ./cmd/relay-app
.\build\relay-app.exe --workspace C:\path\to\workspace
```

For a first evaluation, follow the [installation quick start](INSTALL_QUICK_START.md#build-and-evaluate-from-source) to create a disposable copy of the examples. The examples need a compatible API; they do not start a mock server.

Build on the target platform for macOS or Linux:

```sh
# macOS
go build -tags desktop,production -ldflags "-s -w" -o relay-app ./cmd/relay-app

# Linux, depending on WebKitGTK package naming
go build -tags desktop,production,webkit2_41 -ldflags "-s -w" -o relay-app ./cmd/relay-app
```

Run it with an explicit workspace or set `RELAY_WORKSPACE`:

```sh
./relay-app --workspace my-collection
```

If no workspace is provided, the app uses the OS app-data location: `%APPDATA%\Relay` on Windows, `~/Library/Application Support/Relay` on macOS, or the user configuration directory on Linux. Close all Relay processes using the workspace and back up the complete directory before upgrading. See [storage behavior](#browser-workbench) before assuming desktop edits are available to CLI runs.

## Current Limitations

- OAuth token management and complete-response streaming exist as backend components but are not integrated into the request editor. Use the supported auth helpers; do not treat a response preview as a complete download.
- Postman compatibility is partial. OpenAPI YAML, Postman environment migration, saved examples, and advanced `pm.*` APIs require additional work and acceptance coverage.
- History stores response snapshots, not complete immutable requests and environments. Exact replay and environment filtering are unavailable.
- Test Management definitions remain in SQLite. Export the required automation artifacts for CI; a request-file run is not identical to every UI test case.
- Native workspace picking/recents, file-selection workflows, reversible deletion, and full accessibility and installer/upgrade acceptance remain release work.
- Comparative performance or usability superiority over Postman and other clients has not been established. See the [measured performance baseline](docs/RELEASE_PERFORMANCE.md).

## Development

```sh
go mod verify
go vet ./...
go test ./... -count=1
go build -o build/relay.exe ./cmd/relay
```

Tests use isolated local fixtures. Browser regression setup and screenshot commands are documented in the [UI audit guide](tests/e2e/README.md). Native and installer acceptance must be performed against the exact candidate, not inferred from browser tests.

## Distribution

Release binaries are ordinary single-file executables: `relay` for the CLI and `relay-app` for the desktop workbench. Windows builds include `.syso` resources for icon/version/manifest metadata. See [docs/DISTRIBUTION.md](docs/DISTRIBUTION.md) for local build commands, checksum guidance, and SmartScreen/code-signing notes.

The Windows desktop workbench is also packaged as a per-user MSI:

```powershell
pwsh -File scripts/build-windows-msi.ps1
```

This produces versioned and stable-name installers under `dist/`. Tagged builds publish the installer and checksums to a GitHub prerelease, keeping the public distribution explicitly in beta.

## Project Docs

- [API Client Guide](docs/API_CLIENT_GUIDE.md)
- [CI/CD Integration Guide](docs/ci-cd.md)
- [Distribution Guide](docs/DISTRIBUTION.md)
- [Current Review and Audit Disposition](docs/REVIEW_2026-09-30.md)
- [Production Readiness Handoff](docs/PRODUCTION_READINESS_HANDOFF.md)
- [Product Requirements](docs/PRD.md)
- [UI e2e audit](tests/e2e/README.md)
- Example workspace: [examples/aml-demo](examples/aml-demo)
