// Package assert evaluates the [[assertions]] blocks of a request against a
// response. The jsonpath support is a deliberate subset: dot fields and
// numeric indexes ($.result.items[0].status), which covers the common case
// without a dependency.
package assert

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/engine"
	"github.com/muhaymien96/relay/internal/script"
)

// Outcome is one evaluated assertion.
type Outcome struct {
	Assertion dsl.Assertion
	Passed    bool
	Message   string // human-readable failure (or pass) description
}

// Evaluate runs every assertion against the result.
func Evaluate(assertions []dsl.Assertion, res *engine.Result) []Outcome {
	out := make([]Outcome, 0, len(assertions))
	for _, a := range assertions {
		o := Outcome{Assertion: a}
		o.Passed, o.Message = evalOne(a, res)
		out = append(out, o)
	}
	return out
}

func evalOne(a dsl.Assertion, res *engine.Result) (bool, string) {
	switch a.Type {
	case "status":
		switch a.Op {
		case "is2xx":
			if res.Status >= 200 && res.Status < 300 {
				return true, fmt.Sprintf("status %d is 2xx", res.Status)
			}
			return false, fmt.Sprintf("expected status to be 2xx, got %d", res.Status)
		case "oneof":
			opts := expList(firstNonNil(a.Exp, a.Equals))
			for _, o := range opts {
				if fmt.Sprintf("%d", res.Status) == o {
					return true, fmt.Sprintf("status %d is one of %v", res.Status, opts)
				}
			}
			return false, fmt.Sprintf("expected status to be one of %v, got %d", opts, res.Status)
		default:
			want, ok := expInt(firstNonNil(a.Exp, a.Equals))
			if !ok {
				return false, fmt.Sprintf("status assertion needs an integer `exp`, got %v", a.Exp)
			}
			if int64(res.Status) == want {
				return true, fmt.Sprintf("status is %d", res.Status)
			}
			return false, fmt.Sprintf("expected status %d, got %d", want, res.Status)
		}

	case "jsonpath", "json":
		var doc any
		if err := json.Unmarshal(res.Body, &doc); err != nil {
			return false, fmt.Sprintf("response is not valid JSON: %v", err)
		}
		val, verr := jsonPath(doc, a.Path)
		switch a.Op {
		case "exists":
			if verr == nil {
				return true, fmt.Sprintf("%s exists", a.Path)
			}
			return false, fmt.Sprintf("%s does not exist", a.Path)
		case "gt":
			if verr != nil {
				return false, verr.Error()
			}
			got, gok := expFloat(val)
			want, wok := expFloat(a.Exp)
			if !gok || !wok {
				return false, fmt.Sprintf("%s: cannot compare %v > %v numerically", a.Path, val, a.Exp)
			}
			if got > want {
				return true, fmt.Sprintf("%s = %v > %v", a.Path, got, want)
			}
			return false, fmt.Sprintf("%s = %v, expected > %v", a.Path, got, want)
		case "lengthGt":
			if verr != nil {
				return false, verr.Error()
			}
			n := valueLen(val)
			want, wok := expFloat(a.Exp)
			if !wok {
				return false, fmt.Sprintf("%s: length comparison needs numeric `exp`, got %v", a.Path, a.Exp)
			}
			if float64(n) > want {
				return true, fmt.Sprintf("%s length %d > %v", a.Path, n, want)
			}
			return false, fmt.Sprintf("%s length %d, expected > %v", a.Path, n, want)
		default:
			if verr != nil {
				return false, verr.Error()
			}
			want := firstNonNil(a.Exp, a.Equals)
			if equal(val, want) {
				return true, fmt.Sprintf("%s == %v", a.Path, want)
			}
			return false, fmt.Sprintf("%s: expected %v, got %v", a.Path, want, val)
		}

	case "header":
		got := res.Headers.Get(a.Name)
		switch a.Op {
		case "exists":
			if len(res.Headers.Values(a.Name)) > 0 {
				return true, fmt.Sprintf("header %s exists", a.Name)
			}
			return false, fmt.Sprintf("header %s does not exist", a.Name)
		case "contains":
			want := fmt.Sprintf("%v", firstNonNil(a.Exp, a.Contains))
			if strings.Contains(got, want) {
				return true, fmt.Sprintf("header %s contains %q", a.Name, want)
			}
			return false, fmt.Sprintf("header %s = %q does not contain %q", a.Name, got, want)
		default:
			if a.Contains != "" {
				if strings.Contains(got, a.Contains) {
					return true, fmt.Sprintf("header %s contains %q", a.Name, a.Contains)
				}
				return false, fmt.Sprintf("header %s = %q does not contain %q", a.Name, got, a.Contains)
			}
			want := fmt.Sprintf("%v", firstNonNil(a.Exp, a.Equals))
			if got == want {
				return true, fmt.Sprintf("header %s == %q", a.Name, want)
			}
			return false, fmt.Sprintf("header %s: expected %q, got %q", a.Name, want, got)
		}

	case "contains", "text":
		want := fmt.Sprintf("%v", firstNonNil(a.Exp, a.Contains))
		has := strings.Contains(string(res.Body), want)
		if a.Op == "notcontains" {
			if !has {
				return true, fmt.Sprintf("body does not contain %q", want)
			}
			return false, fmt.Sprintf("body contains %q, expected not to", want)
		}
		if has {
			return true, fmt.Sprintf("body contains %q", want)
		}
		return false, fmt.Sprintf("body does not contain %q", want)

	case "max_ms", "timing":
		ms := res.Timing.Total.Milliseconds()
		limit := a.MaxMs
		if l, ok := expInt(a.Exp); ok {
			limit = l
		}
		if ms <= limit {
			return true, fmt.Sprintf("took %dms (limit %dms)", ms, limit)
		}
		return false, fmt.Sprintf("took %dms, limit %dms", ms, limit)

	case "forall", "exists":
		var doc any
		if err := json.Unmarshal(res.Body, &doc); err != nil {
			return false, fmt.Sprintf("response is not valid JSON: %v", err)
		}
		val, verr := jsonPath(doc, a.Path)
		if verr != nil {
			return false, verr.Error()
		}
		arr, ok := val.([]any)
		if !ok {
			return false, fmt.Sprintf("%s is not an array", a.Path)
		}
		sub := a.Sub
		if sub == "" {
			sub = "=="
		}
		if a.Type == "forall" {
			for i, item := range arr {
				got := itemField(item, a.Field)
				if !compareOp(got, sub, a.Exp) {
					return false, fmt.Sprintf("%s[%d].%s = %v fails %s %v", a.Path, i, fieldOrSelf(a.Field), got, sub, a.Exp)
				}
			}
			return true, fmt.Sprintf("all %d items at %s satisfy %s %s %v", len(arr), a.Path, fieldOrSelf(a.Field), sub, a.Exp)
		}
		for _, item := range arr {
			got := itemField(item, a.Field)
			if compareOp(got, sub, a.Exp) {
				return true, fmt.Sprintf("found item at %s where %s %s %v", a.Path, fieldOrSelf(a.Field), sub, a.Exp)
			}
		}
		return false, fmt.Sprintf("no item at %s has %s %s %v", a.Path, fieldOrSelf(a.Field), sub, a.Exp)

	case "count":
		var doc any
		if err := json.Unmarshal(res.Body, &doc); err != nil {
			return false, fmt.Sprintf("response is not valid JSON: %v", err)
		}
		val, verr := jsonPath(doc, a.Path)
		if verr != nil {
			return false, verr.Error()
		}
		n := valueLen(val)
		want, wok := expFloat(a.Exp)
		if !wok {
			want = 0
		}
		op := a.Op
		if op == "" {
			op = "=="
		}
		if compareNum(float64(n), op, want) {
			return true, fmt.Sprintf("count at %s is %d (%s %v)", a.Path, n, op, want)
		}
		return false, fmt.Sprintf("count at %s is %d, expected %s %v", a.Path, n, op, want)

	case "script":
		return evalScript(a, res)

	default:
		return false, fmt.Sprintf("unknown assertion type %q", a.Type)
	}
}

