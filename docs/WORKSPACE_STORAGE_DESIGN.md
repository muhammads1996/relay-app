# Canonical workspace storage and migration design

Decision for [T0.3](IMPLEMENTATION_PLAN.md): editable request, environment, and test definitions live in versioned files. SQLite holds history, runs, indices, UI preferences, and migration state. The existing `*.req.toml`, `collection.toml`, `folder.toml`, and `environments/*.toml` syntax remains valid. The UI and CLI must load definitions through one package; SQLite copies are never authoritative after migration.

## Layout and identity

```text
workspace/
  workspace.toml                 # schema_version = 2; workspace_id; ordered collection IDs
  collections/
    <slug>--<collection-id>/
      collection.toml            # id, name, headers, vars, auth defaults
      <folder>/folder.toml       # id, name, inherited settings
      <folder>/<name>.req.toml   # id, name, method, URL, spec
      environments/<name>.toml   # id, name, vars, secret names only
  tests/
    folders/<id>.toml            # test folder metadata
    cases/<id>.toml              # id, request_id, optional override, assertions, script
    sets/<id>.toml               # ordered test IDs
    executions/<id>.toml         # saved execution definition and scope
  relay.db                       # local state; ignored by Git
```

Use lowercase UUIDs for new stable IDs, generated once and stored inside files. IDs are independent of names and paths, so rename/move does not break a test link. An existing integer SQLite ID maps to one deterministic UUID during migration; store that map in migration metadata for history and API compatibility. Do not generate IDs afresh on every load. Reserve a unique ID for workspace, each collection/folder/request/environment/test folder/case/set/execution. Reject duplicate IDs and references to missing IDs with a recoverable diagnostic. A linked test refers to `request_id`, not a path or request name. Moving a request across collections preserves its ID and test links while changing inheritance according to the destination.

`workspace.toml` is the explicit marker for schema 2. A directory without it remains a legacy single collection, readable by the current CLI. `relay run <collection-path>` continues to work with legacy and versioned collection folders. `relay run <workspace-path>` requires an explicit collection selector when more than one exists; never choose one silently. Collection ordering and request ordering are explicit fields in versioned definitions, rather than inferred only from lexical filenames. Legacy lexical order stays unchanged until migrated. A new loader resolves body file paths relative to the request file or an explicit workspace root and rejects paths outside the allowed workspace unless the user deliberately selects an external file.

Schema versions are integers. `workspace.toml` declares the workspace schema, and individual files may declare their own `schema_version` when their shape changes; omitted version means legacy v1. Readers accept v1 and v2, reject a future version without writing anything, and preserve unknown fields or stop with an actionable error. Keep environment secret *names* in TOML; secret values belong in OS credentials or `RELAY_SECRET_*`, never exported to the Git tree. Xray credentials and OAuth tokens never enter definition files.

## Write and recovery protocol

Use one workspace writer lock across UI handlers and the desktop process. For each save, capture stable ID, expected content hash, and editor revision. Validate and serialize deterministically, write a same-directory temporary file, flush it to disk, then atomically replace the target and sync the containing directory where supported. Return the new hash/revision only after success. A failed write leaves the old file intact and the draft dirty. Windows replacement semantics need a tested implementation and bounded retry for antivirus sharing violations.

For a rename/move touching more than one path, record an operation journal under local `.relay/transactions/`, stage new files in the destination filesystem, validate the complete resulting workspace, then commit and remove old paths. On startup, finish or roll back an incomplete journal before loading definitions. Index updates occur after file commit; on failure, rebuild the SQLite index from files. A deletion first moves the file to local `.relay/trash/` with a timestamp and stable ID; undo restores it. The trash and journals are not committed to Git.

Maintain a last-known-good workspace snapshot before every schema migration and before a bulk import. Create the backup using SQLite's online backup API so WAL data is included; copy definition files into a sibling staging directory with checksums. Do not manipulate a live `relay.db` by raw file copy. Show backup location and a restore action. A failed migration leaves the old workspace usable. Do not remove the backup automatically until a later, explicit retention policy is implemented.

