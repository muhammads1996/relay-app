// Package tm defines the test-management adapter interface and the shared
// result types that adapters accept. Concrete implementations (Xray, Zephyr,
// TestRail) live in sibling packages and register themselves here.
package tm

import "time"

// Status values for a test result.
const (
	StatusPASS = "PASSED"
	StatusFAIL = "FAILED"
	StatusSKIP = "SKIPPED"
)

// TestResult is one request's outcome in a normalised form suitable for any
// test-management system.
type TestResult struct {
	TestKey      string // e.g. "AML-T142" from meta.xray.test_key (optional)
	Name         string // human name shown in the execution
	Status       string // StatusPASS | StatusFAIL | StatusSKIP
	Comment      string // failure details / assertion messages
	DurationMs   float64
	Steps        []TestStep
	Requirements []string // optional Jira requirement keys used when provisioning an unlinked test
}

type TestStep struct {
	Name     string
	Type     string
	Expected string
	Actual   string
	Status   string
	Comment  string
}

// Execution is a completed collection run ready to be pushed.
type Execution struct {
	ProjectKey  string // Jira project key, e.g. "AML"
	TestPlanKey string // optional existing test-plan issue key
	Summary     string // test execution summary line
	StartedAt   time.Time
	FinishedAt  time.Time
	Results     []TestResult
}

// TestRef identifies an existing test issue found in the TM system.
type TestRef struct {
	IssueID string
	Key     string // e.g. "AML-T142"
	Summary string
}

// NewTest is the input for creating a test issue.
type NewTest struct {
	ProjectKey string
	Summary    string
	TestType   string // e.g. "Generic", "Manual", "Cucumber"; adapter picks a default if empty
	Steps      string // unstructured test definition (free text)
}

// Adapter is the generic interface for pushing executions to a TM system.
type Adapter interface {
	// PushExecution creates a new test execution and returns the issue key.
	PushExecution(exec Execution) (executionKey string, err error)

	// GetTest looks up an existing test issue by key. Returns (nil, nil) when
	// the key does not resolve to a test issue, so callers can distinguish
	// "not found" from a transport/auth error.
	GetTest(key string) (*TestRef, error)

	// CreateTest creates a new test issue and returns its key.
	CreateTest(t NewTest) (key string, err error)

	// LinkRequirements links a test issue to one or more requirement issues.
	LinkRequirements(testKey string, requirementKeys []string) error
}
