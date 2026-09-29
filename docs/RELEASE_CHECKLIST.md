# Relay Windows Installer Setup Checklist

> This checklist describes installer configuration, not product release readiness. Complete the [production-readiness handoff](PRODUCTION_READINESS_HANDOFF.md) and review the [current implementation status](IMPLEMENTATION_STATUS_2026-09-29.md) before creating a release tag.

## ✅ Configuration Status

### Core Infrastructure
- [x] **GitHub Workflow**: `.github/workflows/release-windows.yml` configured
  - Auto-triggers on `git tag v*`
  - Creates GitHub Releases with prerelease flag
  - Uploads MSI, stable MSI, and SHA256SUMS
  - Optional code signing (secrets-based, conditional)

- [x] **MSI Build Script**: `scripts/build-windows-msi.ps1`
  - Builds signed or unsigned binaries
  - Generates SHA256 checksums
  - Validates MSI via Windows Installer extraction
  - Graceful handling of unsigned builds

- [x] **WiX Configuration**: `installer/windows/Relay.wxs`
  - Per-user installation (`%LOCALAPPDATA%\Programs\Relay`)
  - Start Menu shortcut
  - Major upgrade support
  - Preserves workspace database on uninstall

- [x] **Version File**: `VERSION` (currently 0.3.0)
  - Single source of truth for release version
  - Validated by GitHub workflow

### Documentation
- [x] `docs/INSTALLER_SETUP.md` – Complete production setup guide
  - Free unsigned release workflow
  - Code signing certificate integration
  - Local build instructions
  - Marketing site integration examples
  - Troubleshooting

- [x] `docs/MARKETING_DOWNLOAD_SNIPPETS.html` – Copy-paste website code
  - 5 options from simple link to auto-versioned components
  - System requirements
  - Verification instructions
  - Pre-formatted and tested

### CI/CD Integration
- [x] Tests run before packaging (`go test ./...`)
- [x] Format check before build (`gofmt`)
- [x] Module verification (`go mod verify`)
- [x] Vet check (`go vet ./...`)
- [x] Signing is conditional and optional (no failure if secrets missing)

---

## 🚀 Release procedure after product gates pass

Complete the [implementation status gates](IMPLEMENTATION_STATUS_2026-09-29.md) and verify the exact release candidate on a supported native Windows host before following these steps. A passing packaging workflow alone does not establish product readiness.

### For Unsigned Beta Release (Recommended for Now)

**Step 1**: Update version
```powershell
"0.3.1" | Set-Content VERSION
git add VERSION
git commit -m "chore: bump to v0.3.1"
```

**Step 2**: Create and push tag
```powershell
git tag v0.3.1
git push origin main --tags
```

**Step 3**: Wait for workflow
- GitHub Actions runs automatically
- Check `.github/workflows/release-windows.yml` in Actions tab
- Workflow creates prerelease on GitHub (usually 3–5 min)

**Step 4**: Get marketing URL
```
https://github.com/muhammadi1996/relay/releases/download/v0.3.1/Relay-windows-x64.msi
```

**Step 5**: Update marketing site
```html
<a href="https://github.com/muhammadi1996/relay/releases/download/v0.3.1/Relay-windows-x64.msi">
  Download Relay (Windows Installer)
</a>
```

---

## 🔒 Optional: Code Signing (Later)

When you're ready to sign releases (usually after a few beta cycles):

1. **Acquire certificate**: DigiCert, Sectigo, or GoDaddy (~$250–500/year)
2. **Create GitHub secrets**:
   - `WINDOWS_SIGNING_CERTIFICATE_BASE64` (base64-encoded .pfx)
   - `WINDOWS_SIGNING_CERTIFICATE_PASSWORD` (.pfx password)
3. **Workflow auto-signs on next release**
   - No code changes needed
   - SmartScreen will trust future releases

See `docs/INSTALLER_SETUP.md` → **Production: Code-Signed Release** for detailed steps.

---

## ✨ What Users Get

### Unsigned Release (Today)
- ⚠️ Desktop build configured; product release gates remain open
- ✅ Free, no certificate needed
- ⚠️ Supported Windows versions and native WebView2 behavior still require release-candidate verification
- ⚠️ SmartScreen shows "unknown publisher" (one-time) — fixed by signing later
- 📝 Clear warning on GitHub release page

### Signed Release (With Certificate)
- ✅ SmartScreen trusts it (no warning)
- ✅ Signature is verified by Windows
- ✅ Shows your company/project as publisher
- ⚠️ Requires annual certificate cost

---

## 🔍 Testing the Setup Locally (Optional)

If you want to build and test the MSI without GitHub:

```powershell
# Build unsigned MSI
pwsh -File scripts/build-windows-msi.ps1

# Outputs to dist/:
# - Relay-0.3.0-windows-x64.msi (versioned)
# - Relay-windows-x64.msi (stable name)
# - SHA256SUMS-windows.txt

# Test installation
msiexec /i dist\Relay-windows-x64.msi /quiet

# Verify
Get-ChildItem HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall |
  Select-Object -ExpandProperty DisplayName | Select-String Relay
```

---

## 📋 Pre-Release Checklist

Before tagging a release:

```bash
# 1. Format and verify Go code
gofmt -w $(git ls-files -- '*.go')
git diff --check
go mod verify
go vet ./...

# 2. Run tests
go test ./...

# 3. Build locally and smoke test the exact candidate
go build -o relay ./cmd/relay
.\relay version

# 4. Update VERSION
"0.3.1" | Set-Content VERSION

# 5. Commit and tag
git add VERSION
git commit -m "chore: bump to v0.3.1"
git tag v0.3.1
git push origin main --tags
```

---

## ❓ FAQ

**Q: Can users update Relay after installing the MSI?**  
A: Not yet (pre-release). After each release, users download the new MSI and run it; the installer detects the old version and upgrades, preserving the workspace. Auto-update could be added later.

**Q: Does the MSI require admin rights?**  
A: No. It installs per-user under `%LOCALAPPDATA%\Programs\Relay`, so end users don't need admin.

**Q: What about the CLI (`relay.exe`)?**  
A: The MSI only includes the desktop app (`relay-app.exe`). CLI users can:
- Download portable `relay.exe` from the same release page
- Or run `go install github.com/muhammadi1996/relay/cmd/relay@latest`

**Q: What if I mess up the version?**  
A: You can re-run the workflow with `workflow_dispatch` and specify a different tag. See `docs/INSTALLER_SETUP.md` → Troubleshooting.

**Q: How do I know the MSI is safe?**  
A: 
- Compare SHA256 checksum from the release page against your downloaded file
- For signed releases, verify with `Get-AuthenticodeSignature`
- Source code is on GitHub — anyone can inspect it

---

## 📞 Support References

- **Installation issues**: `installer/windows/README.md`
- **Build commands**: `docs/DISTRIBUTION.md`
- **CI/CD integration**: `docs/ci-cd.md`
- **Full setup guide**: `docs/INSTALLER_SETUP.md`
- **Marketing site examples**: `docs/MARKETING_DOWNLOAD_SNIPPETS.html`

---

**Installer configuration last verified**: 2026-08-04

**Release workflow status**: Configured; product release gates remain open.

**Next action**: Complete and record the [open release gates](IMPLEMENTATION_STATUS_2026-09-29.md#open-release-gates) before changing `VERSION` or pushing a release tag.
