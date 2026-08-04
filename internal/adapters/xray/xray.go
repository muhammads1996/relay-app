// Package xray pushes test executions to Xray Cloud (Jira Cloud) via its
// GraphQL API. Credentials are read from RELAY_XRAY_CLIENT_ID and
// RELAY_XRAY_CLIENT_SECRET environment variables.
//
// Xray Cloud GraphQL endpoint: https://xray.cloud.getxray.app/api/v2/graphql
// Auth endpoint:                https://xray.cloud.getxray.app/api/v2/authenticate
package xray

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/muhaymien96/relay/internal/adapters/tm"
)

const (
	defaultAuthURL   = "https://xray.cloud.getxray.app/api/v2/authenticate"
	defaultGQLURL    = "https://xray.cloud.getxray.app/api/v2/graphql"
	defaultImportURL = "https://xray.cloud.getxray.app/api/v2/import/execution"
	tokenCacheTTL    = 55 * time.Minute
)

// Config holds the Xray Cloud connection parameters.
type Config struct {
	ClientID     string // from RELAY_XRAY_CLIENT_ID
	ClientSecret string // from RELAY_XRAY_CLIENT_SECRET
	AuthURL      string // override for testing
	GQLURL       string // override for testing
	ImportURL    string // Xray JSON execution import endpoint; override for testing

	// Jira REST settings, used for requirement linking (Xray's GraphQL API
	// has no issue-link mutation; that's plain Jira). JiraBaseURL/JiraEmail
	// are not secret and may come from store settings; JiraAPIToken is a
	// secret and is only ever read from the environment.
	JiraBaseURL  string // e.g. "https://yourorg.atlassian.net"
	JiraEmail    string
	JiraAPIToken string // from RELAY_JIRA_API_TOKEN
}

type Issue struct {
	IssueID string `json:"issueId,omitempty"`
	Key     string `json:"key,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// ConfigFromEnv reads credentials from standard env vars.
func ConfigFromEnv() Config {
	jiraToken := os.Getenv("RELAY_JIRA_API_TOKEN")
	if jiraToken == "" {
		jiraToken = os.Getenv("JIRA_API_KEY")
	}
	return Config{
		ClientID:     os.Getenv("RELAY_XRAY_CLIENT_ID"),
		ClientSecret: os.Getenv("RELAY_XRAY_CLIENT_SECRET"),
		ImportURL:    os.Getenv("RELAY_XRAY_IMPORT_URL"),
		JiraBaseURL:  os.Getenv("RELAY_JIRA_BASE_URL"),
		JiraEmail:    os.Getenv("RELAY_JIRA_EMAIL"),
		JiraAPIToken: jiraToken,
	}
}

// Client is an Xray Cloud adapter implementing tm.Adapter.
type Client struct {
	cfg        Config
	httpClient *http.Client
	token      string
	tokenExp   time.Time
}

// New creates a new Xray Cloud client.
func New(cfg Config) *Client {
	if cfg.AuthURL == "" {
		cfg.AuthURL = defaultAuthURL
	}
	if cfg.GQLURL == "" {
		cfg.GQLURL = defaultGQLURL
	}
	if cfg.ImportURL == "" {
		cfg.ImportURL = importURLForGraphQL(cfg.GQLURL)
	}
	return &Client{cfg: cfg, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func importURLForGraphQL(gqlURL string) string {
	trimmed := strings.TrimRight(gqlURL, "/")
	if trimmed == strings.TrimSuffix(defaultGQLURL, "/") {
		return defaultImportURL
	}
	if strings.HasSuffix(trimmed, "/graphql") {
		return strings.TrimSuffix(trimmed, "/graphql") + "/import/execution"
	}
	return trimmed
}

func (c *Client) TestConnection() error {
	_, err := c.doGQL(`query RelayConnectionCheck { getTests(jql: "issueType = Test", limit: 1) { total } }`, map[string]any{})
	return err
}

func (c *Client) CreateTestSet(projectKey, summary string, testKeys []string) (*Issue, error) {
	if projectKey == "" {
		return nil, fmt.Errorf("project key required")
	}
	if summary == "" {
		return nil, fmt.Errorf("test set summary required")
	}
	testIssueIDs, err := c.testIssueIDs(testKeys)
	if err != nil {
		return nil, err
	}
	vars := map[string]any{
		"jira": map[string]any{"fields": map[string]any{
			"project": map[string]any{"key": projectKey},
			"summary": summary,
		}},
		"tests": testIssueIDs,
	}
	body, err := c.doGQL(`mutation CreateTestSet($jira: JSON!, $tests: [String]) { createTestSet(jira: $jira, testIssueIds: $tests) { testSet { issueId jira(fields: ["key", "summary"]) } warnings } }`, vars)
	if err != nil {
		return nil, err
	}
	var out struct {
		CreateTestSet struct {
			TestSet struct {
				IssueID string         `json:"issueId"`
				Jira    map[string]any `json:"jira"`
			} `json:"testSet"`
		} `json:"createTestSet"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("xray: bad create test set response: %w", err)
	}
	created := out.CreateTestSet.TestSet
	issueOut := &Issue{IssueID: created.IssueID}
	if v, ok := created.Jira["key"].(string); ok {
		issueOut.Key = v
	}
	if v, ok := created.Jira["summary"].(string); ok {
		issueOut.Summary = v
	}
	if issueOut.Key == "" {
		return nil, fmt.Errorf("xray create test set returned no key")
	}
	return issueOut, nil
}