// evalScript runs an inline assertion script through the same goja-based
// pm.* runtime used for request-level test scripts. A script with no
// pm.test() calls passes if it runs without throwing.
func evalScript(a dsl.Assertion, res *engine.Result) (bool, string) {
	headers := map[string]string{}
	for k := range res.Headers {
		headers[k] = res.Headers.Get(k)
	}
	sr := script.RunTests(a.Code, &script.Scope{
		Env:        map[string]string{},
		Collection: map[string]string{},
		Request:    map[string]string{},
	}, &script.Response{
		Code:       res.Status,
		Status:     res.StatusText,
		Headers:    headers,
		Body:       res.Body,
		DurationMs: float64(res.Timing.Total.Microseconds()) / 1000,
	})
	if len(sr.Errors) > 0 {
		return false, strings.Join(sr.Errors, "; ")
	}
	if len(sr.Tests) == 0 {
		return true, "script ran without throwing"
	}
	msgs := make([]string, 0, len(sr.Tests))
	passed := true
	for _, t := range sr.Tests {
		if !t.Passed {
			passed = false
			msgs = append(msgs, fmt.Sprintf("%s: %s", t.Name, t.Error))
		} else {
			msgs = append(msgs, t.Name+" passed")
		}
	}
	return passed, strings.Join(msgs, "; ")
}

