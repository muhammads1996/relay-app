package runner

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

type junitProperty struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// WriteJUnit renders the report as JUnit XML for CI systems.
func WriteJUnit(w io.Writer, r *Report) error {
	type failure struct {
		Message string `xml:"message,attr"`
		Body    string `xml:",chardata"`
	}
	type testcase struct {
		Name       string          `xml:"name,attr"`
		Classname  string          `xml:"classname,attr"`
		Time       string          `xml:"time,attr"`
		Properties []junitProperty `xml:"properties>property,omitempty"`
		Failure    *failure        `xml:"failure,omitempty"`
	}
	type testsuite struct {
		XMLName  xml.Name   `xml:"testsuite"`
		Name     string     `xml:"name,attr"`
		Tests    int        `xml:"tests,attr"`
		Failures int        `xml:"failures,attr"`
		Time     string     `xml:"time,attr"`
		Cases    []testcase `xml:"testcase"`
	}

	// Count individual pm.test assertions as separate JUnit test cases.
	totalTests := 0
	totalFailures := 0
	var cases []testcase
	for _, res := range r.Results {
		if len(res.ScriptTests) == 0 && len(res.Assertions) == 0 {
			// Plain request — one test case for the HTTP exchange.
			totalTests++
			tc := testcase{
				Name:       res.Name,
				Classname:  res.File,
				Time:       fmt.Sprintf("%.3f", res.Duration.Seconds()),
				Properties: junitProperties(res),
			}
			if res.Failed() {
				totalFailures++
				tc.Failure = &failure{
					Message: failureSummary(res),
					Body:    failureDetail(res),
				}
			}
			cases = append(cases, tc)
			continue
		}
		// Emit one testcase per assertion.
		for _, a := range res.Assertions {
			totalTests++
			tc := testcase{
				Name:       res.Name + " — " + a.Assertion.Type,
				Classname:  res.File,
				Time:       fmt.Sprintf("%.3f", res.Duration.Seconds()),
				Properties: junitProperties(res),
			}
			if !a.Passed {
				totalFailures++
				tc.Failure = &failure{Message: a.Message, Body: failureDetail(res)}
			}
			cases = append(cases, tc)
		}
		// Emit one testcase per pm.test call.
		for _, t := range res.ScriptTests {
			totalTests++
			tc := testcase{
				Name:       res.Name + " — " + t.Name,
				Classname:  res.File,
				Time:       fmt.Sprintf("%.3f", res.Duration.Seconds()),
				Properties: junitProperties(res),
			}
			if !t.Passed {
				totalFailures++
				tc.Failure = &failure{Message: t.Error, Body: failureDetail(res)}
			}
			cases = append(cases, tc)
		}
		// Transport / script runtime errors produce an extra failing case.
		if res.Err != nil {
			totalTests++
			totalFailures++
			cases = append(cases, testcase{
				Name:       res.Name + " — error",
				Classname:  res.File,
				Time:       fmt.Sprintf("%.3f", res.Duration.Seconds()),
				Properties: junitProperties(res),
				Failure:    &failure{Message: res.Err.Error()},
			})
		}
	}

	suite := testsuite{
		Name:     r.Root,
		Tests:    totalTests,
		Failures: totalFailures,
		Time:     fmt.Sprintf("%.3f", r.Duration.Seconds()),
		Cases:    cases,
	}

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	return enc.Encode(suite)
}

// WriteJSON renders the report as JSON.
func WriteJSON(w io.Writer, r *Report) error {
	type assertionJSON struct {
		Type    string `json:"type"`
		Passed  bool   `json:"passed"`
		Message string `json:"message"`
	}
	type scriptTestJSON struct {
		Name   string `json:"name"`
		Passed bool   `json:"passed"`
		Error  string `json:"error,omitempty"`
	}
	type resultJSON struct {
		Name        string           `json:"name"`
		File        string           `json:"file"`
		Method      string           `json:"method"`
		URL         string           `json:"url"`
		XrayKey     string           `json:"xrayKey,omitempty"`
		Tags        []string         `json:"tags,omitempty"`
		Priority    string           `json:"priority,omitempty"`
		Status      int              `json:"status,omitempty"`
		DurationMs  int64            `json:"duration_ms"`
		Error       string           `json:"error,omitempty"`
		Passed      bool             `json:"passed"`
		Assertions  []assertionJSON  `json:"assertions,omitempty"`
		ScriptTests []scriptTestJSON `json:"scriptTests,omitempty"`
	}
	out := struct {
		Root       string       `json:"root"`
		Started    string       `json:"started"`
		DurationMs int64        `json:"duration_ms"`
		Tests      int          `json:"tests"`
		Failures   int          `json:"failures"`
		Results    []resultJSON `json:"results"`
	}{
		Root:       r.Root,
		Started:    r.Started.UTC().Format("2006-01-02T15:04:05Z"),
		DurationMs: r.Duration.Milliseconds(),
		Tests:      len(r.Results),
		Failures:   r.Failures(),
	}
	for _, res := range r.Results {
		rj := resultJSON{
			Name:       res.Name,
			File:       res.File,
			Method:     res.Method,
			URL:        res.URL,
			XrayKey:    res.XrayKey,
			Tags:       append([]string(nil), res.Tags...),
			Priority:   res.Priority,
			Status:     res.Status,
			DurationMs: res.Duration.Milliseconds(),
			Passed:     !res.Failed(),
		}
		if res.Err != nil {
			rj.Error = res.Err.Error()
		}
		for _, a := range res.Assertions {
			rj.Assertions = append(rj.Assertions, assertionJSON{
				Type: a.Assertion.Type, Passed: a.Passed, Message: a.Message,
			})
		}
		for _, t := range res.ScriptTests {
			rj.ScriptTests = append(rj.ScriptTests, scriptTestJSON{
				Name: t.Name, Passed: t.Passed, Error: t.Error,
			})
		}
		out.Results = append(out.Results, rj)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func junitProperties(res RequestResult) []junitProperty {
	var props []junitProperty
	if res.XrayKey != "" {
		props = append(props, junitProperty{Name: "xray.key", Value: res.XrayKey})
	}
	if res.Priority != "" {
		props = append(props, junitProperty{Name: "relay.priority", Value: res.Priority})
	}
	if len(res.Tags) > 0 {
		props = append(props, junitProperty{Name: "relay.tags", Value: strings.Join(res.Tags, ",")})
	}
	props = append(props, junitProperty{Name: "relay.file", Value: res.File})
	return props
}

func failureSummary(res RequestResult) string {
	if res.Err != nil {
		return res.Err.Error()
	}
	for _, a := range res.Assertions {
		if !a.Passed {
			return a.Message
		}
	}
	for _, t := range res.ScriptTests {
		if !t.Passed {
			return t.Name + ": " + t.Error
		}
	}
	return "failed"
}

func failureDetail(res RequestResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", res.Method, res.URL)
	if res.Err != nil {
		fmt.Fprintf(&b, "error: %v\n", res.Err)
	}
	for _, a := range res.Assertions {
		mark := "PASS"
		if !a.Passed {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "[%s] assert(%s): %s\n", mark, a.Assertion.Type, a.Message)
	}
	for _, t := range res.ScriptTests {
		mark := "PASS"
		if !t.Passed {
			mark = "FAIL"
		}
		detail := t.Name
		if t.Error != "" {
			detail += ": " + t.Error
		}
		fmt.Fprintf(&b, "[%s] pm.test: %s\n", mark, detail)
	}
	return b.String()
}
