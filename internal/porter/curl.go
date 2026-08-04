// Package porter converts between Relay's request format and external
// formats (curl, Postman Collection v2.x).
package porter

import (
	"sort"
	"strings"

	"github.com/muhaymien96/relay/internal/vars"
)

// Curl renders a resolved request as a copy-pasteable curl command.
func Curl(r *vars.Resolved) string {
	var parts []string
	parts = append(parts, "curl")
	parts = append(parts, "--location", "--request", r.Method)
	parts = append(parts, "--url", shellQuote(r.URL))

	names := make([]string, 0, len(r.Headers))
	for k := range r.Headers {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		for _, v := range r.Headers[k] {
			parts = append(parts, "--header", shellQuote(k+": "+v))
		}
	}

	if len(r.Body) > 0 {
		parts = append(parts, "--data-raw", shellQuote(string(r.Body)))
	}
	return strings.Join(parts, " ")
}

// shellQuote single-quotes s for POSIX shells.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n\"'\\$&|;<>()*?[]#~%{}`!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