func (c *Client) AddTestsToTestPlan(testPlanKey string, testKeys []string) error {
	if testPlanKey == "" || len(testKeys) == 0 {
		return nil
	}
	planIssueID, err := c.testPlanIssueID(testPlanKey)
	if err != nil {
		return err
	}
	testIssueIDs, err := c.testIssueIDs(testKeys)
	if err != nil {
		return err
	}
	_, err = c.doGQL(`mutation AddTestsToPlan($issueId: String!, $tests: [String]!) { addTestsToTestPlan(issueId: $issueId, testIssueIds: $tests) { addedTests warning } }`,
		map[string]any{"issueId": planIssueID, "tests": testIssueIDs})
	return err
}

// AddTestsToTestSet associates tests with an existing Xray Test Set. Xray's
// GraphQL mutations accept internal issue IDs, so Jira keys are resolved first.
func (c *Client) AddTestsToTestSet(testSetKey string, testKeys []string) (*Issue, error) {
	if strings.TrimSpace(testSetKey) == "" {
		return nil, fmt.Errorf("xray: test set key required")
	}
	issueID, err := c.testSetIssueID(testSetKey)
	if err != nil {
		return nil, err
	}
	testIssueIDs, err := c.testIssueIDs(testKeys)
	if err != nil {
		return nil, err
	}
	if len(testIssueIDs) > 0 {
		if _, err := c.doGQL(`mutation AddTestsToSet($issueId: String!, $tests: [String]!) { addTestsToTestSet(issueId: $issueId, testIssueIds: $tests) { addedTests warning } }`,
			map[string]any{"issueId": issueID, "tests": testIssueIDs}); err != nil {
			return nil, err
		}
	}
	return &Issue{IssueID: issueID, Key: testSetKey}, nil
}

