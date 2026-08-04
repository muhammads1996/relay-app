# PRD: Relay — A Lightweight, Local-First API Client in Go

**Working name:** Curlman (alternatives: Relay, Strider, Conduit)
**Author:** Muhammad
**Status:** Draft v0.1
**Date:** June 2026

> Current state note: this PRD is a product/roadmap document, not the exact shipped-feature reference. The current implementation uses Go 1.25, Wails v2 for `relay-app`, a localhost browser workbench, SQLite-backed UI state, file-based CLI runs, Postman/OpenAPI/curl import, Postman/OpenAPI/curl/k6/Playwright export, scripts, Test Management in the UI, and Xray push through the UI/local API. Some PRD items remain roadmap work, including HAR/Insomnia/Bruno import, OAuth2/SigV4/mTLS helpers, headless CLI Xray push, and canonical `.req.toml` storage for UI-managed Test Management cases.

---

## 1. Problem Statement

Postman has become the default API client for developers and QA engineers, but it has drifted from its original purpose:

- **Bloat.** Electron-based, routinely consumes 500MB–1.5GB of RAM, slow cold starts, sluggish on large collections.
- **Forced cloud.** Account login is effectively mandatory; collections sync to Postman's cloud by default, which is a non-starter for enterprises with strict data governance (banking, insurance, government work — e.g., POPIA-sensitive environments).
- **Pricing pressure.** Collaboration, mocking, and monitoring features are increasingly gated behind per-seat pricing.
- **Weak QE integration.** Postman treats test automation as an afterthought. Exporting work into real test tooling (k6, Playwright) or test management systems (Xray, Zephyr, TestRail) requires manual translation or brittle third-party scripts.

Existing alternatives each solve part of the problem: Bruno (local-first, git-friendly, but Electron), Insomnia (lighter, but cloud-pushed post-Kong acquisition), Hoppscotch (web-first, limited desktop story). **None are native, none treat the QE workflow as a first-class citizen.**

## 2. Vision

A **fast, native, local-first desktop API client** that a quality engineer or backend developer can open in under a second, that stores everything as plain, git-diffable files, and that treats the path from "exploratory request" to "automated test in CI with traceability in test management" as the core workflow — not an export afterthought.

**One-liner:** *Postman's request builder, Bruno's local-first philosophy, and a QE pipeline bridge — in a single ~30MB native binary.*

## 3. Target Users

| Persona | Pain today | What Relay gives them |
|---|---|---|
| **Senior QE / SDET** | Manually rewrites Postman requests as k6/Playwright tests; copy-pastes results into Xray | One-click export to k6 scripts, Playwright API tests; push executions to Xray via GraphQL |
| **Backend developer** | Postman is heavy for "just hit this endpoint"; curl lacks ergonomics | Sub-second launch, keyboard-driven request builder, native performance |
| **Enterprise/regulated teams** | Cloud sync violates data governance; secrets leak into synced environments | 100% local storage, OS-keychain-backed secrets, no telemetry, no account |
| **Platform/API teams** | Collections drift from OpenAPI specs | Bi-directional OpenAPI import/sync; spec-driven collection generation |

## 4. Goals & Non-Goals

### Goals (v1)

1. Native desktop app (Windows, macOS, Linux) under 50MB installed, under 150MB RAM with a large collection open.
2. Full request lifecycle: build → send → inspect → save → organize → export.
3. Local-first, plain-text, git-friendly storage format.
4. First-class header management (presets, inheritance, overrides).
5. Export to: Postman Collection v2.1, OpenAPI 3.x, curl, k6, Playwright (API tests), HAR.
6. Test management integration: Xray Cloud (GraphQL) as the flagship; pluggable adapter interface for Zephyr/TestRail later.

### Non-Goals (v1)

- Cloud sync / team workspaces (git *is* the sync mechanism).
- API mocking server (v2 candidate).
- API monitoring/scheduling (v2+).
- GraphQL/gRPC/WebSocket clients (v1.x — REST and SOAP first, since SOAP still dominates enterprise estates like IBM ACE/IIB shops).
- Browser/web version.

## 5. Core Features

### 5.1 Request Builder

