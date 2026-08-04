package xray_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/muhaymien96/relay/internal/adapters/tm"
	"github.com/muhaymien96/relay/internal/adapters/xray"
)

func TestPushExecution(t *testing.T) {
	// Stub auth endpoint returns a bearer token.
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`"test-bearer-token"`))
	}))
	defer authSrv.Close()

	// Stub Xray JSON import endpoint records the request and returns a canned response.
	var gotBody map[string]any
	importSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-bearer-token" {
			t.Errorf("authorization = %q", got)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"10042","key":"AML-999"}`))
	}))
	defer importSrv.Close()

	client := xray.New(xray.Config{
		ClientID:     "test-id",
		ClientSecret: "test-secret",
		AuthURL:      authSrv.URL,
		ImportURL:    importSrv.URL,
	})

	startedAt := time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)
	exec := tm.Execution{
		ProjectKey:  "AML",
		TestPlanKey: "AML-88",
		Summary:     "Relay automated run",
		StartedAt:   startedAt,
		FinishedAt:  startedAt.Add(5 * time.Second),
		Results: []tm.TestResult{
			{TestKey: "AML-T142", Name: "Verify Individual", Status: tm.StatusPASS, DurationMs: 312},
			{TestKey: "AML-T143", Name: "Verify Entity", Status: tm.StatusFAIL, Comment: "status 500", DurationMs: 98},
			{Name: "Unlinked", Status: tm.StatusSKIP, Requirements: []string{"AML-R1"}},
		},
	}

	key, err := client.PushExecution(exec)
	if err != nil {
		t.Fatalf("PushExecution: %v", err)
	}
	if key != "AML-999" {
		t.Errorf("expected key AML-999, got %q", key)
	}
	info, _ := gotBody["info"].(map[string]any)
	if info["project"] != "AML" || info["testPlanKey"] != "AML-88" {
		t.Errorf("unexpected execution info: %v", info)
	}
	tests, _ := gotBody["tests"].([]any)
	if len(tests) != 3 {
		t.Fatalf("tests = %v", tests)
	}
	first, _ := tests[0].(map[string]any)
	if first["testKey"] != "AML-T142" || first["status"] != "PASSED" {
		t.Errorf("linked test payload = %v", first)
	}
	third, _ := tests[2].(map[string]any)
	if third["testKey"] != nil || third["status"] != "TODO" {
		t.Errorf("unlinked test payload = %v", third)
	}
	testInfo, _ := third["testInfo"].(map[string]any)
	if testInfo["projectKey"] != "AML" || testInfo["type"] != "Generic" {
		t.Errorf("testInfo = %v", testInfo)
	}
	requirements, _ := testInfo["requirementKeys"].([]any)
	if len(requirements) != 1 || requirements[0] != "AML-R1" {
		t.Errorf("requirement keys = %v", requirements)
	}
}

func TestGetTest(t *testing.T) {
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`"test-bearer-token"`))
	}))
	defer authSrv.Close()

	var gotBody map[string]any
	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"getTests": {
					"results": [{"issueId": "1", "jira": {"key": "AML-T142", "summary": "Verify Individual"}}]
				}
			}
		}`))
	}))
	defer gqlSrv.Close()

	client := xray.New(xray.Config{ClientID: "id", ClientSecret: "secret", AuthURL: authSrv.URL, GQLURL: gqlSrv.URL})
	ref, err := client.GetTest("AML-T142")
	if err != nil {
		t.Fatalf("GetTest: %v", err)
	}
	if ref == nil || ref.Key != "AML-T142" || ref.Summary != "Verify Individual" {
		t.Errorf("unexpected ref: %+v", ref)
	}
	vars, _ := gotBody["variables"].(map[string]any)
	if vars["jql"] != `key = "AML-T142"` {
		t.Errorf("unexpected jql: %v", vars["jql"])
	}
}

func TestGetTestNotFound(t *testing.T) {
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`"test-bearer-token"`))
	}))
	defer authSrv.Close()
	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data": {"getTests": {"results": []}}}`))
	}))
	defer gqlSrv.Close()

	client := xray.New(xray.Config{ClientID: "id", ClientSecret: "secret", AuthURL: authSrv.URL, GQLURL: gqlSrv.URL})
	ref, err := client.GetTest("AML-T999")
	if err != nil {
		t.Fatalf("GetTest: %v", err)
	}
	if ref != nil {
		t.Errorf("expected nil ref for not-found test, got %+v", ref)
	}
}