// PushExecution creates a new Xray test execution from the normalised run data
// and returns the resulting Jira issue key.
func (c *Client) PushExecution(exec tm.Execution) (string, error) {
	if strings.TrimSpace(exec.ProjectKey) == "" {
		return "", fmt.Errorf("xray: project key required")
	}
	if len(exec.Results) == 0 {
		return "", fmt.Errorf("xray: execution has no test results")
	}
	token, err := c.authenticate()
	if err != nil {
		return "", fmt.Errorf("xray auth: %w", err)
	}

	finishedAt := exec.FinishedAt
	if finishedAt.IsZero() {
		finishedAt = time.Now()
	}
	startedAt := exec.StartedAt
	if startedAt.IsZero() {
		startedAt = finishedAt
	}
	results := make([]map[string]any, 0, len(exec.Results))
	for _, r := range exec.Results {
		resultStart := finishedAt.Add(-time.Duration(r.DurationMs * float64(time.Millisecond)))
		if resultStart.Before(startedAt) {
			resultStart = startedAt
		}
		entry := map[string]any{
			"testKey": r.TestKey,
			"status":  xrayStatus(r.Status),
			"comment": stepComment(r),
			"start":   resultStart.UTC().Format(time.RFC3339),
			"finish":  finishedAt.UTC().Format(time.RFC3339),
		}
		if r.TestKey == "" {
			delete(entry, "testKey")
			entry["testInfo"] = map[string]any{
				"projectKey": exec.ProjectKey,
				"summary":    r.Name,
				"type":       "Generic",
				"definition": r.Name,
			}
			if len(r.Requirements) > 0 {
				entry["testInfo"].(map[string]any)["requirementKeys"] = r.Requirements
			}
		}
		results = append(results, entry)
	}

	info := map[string]any{
		"project":    exec.ProjectKey,
		"summary":    firstNonEmpty(exec.Summary, "Relay automated execution"),
		"startDate":  startedAt.UTC().Format(time.RFC3339),
		"finishDate": finishedAt.UTC().Format(time.RFC3339),
	}
	if exec.TestPlanKey != "" {
		info["testPlanKey"] = exec.TestPlanKey
	}
	payload, err := json.Marshal(map[string]any{
		"info":  info,
		"tests": results,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", c.cfg.ImportURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("xray import: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("xray import: HTTP %d: %s", resp.StatusCode, body)
	}

	var imported struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &imported); err != nil {
		return "", fmt.Errorf("xray: bad import response: %w", err)
	}
	if imported.Key == "" {
		return "", fmt.Errorf("xray import returned no execution key")
	}
	return imported.Key, nil
}

func xrayStatus(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case tm.StatusPASS, "PASS":
		return tm.StatusPASS
	case tm.StatusSKIP, "SKIP", "TODO":
		return "TODO"
	default:
		return tm.StatusFAIL
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stepComment(r tm.TestResult) string {
	var parts []string
	if strings.TrimSpace(r.Comment) != "" {
		parts = append(parts, strings.TrimSpace(r.Comment))
	}
	for _, st := range r.Steps {
		line := strings.TrimSpace(st.Name)
		if line == "" {
			line = strings.TrimSpace(st.Type)
		}
		if line == "" {
			line = "step"
		}
		if st.Status != "" {
			line += ": " + st.Status
		}
		if st.Comment != "" {
			line += " - " + st.Comment
		}
		if st.Expected != "" {
			line += " (expected: " + st.Expected + ")"
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, "\n")
}

// doGQL authenticates, posts a GraphQL request, and returns the raw "data"
// payload (or an error built from transport failures or the "errors" array).
func (c *Client) doGQL(query string, vars map[string]any) (json.RawMessage, error) {
	token, err := c.authenticate()
	if err != nil {
		return nil, fmt.Errorf("xray auth: %w", err)
	}
	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", c.cfg.GQLURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xray gql: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("xray gql: HTTP %d: %s", resp.StatusCode, body)
	}

	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("xray: bad response: %w", err)
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, len(env.Errors))
		for i, e := range env.Errors {
			msgs[i] = e.Message
		}
		return nil, fmt.Errorf("xray gql errors: %s", strings.Join(msgs, "; "))
	}
	return env.Data, nil
}

