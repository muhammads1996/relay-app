package playwrightpack

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/muhaymien96/relay/internal/adapters/tm"
)

type ResultsFile struct {
	Schema     string       `json:"schema"`
	StartedAt  string       `json:"startedAt"`
	FinishedAt string       `json:"finishedAt"`
	Summary    Summary      `json:"summary"`
	Results    []TestResult `json:"results"`
}

type Summary struct {
	Tests   int `json:"tests"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

type TestResult struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	TestKey      string       `json:"testKey,omitempty"`
	TestPlanKey  string       `json:"testPlanKey,omitempty"`
	Requirements []string     `json:"requirements,omitempty"`
	Status       string       `json:"status"`
	DurationMs   float64      `json:"durationMs"`
	Comment      string       `json:"comment,omitempty"`
	Steps        []StepResult `json:"steps,omitempty"`
}

type StepResult struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Status   string `json:"status"`
	Comment  string `json:"comment,omitempty"`
}

func LoadResults(path string) (*ResultsFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out ResultsFile
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if out.Schema != "relay.playwright.results.v1" {
		return nil, fmt.Errorf("%s: unsupported results schema %q", path, out.Schema)
	}
	return &out, nil
}

func (r *ResultsFile) ToExecution(projectKey, planKey, summary string) tm.Execution {
	started := parseTime(r.StartedAt)
	finished := parseTime(r.FinishedAt)
	if finished.IsZero() {
		finished = time.Now()
	}
	if started.IsZero() {
		started = finished
	}
	if summary == "" {
		summary = "Relay Playwright execution"
	}
	exec := tm.Execution{
		ProjectKey: projectKey, TestPlanKey: planKey, Summary: summary,
		StartedAt: started, FinishedAt: finished,
	}
	for _, res := range r.Results {
		status := tm.StatusFAIL
		switch res.Status {
		case tm.StatusPASS, "passed", "PASS":
			status = tm.StatusPASS
		case tm.StatusSKIP, "skipped", "SKIP":
			status = tm.StatusSKIP
		}
		steps := make([]tm.TestStep, 0, len(res.Steps))
		for _, st := range res.Steps {
			stStatus := tm.StatusFAIL
			switch st.Status {
			case tm.StatusPASS, "passed", "PASS":
				stStatus = tm.StatusPASS
			case tm.StatusSKIP, "skipped", "SKIP":
				stStatus = tm.StatusSKIP
			}
			steps = append(steps, tm.TestStep{
				Name: st.Name, Type: st.Type, Expected: st.Expected, Actual: st.Actual,
				Status: stStatus, Comment: st.Comment,
			})
		}
		exec.Results = append(exec.Results, tm.TestResult{
			TestKey: res.TestKey, Name: res.Name, Status: status, Comment: res.Comment,
			DurationMs: res.DurationMs, Steps: steps, Requirements: append([]string(nil), res.Requirements...),
		})
	}
	return exec
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