- Methods: GET/POST/PUT/PATCH/DELETE/HEAD/OPTIONS + custom verbs.
- URL bar with environment-variable interpolation (`{{baseUrl}}/v2/verify`) and live preview of the resolved URL.
- Body editors: JSON (with formatting/validation), XML (SOAP envelopes with WSDL-aware snippets), form-data, x-www-form-urlencoded, raw, binary file.
- Auth helpers: Bearer, Basic, API key, OAuth2 (auth code + client credentials flows with built-in token capture), AWS SigV4, mTLS client certificates.
- Pre-request and post-response scripting via an embedded JS engine (goja) — Postman-compatible `pm.*` shim for the common 80% (variables, assertions) to ease migration.

### 5.2 Header Management (first-class, not a tab)

- **Header presets:** named, reusable header sets ("OM Gateway Auth", "JSON defaults", "Correlation IDs") attachable at workspace, collection, folder, or request level.
- **Inheritance model:** request inherits folder → collection → workspace headers, with visual indicators showing where each header came from and per-request override/disable toggles.
- **Computed headers:** dynamic values (`{{$uuid}}`, `{{$timestamp}}`, `{{$randomInt}}`, custom JS expressions) for correlation IDs and idempotency keys.
- **Bulk edit mode:** paste raw header blocks (e.g., copied from DevTools or a HAR) and have them parsed.
- Secret-flagged headers masked in UI and excluded from exports by default.

### 5.3 Collections & Organization

- Tree view: workspace → collections → folders → requests, drag-and-drop reorder.
- Each request = one human-readable file on disk (TOML or a Bruno-style DSL); collections are directories. **The repo is the source of truth** — branch, diff, review, and merge collections in normal git workflows.
- Tabbed request interface, request history with replay, full-text search across collections.
- Example/saved responses per request (useful as contract snapshots).

### 5.4 Environments & Variables

- Environment files (dev/sit/uat/prod) with scoped variables; secrets stored in the OS keychain (Keychain/DPAPI/libsecret), referenced by name in env files so secrets never touch git.
- Variable resolution order: request → folder → collection → environment → workspace globals.
- `.env` import.

### 5.5 Response Viewer