// GetTest looks up an existing Xray test issue by Jira key.
func (c *Client) GetTest(key string) (*tm.TestRef, error) {
	query := `
	query GetTests($jql: String!) {
		getTests(jql: $jql, limit: 1) {
			results { issueId jira(fields: ["key", "summary"]) }
		}
	}`
	data, err := c.doGQL(query, map[string]any{"jql": fmt.Sprintf("key = %q", key)})
	if err != nil {
		return nil, err
	}
	var parsed struct {
		GetTests struct {
			Results []struct {
				IssueID string         `json:"issueId"`
				Jira    map[string]any `json:"jira"`
			} `json:"results"`
		} `json:"getTests"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("xray: bad getTests response: %w", err)
	}
	if len(parsed.GetTests.Results) == 0 {
		return nil, nil
	}
	j := parsed.GetTests.Results[0].Jira
	ref := &tm.TestRef{IssueID: parsed.GetTests.Results[0].IssueID}
	if k, ok := j["key"].(string); ok {
		ref.Key = k
	}
	if s, ok := j["summary"].(string); ok {
		ref.Summary = s
	}
	return ref, nil
}

func (c *Client) testIssueIDs(keys []string) ([]string, error) {
	seen := map[string]bool{}
	ids := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		ref, err := c.GetTest(key)
		if err != nil {
			return nil, fmt.Errorf("xray: resolve test %s: %w", key, err)
		}
		if ref == nil || ref.IssueID == "" {
			return nil, fmt.Errorf("xray: test %s not found", key)
		}
		ids = append(ids, ref.IssueID)
	}
	return ids, nil
}

func (c *Client) testPlanIssueID(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("xray: test plan key required")
	}
	data, err := c.doGQL(`query GetTestPlan($jql: String!) { getTestPlans(jql: $jql, limit: 1) { results { issueId jira(fields: ["key"]) } } }`,
		map[string]any{"jql": fmt.Sprintf("key = %q", key)})
	if err != nil {
		return "", err
	}
	var parsed struct {
		GetTestPlans struct {
			Results []struct {
				IssueID string `json:"issueId"`
			} `json:"results"`
		} `json:"getTestPlans"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("xray: bad getTestPlans response: %w", err)
	}
	if len(parsed.GetTestPlans.Results) == 0 || parsed.GetTestPlans.Results[0].IssueID == "" {
		return "", fmt.Errorf("xray: test plan %s not found", key)
	}
	return parsed.GetTestPlans.Results[0].IssueID, nil
}

