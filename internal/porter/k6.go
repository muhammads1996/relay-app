package porter

import (
	"fmt"
	"strings"

	"github.com/muhaymien96/relay/internal/dsl"
)

var k6Dialect = jsDialect{
	uuid:   "uuid()",
	ts:     "Math.floor(Date.now() / 1000)",
	iso:    "new Date().toISOString()",
	rand:   "randomInt()",
	envRef: func(name string) string { return "__ENV." + name },
}

// K6 generates a k6 load-test script from a collection directory: folders
// become groups, assertions become checks. Secrets are referenced as
// __ENV.RELAY_SECRET_* so the script carries no secret values.
func K6(root string, env *dsl.Environment) (string, error) {
	items, err := collectExportItems(root, env)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(`import http from 'k6/http';
import { check, group } from 'k6';

export const options = {
  vus: 1,
  iterations: 1,
  // Scale up once the scenario is green, e.g.:
  // stages: [
  //   { duration: '30s', target: 10 },
  //   { duration: '1m', target: 10 },
  //   { duration: '30s', target: 0 },
  // ],
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<2000'],
  },
};

function uuid() {
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    return (c === 'x' ? r : (r & 0x3) | 0x8).toString(16);
  });
}

function randomInt() {
  return Math.floor(Math.random() * 1000);
}

export default function () {
`)

	byGroup := map[string][]exportItem{}
	for _, it := range items {
		byGroup[it.Group] = append(byGroup[it.Group], it)
	}
	for _, g := range groupOrder(items) {
		indent := "  "
		if g != "" {
			fmt.Fprintf(&b, "  group(%s, () => {\n", jsString(g))
			indent = "    "
		}
		for _, it := range byGroup[g] {
			writeK6Request(&b, indent, it)
		}
		if g != "" {
			b.WriteString("  });\n")
		}
	}

	b.WriteString("}\n")
	return b.String(), nil
}

func writeK6Request(b *strings.Builder, indent string, it exportItem) {
	fmt.Fprintf(b, "%s// %s\n", indent, it.Name)
	body := "null"
	if it.Body != "" {
		body = k6Dialect.tmpl(it.Body)
	}
	fmt.Fprintf(b, "%s{\n", indent)
	in := indent + "  "
	fmt.Fprintf(b, "%sconst r = http.request(%s, %s, %s, {\n", in, jsString(it.Method), k6Dialect.tmpl(it.URL), body)
	fmt.Fprintf(b, "%s  headers: {\n", in)
	for _, h := range it.Headers {
		fmt.Fprintf(b, "%s    %s: %s,\n", in, jsString(h[0]), k6Dialect.tmpl(h[1]))
	}
	fmt.Fprintf(b, "%s  },\n%s});\n", in, in)

	if len(it.Assertions) > 0 {
		fmt.Fprintf(b, "%scheck(r, {\n", in)
		for _, a := range it.Assertions {
			name, expr, ok := k6Check(a)
			if !ok {
				fmt.Fprintf(b, "%s  // unsupported assertion type %q skipped\n", in, a.Type)
				continue
			}
			fmt.Fprintf(b, "%s  %s: (r) => %s,\n", in, jsString(name), expr)
		}
		fmt.Fprintf(b, "%s});\n", in)
	}
	fmt.Fprintf(b, "%s}\n", indent)
}

func k6Check(a dsl.Assertion) (name, expr string, ok bool) {
	switch a.Type {
	case "status":
		return fmt.Sprintf("status is %v", a.Equals),
			fmt.Sprintf("r.status === %s", jsValue(a.Equals)), true
	case "jsonpath":
		sel, err := gjsonSelector(a.Path)
		if err != nil {
			return "", "", false
		}
		return fmt.Sprintf("%s == %v", a.Path, a.Equals),
			fmt.Sprintf("r.json(%s) === %s", jsString(sel), jsValue(a.Equals)), true
	case "header":
		if a.Contains != "" {
			return fmt.Sprintf("header %s contains %s", a.Name, a.Contains),
				fmt.Sprintf("(r.headers[%s] || '').includes(%s)", jsString(canonicalK6Header(a.Name)), jsString(a.Contains)), true
		}
		return fmt.Sprintf("header %s equals %v", a.Name, a.Equals),
			fmt.Sprintf("r.headers[%s] === %s", jsString(canonicalK6Header(a.Name)), jsValue(a.Equals)), true
	case "contains":
		return fmt.Sprintf("body contains %s", a.Contains),
			fmt.Sprintf("r.body.includes(%s)", jsString(a.Contains)), true
	case "max_ms":
		return fmt.Sprintf("duration <= %dms", a.MaxMs),
			fmt.Sprintf("r.timings.duration <= %d", a.MaxMs), true
	}
	return "", "", false
}

// canonicalK6Header matches k6's response header capitalization
// (Canonical-Kebab-Case).
func canonicalK6Header(name string) string {
	parts := strings.Split(name, "-")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
		}
	}
	return strings.Join(parts, "-")
}

// gjsonSelector converts our JSONPath subset ($.a.b[0]["k"]) to the gjson
// selector syntax k6's r.json() expects (a.b.0.k).
func gjsonSelector(path string) (string, error) {
	p := strings.TrimSpace(path)
	if !strings.HasPrefix(p, "$") {
		return "", fmt.Errorf("jsonpath must start with $: %q", path)
	}
	p = p[1:]
	var parts []string
	for len(p) > 0 {
		switch {
		case strings.HasPrefix(p, "."):
			p = p[1:]
			end := strings.IndexAny(p, ".[")
			if end == -1 {
				end = len(p)
			}
			parts = append(parts, p[:end])
			p = p[end:]
		case strings.HasPrefix(p, "["):
			end := strings.Index(p, "]")
			if end == -1 {
				return "", fmt.Errorf("unclosed [ in %q", path)
			}
			idx := p[1:end]
			p = p[end+1:]
			if len(idx) >= 2 && (idx[0] == '"' || idx[0] == '\'') {
				idx = idx[1 : len(idx)-1]
			}
			parts = append(parts, idx)
		default:
			return "", fmt.Errorf("unexpected %q in %q", p[:1], path)
		}
	}
	return strings.Join(parts, "."), nil
}
