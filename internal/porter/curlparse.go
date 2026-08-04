package porter

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/muhaymien96/relay/internal/dsl"
)

// ParseCurl converts a curl command line into a request. It understands the
// flags people actually paste from DevTools, docs, and terminals: method,
// headers, data, basic auth, cookies, user agent; transport-only flags are
// ignored.
func ParseCurl(command string) (*dsl.Request, error) {
	tokens, err := shellSplit(command)
	if err != nil {
		return nil, err
	}
	start := -1
	for i, tok := range tokens {
		if isCurlToken(tok) {
			start = i
			break
		}
	}
	if start == -1 {
		return nil, fmt.Errorf("not a curl command")
	}
	tokens = tokens[start:]

	req := &dsl.Request{Headers: map[string]string{}}
	var dataParts []string
	asGet := false

	next := func(i *int, flag string) (string, error) {
		*i++
		if *i >= len(tokens) {
			return "", fmt.Errorf("%s needs a value", flag)
		}
		return tokens[*i], nil
	}

	for i := 1; i < len(tokens); i++ {
		t := tokens[i]
		switch {
		case t == "^" || t == "`":
			// Windows line continuation markers; ignore as no-op tokens.
			continue
		case strings.HasPrefix(t, "--request="):
			req.Method = strings.ToUpper(strings.TrimPrefix(t, "--request="))
		case strings.HasPrefix(t, "--header="):
			v := strings.TrimPrefix(t, "--header=")
			name, value, ok := strings.Cut(v, ":")
			if !ok {
				continue
			}
			req.Headers[strings.TrimSpace(name)] = strings.TrimSpace(value)
		case strings.HasPrefix(t, "--url="):
			req.URL = strings.TrimPrefix(t, "--url=")
		case strings.HasPrefix(t, "--data="):
			dataParts = append(dataParts, strings.TrimPrefix(t, "--data="))
		case strings.HasPrefix(t, "--data-raw="):
			dataParts = append(dataParts, strings.TrimPrefix(t, "--data-raw="))
		case strings.HasPrefix(t, "--data-binary="):
			dataParts = append(dataParts, strings.TrimPrefix(t, "--data-binary="))
		case t == "-X" || t == "--request":
			v, err := next(&i, t)
			if err != nil {
				return nil, err
			}
			req.Method = strings.ToUpper(v)
		case t == "-H" || t == "--header":
			v, err := next(&i, t)
			if err != nil {
				return nil, err
			}
			name, value, ok := strings.Cut(v, ":")
			if !ok {
				continue
			}
			req.Headers[strings.TrimSpace(name)] = strings.TrimSpace(value)
		case t == "-d" || t == "--data" || t == "--data-raw" || t == "--data-binary" ||
			t == "--data-ascii" || t == "--data-urlencode":
			v, err := next(&i, t)
			if err != nil {
				return nil, err
			}
			dataParts = append(dataParts, v)
		case t == "-u" || t == "--user":
			v, err := next(&i, t)
			if err != nil {
				return nil, err
			}
			user, pass, _ := strings.Cut(v, ":")
			req.Auth = &dsl.Auth{Type: "basic", Username: user, Password: pass}
		case t == "-A" || t == "--user-agent":
			v, err := next(&i, t)
			if err != nil {
				return nil, err
			}
			req.Headers["User-Agent"] = v
		case t == "-e" || t == "--referer":
			v, err := next(&i, t)
			if err != nil {
				return nil, err
			}
			req.Headers["Referer"] = v
		case t == "-b" || t == "--cookie":
			v, err := next(&i, t)
			if err != nil {
				return nil, err
			}
			req.Headers["Cookie"] = v
		case t == "--url":
			v, err := next(&i, t)
			if err != nil {
				return nil, err
			}
			req.URL = v
		case t == "-G" || t == "--get":
			asGet = true
		// Value-taking flags we deliberately drop (transport/output only).
		case t == "-o" || t == "--output" || t == "--connect-timeout" || t == "--max-time" ||
			t == "--retry" || t == "-m" || t == "--cacert" || t == "--cert" || t == "--key":
			if _, err := next(&i, t); err != nil {
				return nil, err
			}
		case strings.HasPrefix(t, "-"):
			// Boolean flags: -s, -L, -k, -i, -v, --compressed, --insecure, …
		default:
			if req.URL == "" {
				req.URL = t
			}
		}
	}

	if req.URL == "" {
		return nil, fmt.Errorf("no URL in curl command")
	}
	data := strings.Join(dataParts, "&")

	switch {
	case asGet && data != "":
		req.Method = firstNonEmpty(req.Method, "GET")
		sep := "?"
		if strings.Contains(req.URL, "?") {
			sep = "&"
		}
		req.URL += sep + data
	case data != "":
		req.Method = firstNonEmpty(req.Method, "POST")
		req.Body = &dsl.Body{Type: curlBodyType(req.Headers, data), Content: data}
	default:
		req.Method = firstNonEmpty(req.Method, "GET")
	}

	// Bearer tokens become the auth helper instead of a raw header.
	if auth := req.Headers["Authorization"]; req.Auth == nil && strings.HasPrefix(auth, "Bearer ") {
		req.Auth = &dsl.Auth{Type: "bearer", Token: strings.TrimPrefix(auth, "Bearer ")}
		delete(req.Headers, "Authorization")
	}
	if len(req.Headers) == 0 {
		req.Headers = nil
	}
	req.Name = curlName(req.Method, req.URL)
	return req, nil
}