func TestCreateTest(t *testing.T) {
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`"test-bearer-token"`))
	}))
	defer authSrv.Close()

	var gotBody map[string]any
	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{
			"data": { "createTest": { "test": {"issueId": "55", "jira": {"key": "AML-T200"}}, "warnings": [] } }
		}`))
	}))
	defer gqlSrv.Close()

	client := xray.New(xray.Config{ClientID: "id", ClientSecret: "secret", AuthURL: authSrv.URL, GQLURL: gqlSrv.URL})
	key, err := client.CreateTest(tm.NewTest{ProjectKey: "AML", Summary: "New test", Steps: "do the thing"})
	if err != nil {
		t.Fatalf("CreateTest: %v", err)
	}
	if key != "AML-T200" {
		t.Errorf("expected key AML-T200, got %q", key)
	}
	vars, _ := gotBody["variables"].(map[string]any)
	testType, _ := vars["testType"].(map[string]any)
	if testType["name"] != "Generic" {
		t.Errorf("expected Generic test type, got %v", testType)
	}
	if vars["unstructured"] != "do the thing" {
		t.Errorf("expected unstructured definition, got %v", vars["unstructured"])
	}
	jira, _ := vars["jira"].(map[string]any)
	fields, _ := jira["fields"].(map[string]any)
	if fields["summary"] != "New test" {
		t.Errorf("expected summary New test, got %v", fields["summary"])
	}
}

func TestCreateTestRejectsManualFreeTextDefinition(t *testing.T) {
	client := xray.New(xray.Config{})
	_, err := client.CreateTest(tm.NewTest{ProjectKey: "AML", Summary: "Manual test", TestType: "Manual", Steps: "do the thing"})
	if err == nil || !strings.Contains(err.Error(), "Generic") {
		t.Fatalf("expected Generic compatibility error, got %v", err)
	}
}

func TestCreateTestSetUsesIssueIDs(t *testing.T) {
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`"test-bearer-token"`))
	}))
	defer authSrv.Close()

	var createVars map[string]any
	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		query, _ := body["query"].(string)
		if strings.Contains(query, "getTests") {
			_, _ = w.Write([]byte(`{"data":{"getTests":{"results":[{"issueId":"55","jira":{"key":"AML-T1"}}]}}}`))
			return
		}
		createVars, _ = body["variables"].(map[string]any)
		_, _ = w.Write([]byte(`{"data":{"createTestSet":{"testSet":{"issueId":"77","jira":{"key":"AML-SET1","summary":"Smoke"}},"warnings":[]}}}`))
	}))
	defer gqlSrv.Close()

	client := xray.New(xray.Config{ClientID: "id", ClientSecret: "secret", AuthURL: authSrv.URL, GQLURL: gqlSrv.URL})
	issue, err := client.CreateTestSet("AML", "Smoke", []string{"AML-T1"})
	if err != nil {
		t.Fatal(err)
	}
	if issue.Key != "AML-SET1" || issue.IssueID != "77" {
		t.Fatalf("issue = %+v", issue)
	}
	tests, _ := createVars["tests"].([]any)
	if len(tests) != 1 || tests[0] != "55" {
		t.Fatalf("test issue ids = %v", tests)
	}
	jira, _ := createVars["jira"].(map[string]any)
	fields, _ := jira["fields"].(map[string]any)
	project, _ := fields["project"].(map[string]any)
	if fields["summary"] != "Smoke" || project["key"] != "AML" {
		t.Fatalf("jira fields = %v", fields)
	}
}

func TestAddTestsToTestPlanUsesIssueIDs(t *testing.T) {
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`"test-bearer-token"`))
	}))
	defer authSrv.Close()

	var addVars map[string]any
	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		query, _ := body["query"].(string)
		switch {
		case strings.Contains(query, "getTestPlans"):
			_, _ = w.Write([]byte(`{"data":{"getTestPlans":{"results":[{"issueId":"88","jira":{"key":"AML-P1"}}]}}}`))
		case strings.Contains(query, "getTests"):
			_, _ = w.Write([]byte(`{"data":{"getTests":{"results":[{"issueId":"55","jira":{"key":"AML-T1"}}]}}}`))
		default:
			addVars, _ = body["variables"].(map[string]any)
			_, _ = w.Write([]byte(`{"data":{"addTestsToTestPlan":{"addedTests":["55"],"warning":null}}}`))
		}
	}))
	defer gqlSrv.Close()

	client := xray.New(xray.Config{ClientID: "id", ClientSecret: "secret", AuthURL: authSrv.URL, GQLURL: gqlSrv.URL})
	if err := client.AddTestsToTestPlan("AML-P1", []string{"AML-T1"}); err != nil {
		t.Fatal(err)
	}
	if addVars["issueId"] != "88" {
		t.Fatalf("plan issue id = %v", addVars["issueId"])
	}
	tests, _ := addVars["tests"].([]any)
	if len(tests) != 1 || tests[0] != "55" {
		t.Fatalf("test issue ids = %v", tests)
	}
}

func TestAddTestsToTestSetUsesIssueIDs(t *testing.T) {
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`"test-bearer-token"`))
	}))
	defer authSrv.Close()

	var addVars map[string]any
	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		query, _ := body["query"].(string)
		switch {
		case strings.Contains(query, "getTestSets"):
			_, _ = w.Write([]byte(`{"data":{"getTestSets":{"results":[{"issueId":"77","jira":{"key":"AML-SET1"}}]}}}`))
		case strings.Contains(query, "getTests"):
			_, _ = w.Write([]byte(`{"data":{"getTests":{"results":[{"issueId":"55","jira":{"key":"AML-T1"}}]}}}`))
		default:
			addVars, _ = body["variables"].(map[string]any)
			_, _ = w.Write([]byte(`{"data":{"addTestsToTestSet":{"addedTests":["55"],"warning":null}}}`))
		}
	}))
	defer gqlSrv.Close()

	client := xray.New(xray.Config{ClientID: "id", ClientSecret: "secret", AuthURL: authSrv.URL, GQLURL: gqlSrv.URL})
	issue, err := client.AddTestsToTestSet("AML-SET1", []string{"AML-T1"})
	if err != nil {
		t.Fatal(err)
	}
	if issue.Key != "AML-SET1" || issue.IssueID != "77" {
		t.Fatalf("issue = %+v", issue)
	}
	if addVars["issueId"] != "77" {
		t.Fatalf("test set issue id = %v", addVars["issueId"])
	}
	tests, _ := addVars["tests"].([]any)
	if len(tests) != 1 || tests[0] != "55" {
		t.Fatalf("test issue ids = %v", tests)
	}
}

func TestUpdateTestUsesSupportedXrayMutationsAndJiraREST(t *testing.T) {
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`"test-bearer-token"`))
	}))
	defer authSrv.Close()

	var queries []string
	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		queries = append(queries, body["query"].(string))
		_, _ = w.Write([]byte(`{"data":{"updateTestType":{"issueId":"55"},"updateUnstructuredTestDefinition":{"issueId":"55"}}}`))
	}))
	defer gqlSrv.Close()

	var jiraBody map[string]any
	jiraSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/rest/api/3/issue/AML-T1" {
			t.Errorf("unexpected Jira request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&jiraBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer jiraSrv.Close()

	client := xray.New(xray.Config{
		ClientID: "id", ClientSecret: "secret", AuthURL: authSrv.URL, GQLURL: gqlSrv.URL,
		JiraBaseURL: jiraSrv.URL, JiraEmail: "me@example.com", JiraAPIToken: "token",
	})
	err := client.UpdateTest(tm.TestRef{IssueID: "55", Key: "AML-T1", Summary: "Old"}, tm.NewTest{Summary: "New", TestType: "Generic", Steps: "GET /health"})
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || !strings.Contains(queries[0], "updateTestType") || !strings.Contains(queries[1], "updateUnstructuredTestDefinition") {
		t.Fatalf("queries = %v", queries)
	}
	fields, _ := jiraBody["fields"].(map[string]any)
	if fields["summary"] != "New" {
		t.Fatalf("jira body = %v", jiraBody)
	}
}

func TestLinkRequirements(t *testing.T) {
	var gotLinks []map[string]any
	jiraSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issueLink" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "me@example.com" || pass != "tok123" {
			t.Errorf("missing/wrong basic auth: %s/%s", user, pass)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotLinks = append(gotLinks, body)
		w.WriteHeader(201)
	}))
	defer jiraSrv.Close()

	client := xray.New(xray.Config{
		JiraBaseURL:  jiraSrv.URL,
		JiraEmail:    "me@example.com",
		JiraAPIToken: "tok123",
	})
	err := client.LinkRequirements("AML-T142", []string{"AML-1", "AML-2"})
	if err != nil {
		t.Fatalf("LinkRequirements: %v", err)
	}
	if len(gotLinks) != 2 {
		t.Fatalf("expected 2 link requests, got %d", len(gotLinks))
	}
	outward, _ := gotLinks[0]["outwardIssue"].(map[string]any)
	if outward["key"] != "AML-T142" {
		t.Errorf("expected outward AML-T142, got %v", outward["key"])
	}
}

func TestLinkRequirementsMissingConfig(t *testing.T) {
	client := xray.New(xray.Config{})
	if err := client.LinkRequirements("AML-T142", []string{"AML-1"}); err == nil {
		t.Fatal("expected error for missing Jira config")
	}
}

func TestAuthError(t *testing.T) {
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error": "invalid credentials"}`))
	}))
	defer authSrv.Close()

	client := xray.New(xray.Config{
		ClientID:     "bad",
		ClientSecret: "bad",
		AuthURL:      authSrv.URL,
		GQLURL:       "http://unused",
	})

	_, err := client.PushExecution(tm.Execution{})
	if err == nil {
		t.Fatal("expected auth error")
	}
}
