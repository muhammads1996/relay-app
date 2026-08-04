# Relay

<img src="logo.png" alt="Relay logo" width="96">

Relay is a fast, lightweight, local-first API client and test runner written in Go. Requests live in plain TOML files, so collections can be branched, reviewed, and merged like code. The same request engine powers the CLI, browser workbench, and native desktop app.

> [!WARNING]
> Relay is beta software. Back up important workspaces before upgrading, and expect breaking changes between beta releases.

Current implementation highlights:

- Send one request or run every `*.req.toml` file in a collection from the CLI.
- Generate JUnit or JSON reports for CI.
- Import Postman collections, OpenAPI specs, and pasted curl commands.
- Export curl, Postman, OpenAPI, k6, and Playwright artifacts.
- Use a localhost browser workbench with collections, folders, requests, environments, header presets, history, scripts, runners, and Test Management.
- Use the native `relay-app` desktop wrapper around the same workbench.
- Push Test Management runs to Xray Cloud from the UI/local API.

No Electron, no account, no cloud sync, and no telemetry.

## Install

### Windows desktop

[Download the latest Relay Windows installer](https://github.com/muhammadi1996/relay/releases).

The MSI installs the desktop workbench for the current user, adds a Start Menu shortcut, and does not require administrator access. Uninstalling Relay preserves the workspace database. For the direct download link to use in your marketing site, see [docs/INSTALLER_SETUP.md](docs/INSTALLER_SETUP.md). You can verify downloaded files against the published SHA-256 checksums on the release page.

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

A request is one `.req.toml` file:

```toml
name = "Verify Individual"
method = "POST"
url = "{{baseUrl}}/aml/v2/verify"
tags = ["regression", "contract"]
priority = "high"
xray_key = "AML-T142"
requirements = ["AML-88"]

[headers]
Content-Type = "application/json"
X-Correlation-Id = "{{$uuid}}"

[auth]
type = "bearer"
token = "{{apiToken}}"

[body]
type = "json"
content = '''
{ "idNumber": "{{testIdNumber}}", "channel": "API" }
'''

[[assertions]]
type = "status"
equals = 200

[[assertions]]
type = "jsonpath"
path = "$.result.status"
equals = "VERIFIED"

[[assertions]]
type = "max_ms"
max_ms = 2000

[scripts]
tests = '''
pm.test("status is 200", function () {
    pm.expect(pm.response.code).to.equal(200);
});
'''
```

Send it:

```sh
relay send 02-verify-individual.req.toml --env local -v
```

Run a whole collection in lexical order and write JUnit for CI:

```sh
relay run examples/aml-demo --env local --report junit --out report.xml
```

Use JSON instead when another tool needs machine-readable results:

```sh
relay run examples/aml-demo --env local --report json --out report.json
```

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
- `query` table
- `headers` table
- `vars` table
- `auth` table with `bearer`, `basic`, or `apikey`
- `body` table with `json`, `xml`, `raw`, `urlencoded`, `formdata`, or `binary`
- `assertions` array
- `scripts.pre_request` and `scripts.tests`
- top-level test metadata: `tags`, `owner`, `priority`, `xray_key`, `requirements`

Supported assertions:

| Type | Fields | Checks |
|---|---|---|
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

Postman import maps folders to directories, requests to `.req.toml` files, collection variables to `collection.toml`, and common bearer/basic/API-key auth to Relay auth helpers. OpenAPI import creates a request per operation. curl import accepts a command argument or stdin.

Exporters keep secrets out of generated artifacts by using `RELAY_SECRET_*` environment references where applicable.

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

When a new database opens on an existing directory of `.req.toml` files, Relay seeds the SQLite workspace from those files. The CLI continues to run directly from the files; the workbench stores its live workspace, history, presets, settings, and Test Management data in SQLite.

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

```sh
# Windows
go build -tags desktop,production -ldflags "-s -w -H windowsgui" -o relay-app.exe ./cmd/relay-app

# macOS
go build -tags desktop,production -ldflags "-s -w" -o relay-app ./cmd/relay-app

# Linux, depending on WebKitGTK package naming
go build -tags desktop,production,webkit2_41 -ldflags "-s -w" -o relay-app ./cmd/relay-app
```

Run it with an explicit workspace or set `RELAY_WORKSPACE`:

```sh
relay-app --workspace my-collection
```

If no workspace is provided, the app uses the OS app-data location.

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
- [Product Requirements](docs/PRD.md)
- [UI e2e audit](tests/e2e/README.md)
- Example workspace: [examples/aml-demo](examples/aml-demo)
