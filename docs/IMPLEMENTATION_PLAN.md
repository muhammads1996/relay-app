# Relay production readiness implementation plan

Source: [desktop and application UX audit](UX_AUDIT_2026-09-28.md), 28 September 2026. This plan covers findings F01-F24 and the desktop and performance gaps recorded there. It is a work plan, not a claim that Relay is production ready. The audit's browser and source evidence is useful; native desktop behavior and fresh Go tests still require verification.

## Product and release contract

Relay's first strong use case is a local REST workbench whose requests become versioned tests and repeatable CI runs. Support existing HTTP and QA workflows thoroughly before expanding into monitors, mock servers, cloud collaboration, or additional protocols. A usable daily replacement requires trustworthy saves and sends, migration that discloses loss, UI/CLI agreement, accessible navigation, and tested native behavior.

**Definition of done for each task:** implement the observable behavior, add focused regression coverage at the appropriate boundary, preserve existing workspace data, document any unsupported import or scripting behavior, and verify the changed flow in the browser plus the native Windows shell when it touches desktop behavior. Go changes require `gofmt`, focused tests, `go test ./...`, `go vet ./...`, and build checks when the local environment permits. Record a blocked command and run it in CI rather than treating an old binary as proof. UI changes require screenshots at 760x480, 1024x760, 1280x820, and a wide desktop size, plus keyboard checks; native changes require actual WebView2 testing. Do not overwrite `build/` or `dist/` outside release work.

**Data rule:** migrations must be transactional and versioned, with a recoverable backup before altering an existing workspace. Files containing tokens, databases, and generated reports stay out of Git. No release tag, published installer, or performance claim follows solely from passing unit tests.

## Decisions and dependencies

1. **Canonical definitions (F08):** make requests, environments, and test definitions under a workspace directory the source of truth for editable behavior, with SQLite retaining history, indices, UI preferences, and cached metadata. The editor saves an atomic file update, and CLI uses the same loader. Preserve a migration path from existing database workspaces and explicit conflict handling for external edits/Git branch switches. This is a deliberate architecture decision because an indefinitely optional export button does not satisfy the audit's UI/CLI continuity requirement. First deliver a migration specification and fixture tests before changing storage.
2. **Draft identity:** represent each open request by stable ID, revision, dirty/saving/error state, and its own latest execution. A send uses the saved revision and captured environment. The persisted state must never be inferred from a successful network request alone.
3. **Execution semantics:** use one request execution pipeline for UI, tests, and CLI. Console output, runtime exceptions, assertions, and transport failures must remain distinct. Cancellation uses `context.Context` end to end. Secret values remain masked in displays and logs.
4. **Migration reporting:** every import returns counts and warnings for preserved, converted, unsupported, and skipped behavior. Unsupported executable content must remain available in an original backup and be shown before the import is accepted.

The following tranches are ordered by dependency. Independent work inside a tranche can proceed in parallel only when file ownership is separate; `internal/ui/index.html` is a shared hot spot and should have one integrator at a time. An implementer should finish each task with its tests and a small reviewable diff, then continue to the next task.

## Tranche 0 — establish a reliable baseline

| Task | Main owner/files | Acceptance |
|---|---|---|
| T0.1 Baseline and fixtures | `internal/ui/ui_test.go`, package test fixtures, `docs/` | Capture current test/build results; create local delayed HTTP, failing save, import, and large-response fixtures with no external credentials. CI verifies the full Go suite on a host where Go execution is allowed. |
| T0.2 UI test harness | `internal/ui/` tests and test tooling | Browser automation can run against the current HTML and a disposable workspace; assertions inspect visible state and request traffic, rather than source substrings. Keep native WebView tests separate. |
| T0.3 Data migration design | `internal/store/`, `internal/dsl/`, `docs/` | Written schema/loader specification covers stable IDs, folder structure, inheritance, duplicate fields, test links, atomic writes, SQLite migration, external edits, conflicts, and rollback. Fixtures prove old database data can be read. |

## Tranche 1 — correctness and data trust