func (c *Client) testSetIssueID(key string) (string, error) {
	key = strings.TrimSpace(key)
	data, err := c.doGQL(`query GetTestSet($jql: String!) { getTestSets(jql: $jql, limit: 1) { results { issueId jira(fields: ["key"]) } } }`,
		map[string]any{"jql": fmt.Sprintf("key = %q", key)})
	if err != nil {
		return "", err
	}
	var parsed struct {
		GetTestSets struct {
			Results []struct {
				IssueID string `json:"issueId"`
			} `json:"results"`
		} `json:"getTestSets"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("xray: bad getTestSets response: %w", err)
	}
	if len(parsed.GetTestSets.Results) == 0 || parsed.GetTestSets.Results[0].IssueID == "" {
		return "", fmt.Errorf("xray: test set %s not found", key)
	}
	return parsed.GetTestSets.Results[0].IssueID, nil
}

func (c *Client) UpdateTest(ref tm.TestRef, t tm.NewTest) error {
	if ref.IssueID == "" {
		return fmt.Errorf("xray: missing issueId for %s", ref.Key)
	}
	testType, hasUnstructuredDefinition, err := unstructuredTestType(t)
	if err != nil {
		return err
	}
	updateSummary := strings.TrimSpace(t.Summary) != "" && t.Summary != ref.Summary
	if updateSummary && (c.cfg.JiraBaseURL == "" || c.cfg.JiraEmail == "" || c.cfg.JiraAPIToken == "") {
		return fmt.Errorf("xray: Jira URL, email and API token are required to update the test summary")
	}
	if _, err := c.doGQL(`mutation UpdateTestType($issueId: String!, $testType: UpdateTestTypeInput!) { updateTestType(issueId: $issueId, testType: $testType) { issueId } }`, map[string]any{
		"issueId":  ref.IssueID,
		"testType": map[string]any{"name": testType},
	}); err != nil {
		return err
	}
	if hasUnstructuredDefinition {
		if _, err := c.doGQL(`mutation UpdateTestDefinition($issueId: String!, $unstructured: String!) { updateUnstructuredTestDefinition(issueId: $issueId, unstructured: $unstructured) { issueId } }`, map[string]any{
			"issueId":      ref.IssueID,
			"unstructured": t.Steps,
		}); err != nil {
			return err
		}
	}
	if updateSummary {
		return c.updateJiraSummary(ref.Key, t.Summary)
	}
	return nil
}

func (c *Client) updateJiraSummary(key, summary string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("xray: Jira issue key required to update summary")
	}
	body, err := json.Marshal(map[string]any{"fields": map[string]any{"summary": summary}})
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(c.cfg.JiraBaseURL, "/") + "/rest/api/3/issue/" + url.PathEscape(key)
	req, err := http.NewRequest(http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.cfg.JiraEmail, c.cfg.JiraAPIToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("jira update %s: %w", key, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("jira update %s: HTTP %d: %s", key, resp.StatusCode, respBody)
	}
	return nil
}

// CreateTest creates a new Xray test issue and returns its Jira key.
func (c *Client) CreateTest(t tm.NewTest) (string, error) {
	testType, hasUnstructuredDefinition, err := unstructuredTestType(t)
	if err != nil {
		return "", err
	}
	mutation := `
	mutation CreateTest($testType: UpdateTestTypeInput, $jira: JSON!) {
		createTest(testType: $testType, jira: $jira) {
			test { issueId jira(fields: ["key"]) }
			warnings
		}
	}`
	vars := map[string]any{
		"testType": map[string]any{"name": testType},
		"jira": map[string]any{
			"fields": map[string]any{
				"summary": t.Summary,
				"project": map[string]any{"key": t.ProjectKey},
			},
		},
	}
	if hasUnstructuredDefinition {
		mutation = `
		mutation CreateTest($testType: UpdateTestTypeInput, $unstructured: String, $jira: JSON!) {
			createTest(testType: $testType, unstructured: $unstructured, jira: $jira) {
				test { issueId jira(fields: ["key"]) }
				warnings
			}
		}`
		vars["unstructured"] = t.Steps
	}
	data, err := c.doGQL(mutation, vars)
	if err != nil {
		return "", err
	}
	var parsed struct {
		CreateTest struct {
			Test struct {
				Jira map[string]any `json:"jira"`
			} `json:"test"`
			Warnings []string `json:"warnings"`
		} `json:"createTest"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("xray: bad createTest response: %w", err)
	}
	key, _ := parsed.CreateTest.Test.Jira["key"].(string)
	if key == "" {
		return "", fmt.Errorf("xray: createTest did not return a key")
	}
	return key, nil
}

// unstructuredTestType validates the test type against Relay's free-text test
// definition. Xray only supports the unstructured field for Generic tests;
// Manual tests require a collection of structured steps instead.
func unstructuredTestType(t tm.NewTest) (testType string, hasUnstructuredDefinition bool, err error) {
	testType = strings.TrimSpace(t.TestType)
	if testType == "" {
		testType = "Generic"
	}
	hasUnstructuredDefinition = strings.EqualFold(testType, "Generic")
	if !hasUnstructuredDefinition && strings.TrimSpace(t.Steps) != "" {
		return "", false, fmt.Errorf("xray: %s tests require structured steps; Relay's text definition requires a Generic test", testType)
	}
	return testType, hasUnstructuredDefinition, nil
}

