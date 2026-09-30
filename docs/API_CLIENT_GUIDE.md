# Relay API Client Guide

This guide covers day-to-day use of the Relay browser workbench and desktop app.

Start the browser workbench from a collection directory:

```sh
relay ui my-collection
```

Relay binds to localhost and prints the URL, usually `http://127.0.0.1:7717`. The desktop app opens the same workbench through `relay-app --workspace my-collection`.

## 1. Workspace And Storage

Relay uses two storage layers. In schema v2 workspaces (`workspace.toml` is present), request and environment definitions are canonical plain files shared by the editor and CLI. SQLite stores history, local preferences, Test Management data, a rebuildable index, and recovery drafts. Older unmarked workspaces still use SQLite for editable workbench definitions; an empty legacy database can be seeded from `.req.toml` files. The CLI runs collection files directly with `relay run`.

The workbench checks schema v2 files in the background while visible and updates the collection tree when they change. The status bar shows the last check or a scan failure; **Refresh files** requests an immediate full check. A failed check keeps the last verified view available. A clean open request reloads when its file changes. If you have unsaved edits, Relay retains your draft and marks a conflict; saving stale content requires you to review the newer version first.

### Unsaved draft recovery and privacy

The editor saves a local recovery copy of an unsaved request in the workspace's `relay.db`. On reopening the request after a restart, choose **Recover draft** to bring it into the editor as unsaved work or **Discard draft** to remove that copy. If the saved request changed since editing began, Relay marks a conflict; overwriting that newer version requires an explicit choice. A successful save removes its matching recovery copy. Copies are bounded to 1 MiB per request, up to 50 per workspace and 500 per database, and expire after 30 days.

Recovery copies are **plaintext in the local SQLite database**, like ordinary request files. A literal token or password typed into an unsaved request may therefore be present in `relay.db` and its backups even if the request is never saved. Keep databases and backups private, do not commit them, and prefer `RELAY_SECRET_<NAME>` references for credentials. Autosave and the close-time write are best-effort recovery measures; check the editor's draft status before closing after a storage error.

## 2. Collections And Requests

Open the Collections view to create and organize API requests.

Common actions:

- Create a collection from the side pane add button.
- Create folders inside a collection.
- Create requests at collection or folder level.
- Open collection or folder settings to edit inherited headers and variables.
- Move requests between folders from request actions.
- Send a request from the editor.
- Run a request, folder, or collection with the selected environment.

Each request editor supports method, URL, query params, headers, auth, body, variables, assertions, and scripts.

While an individual request is running, **Cancel** shows elapsed time and stops waiting for the response. In the browser, cancellation also closes the upstream operation through the Go request context. Cancellation does not undo an operation the API has already processed. The previous response remains available and cancellation is recorded in the response pane. Native WebView2 cancellation still requires acceptance verification.

## 3. Environments And Variables

Relay placeholders use this syntax:

```text
{{variableName}}
```

Use placeholders in URLs, query params, headers, auth fields, bodies, assertions, and scripts.

Variable resolution order is:

1. Request variables
2. Folder variables
3. Collection variables
4. Selected environment variables and secrets

Computed variables are also available:

```text
{{$uuid}}
{{$timestamp}}
{{$isoTimestamp}}
{{$randomInt}}
```

To configure an environment in the UI:

1. Open Environments.
2. Create or select an environment such as `local`, `sit`, or `uat`.
3. Add variables like `baseUrl` or `customerId`.
4. Add secret names like `apiToken`.
5. Set matching process variables before starting Relay, for example `RELAY_SECRET_APITOKEN`.

Secrets are resolved from the process environment. They are masked in relevant UI output and should not be committed to collection files.

## 4. Header Presets

Header Presets are reusable header sets that can be attached to collections and folders.

Use them for repeated headers such as:

- `Content-Type: application/json`
- `X-Correlation-Id: {{$uuid}}`
- API gateway client identifiers
- Shared auth headers when they are not better represented by request auth

Preset values marked as secret are stored locally in the workbench database, masked in the UI, and excluded or protected during export paths that support secret handling.

## 5. Request Bodies And Auth

Supported auth helpers:

- Bearer token
- Basic auth
- API key in header or query

Supported body types:

- JSON
- XML
- Raw text
- URL encoded form
- Multipart form data
- Binary file

Use variables freely in body content and auth fields.

Token, password, and API-key value fields are masked by default; **Show** reveals the selected field. Masking is a display control only. Literal credentials are stored with request definitions and recovery drafts as plaintext. Prefer references such as `{{apiToken}}` backed by `RELAY_SECRET_APITOKEN`.