| Task | Audit IDs; main owner/files | Acceptance |
|---|---|---|
| T1.1 Save and draft state | F01; `internal/ui/index.html`, `internal/ui/crud.go` | A rejected, offline, 422, or 500 save blocks Send and navigation. The visible draft survives and offers Retry. A late save cannot mark a newer revision clean; tab/view rerenders preserve dirty state. Verify rapid edit, switch, close, and reopen. |
| T1.2 Execution ownership | F02, F12; `internal/ui/index.html`, `internal/ui/ui.go`, `internal/engine/` | Starting A and selecting B never shows A's response or stats under B. Ctrl+Enter is guarded while executing. Navigation cannot throw when Send is absent. Cancel stops the network operation and elapsed time/terminal state are visible. |
| T1.3 View routing and mode | F03, F10; `internal/ui/index.html` | Changing environment preserves Tests, History, Settings, Presets, and their selected entities; Basic/Full mode switches without a JavaScript exception and persists across restart. Keyboard shortcuts act on the active view. |
| T1.4 Settings truth | F04; `internal/ui/index.html`, `internal/ui/ui_test.go` | All checkbox booleans render and round-trip true/false. A disabled TLS verification setting is visible in the active request workspace and matches the engine setting. |
| T1.5 Script result model | F05; `internal/script/`, `internal/ui/ui.go`, `internal/runner/` | `console.log/warn` appear in a console without failing a run. Failed assertions and uncaught exceptions still fail in UI and CLI. Warnings and errors retain correct order and masking. |
| T1.6 Transactional request move | F06; `internal/store/store.go`, `internal/ui/crud.go`, `internal/ui/index.html` | Move changes `collection_id` and valid `folder_id` atomically and survives restart. Destination inheritance applies; invalid destination folders fail without partial mutation. Linked tests retain a defined request association. |
| T1.7 Immediate import safety | F07; `internal/porter/postman.go`, `internal/ui/ui.go`, `internal/ui/index.html` | A Postman import preview discloses inherited auth, scripts, unsupported APIs, dropped fields, and counts before commit. Preserve original source for recovery. The existing sample with inherited bearer auth and scripts cannot silently report full success. |
| T1.8 Honest errors and capability labels | F23, F24; `internal/ui/index.html`, `cmd/relay-app/`, `VERSION` | Send failure remains visible beside the timestamped prior result with DNS/TLS/timeout/script stage. Remove disabled roadmap navigation and static “AGENT ONLINE.” Show the built version from one source. |

**Gate 1:** all reproduced priority-one defects have passing regressions. A save failure cannot dispatch. A slow response cannot cross request identity. A migration report cannot claim full preservation when executable behavior was lost.

## Tranche 2 — canonical workspace and daily workbench

T0.3 must be reviewed before T2.1. Complete T1.1/T1.2 state work before tabs. Keep data format and UI implementation in separate changes so failures can be bisected.

| Task | Audit IDs; main owner/files | Acceptance |
|---|---|---|
| T2.1 File-backed definitions and migration | F08; `internal/store/`, `internal/dsl/`, `internal/runner/`, `cmd/relay/`, `cmd/relay-app/` | Old SQLite workspaces migrate with backup, stable IDs, test links, and no secret leakage. Saving a UI request/env/test produces a reviewable file diff; running those files through CLI yields the same effective request and assertions. External edits and branch switches reload safely or present a conflict with both versions recoverable. |
| T2.2 Editor tabs and search | F11; `internal/ui/index.html`, search/index backend as needed | Independent tabs retain drafts, environment, active editor subtab, and last response. Restore prior session and provide recent requests. Search by name, method, URL, collection, and folder; keyboard command opens a result. Close/switch never loses unsaved content. |
| T2.3 Workspace layout | F09, F17, F24; `internal/ui/index.html`, desktop minimum in `cmd/relay-app/main.go` | Navigation collapses to about 48px, collection tree is resizable, metadata moves to an optional drawer. At 1280px the central work surface is approximately 900px. At every supported size the method, URL, Send, editor, and response are reachable; Test Management content has nonzero height at 760x480. Minimum window size is raised if needed after measuring actual native layout. |
| T2.4 Accessibility foundation | F16, F17; `internal/ui/index.html` | All interactive collection rows are keyboard operable with correct semantics. Dialogs trap and restore focus, form labels have programmatic names, toasts/status use live regions, tabs expose selection. Normal text contrast is at least 4.5:1; supporting text is at least 12px and editor text at least 13px. Validate by keyboard pass and automated accessibility scan. |
| T2.5 Recovery and first run | F22, F23; `internal/ui/index.html`, `internal/store/` | New workspace offers paste URL/curl, import, open workspace, and sample. Destructive actions live in overflow, deletion is reversible for a defined retention period, and drafts recover after process exit. A single import/export dialog shows scope and preview. |
| T2.6 Collection run progress | F12; `internal/ui/ui.go`, `internal/ui/index.html`, `internal/runner/` | Run shows current request, completed/total, partial results, elapsed time, and Cancel. Cancel reports completed work accurately and does not leave the UI stuck. |

**Gate 2:** create or edit a request in desktop, observe a file diff, run it via CLI, switch branches, resolve a conflict, and recover an interrupted draft. Complete the entire flow with keyboard alone. Check layout in browser and native Windows at 760x480, 1024x760, 1280x820 and 125%, 150%, 200% scaling.

## Tranche 3 — migration fidelity and practical HTTP parity