## SQLite to files migration

1. Detect `workspace.toml`. If absent and `relay.db` contains authored records, offer a migration preview listing collections, requests, environments, tests, presets, and unsupported/lossy fields. If the directory is a legacy file collection without DB records, load it as v1 and make upgrading explicit.
2. Make a checked backup, then write all v2 definitions to a sibling staging directory. Use deterministic ID mapping for current integer IDs and persist the mapping. Export nested collection/folder hierarchy without flattening names. Preserve request body paths when valid; report unresolved paths rather than silently clearing them. Preserve test case overrides, assertions, scripts, sets, and execution scopes. Keep run results/history only in SQLite.
3. Load the staged tree with the same loader used by UI and CLI. Compare semantic counts, references, inheritance, and executable request/test behavior against the database snapshot. Show any unresolved differences in the preview and block automatic commit if executable behavior would change without an explicit resolution.
4. Commit with an atomic marker/manifest transition and record migration version in SQLite. Rebuild indices from files. On restart, resume or roll back a journaled interrupted migration. Never seed the database over newer files.

Presets currently live in SQLite and can affect effective headers. Migrate their definitions and attachments to files or materialize them with a clearly documented inheritance rule before declaring UI/CLI parity. Their secret flagged values must stay out of Git. Execution `last_summary`, run status, and timestamps belong in SQLite; the saved execution selection and filters belong in TOML. If a test references a missing request after migration, quarantine the test with its original source and report it; do not delete it.

## External edits and Git branch switches

The editor tracks a base hash for each open definition. A file watcher plus a rescan on window focus and before save compares current disk hash with the base. If the tab is clean, reload it and retain selection. If the tab is dirty, preserve the draft and show a three-way comparison of base, disk, and draft with Reload, Keep draft as new copy, or Resolve merge. Never overwrite disk on a stale hash or discard the draft from a timer. A deleted or renamed file remains as a recoverable draft until the user resolves it. Branch switches may change many files: validate the whole new tree, report broken references, and do not write automatic migrations during the switch. UI sends capture a saved revision; an unresolved conflict blocks sending a stale disk request under the visible draft.

## Fixture and acceptance matrix for implementation

Build fixtures under `internal/store/testdata/workspace/` (or a shared testdata package) and test through the public loader, save API, and CLI runner:

| Fixture | Expected check |
|---|---|
| Legacy `examples/aml-demo` | Same lexical run order and effective HTTP requests; load without `workspace.toml`; explicit upgrade yields equal behavior. |
| SQLite v1 with two collections, nested folders, environments, presets, request scripts and test case overrides/sets/executions | Migrated counts and stable IDs match; all links resolve; CLI and UI execute equal effective requests/assertions; history survives in DB. |
| Duplicate names, rename and cross-collection move | IDs and links remain stable; destination inheritance applies; files produce a reviewable Git diff. |
| Missing/duplicate IDs and a future schema version | Loader gives actionable errors and performs no write. Original files remain intact. |
| Secret names, auth fields, Xray credentials and token fixtures | No secret value appears in TOML, backup preview, log, or Git diff; credential references still resolve locally. |
| Commented and externally edited TOML | Save preserves comments/unknown fields where supported; otherwise warns before a rewrite and provides the original in backup. Dirty tab versus disk produces a conflict, with both variants recoverable. |
| Unwritable directory, full disk simulation, crash after staging, crash after file commit before index update | No truncated file, no clean status for failed save, no duplicate migration; recovery produces one coherent tree and rebuilt index. |
| Git branch switch while a draft is open | Clean tabs reload; dirty draft survives; stale save/send is blocked until resolved. |

Measure v2 load/search against 1,000 and 10,000 request fixtures. Keep file and SQLite changes separable so the cache can be rebuilt without modifying canonical definitions. The implementation is complete only when a saved UI edit changes a file and the CLI run uses that exact saved definition without an export step.