- Pretty/raw/preview modes, JSON tree with path copy (JSONPath), XML pretty-print, large-response streaming (don't lock the UI on a 50MB payload).
- Timing waterfall (DNS, TCP, TLS, TTFB, download), response size, status with HTTP semantics hints.
- Diff view: compare two responses side-by-side (current vs saved example, or across environments — powerful for SIT vs UAT parity checks).
- Search within response; save response to file.

### 5.6 Runner

- Collection runner: sequential execution with per-request assertions, data-driven runs from CSV/JSON, iteration/delay controls.
- CLI companion (`relay run collection/ --env sit --report junit`) — same engine, headless, for CI pipelines (Azure DevOps, GitHub Actions). Outputs JUnit XML and JSON.

### 5.7 Import / Export

**Import:** Postman Collection v2.x (+ environments), OpenAPI 3.x / Swagger 2.0, curl commands, HAR, Insomnia, Bruno.

**Export:**

| Target | Notes |
|---|---|
| Postman Collection v2.1 | Lossless round-trip for the migration-hesitant |
| OpenAPI 3.x | Generate/update spec from collection |
| curl | Per-request, copy to clipboard |
| **k6 script** | Request groups → k6 scenarios; assertions → checks; env vars → k6 env config; sensible threshold scaffolding |
| **Playwright API test** | Requests → `request.newContext()` specs with expect assertions; collection folders → describe blocks |

## 6. Technical Architecture

### 6.1 Stack

- **Language:** Go 1.23+
- **Desktop framework:** **Wails v3** — Go backend with a webview frontend (system webview, not bundled Chromium → small binaries, native feel). Frontend in React/TypeScript or Svelte.
  - *Considered:* Fyne (fully native Go but weak rich-text/code-editor story), Gio (too low-level for editor-heavy UI). The request/response editors demand a mature code-editor component (CodeMirror 6 / Monaco-lite), which tips this to Wails.
- **HTTP engine:** custom client on `net/http` with fine-grained tracing via `httptrace` (powers the timing waterfall), HTTP/2, configurable redirects/proxies/TLS.
- **Scripting:** goja (pure-Go ES2017 runtime) — no CGO, no Node dependency.
- **Storage:** plain files (request DSL + TOML config) in a workspace directory; SQLite (modernc.org/sqlite, pure Go) only for history/search index/cache — never source-of-truth data.
- **Secrets:** go-keyring → OS-native stores.

### 6.2 High-Level Components

```
┌─────────────────────────────────────────────┐
│  UI (webview: React/TS, CodeMirror editors) │
└──────────────────┬──────────────────────────┘
                   │ Wails bindings
┌──────────────────┴──────────────────────────┐
│  Core (Go)                                  │
│  ├─ RequestEngine (http, trace, auth)       │
│  ├─ ScriptRuntime (goja, pm-shim)           │
│  ├─ WorkspaceStore (file DSL, watcher)      │
│  ├─ VariableResolver (scoping, secrets)     │
│  ├─ Runner (sequential, data-driven)        │
│  ├─ Porters (import/export codecs)          │
│  └─ Adapters (Xray GraphQL, future TM)      │
└──────────────────┬──────────────────────────┘
                   │ shared engine
            ┌──────┴───────┐
            │  relay CLI   │  (headless runner for CI)
            └──────────────┘
```

### 6.3 File Format Sketch

```toml
# collections/aml-verification/verify-individual.req.toml
name = "Verify Individual"
method = "POST"
url = "{{baseUrl}}/aml/v2/verify"

[meta.xray]
test_key = "AML-T142"
requirements = ["AML-88"]

[headers]
Content-Type = "application/json"
X-Correlation-Id = "{{$uuid}}"

[headers.inherit]
presets = ["om-gateway-auth"]

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
```

## 7. Performance Targets

| Metric | Target | Postman (typical) |
|---|---|---|
| Cold start | < 1s | 5–15s |
| Installed size | < 50MB | ~500MB |
| RAM (large workspace) | < 150MB | 600MB–1.5GB |
| Request overhead (app-added latency) | < 5ms | 20–50ms |

## 8. MVP Scoping & Roadmap

### Phase 1 — Core client (6–8 weeks)

Request builder (REST), header management with presets + inheritance, environments + keychain secrets, response viewer with timing, file-based workspace, history, Postman import, curl export.

### Phase 2 — QE bridge (4–6 weeks)

Collection runner + assertions, CLI runner with JUnit output, k6 export, Playwright export, OpenAPI import/export, response diff view.

### Phase 3 — Integration & polish (4–6 weeks)

Xray adapter (push executions, traceability metadata), SOAP/WSDL support, OAuth2 flows, scripting (goja + pm-shim), HAR import/export, data-driven runs.

### v2 candidates

Mock server, GraphQL/gRPC clients, Zephyr/TestRail adapters, request chaining visual editor, AI-assisted test generation from specs (MCP server exposing the workspace so agents can author/run requests).

## 9. Success Metrics

- Time from "Postman collection import" to "k6 script running" under 5 minutes.
- Cold start and RAM targets hit on a 500-request workspace.
- A QE can push a full regression run's results to Xray without leaving the app or writing glue code.
- Collections survive a git merge between two contributors with zero corruption.

See [API_CLIENT_GUIDE.md](API_CLIENT_GUIDE.md) for the implemented Test Management and Xray workflow, and [ci-cd.md](ci-cd.md) for headless execution and pipeline integration.

## 10. Risks & Mitigations

| Risk | Mitigation |
|---|---|
| Webview inconsistencies across OSes (Wails) | Pin to WebView2/WKWebView/WebKitGTK baselines; CI screenshot tests per OS |
| Postman script compatibility expectations | Scope the pm-shim explicitly; publish a compatibility matrix; fail loudly on unsupported APIs |
| Xray API changes | Adapter isolation + contract tests against Xray sandbox |
| Editor performance on huge payloads | Stream + virtualize responses; cap pretty-print, offer raw mode beyond threshold |
| Scope creep toward full Postman parity | Phase gates; "QE bridge" is the identity — features that don't serve build→test→trace get deferred |
