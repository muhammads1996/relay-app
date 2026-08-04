# CI/CD Integration

Relay is designed to run the same file-based collections locally and in CI. Keep request files, environment TOML, and pipeline configuration in source control; inject secret values through the CI secret store.

## Repository verification

Pull requests and pushes run `.github/workflows/ci.yml` on Windows. The workflow:

1. checks `gofmt` output;
2. verifies downloaded modules;
3. runs `go vet ./...` and `go test ./...`;
4. builds production CLI and desktop binaries into the runner's temporary directory.

Run the same checks before pushing:

```sh
gofmt -w path/to/changed.go
git diff --check
go mod verify
go vet ./...
go test ./...
go build -trimpath -ldflags "-s -w" -o relay ./cmd/relay
```

Review the formatting diff before committing it. The CI workflow formats every tracked Go file and fails when that produces a substantive source change.

## Running API tests in a pipeline

A typical `relay.ci.json` is:

```json
{
  "report": "junit",
  "out": "relay-results.xml",
  "jsonOut": "relay-results.json",
  "tags": ["regression"],
  "priorities": ["high", "critical"],
  "gates": {
    "failOnAnyFailure": true,
    "failOnXrayError": true
  },
  "xray": {
    "push": false,
    "projectKey": "AML",
    "testPlanKey": "AML-TP1",
    "summary": "Relay automated API execution"
  }
}
```

Run it with:

```sh
relay run collections/aml --env sit --config relay.ci.json
```

Publish `relay-results.xml` as JUnit and retain `relay-results.json` as a diagnostic artifact. Relay returns a non-zero status when execution fails or an enabled quality gate fails. An Xray push failure exits with status `3` when `failOnXrayError` is enabled.

## Secrets

Environment files declare secret names, never values. Define each value in the pipeline as `RELAY_SECRET_<NAME>` with the name uppercased; for `apiToken`, use `RELAY_SECRET_APITOKEN`.

Xray Cloud uses `RELAY_XRAY_CLIENT_ID` and `RELAY_XRAY_CLIENT_SECRET`. Jira linking additionally uses `RELAY_JIRA_BASE_URL`, `RELAY_JIRA_EMAIL`, and `RELAY_JIRA_API_TOKEN`. Store every credential in the CI platform's masked secret store and do not echo it.

## Azure DevOps example

```yaml
steps:
  - task: GoTool@0
    inputs:
      version: '1.25.0'

  - powershell: |
      go test ./...
      go build -trimpath -ldflags "-s -w" -o relay.exe ./cmd/relay
      .\relay.exe run examples/aml-demo --env sit --config relay.ci.json
    displayName: Build and run Relay
    env:
      RELAY_SECRET_APITOKEN: $(relayApiToken)

  - task: PublishTestResults@2
    condition: always()
    inputs:
      testResultsFormat: JUnit
      testResultsFiles: relay-results.xml
```

For other CI systems, use the same three phases: build Relay, inject masked environment variables, and publish the JUnit/JSON outputs even when the test step fails.