The JSON and XML editors include line numbers, syntax highlighting, variable highlighting, and format/minify actions. URL-encoded bodies can be normalized, raw bodies use content-aware highlighting when their `Content-Type` or contents identify a supported format, and binary bodies use a collection-relative file path.

Response bodies are detected from `Content-Type` with content sniffing as a fallback. JSON, XML/HTML, YAML, and URL-encoded responses are syntax highlighted; JSON and XML/HTML are pretty-printed by default. Use the response controls to switch between pretty and raw content or to toggle line wrapping. Stored history uses the same content-aware rendering.

The request editor and response viewer use a horizontal split like Postman. Drag the divider to resize the panes; the response share is saved in local browser storage. The divider also supports Up/Down arrow keys when focused and resets on double-click. Use **Reset** for the default split or **Maximize** to give the response the full work area. Long bodies, headers, and timing details scroll inside the response pane instead of shrinking it.

## 6. Assertions

Assertions run after the response is received. Add them from the request editor or Test Management editor.

Supported assertion types:

| Type | Example |
|---|---|
| Status | status equals `200` |
| JSONPath | `$.result.status` equals `VERIFIED` |
| Header | `Content-Type` contains `application/json` |
| Contains | body contains `success` |
| Max ms | total duration is under `2000` ms |

Keep at least one status assertion on important requests, then add body or header assertions for business behavior.

## 7. Scripts

Relay supports pre-request scripts and post-response test scripts through a sandboxed JavaScript runtime.

Pre-request example:

```js
pm.environment.set("requestId", "req-" + Date.now());
```

Test script examples:

```js
pm.test("Status is 200", function () {
  pm.expect(pm.response.code).to.equal(200);
});

pm.test("Body includes success", function () {
  pm.expect(pm.response.text()).to.include("success");
});

pm.test("Capture token", function () {
  var body = pm.response.json();
  pm.environment.set("token", body.token);
});
```

Implemented Postman-style APIs include `pm.test`, `pm.expect`, `pm.environment`, `pm.collectionVariables`, `pm.variables`, `pm.response`, and `console` methods. Scripts do not have filesystem or network access.

## 8. Send And Run Workflow

For a single request:

1. Select the environment from the toolbar.
2. Edit the request fields.
3. Save the request.
4. Click Send.
5. Review response body, headers, timing, assertions, and script test results.

For a wider run:

1. Use run actions for a request, folder, or collection.
2. Relay executes matching requests with the selected environment.
3. Review pass/fail counts, failed assertion messages, and timing.

CLI equivalent:

```sh
relay run my-collection --env sit --report junit --out relay-results.xml
```

Automation repo equivalent with gates and Xray Cloud push:

```sh
relay run collections/aml --env sit --config relay.ci.json
```

## 9. Import And Export

The workbench can import Postman collection JSON, OpenAPI JSON, and pasted curl commands.

The CLI can import the same sources:

```sh
relay import postman collection.json --out my-collection
relay import openapi openapi.json --out my-api
relay import curl 'curl https://api.example.com/health'
```

The workbench and CLI can export Postman collection JSON, OpenAPI JSON, curl, k6, and Playwright API tests.

In the workbench, choose an entire collection, the current folder, or the current request. Relay package export produces a zip of editable `collection.toml`, `folder.toml`, and `*.req.toml` files and can include no environments, the selected environment, or all environments. Environment exports include variable values and secret names, but never secret values. An individual environment can also be downloaded as TOML from the Environments view.

CLI examples:

```sh
relay export postman my-collection --out collection.json
relay export openapi my-collection --out openapi.json
relay export curl my-collection/health.req.toml --env sit
relay export k6 my-collection --env sit --out load.js
relay export playwright my-collection --env sit --out api.spec.ts
```

## 10. History And Stats

The History view stores sent requests and responses locally in `relay.db`. Use it to inspect previous responses and replay recent behavior. Request stats show recent timing data for a request.

## 11. Test Management

Open Test Management from the left rail in Full mode. The navigation separates **Tests**, **Test sets**, and **Executions**. Folders narrow the test inventory without changing the source request collection.

### Organize And Select

The test inventory shows each test's endpoint, latest verdict, priority, owner, tags, and Xray key. Search matches test names, endpoints, owners, tags, requirement keys, and Xray keys. Combine search with collection, priority, Xray-link, and result filters. Result filters use the latest result across environments, not only the active environment.

Select individual rows or all visible tests. **Run selected (N)** counts only enabled tests. Changing filters clears the selection so hidden tests cannot remain in a run accidentally. **Organize** moves selected tests to a folder, adds them to an existing or new test set, or enables/disables them.

### Author API Tests

A request can have multiple test cases. Open a test to use these sections:

