# 🚀 Relay Installer: Quick Start for Marketing

**Your installer is ready to use. Here's the minimum you need to do.**

---

## In 3 Minutes: Get Your First Release Live

### 1. Create Release Tag (from repository root)

```powershell
# Verify current version
(Get-Content VERSION).Trim()  # Should be 0.3.0 (or your version)

# Create a tag for this version
git tag v0.3.0
git push origin main --tags
```

### 2. Wait for GitHub Actions (3–5 min)

- GitHub automatically builds and publishes the MSI
- Check: https://github.com/muhammadi1996/relay/actions
- Look for **Release Windows MSI** workflow

### 3. Get Your Download URL

Once the workflow succeeds, your download link is:

```
https://github.com/muhammadi1996/relay/releases/download/v0.3.0/Relay-windows-x64.msi
```

**Replace `v0.3.0` with whatever version you tagged.**

---

## On Your Marketing Site

Add this button:

```html
<a href="https://github.com/muhammadi1996/relay/releases/download/v0.3.0/Relay-windows-x64.msi"
   class="btn btn-primary">
  Download Relay for Windows
</a>
```

Or visit `docs/MARKETING_DOWNLOAD_SNIPPETS.html` for 5 ready-to-use options.

---

## Important Notes

✅ **No secrets needed** — The release workflow works without any GitHub secrets configured. The MSI will be unsigned, which is fine for beta releases.

⚠️ **Use the versioned URL** — Always include the version tag (`v0.3.0`), not `releases/latest`. GitHub's "latest" excludes prereleases.

📖 **Learn more** — Full guide at `docs/INSTALLER_SETUP.md`

---

## Next Release

When you're ready to ship v0.3.1:

```powershell
# 1. Update version file
"0.3.1" | Set-Content VERSION

# 2. Commit it
git add VERSION
git commit -m "chore: bump to v0.3.1"

# 3. Tag and push
git tag v0.3.1
git push origin main --tags

# 4. Update marketing link to:
# https://github.com/muhammadi1996/relay/releases/download/v0.3.1/Relay-windows-x64.msi
```

---

## Issues?

- **Workflow fails**: Check `.github/workflows/release-windows.yml` logs in Actions tab
- **GitHub release not created**: Ensure tag is exactly `v0.3.0` format (no extra text)
- **Questions**: Read `docs/INSTALLER_SETUP.md` (complete setup guide)
