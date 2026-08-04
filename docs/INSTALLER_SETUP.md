# Relay Installer & Release Setup

This guide covers building, signing, and publishing the Relay Windows MSI installer through GitHub Releases for your marketing site.

## Quick Start: Free Unsigned Release

For a quick beta release without code signing:

1. **Update VERSION**:
   ```powershell
   "0.3.1" | Set-Content VERSION
   git add VERSION
   git commit -m "chore: bump to v0.3.1"
   git tag v0.3.1
   git push origin main --tags
   ```

2. **GitHub Release Workflow Auto-publishes**:
   - CI workflow tests and builds the MSI
   - Creates a GitHub prerelease with the tag
   - Uploads `Relay-windows-x64.msi` and SHA256SUMS
   - ✅ No secrets or code signing certificate needed

3. **Get Download URL**:
   ```text
   https://github.com/muhammads1996/relay/releases/download/v0.3.1/Relay-windows-x64.msi
   ```

   **Important**: GitHub's `releases/latest` redirects exclude prereleases, so **always use the explicit version tag** in marketing links.

4. **Add to Marketing Site**:
   ```html
   <a href="https://github.com/muhammads1996/relay/releases/download/v0.3.1/Relay-windows-x64.msi">
     Download Relay (Windows Installer)
   </a>
   ```

   Include a note: *"Beta release — unsigned. Windows SmartScreen may warn on first run. [Learn more](https://github.com/muhammads1996/relay/blob/main/installer/windows/README.md#windows-protected-your-pc-smartscreen)."*

## Production: Code-Signed Release

When ready to sign releases with an Authenticode certificate:

### 1. Acquire a Code Signing Certificate

Options (costs vary):
- **DigiCert** (recommended): ~$300–500/year, works globally
- **Sectigo**: ~$250–400/year
- **GoDaddy**: ~$80–120/year (lower-cost option)

Requirements:
- Validating certificate (NOT self-signed)
- `.pfx` format with private key
- Password protected

### 2. Configure GitHub Secrets

In your GitHub repository settings (`Settings` → `Secrets and variables` → `Actions`):

#### Secret 1: `WINDOWS_SIGNING_CERTIFICATE_BASE64`

Convert your `.pfx` certificate to base64:

```powershell
# Windows PowerShell
$pfx = [System.IO.File]::ReadAllBytes("C:\path\to\relay-signing.pfx")
$base64 = [Convert]::ToBase64String($pfx)
$base64 | Set-Clipboard
# Paste into GitHub secret
```

```bash
# macOS/Linux
base64 -i /path/to/relay-signing.pfx | pbcopy  # macOS
base64 -i /path/to/relay-signing.pfx | xclip   # Linux
# Paste into GitHub secret
```

#### Secret 2: `WINDOWS_SIGNING_CERTIFICATE_PASSWORD`

Set this to the password protecting your `.pfx` file.

### 3. Trigger a Signed Release

```powershell
# Update VERSION
"0.3.1" | Set-Content VERSION
git add VERSION
git commit -m "chore: bump to v0.3.1"
git tag v0.3.1
git push origin main --tags
```

The workflow will:
1. Detect the secrets
2. Import your certificate (cleaned up after)
3. Sign `relay-app.exe` with Authenticode
4. Sign the MSI
5. Verify the signatures
6. Publish to GitHub Releases

**Result**: Users will see your company/Relay as the publisher, SmartScreen will trust the binary, and signature verification shows `✓ Signed`.

## Building Locally (Without CI)

### Unsigned Local Build

```powershell
pwsh -File scripts/build-windows-msi.ps1
# Creates: dist/Relay-0.3.0-windows-x64.msi
```

### Signed Local Build (if you have the certificate)

```powershell
pwsh -File scripts/build-windows-msi.ps1 `
  -CertificateThumbprint "ABC123DEF456..." `
  -SignToolPath "C:\Program Files (x86)\Windows Kits\10\bin\x64\signtool.exe"
```

To find your certificate thumbprint:
```powershell
Get-ChildItem Cert:\CurrentUser\My | Format-Table -Property Thumbprint, Subject
```

## Validating Your Release

### Verify SHA256 Checksums

After download:
```powershell
(Get-FileHash Relay-windows-x64.msi -Algorithm SHA256).Hash
# Compare against GitHub release's SHA256SUMS-windows.txt
```

### Check Signature (Signed Releases Only)

```powershell
Get-AuthenticodeSignature Relay-windows-x64.msi
# Should show Status: Valid, with your signer info
```

