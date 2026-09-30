# Install and Start Relay

Relay is beta software. Use a disposable workspace for evaluation and back up the complete workspace before upgrading. A successful build is not production approval; see the [current review](docs/REVIEW_2026-09-30.md) for verified behavior and remaining acceptance work.

## Install on Windows

1. Ensure Microsoft Edge WebView2 Runtime is available. On a managed device, ask IT to provide it if missing.
2. Open the [GitHub releases page](https://github.com/muhammadi1996/relay/releases), choose a beta version, and read its release notes.
3. Download `Relay-windows-x64.msi` and `SHA256SUMS-windows.txt` from the same release. Compare the MSI's hash with the published entry before opening it.
4. Run the installer, then open **Relay** from the Start Menu. It installs for the current user under `%LOCALAPPDATA%\Programs\Relay` without requiring administrator access.

To calculate the hash, run this from the download directory:

```powershell
Get-FileHash .\Relay-windows-x64.msi -Algorithm SHA256
```

The MSI contains the desktop app, not the CLI. Unsigned downloads may trigger Windows security warnings. A checksum checks file integrity, not publisher identity; follow your organization's software policy and do not bypass an administrative block.

## Build and Evaluate from Source

Use Go 1.25 and PowerShell from the repository root. Close any Relay processes using these output binaries before rebuilding.

```powershell
go mod verify
go build -o build/relay.exe ./cmd/relay
go build -tags desktop,production -ldflags "-s -w -H windowsgui" -o build/relay-app.exe ./cmd/relay-app
```

For first-time evaluation, create a new copy of the sample collection. Choose a different directory if this one already contains work you want to keep:

```powershell
New-Item -ItemType Directory -Path build/local-evaluation -ErrorAction Stop
Copy-Item examples/aml-demo/* build/local-evaluation -Recurse
.\build\relay-app.exe --workspace .\build\local-evaluation
```

The sample collection is not a mock API. Configure an endpoint you control and the required environment values before sending requests. Do not put real credentials into the sample files.

To open the same copied collection in a browser, close the desktop app first, then run:

```powershell
.\build\relay.exe ui .\build\local-evaluation --port 7717
```

Open the URL printed in the terminal. If port 7717 is in use, use `--port 0` to select a free port. Keep the terminal running while using the browser workbench. Automated browser tests modify requests; follow the separate [UI audit setup](tests/e2e/README.md) for an expendable test workspace.

## Choose and Protect a Workspace

The desktop app accepts `--workspace <directory>` or the `RELAY_WORKSPACE` environment variable. With neither set, Windows uses `%APPDATA%\Relay`. The directory contains the local database and any workspace files; it is separate from the installation directory.

Storage behavior depends on the workspace format:

- A schema-v2 workspace has a `workspace.toml` marker. Request and environment edits update canonical TOML files.
- An unmarked legacy workspace seeds a new database from its files, but later workbench edits do not automatically update the CLI files.
- Test Management definitions, history, settings, and recovery drafts remain in SQLite. A request-file CLI run is not equivalent to every UI test case.

Do not add a marker manually to convert a workspace. Read the [workspace and storage guide](docs/API_CLIENT_GUIDE.md#1-workspace-and-storage) before migration.

Before an upgrade or backup, close Relay windows and browser servers that use the workspace, then copy the entire workspace directory to a protected location. Literal credentials in files, databases, drafts, and backups are not encrypted. Prefer `RELAY_SECRET_*` references; see [environment and secret handling](README.md#environments-and-secrets).

Uninstalling the MSI preserves workspace data. Do not delete the workspace unless you intend to remove that data.

## Troubleshooting

| Symptom | Action |
| --- | --- |
| `unable to open database file` | Check that the workspace and the parent of a custom `--db` path exist and are writable. Never keep a live audit database under `test-results`; Playwright cleans that directory. |
| Browser cannot connect | Use the URL printed by the running server. A page left open from an earlier session may point at a stopped server or a different workspace. |
| Build cannot replace an executable | Close the desktop app or CLI server using that binary, then rebuild. |
| Desktop window does not open | Confirm WebView2 is installed and consult local security policy. Use the browser workbench to isolate a native-runtime problem. |
| CLI results differ from the workbench | Check the workspace format, selected environment, and whether the tests are request-file tests or SQLite Test Management definitions. |

## Next Steps

- [API Client Guide](docs/API_CLIENT_GUIDE.md): daily request and test workflows.
- [Current Review](docs/REVIEW_2026-09-30.md): capabilities verified and release gates still open.
- [Distribution Guide](docs/DISTRIBUTION.md): portable builds and packaging.
- [Installer Setup](docs/INSTALLER_SETUP.md): maintainer-only installer and release automation.