// jsonPath walks a subset of JSONPath: `$`, `.field`, `["field"]`, `[N]`.
func jsonPath(doc any, path string) (any, error) {
	p := strings.TrimSpace(path)
	if !strings.HasPrefix(p, "$") {
		return nil, fmt.Errorf("jsonpath must start with $: %q", path)
	}
	p = p[1:]
	cur := doc
	for len(p) > 0 {
		switch {
		case strings.HasPrefix(p, "."):
			p = p[1:]
			end := strings.IndexAny(p, ".[")
			if end == -1 {
				end = len(p)
			}
			field := p[:end]
			p = p[end:]
			if field == "" {
				return nil, fmt.Errorf("empty field in jsonpath %q", path)
			}
			cur2, err := getField(cur, field, path)
			if err != nil {
				return nil, err
			}
			cur = cur2
		case strings.HasPrefix(p, "["):
			end := strings.Index(p, "]")
			if end == -1 {
				return nil, fmt.Errorf("unclosed [ in jsonpath %q", path)
			}
			idx := p[1:end]
			p = p[end+1:]
			if len(idx) >= 2 && (idx[0] == '"' || idx[0] == '\'') {
				cur2, err := getField(cur, idx[1:len(idx)-1], path)
				if err != nil {
					return nil, err
				}
				cur = cur2
				continue
			}
			n, err := strconv.Atoi(idx)
			if err != nil {
				return nil, fmt.Errorf("bad index %q in jsonpath %q", idx, path)
			}
			arr, ok := cur.([]any)
			if !ok {
				return nil, fmt.Errorf("%s: not an array at [%d]", path, n)
			}
			if n < 0 || n >= len(arr) {
				return nil, fmt.Errorf("%s: index %d out of range (len %d)", path, n, len(arr))
			}
			cur = arr[n]
		default:
			return nil, fmt.Errorf("unexpected %q in jsonpath %q", p[:1], path)
		}
	}
	return cur, nil
}

func getField(cur any, field, path string) (any, error) {
	obj, ok := cur.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: not an object at %q", path, field)
	}
	v, ok := obj[field]
	if !ok {
		return nil, fmt.Errorf("%s: field %q not found", path, field)
	}
	return v, nil
}

// equal compares a JSON value with a TOML literal, normalizing numbers
// (JSON decodes to float64, TOML to int64).
func equal(got, want any) bool {
	if gn, ok := toFloat(got); ok {
		if wn, ok := toFloat(want); ok {
			return gn == wn
		}
	}
	return fmt.Sprintf("%v", got) == fmt.Sprintf("%v", want)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}

func toInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case float64:
		return int64(x), true
	}
	return 0, false
}

// firstNonNil returns a if it is non-nil and not an empty string, else b.
// Used to prefer the new Exp field over the legacy Equals/Contains fields.
func firstNonNil(a, b any) any {
	if a == nil {
		return b
	}
	if s, ok := a.(string); ok && s == "" {
		return b
	}
	return a
}

// expFloat coerces an assertion exp value (JSON float64, TOML int64, or a
// string left over from {{var}} interpolation) into a float64.
func expFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err == nil {
			return f, true
		}
	}
	return 0, false
}

// expInt is expFloat truncated to an integer.
func expInt(v any) (int64, bool) {
	f, ok := expFloat(v)
	if !ok {
		return 0, false
	}
	return int64(f), true
}

// expList splits a oneof exp value into string terms: a JSON/TOML array or
// a comma-separated string ("200, 201, 204").
func expList(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, len(x))
		for i, e := range x {
			out[i] = fmt.Sprintf("%v", e)
		}
		return out
	case string:
		var out []string
		for _, p := range strings.Split(x, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return nil
}

// valueLen reports the length of an array, object, or string JSON value.
func valueLen(v any) int {
	switch x := v.(type) {
	case []any:
		return len(x)
	case map[string]any:
		return len(x)
	case string:
		return len(x)
	}
	return 0
}

// itemField extracts a named field from an array item for forall/exists
// quantifiers. An empty field compares the item itself.
func itemField(item any, field string) any {
	if field == "" {
		return item
	}
	if m, ok := item.(map[string]any); ok {
		return m[field]
	}
	return nil
}

func fieldOrSelf(field string) string {
	if field == "" {
		return "value"
	}
	return field
}

// compareOp evaluates `got <sub> want`, comparing numerically when both
// sides parse as numbers and falling back to string equality otherwise.
func compareOp(got any, sub string, want any) bool {
	if gf, gok := expFloat(got); gok {
		if wf, wok := expFloat(want); wok {
			return compareNum(gf, sub, wf)
		}
	}
	gs := fmt.Sprintf("%v", got)
	ws := fmt.Sprintf("%v", want)
	switch sub {
	case "!=":
		return gs != ws
	default:
		return gs == ws
	}
}

func compareNum(got float64, op string, want float64) bool {
	switch op {
	case ">":
		return got > want
	case "<":
		return got < want
	case ">=":
		return got >= want
	case "<=":
		return got <= want
	case "!=":
		return got != want
	default: // "==" and "equals"
		return got == want
	}
}
