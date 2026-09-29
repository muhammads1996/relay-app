# Relay production-readiness handoff

Status: **release candidate work in progress**. Do not tag, publish an installer, or describe Relay as a complete Postman replacement yet. The current implementation and measured limits are in [implementation status](IMPLEMENTATION_STATUS_2026-09-29.md); the original findings are in the [UX audit](UX_AUDIT_2026-09-28.md).

## What you need to provide or decide

1. **An unrestricted Windows test host.** It must be able to run newly built Go test executables, `vet.exe`, the fresh Wails desktop binary, and the MSI. This development host's Windows Application Control blocks some of them. Ask your IT/security administrator to allow the approved development/release toolchain; do not turn off endpoint protection merely to make a test pass. Record Windows version, WebView2 version, display scaling, CPU, and memory.
2. **A disposable real-world acceptance corpus.** Provide sanitized Postman collections/environments and OpenAPI documents that reflect your actual workflows, with expected request counts, auth behavior, script outcomes, duplicate headers/query fields, disabled rows, and CLI run results. Keep production secrets out of fixtures. The current fixture covers common inherited auth and scripts, but cannot establish full migration fidelity.
3. **A release-scope decision.** Decide whether the first release promises Windows-only REST/JSON workflows or broader Postman parity. In particular, decide whether OAuth in the editor, OpenAPI YAML/environment import, saved Postman examples and advanced `pm.*` scripts, exact history replay, and native workspace picking are required. Either finish each promised capability or explicitly mark it unsupported in the product and release notes; never silently drop executable import behavior.
4. **Five representative users and a release owner.** Arrange a short independent task study with developers/QA engineers. The release owner records failures, decides whether each is blocking, and signs off the exact candidate artifact. Signing credentials are optional for a beta, but if signing is used, keep them in the release secret store, not this repository.

## Engineering work still required before a production claim

| Gate | Required outcome |
|---|---|
| Native workspace flow | A visible picker/recents, Open/Reveal folder, actionable startup/migration errors, backup/restore, and native file selection for multipart/binary bodies. Test window close and restart with unsaved drafts. |
| Execution and response flow | Wire the existing OAuth manager and full-file streaming download into the editor. Show actual, preview, and stored byte counts. Complete a 100 MiB download through the desktop UI without a partial file being presented as complete. |
| History | If replay is promised, capture an immutable request and environment snapshot with an explicit secret and retention policy. Otherwise keep replay disabled and say why. |
| Import fidelity | Expand fixtures for inherited auth, scripts, disabled/duplicate fields, nested folders, OAuth modes, examples, Postman environments, and OpenAPI YAML. Every unsupported behavior must appear in preview and retained reports. |
| Large-workspace responsiveness | Remove the multi-second full scan from single-request Send/curl preflight only after a targeted resolver proves it uses current request, collection, folder, environment, and stable identities. Measure the end-to-end path. |
| Native polish | Review the current UI in the actual WebView2 release binary at 760×480, 1024×760, and 1280×820 and 100%, 125%, 150%, and 200% scaling. Fix clipping, focus order, text wrapping, download dialogs, clipboard, and visual alignment found there. The latest CSS pass has semantic browser verification, but screenshot capture and native visual review remain outstanding. |

These are product tasks, not checks the user can complete by merely clicking through an installer. If a narrower beta scope is chosen, publish the unsupported list beside the download and retain the blockers for a later production release.

## Verification on the unrestricted host

Run from a clean checkout of the intended commit. Preserve the console output, build logs, screenshots, benchmark results, and the resulting binary/MSI checksums with that commit. Do not rely on older `dist/` or `build/windows/` binaries.

```powershell
go version
go mod verify
go vet ./...
go test ./... -count=1
go build -o build/relay.exe ./cmd/relay
go build -tags desktop,production -ldflags "-s -w" -o build/relay-app.exe ./cmd/relay-app
git diff --check
```

All commands must exit successfully. The latest restricted-host run passed all `-vet=off` packages except `internal/workspace`, whose generated executable was blocked before it ran. Plain `go test ./...` could not start because `vet.exe` was blocked. The workspace test binary did compile. Those results are **not** a passing release suite.

Use a disposable schema-v2 workspace and run this functional matrix in both the browser workbench and fresh native binary:

- Edit a request, force a save failure, and press Send. It must not execute an older saved request. Restore connectivity, retry, and verify the file and CLI execution agree. Close/reopen with an unsaved draft; Recover and Discard must each do exactly what they say.
- Edit a request file externally, including a same-size edit with its timestamp preserved. Check the clean editor refreshes and a dirty editor warns instead of being overwritten. Verify stale saves fail with a conflict.
- Start a slow request A, switch to B, then finish A. A's response must stay with A. Send B again while its older result is visible; progress and the previous-result label must be clear. Cancel a collection run partway through and verify partial results and counts.
- Import each acceptance fixture. Compare preview/report and retained source with the original, then run its key requests in Relay and CI. Confirm no auth/script/field loss is silent.
- Exercise cookies, environment variables, secret availability, custom CA, client certificates, TLS warning state, redirects, and proxy behavior with isolated local fixtures. Never send test secrets to a public endpoint.
- Use keyboard only to search/open/save/send, switch tabs, edit headers, inspect responses, open/close dialogs, and reach every primary control. Verify visible focus, labels, live status, and 760×480 scrolling.
- Install the MSI over an earlier version; confirm the workspace remains intact. Backup the **whole workspace** (TOML files and SQLite history/settings), restore it to another disposable location, and verify requests, environments, history, and drafts. Test multiple instances and sleep/resume.

For the lightweight claim, rerun [the performance harness](RELEASE_PERFORMANCE.md) and the [plan's benchmark matrix](IMPLEMENTATION_PLAN.md) on the exact release build. Record warm/cold startup, 1,000/10,000-request search and open, Send latency, a 100 MiB UI download, 30-minute repeated sends, idle CPU, and attributable native/WebView2 memory. Initial targets are warm editor under 1 second, cold editor under 2 seconds, idle CPU under 1%, and an aim of under 150 MiB private working set. Report misses rather than changing targets after measurement. Do not claim Relay is lighter or faster than Postman without side-by-side measurements on the same host and corpus.

## Release decision

Stop the release for any reproducible data loss, stale request sent after a failed save, response shown under the wrong request, misleading TLS/secret state, silent executable import loss, incomplete download presented as complete, failed test/vet/build, or inaccessible primary workflow. Record each failure with reproduction steps and an owner, fix it, then rerun the affected matrix on the **new exact artifact**.

Only after the above gates pass, use the [Windows release checklist](RELEASE_CHECKLIST.md) to set the version, build and inspect the MSI, calculate checksums, and publish. Keep unsigned builds labeled beta/pre-release and state the supported OS and known limitations in the release notes.