- **Assertions**: response checks and `pm.test` scripts.
- **Request**: per-test method, URL, headers, and body.
- **Details**: name, linked request, enabled state, owner, priority, folder, and tags.
- **Last result**: latest recorded verdict, timestamp, duration, and step details.
- **Xray links**: test key, requirements, test plan, validation, and existing Xray actions. This section is available even before credentials are configured.

Changes autosave with a visible status. Run, navigation, export, and Xray actions wait for pending changes. If saving fails, edits remain in the current page and **Save changes** retries them; execution is blocked until saving succeeds. Unsaved Test Management edits are not recovery drafts and do not survive closing or reloading the page. Duplicating a test clears its Xray test key so a new case does not silently reuse the original issue.

### Execute And Investigate

**Run test**, **Run selected**, folder runs, and **Run set** create named, saved executions using the active environment. **Plan execution** creates an execution without running it. The execution's own environment applies to subsequent runs.

New execution results retain the test name, HTTP status, verdict, duration, environment, completion time, errors, and assertion/script steps. Full response payloads are not included in these execution snapshots. Failed tests open expanded; **Failed only** narrows the result list and **Rerun failed** creates a new execution for the failed tests that are still enabled. **Edit test** opens the corresponding definition.

Reopening an execution shows its recorded results, not the latest result of each test from a different execution. Running the same saved execution again replaces its previous result snapshot; use a new execution to retain a separate run. Older executions may have only summary totals. Execution snapshots are not complete immutable copies of requests or environments.

**Export** asks for an explicit scope and either a Relay pack or Playwright project. Selected tests, filtered tests, named collections, folders, sets, and executions remain available. Execution details also offer direct exports.

Important current-state detail: Test Management data is stored in the SQLite workspace database. The CLI runs assertions and scripts stored in `.req.toml` request files. For CI, export or maintain the automation collection as files and use `relay run --config relay.ci.json`.

## 12. Xray Cloud

Open Settings, then configure Xray Cloud:

- Project key
- Optional test plan key
- Optional Cloud GraphQL URL override
- Optional auth URL override
- Default labels and component

Credentials can be provided through environment variables before starting Relay:

```sh
export RELAY_XRAY_CLIENT_ID=your-client-id
export RELAY_XRAY_CLIENT_SECRET=your-client-secret
```

On Windows, credentials saved through `relay-app` or `relay ui` go to Windows Credential Manager under a workspace-specific key. Older workspaces may still have a plaintext credential row in `relay.db`; Settings identifies that legacy source. Migration is explicit and requires a new, named full-database backup:

```powershell
relay-app --workspace C:\path\to\workspace --migrate-xray-credentials --xray-backup C:\path\to\xray-migration-backup.db
```

The command verifies the vault copy before removing the SQLite row and keeps the backup for recovery. The backup itself still contains the old credentials, so store it securely. On other desktop platforms, local UI credential saves continue to use SQLite; prefer environment variables for shared or CI usage.

From Test Management you can validate an existing Xray test key, create an Xray test, link requirement keys, create an Xray test set, create Relay executions, and push selected test runs to Xray as a new Test Execution.

**Run and push to Xray** asks for confirmation, reruns the selected tests, and publishes the new results. It does not upload an earlier saved result snapshot. Live Xray actions require valid local credentials and project settings; linking metadata does not require adding any other integration.

Playwright project exports include a Relay reporter. After a run, set `XRAY_PROJECT` and run `npm run xray:import`, or use `relay xray import relay-playwright-results.json --project KEY`. Exported `.env` files contain secret placeholders only; populate `RELAY_SECRET_*` locally or in CI.

Headless Xray Cloud push is available from `relay run --xray-push` or `relay run --config relay.ci.json`. Existing Xray execution-key append/update is intentionally not enabled until the exact Xray Cloud API path is verified; omit `xray.executionKey` to create a new Test Execution per run.

## 13. Troubleshooting

Variables not resolving:

- Confirm the intended environment is selected.
- Confirm the variable exists in request, folder, collection, or environment scope.
- For secrets, confirm the `RELAY_SECRET_*` variable was set before Relay started.

Scripts not showing test results:

- Put assertions inside `pm.test` blocks in the Tests script.
- Save the request or test, then send or run again.
- Check for runtime errors in the response/test output.

CLI and UI disagree:

- The CLI runs from `.req.toml` files.
- The workbench runs from `relay.db` after seeding/import.
- Export or recreate files when you need file-based CLI runs to reflect UI-only edits.

Xray push fails:

- Confirm project key is set.
- Confirm credentials are present from environment variables or local UI credentials.
- Validate individual Xray test keys before pushing a larger run.
- Ensure selected tests are enabled.