### Test Installation

```powershell
# Silent install
msiexec /i Relay-windows-x64.msi /quiet /norestart

# Verify it appears in installed apps
Get-ChildItem HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall |
  Select-Object -ExpandProperty DisplayName | Select-String Relay
```

## GitHub Release Asset Lifecycle

| Status | SmartScreen | URL | Use Case |
|--------|-------------|-----|----------|
| **Unsigned Prerelease** | ⚠️ "Windows protected" | `v0.3.0` tag → `releases/latest` **excludes** | Beta testing, dev preview |
| **Signed Release** | ✅ Trusted | `v0.3.0` tag → `releases/latest` **includes** | Production download |

**Key URL patterns**:
- **Versioned**: `https://github.com/muhammads1996/relay/releases/download/v0.3.1/Relay-windows-x64.msi`
- **Latest (signed only)**: `https://github.com/muhammads1996/relay/releases/latest/download/Relay-windows-x64.msi`
- **Always versioned for marketing** to avoid surprises when you push a new release.

## Marketing Site Integration

### Simple Download Button

```html
<a href="https://github.com/muhammads1996/relay/releases/download/v0.3.1/Relay-windows-x64.msi" 
   class="btn btn-primary">
  Download Relay (Windows Installer)
</a>
<small>
  Requires Windows 10/11 with WebView2 runtime (auto-installed on modern systems).
  <a href="https://learn.microsoft.com/en-us/microsoft-edge/webview2/get-started/system-requirements">Learn about system requirements</a>.
</small>
```

### With Version Display

```html
<div class="installer-section">
  <h3>Install Relay</h3>
  <p>
    <a href="https://github.com/muhammads1996/relay/releases/download/v0.3.1/Relay-windows-x64.msi">
      Download for Windows (v0.3.1)
    </a>
  </p>
  <details>
    <summary>Other options</summary>
    <ul>
      <li><a href="https://github.com/muhammads1996/relay/releases/tag/v0.3.1">View all release assets</a> (SHA256 checksums, CLI tools)</li>
      <li><a href="https://github.com/muhammads1996/relay">GitHub repository</a> (build from source)</li>
    </ul>
  </details>
</div>
```

### With Auto-Version Detection

Use GitHub's JSON API to embed the latest signed release link (client-side or build-time):

```javascript
// Fetch latest signed release
fetch('https://api.github.com/repos/muhammads1996/relay/releases')
  .then(r => r.json())
  .then(releases => {
    const signed = releases.find(r => !r.prerelease);
    if (signed) {
      const msi = signed.assets.find(a => a.name.endsWith('.msi'));
      document.getElementById('download-link').href = msi.browser_download_url;
      document.getElementById('version').textContent = signed.tag_name;
    }
  });
```

```html
<a id="download-link" href="https://github.com/muhammads1996/relay/releases">
  Download Relay (<span id="version">Latest</span>)
</a>
```

## Troubleshooting

### "Windows protected your PC" on Unsigned Builds

This is expected for unsigned binaries. Tell users:
1. Click **"More info"** → **"Run anyway"** (safe, we built it)
2. Or [build from source](https://github.com/muhammads1996/relay) for transparency
3. Or wait for a signed release (requires code signing certificate)

### Workflow Dispatch Not Working

If manual trigger via `workflow_dispatch:` fails, ensure:
1. You're pushing to `main` branch
2. Tag format is exactly `v0.3.1` (no extra text)
3. `VERSION` file matches the tag (e.g., `0.3.1`)

Run manually:
```bash
gh workflow run release-windows.yml -f tag=v0.3.1
```

### MSI Installation Fails

Check Windows Installer logs:
```powershell
Get-ChildItem $env:TEMP\MSI*.log -Filter '*Relay*' |
  Sort-Object LastWriteTime -Descending |
  Select-Object -First 1 |
  Get-Content -Tail 20
```

Common issues:
- Missing WebView2 runtime → User installs from [Microsoft](https://go.microsoft.com/fwlink/p/?LinkId=2124703)
- Corrupted download → Re-download and verify SHA256
- Previous version locked → Uninstall, restart, reinstall

## See Also

- [DISTRIBUTION.md](DISTRIBUTION.md) – Low-level build commands and local signing
- [installer/windows/README.md](../installer/windows/README.md) – MSI packaging and signing details
- [.github/workflows/release-windows.yml](../.github/workflows/release-windows.yml) – CI workflow source