| Task | Audit IDs; main owner/files | Acceptance |
|---|---|---|
| T3.1 Complete Postman and OpenAPI migration | F07; `internal/porter/`, `internal/ui/ui.go`, `internal/ui/index.html` | Corpus covers nested folders, inherited and overridden auth, scripts at collection/folder/request scopes, variables/environments, disabled rows, body formats, duplicate query fields, and OpenAPI JSON/YAML. Preserve what Relay can execute; flag every unsupported `pm.*` API with location. Imported originals and reports are recoverable. |
| T3.2 Ordered request fields | F15; `internal/dsl/`, `internal/vars/`, `internal/store/`, `internal/engine/`, `internal/ui/index.html` | Headers and query fields retain order, enabled state, duplicate keys, and values across edit/save/export/CLI/send. URL and Params stay synchronized without collapsing duplicates. Existing map-based request files load through a documented migration. |
| T3.3 Cookie sessions | F13; `internal/engine/`, `internal/ui/ui.go`, `internal/runner/`, `internal/ui/index.html` | Login `Set-Cookie` is sent on a subsequent matching request within the same workspace session, not to an unrelated workspace. Users can inspect, edit, clear, and disable cookies. CLI tests start from deterministic isolated jars unless explicitly configured otherwise. |
| T3.4 OAuth and enterprise TLS | F14, F19; `internal/engine/`, `internal/ui/`, credential adapter, `internal/store/` | OAuth2 authorization code with PKCE and client credentials refresh correctly; inherited auth is clear in the editor. User-selected CA and client certificates work; proxy state is discoverable. Desktop secrets use OS credential storage with masked reveal and environment-variable fallback for CI. No plaintext Xray/token migration remains unexplained. |
| T3.5 Complete response and history semantics | F20, F21; `internal/engine/`, `internal/store/`, `internal/ui/` | Response UI distinguishes actual, buffered, preview, and stored byte counts and marks truncation everywhere. A 100MiB download streams to a complete file within bounded memory. History supports URL/status/environment/time filters, exact request snapshot replay, comparison, and explicit retention. |

**Gate 3:** representative imported collections run with expected auth, scripts, variables and assertions. OAuth, cookies, certificates, duplicate fields, proxy, and large body behavior pass isolated integration fixtures. No successful import conceals changed executable behavior.

## Tranche 4 — QA and native desktop polish

| Task | Audit IDs; main owner/files | Acceptance |
|---|---|---|
| T4.1 Focus Test Management | F18; `internal/ui/index.html`, `internal/ui/test_management.go` | First screen emphasizes request summary, assertions, and last result. Identity and overrides are collapsed; Xray appears when configured. Each inherited field shows its source and effect of override. Existing suite/export results remain stable. |
| T4.2 Response tools | F20, F21; `internal/ui/index.html` | Pretty/Raw, Wrap, Search, Copy and overflow are clear and keyboard usable. JSON collapse and path navigation handle 1MiB responses without sustained typing/navigation stalls. Response comparison shows selected history entries with explicit truncation labels. |
| T4.3 Native workspace flow | Desktop audit; `cmd/relay-app/main.go`, `internal/ui/`, `internal/store/` | Visible workspace picker and recents, Open/Reveal folder, backup/restore, native binary and multipart file selection, saved window geometry, and actionable startup/migration failure UI. Window close flushes or preserves drafts. Relative file paths resolve from an explicit workspace root. |
| T4.4 Native verification | Desktop audit; installer and UI test tooling | On supported Windows versions verify WebView2 clipboard, downloads, file dialogs, keyboard shortcuts, scaling, multiple instances, sleep/resume, installation/upgrade, and DB backup/restore. Repeat documented supported subset on macOS/Linux if those platforms are shipped. No Chrome-only result is presented as native proof. |
| T4.5 About and update information | F24, desktop audit; `cmd/relay-app/`, `internal/ui/`, release metadata | About reports exact version/build. Update availability is accurate and optional; startup errors are actionable. Release notes state unsupported behavior and migration changes. |

**Gate 4:** five representative developers/QA engineers independently import or create a collection, configure auth/environment, diagnose a failure, add assertions, run a suite, and reproduce it in CI. Record completion, time, errors and recovery; resolve blocking observations before release.

## Tranche 5 — demonstrate lightweight behavior

| Task | Main owner/files | Acceptance |
|---|---|---|
| T5.1 Benchmark harness | `internal/` benchmarks, `scripts/`, `docs/` | Repeatable datasets for 1,000/10,000 requests, 1MiB JSON, 100MiB download, 30-minute repeated sends; record hardware, OS, cold/warm state and all attributable webview processes. |
| T5.2 Measured optimizations | `internal/engine/`, `internal/store/`, `internal/ui/` | Profile first, then reuse bounded transports, index/lazy-load or virtualize lists, and move expensive formatting off the interaction path as evidence requires. Demonstrate no accumulating sockets/goroutines and a memory plateau. |
| T5.3 Public performance report | `docs/` | Publish reproducible Relay and competitor measurements on identical hardware/data. Initial targets: warm editor <1s, cold editor <2s, idle CPU <1%, aim for <150MiB attributable private working set, 1,000-request switch p95 <100ms, indexed 10,000-request search p95 <100ms. Report misses and hardware instead of claiming unmeasured superiority. |

## Final release gate

Run the full suite, vet, CLI and desktop builds, installer extraction/upgrade test, UI automation, accessibility checks, native Windows checklist, import corpus, backup/restore rehearsal, security review of secrets, and benchmark matrix on a release candidate. Include screenshots or a recording for UI work. Mark supported operating systems explicitly. Treat any unresolved data-loss, response-ownership, misleading-security-state, or silent-import-loss finding as a release blocker. Tag and publish only after these gates pass on the exact built artifacts.