func curlBodyType(headers map[string]string, data string) string {
	ct := ""
	for k, v := range headers {
		if strings.EqualFold(k, "Content-Type") {
			ct = v
		}
	}
	switch {
	case strings.Contains(ct, "json"):
		return "json"
	case strings.Contains(ct, "xml"):
		return "xml"
	case strings.Contains(ct, "urlencoded"):
		return "urlencoded"
	case ct == "":
		trimmed := strings.TrimSpace(data)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			return "json"
		}
		return "urlencoded" // curl's default for -d
	}
	return "raw"
}

func curlName(method, raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Path != "" && u.Path != "/" {
		return method + " " + u.Path
	}
	return method + " request"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func isCurlToken(tok string) bool {
	tok = strings.ToLower(strings.TrimSpace(tok))
	if tok == "curl" || tok == "curl.exe" {
		return true
	}
	tok = strings.ReplaceAll(tok, "\\", "/")
	return strings.HasSuffix(tok, "/curl") || strings.HasSuffix(tok, "/curl.exe")
}

// shellSplit tokenizes a POSIX-ish command line: single quotes (no
// escapes), double quotes (\\ \" escapes), bare-word backslash escapes, and
// backslash-newline continuations.
func shellSplit(s string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	started := false
	i := 0
	for i < len(s) {
		c := s[i]
		switch c {
		case ' ', '\t', '\n', '\r':
			if started {
				tokens = append(tokens, cur.String())
				cur.Reset()
				started = false
			}
			i++
		case '\'':
			started = true
			end := strings.IndexByte(s[i+1:], '\'')
			if end == -1 {
				return nil, fmt.Errorf("unterminated single quote")
			}
			cur.WriteString(s[i+1 : i+1+end])
			i += end + 2
		case '"':
			started = true
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\\\"$`\n", s[i+1]) != -1 {
					if s[i+1] != '\n' {
						cur.WriteByte(s[i+1])
					}
					i += 2
					continue
				}
				cur.WriteByte(s[i])
				i++
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated double quote")
			}
			i++
		case '\\':
			if i+1 < len(s) {
				if s[i+1] == '\n' { // line continuation
					i += 2
					continue
				}
				if s[i+1] == '\r' { // CRLF continuation
					i += 2
					if i < len(s) && s[i] == '\n' {
						i++
					}
					continue
				}
				started = true
				cur.WriteByte(s[i+1])
				i += 2
			} else {
				i++
			}
		default:
			started = true
			cur.WriteByte(c)
			i++
		}
	}
	if started {
		tokens = append(tokens, cur.String())
	}
	return tokens, nil
}
