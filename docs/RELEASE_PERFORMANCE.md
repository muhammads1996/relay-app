# Lightweight release performance checks

Run the local harness from the repository root:

```powershell
$env:GOCACHE = 'C:\Apps\relay-app\test-results\go-cache'
$env:GOTMPDIR = 'C:\Apps\relay-app\test-results\go-temp'
go run ./scripts/perf -sends=1000
```

The harness creates unique, disposable 1,000-request and 10,000-request schema-v2 workspaces under an ignored `test-results/perf-*` directory. For each size it records three `workspace.Open` timings, a cold SQLite index build through `ui.Server.Prepare`, and three warm `Prepare` timings. It then streams a generated 100 MiB response from a local HTTP server to a temporary file with a 1 MiB response-preview cap, and performs sequential small-response sends to report elapsed throughput, post-GC heap change, total allocation delta, and goroutine delta. The 100 MiB downloaded file is removed after its size and transfer completeness are verified; workspace and database fixtures remain under `test-results` for inspection.

The output reports the Go runtime, OS/architecture, logical CPU count, and `PROCESSOR_IDENTIFIER`. It does not report installed physical memory because the current restricted Windows environment does not expose that value. This is a local repeatable baseline, not a platform-independent release budget: compare runs only on the same machine, OS image, Go version, and broadly similar load. It does not measure UI rendering, native desktop RSS, startup packaging, or another API client's performance.

## Baseline recorded on 2026-09-29

Host: Windows 10 Pro, Go 1.27.1, `windows/amd64`, 14 logical CPUs, processor identifier `Intel64 Family 6 Model 181 Stepping 0, GenuineIntel`. The exact model name and physical-memory total were unavailable in this restricted environment.

Host details were captured with `$env:PROCESSOR_IDENTIFIER`, `[Environment]::ProcessorCount`, `go version`, and `Get-ComputerInfo -Property WindowsProductName,WindowsVersion,OsBuildNumber,CsTotalPhysicalMemory`; the latter did not expose the build or memory fields in this environment.

Exact PowerShell command (workspace-local build cache and temp directory):

```powershell
$env:GOCACHE = 'C:\Apps\relay-app\test-results\go-cache'
$env:GOTMPDIR = 'C:\Apps\relay-app\test-results\go-temp'
go run ./scripts/perf -sends=1000
```

Recorded output directory: `test-results/perf-2149126793`.

| Check | Result |
|---|---:|
| 1,000-request `workspace.Open` (three runs) | 1,533.76 / 702.53 / 461.37 ms |
| 1,000-request cold SQLite index (`Prepare`) | 2,524.14 ms |
| 1,000-request warm `Prepare` (three runs) | 568.39 / 665.00 / 854.92 ms |
| 10,000-request `workspace.Open` (three runs) | 10,531.09 / 7,419.76 / 6,711.78 ms |
| 10,000-request cold SQLite index (`Prepare`) | 61,217.76 ms |
| 10,000-request warm `Prepare` (three runs) | 9,611.73 / 9,874.88 / 8,940.14 ms |
| Local 100 MiB streamed download | 0.502 s; 199.15 MiB/s; 1 MiB buffered preview; complete |
| Sequential repeat sends | 1,000 in 1.177 s (849.83 sends/s) |
| Post-GC heap delta / total allocation delta | +123,392 bytes / 59,921,688 bytes |
| Goroutine delta after repeat sends | 0 |

These values are one local baseline only. The first `Open` is slower because it includes cold filesystem/cache effects, and index timings are sensitive to SQLite and disk state. Keep the raw console output with the release run; each invocation prints its generated fixture directory. Do not treat these results as a budget or compare them to Postman or native application memory usage.

## Reconciliation optimization check on 2026-09-29

