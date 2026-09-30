# UI e2e audit

`ui-audit.spec.js` covers eight scenarios: layout from 760x480 to 1440x900 with resizing and successful local sends; delayed History navigation; delayed Settings navigation; credential masking; failed-save protection; upstream cancellation; redirect/TLS settings; and persisted mode with view-aware environment changes. It also checks keyboard-operated test details and uncaught browser exceptions. Screenshots go under the ignored `test-results/ui-audit` directory. These are browser regressions, not installer or native OS-scaling certification.

Start Relay from the repository root with a disposable copy of the example workspace. The suite edits requests. Never point it at your working collection. Keep its database outside `test-results`, which Playwright deletes before each run:

```powershell
New-Item -ItemType Directory -Force build/ui-audit/workspace
Copy-Item examples/aml-demo/* build/ui-audit/workspace -Recurse -Force
go run ./cmd/relay ui build/ui-audit/workspace --port 7717
```

In another terminal, install the Playwright test runner and its Chromium browser, then run:

```sh
npm install --no-save --no-package-lock @playwright/test
npx playwright install chromium
npx playwright test tests/e2e/ui-audit.spec.js
```

Set `RELAY_UI_URL` when Relay uses a different port. Set `RELAY_E2E_OUTPUT` to override the screenshot directory. Port 18080 must be free for the test-owned HTTP fixture. The tests need no real API credentials.