// LinkRequirements links a test issue to one or more requirement issues
// using Jira's "Tests" issue link type (the test "tests" the requirement;
// the requirement "is tested by" the test). This is a plain Jira REST call
// — Xray Cloud's GraphQL API has no issue-link mutation — so it needs Jira
// basic-auth credentials (email + API token) rather than the Xray client
// id/secret used for GraphQL.
func (c *Client) LinkRequirements(testKey string, requirementKeys []string) error {
	testKey = strings.TrimSpace(testKey)
	if testKey == "" {
		return fmt.Errorf("xray: test key required before linking requirements")
	}
	cleanKeys := make([]string, 0, len(requirementKeys))
	seen := map[string]bool{}
	for _, reqKey := range requirementKeys {
		reqKey = strings.TrimSpace(reqKey)
		if reqKey == "" || seen[reqKey] {
			continue
		}
		seen[reqKey] = true
		cleanKeys = append(cleanKeys, reqKey)
	}
	if len(cleanKeys) == 0 {
		return fmt.Errorf("xray: at least one requirement key is required")
	}
	if c.cfg.JiraBaseURL == "" {
		return fmt.Errorf("xray: Jira base URL not configured")
	}
	if c.cfg.JiraEmail == "" || c.cfg.JiraAPIToken == "" {
		return fmt.Errorf("Jira email and API token must be set")
	}
	url := strings.TrimRight(c.cfg.JiraBaseURL, "/") + "/rest/api/3/issueLink"
	for _, reqKey := range cleanKeys {
		body, err := json.Marshal(map[string]any{
			"type":         map[string]string{"name": "Tests"},
			"inwardIssue":  map[string]string{"key": reqKey},
			"outwardIssue": map[string]string{"key": testKey},
		})
		if err != nil {
			return err
		}
		req, err := http.NewRequest("POST", url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.SetBasicAuth(c.cfg.JiraEmail, c.cfg.JiraAPIToken)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("jira issueLink %s: %w", reqKey, err)
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 && resp.StatusCode != 201 {
			return fmt.Errorf("jira issueLink %s: HTTP %d: %s", reqKey, resp.StatusCode, respBody)
		}
	}
	return nil
}

// authenticate returns a cached bearer token, refreshing if needed.
func (c *Client) authenticate() (string, error) {
	if c.token != "" && time.Now().Before(c.tokenExp) {
		return c.token, nil
	}
	if c.cfg.ClientID == "" || c.cfg.ClientSecret == "" {
		return "", fmt.Errorf("RELAY_XRAY_CLIENT_ID and RELAY_XRAY_CLIENT_SECRET must be set")
	}
	payload, _ := json.Marshal(map[string]string{
		"client_id":     c.cfg.ClientID,
		"client_secret": c.cfg.ClientSecret,
	})
	resp, err := c.httpClient.Post(c.cfg.AuthURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	// Response is a bare quoted string: "eyJ..."
	token := strings.Trim(strings.TrimSpace(string(body)), `"`)
	if token == "" {
		return "", fmt.Errorf("empty token from Xray auth endpoint")
	}
	c.token = token
	c.tokenExp = time.Now().Add(tokenCacheTTL)
	return token, nil
}

// BuildExecution converts a runner.Report-like structure to tm.Execution.
// Import this from the xray package rather than importing runner (avoids
// a circular dependency: runner → xray → runner).
type RunnerReport interface {
	GetResults() []RunnerResult
	GetStarted() time.Time
	GetFinished() time.Time
}

// RunnerResult is the per-request data the adapter needs.
type RunnerResult interface {
	GetName() string
	GetTestKey() string // meta.xray.test_key if set, else ""
	IsFailed() bool
	GetComment() string
	GetDurationMs() float64
}

// FromRunnerReport converts a RunnerReport into a tm.Execution.
func FromRunnerReport(report RunnerReport, projectKey, testPlanKey, summary string) tm.Execution {
	exec := tm.Execution{
		ProjectKey:  projectKey,
		TestPlanKey: testPlanKey,
		Summary:     summary,
		StartedAt:   report.GetStarted(),
		FinishedAt:  report.GetFinished(),
	}
	for _, r := range report.GetResults() {
		status := tm.StatusPASS
		if r.IsFailed() {
			status = tm.StatusFAIL
		}
		exec.Results = append(exec.Results, tm.TestResult{
			TestKey:    r.GetTestKey(),
			Name:       r.GetName(),
			Status:     status,
			Comment:    r.GetComment(),
			DurationMs: r.GetDurationMs(),
		})
	}
	return exec
}
