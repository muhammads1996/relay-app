package porter

import (
	"fmt"
	"strings"

	"github.com/muhaymien96/relay/internal/dsl"
)

var pwDialect = jsDialect{
	uuid:   "crypto.randomUUID()",
	ts:     "Math.floor(Date.now() / 1000)",
	iso:    "new Date().toISOString()",
	rand:   "Math.floor(Math.random() * 1000)",
	envRef: func(name string) string { return "process.env." + name },
}

// Playwright generates a Playwright API test spec from a collection
// directory: folders become describe blocks, assertions become expects.
// Secrets are referenced as process.env.RELAY_SECRET_*.
func Playwright(root string, env *dsl.Environment) (string, error) {
	items, err := collectExportItems(root, env)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("import { test, expect } from '@playwright/test';\n")

	byGroup := map[string][]exportItem{}
	for _, it := range items {
		byGroup[it.Group] = append(byGroup[it.Group], it)
	}
	for _, g := range groupOrder(items) {
		indent := ""
		if g != "" {
			fmt.Fprintf(&b, "\ntest.describe(%s, () => {\n", jsString(g))
			indent = "  "
		}
		for _, it := range byGroup[g] {
			writePlaywrightTest(&b, indent, it)
		}
		if g != "" {
			b.WriteString("});\n")
		}
	}
	return b.String(), nil
}

func writePlaywrightTest(b *strings.Builder, indent string, it exportItem) {
	fmt.Fprintf(b, "\n%stest(%s, async ({ request }) => {\n", indent, jsString(it.Name))
	in := indent + "  "

	fmt.Fprintf(b, "%sconst r = await request.fetch(%s, {\n", in, pwDialect.tmpl(it.URL))
	fmt.Fprintf(b, "%s  method: %s,\n", in, jsString(it.Method))
	fmt.Fprintf(b, "%s  headers: {\n", in)
	for _, h := range it.Headers {
		fmt.Fprintf(b, "%s    %s: %s,\n", in, jsString(h[0]), pwDialect.tmpl(h[1]))
	}
	fmt.Fprintf(b, "%s  },\n", in)
	if it.Body != "" {
		fmt.Fprintf(b, "%s  data: %s,\n", in, pwDialect.tmpl(it.Body))
	}
	fmt.Fprintf(b, "%s});\n", in)

	needsJSON, needsText := false, false
	for _, a := range it.Assertions {
		switch a.Type {
		case "jsonpath":
			needsJSON = true
		case "contains":
			needsText = true
		}
	}
	if needsJSON {
		fmt.Fprintf(b, "%sconst body = await r.json();\n", in)
	}
	if needsText {
		fmt.Fprintf(b, "%sconst text = await r.text();\n", in)
	}

	for _, a := range it.Assertions {
		line, ok := pwExpect(a)
		if !ok {
			fmt.Fprintf(b, "%s// unsupported assertion type %q skipped\n", in, a.Type)
			continue
		}
		fmt.Fprintf(b, "%s%s\n", in, line)
	}
	fmt.Fprintf(b, "%s});\n", indent)
}

func pwExpect(a dsl.Assertion) (string, bool) {
	switch a.Type {
	case "status":
		return fmt.Sprintf("expect(r.status()).toBe(%s);", jsValue(a.Equals)), true
	case "jsonpath":
		acc, err := jsAccessor(a.Path)
		if err != nil {
			return "", false
		}
		return fmt.Sprintf("expect(body%s).toBe(%s);", acc, jsValue(a.Equals)), true
	case "header":
		ref := fmt.Sprintf("r.headers()[%s]", jsString(strings.ToLower(a.Name)))
		if a.Contains != "" {
			return fmt.Sprintf("expect(%s).toContain(%s);", ref, jsString(a.Contains)), true
		}
		return fmt.Sprintf("expect(%s).toBe(%s);", ref, jsValue(a.Equals)), true
	case "contains":
		return fmt.Sprintf("expect(text).toContain(%s);", jsString(a.Contains)), true
	case "max_ms":
		// Playwright's request fixture exposes no per-request timing; the
		// latency budget stays in `relay run` / k6.
		return fmt.Sprintf("// max_ms %d: latency assertions are not exported to Playwright", a.MaxMs), true
	}
	return "", false
}

// jsAccessor converts our JSONPath subset to a JS property accessor:
// $.a.b[0]["k"] → .a.b[0][\"k\"].
func jsAccessor(path string) (string, error) {
	p := strings.TrimSpace(path)
	if !strings.HasPrefix(p, "$") {
		return "", fmt.Errorf("jsonpath must start with $: %q", path)
	}
	p = p[1:]
	var b strings.Builder
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
			if isJSIdent(field) {
				b.WriteString("." + field)
			} else {
				fmt.Fprintf(&b, "[%s]", jsString(field))
			}
		case strings.HasPrefix(p, "["):
			end := strings.Index(p, "]")
			if end == -1 {
				return "", fmt.Errorf("unclosed [ in %q", path)
			}
			idx := p[1:end]
			p = p[end+1:]
			if len(idx) >= 2 && (idx[0] == '"' || idx[0] == '\'') {
				fmt.Fprintf(&b, "[%s]", jsString(idx[1:len(idx)-1]))
			} else {
				fmt.Fprintf(&b, "[%s]", idx)
			}
		default:
			return "", fmt.Errorf("unexpected %q in %q", p[:1], path)
		}
	}
	return b.String(), nil
}

func isJSIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		ok := r == '_' || r == '$' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(i > 0 && r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}
