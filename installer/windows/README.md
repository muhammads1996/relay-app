# Relay Windows MSI

The Relay MSI installs the desktop workbench for the current Windows user.

## Installed behavior

- Installs `relay-app.exe` under `%LOCALAPPDATA%\Programs\Relay`.
- Adds a **Relay** Start Menu shortcut.
- Appears as **Relay** in Windows installed-apps management.
- Supports major upgrades and blocks accidental downgrades.
- Uninstalls the application but preserves the user workspace and `relay.db` under the operating-system app-data directory.
- Does not install the separate `relay.exe` CLI or add anything to `PATH`.

The desktop app uses the Windows system WebView2 runtime. It is present on supported, updated Windows 10/11 systems; machines without it must install the runtime separately.

## Build

From the repository root:

```powershell
pwsh -File scripts/build-windows-msi.ps1
```

The script reads `VERSION`, restores the repository-pinned WiX tool, builds the Wails desktop executable, creates a self-contained MSI, validates the package through a Windows Installer administrative extraction, and writes:

```text
dist/Relay-<version>-windows-x64.msi
dist/Relay-windows-x64.msi
dist/SHA256SUMS-windows.txt
```

Use the versioned file for archival releases and the stable asset name for the website's versioned prerelease link.

Override the version when preparing a release:

```powershell
pwsh -File scripts/build-windows-msi.ps1 -Version 0.3.1
```

## Signing

An unsigned MSI installs, but Windows identifies its publisher as unknown. For a public download, sign both the embedded executable and the final MSI with an Authenticode code-signing certificate:

```powershell
pwsh -File scripts/build-windows-msi.ps1 `
  -CertificateThumbprint "<certificate thumbprint>" `
  -TimestampUrl "http://timestamp.acs.microsoft.com"
```

The script discovers `signtool.exe` from `PATH`. Pass `-SignToolPath` when the Windows SDK tool is installed elsewhere.

## Website URL

After the tag workflow publishes the beta GitHub prerelease, the marketing-site install button for version `0.3.0` can point to:

```text
https://github.com/muhaymien96/relay/releases/download/v0.3.0/Relay-windows-x64.msi
```

GitHub's `releases/latest` URL excludes prereleases, so keep the explicit beta tag in this link. Do not deploy the public button until the prerelease exists. Clearly label unsigned beta artifacts if code signing has not been configured.

## Packaging tool

The build pins WiX Toolset `4.0.6`, whose NuGet package is published under the Microsoft Reciprocal License. Newer WiX binaries use separate OSMF terms, so upgrade the pinned tool only after reviewing the applicable license. Relay's generated MSI does not install or redistribute WiX itself.
