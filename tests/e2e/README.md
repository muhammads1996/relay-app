# UI e2e audit

`ui-audit.spec.js` is the reusable Playwright layout audit retained from the UI cleanup. It verifies the supported desktop widths, checks for clipped content, exercises request/response resizing, sends a request to its own local fixture server, and captures screenshots under the ignored `test-results/ui-audit` directory.

Start Relay from the repository root with a disposable database:

```powershell
$env:RELAY_SECRET_APITOKEN = "e2e-placeholder"
go run ./cmd/relay ui examples/aml-demo --db test-results/relay-e2e.db --port 7717
```

In another terminal, install the Playwright test runner and its Chromium browser, then run:

```sh
npm install --no-save --no-package-lock @playwright/test
npx playwright install chromium
npx playwright test tests/e2e/ui-audit.spec.js
```

Set `RELAY_UI_URL` when Relay uses a different port. Set `RELAY_E2E_OUTPUT` to override the screenshot directory.