The same host and harness were used after batching request index changes in one SQLite transaction, skipping serialization of unchanged requests, and reading each request TOML file once for parsing and hashing. The harness now also measures one `/api/state` response. The 10,000-request fixture is `test-results/perf-2396248650`; a fresh generated fixture and changing OS file cache mean the individual `Open` samples are not a controlled before/after comparison.

| 10,000-request check | Before | After |
|---|---:|---:|
| `workspace.Open` (three runs) | 10,531 / 7,420 / 6,712 ms | 13,019 / 9,162 / 4,976 ms |
| Cold SQLite index (`Prepare`) | 61,218 ms | 8,133 ms |
| Warm `Prepare` (three runs) | 9,612 / 9,875 / 8,940 ms | 6,918 / 7,141 / 8,618 ms |
| `/api/state` | not recorded | 9,898 ms |

The cold index improved by about 7.5×, but full workspace scans still dominate warm opening and repeated state reads. A 9.9-second state response is a usability blocker for a large workspace. Further profiling and a safe refresh strategy are required before treating the 10,000-request path as release ready. The 100 MiB stream remained complete with a 1 MiB preview; 1,000 local sends in this run took 2.178 seconds, ended with zero extra goroutines, and had a post-GC heap delta of about 152 KiB. These send timings are not an apples-to-apples speedup claim because host load varied.

A subsequent bounded eight-worker request-file loader preserved file hashes and deterministic traversal order. Its 10,000-request fixture is `test-results/perf-2183907186`: `workspace.Open` took 6,042 / 3,831 / 3,975 ms, cold `Prepare` 5,375 ms, warm `Prepare` 4,421 / 3,562 / 3,695 ms, and `/api/state` 8,024 ms. A CPU profile attributed almost all loader time to filesystem traversal and OS file opens; TOML decoding and SQLite were small shares. The state endpoint still performs a full workspace refresh and remains too slow for a responsive 10,000-request workbench. The 100 MiB stream completed at 188 MiB/s in this run; it used only one repeat send, so its send throughput is not comparable with the 1,000-send runs.

## Cached state and refresh status on 2026-09-29

The v2 state endpoint now serializes the last fully reconciled workspace snapshot and its stable-ID maps without running a full filesystem scan for each GET. It starts at most one full scan at a time, with a five-second minimum interval. `GET /api/workspace/refresh-status` reports `generation`, `scanning`, `lastScan`, and a safe `error` message, and also schedules a due scan. `POST /api/workspace/refresh` waits for an in-flight scan or runs one immediately. Successful workspace writes retain their on-disk content-hash checks, so changes made after a background scan are rejected as conflicts.

The same Windows host and harness measured `/api/state` at 72.76 ms for 10,000 requests, down from 8,024 ms in the preceding run (about 110× faster). The fixture was `test-results/perf-1928704759`; `workspace.Open` was 3,642 / 4,615 / 4,407 ms, cold `Prepare` 5,732 ms, and warm `Prepare` 3,645 / 3,947 / 3,721 ms. The full scan still costs several seconds, but it runs separately from state serialization, exposes progress and errors, and can be requested synchronously. These are local measurements from generated workspaces, not comparisons with Postman or native app memory use.

The request-open endpoint now reads the selected file once and returns the content hash of those exact bytes. A live localhost check on the earlier 10,000-request fixture returned request 1 in 18.4 / 8.7 / 4.7 ms, and in 11.9 ms while a background scan was active. Those figures include PowerShell HTTP and JSON handling and are single-request samples, not a UI interaction budget. A browser check rendered the first 300 requests with a Show more row, found request 10,000 through search, and opened it successfully. The first client-side state parse and render, full-scan cost, native startup, and send latency still need dedicated release measurement.

Single-request Send and curl export still require full reconciliation before resolving collection, folder, and environment values. The warm `Prepare` samples above (3,645 / 3,947 / 3,721 ms) approximate that preflight cost on 10,000 requests; they do not include request execution. Skipping the scan without a targeted resolver would risk sending with stale variables or a changed file identity, so this remains a latency gate.
