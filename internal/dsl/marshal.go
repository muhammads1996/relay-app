package dsl

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Marshal serializes a Request deterministically: fixed section order,
// sorted keys within tables, so imports and edits produce minimal git diffs.
func Marshal(r *Request) []byte {
	var b strings.Builder
	kv := func(k, v string) { fmt.Fprintf(&b, "%s = %s\n", k, quote(v)) }

	kv("name", r.Name)
	kv("method", r.Method)
	kv("url", r.URL)
	if r.Owner != "" {
		kv("owner", r.Owner)
	}
	if r.Priority != "" {
		kv("priority", r.Priority)
	}
	if r.XrayKey != "" {
		kv("xray_key", r.XrayKey)
	}
	if r.XrayPlan != "" {
		kv("xray_plan", r.XrayPlan)
	}
	if r.XraySet != "" {
		kv("xray_set", r.XraySet)
	}
	writeList(&b, "tags", r.Tags)
	writeList(&b, "requirements", r.Requirements)

	writeTable(&b, "query", r.Query)
	writeTable(&b, "headers", r.Headers)
	writeTable(&b, "vars", r.Vars)
	writeEntries(&b, "query_entries", r.QueryEntries)
	writeEntries(&b, "header_entries", r.HeaderEntries)

	if a := r.Auth; a != nil {
		b.WriteString("\n[auth]\n")
		kv("type", a.Type)
		switch a.Type {
		case "bearer":
			kv("token", a.Token)
		case "basic":
			kv("username", a.Username)
			kv("password", a.Password)
		case "apikey":
			kv("key", a.Key)
			kv("value", a.Value)
			if a.In != "" {
				kv("in", a.In)
			}
		}
	}

	if body := r.Body; body != nil {
		b.WriteString("\n[body]\n")
		kv("type", body.Type)
		if body.File != "" {
			kv("file", body.File)
		} else if body.Content != "" {
			writeContent(&b, body.Content)
		}
		for _, f := range body.FormData {
			b.WriteString("\n[[body.form_data]]\n")
			kv("key", f.Key)
			if f.Type != "" {
				kv("type", f.Type)
			}
			if f.File != "" {
				kv("file", f.File)
			}
			if f.Value != "" {
				kv("value", f.Value)
			}
			if f.Disabled {
				fmt.Fprintf(&b, "disabled = true\n")
			}
		}
	}

	for _, a := range r.Assertions {
		b.WriteString("\n[[assertions]]\n")
		kv("type", a.Type)
		if a.Op != "" {
			kv("op", a.Op)
		}
		if a.Path != "" {
			kv("path", a.Path)
		}
		if a.Name != "" {
			kv("name", a.Name)
		}
		if a.Field != "" {
			kv("field", a.Field)
		}
		if a.Sub != "" {
			kv("sub", a.Sub)
		}
		if a.Exp != nil {
			fmt.Fprintf(&b, "exp = %s\n", literal(a.Exp))
		}
		if a.Code != "" {
			writeScript(&b, "code", a.Code)
		}
		if a.Equals != nil {
			fmt.Fprintf(&b, "equals = %s\n", literal(a.Equals))
		}
		if a.Contains != "" {
			kv("contains", a.Contains)
		}
		if a.MaxMs > 0 {
			fmt.Fprintf(&b, "max_ms = %d\n", a.MaxMs)
		}
	}

	if s := r.Scripts; s != nil && (s.PreRequest != "" || s.Tests != "") {
		b.WriteString("\n[scripts]\n")
		if s.PreRequest != "" {
			writeScript(&b, "pre_request", s.PreRequest)
		}
		if s.Tests != "" {
			writeScript(&b, "tests", s.Tests)
		}
	}

	return []byte(b.String())
}

func writeEntries(b *strings.Builder, name string, entries []Entry) {
	for _, entry := range entries {
		b.WriteString("\n[[" + name + "]]\n")
		fmt.Fprintf(b, "key = %s\n", quote(entry.Key))
		fmt.Fprintf(b, "value = %s\n", quote(entry.Value))
		if entry.Disabled {
			b.WriteString("disabled = true\n")
		}
	}
}

func writeList(b *strings.Builder, name string, items []string) {
	if len(items) == 0 {
		return
	}
	parts := make([]string, len(items))
	for i, s := range items {
		parts[i] = quote(s)
	}
	fmt.Fprintf(b, "%s = [%s]\n", name, strings.Join(parts, ", "))
}

// writeScript writes a JS script value using TOML multiline literal strings so
// that the source code is readable and git-diffable.
func writeScript(b *strings.Builder, key, src string) {
	if multilineSafe(src) {
		fmt.Fprintf(b, "%s = '''\n%s'''\n", key, src)
	} else {
		fmt.Fprintf(b, "%s = %s\n", key, quote(src))
	}
}

func writeTable(b *strings.Builder, name string, m map[string]string) {
	if len(m) == 0 {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(b, "\n[%s]\n", name)
	for _, k := range keys {
		fmt.Fprintf(b, "%s = %s\n", key(k), quote(m[k]))
	}
}

// writeContent prefers a multiline literal string (readable, diff-friendly)
// and falls back to a basic string when the content can't be represented.
// The content round-trips byte-exactly: no newline is appended.
func writeContent(b *strings.Builder, s string) {
	if multilineSafe(s) {
		// The newline right after the opening ''' is trimmed by TOML; the
		// closing delimiter sits on the same line unless the content itself
		// ends with a newline.
		fmt.Fprintf(b, "content = '''\n%s'''\n", s)
		return
	}
	fmt.Fprintf(b, "content = %s\n", quote(s))
}

func multilineSafe(s string) bool {
	if strings.Contains(s, "'''") || strings.HasSuffix(s, "'") {
		return false
	}
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}

func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// key writes a TOML key, quoting it when it isn't a bare key.
func key(s string) string {
	for _, r := range s {
		if !(r == '-' || r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return quote(s)
		}
	}
	if s == "" {
		return `""`
	}
	return s
}

func literal(v any) string {
	switch x := v.(type) {
	case string:
		return quote(x)
	case bool:
		return fmt.Sprintf("%t", x)
	case int:
		return fmt.Sprintf("%d", x)
	case int8:
		return fmt.Sprintf("%d", x)
	case int16:
		return fmt.Sprintf("%d", x)
	case int32:
		return fmt.Sprintf("%d", x)
	case int64:
		return fmt.Sprintf("%d", x)
	case uint:
		return fmt.Sprintf("%d", x)
	case uint8:
		return fmt.Sprintf("%d", x)
	case uint16:
		return fmt.Sprintf("%d", x)
	case uint32:
		return fmt.Sprintf("%d", x)
	case uint64:
		return fmt.Sprintf("%d", x)
	case float32:
		return fmt.Sprintf("%g", x)
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	case json.Number:
		return x.String()
	default:
		return quote(fmt.Sprintf("%v", x))
	}
}
