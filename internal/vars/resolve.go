package vars

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/muhaymien96/relay/internal/dsl"
)

// Resolved is a request after variable interpolation, header inheritance,
// and auth application: exactly what goes on the wire.
type Resolved struct {
	Method  string
	URL     string
	Headers http.Header
	// HeaderOrigin records where each header came from ("request",
	// "inherited", "auth") for display and export decisions.
	HeaderOrigin map[string]string
	Body         []byte
	BodyType     string
}

// Resolve merges inherited headers (collection → folder, lowest precedence)
// with the request's own, interpolates every part, and applies auth.
func Resolve(r *dsl.Request, inherited map[string]string, scope *Scope) (*Resolved, error) {
	res := &Resolved{
		Method:       r.Method,
		Headers:      http.Header{},
		HeaderOrigin: map[string]string{},
	}

	u, err := scope.Interpolate(r.URL)
	if err != nil {
		return nil, fmt.Errorf("url: %w", err)
	}
	if len(r.Query) > 0 {
		parsed, err := url.Parse(u)
		if err != nil {
			return nil, fmt.Errorf("url %q: %w", u, err)
		}
		q := parsed.Query()
		for k, v := range r.Query {
			iv, err := scope.Interpolate(v)
			if err != nil {
				return nil, fmt.Errorf("query %s: %w", k, err)
			}
			q.Set(k, iv)
		}
		parsed.RawQuery = q.Encode()
		u = parsed.String()
	}
	res.URL = u

	setHeader := func(name, value, origin string) error {
		v, err := scope.Interpolate(value)
		if err != nil {
			return fmt.Errorf("header %s: %w", name, err)
		}
		res.Headers.Set(name, v)
		res.HeaderOrigin[http.CanonicalHeaderKey(name)] = origin
		return nil
	}
	for k, v := range inherited {
		if err := setHeader(k, v, "inherited"); err != nil {
			return nil, err
		}
	}
	for k, v := range r.Headers {
		if v == "" { // empty value disables an inherited header
			res.Headers.Del(k)
			delete(res.HeaderOrigin, http.CanonicalHeaderKey(k))
			continue
		}
		if err := setHeader(k, v, "request"); err != nil {
			return nil, err
		}
	}

	if r.Body != nil {
		if err := resolveBody(r, res, scope); err != nil {
			return nil, err
		}
	}

	if r.Auth != nil {
		if err := applyAuth(r.Auth, res, scope); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func resolveBody(r *dsl.Request, res *Resolved, scope *Scope) error {
	b := r.Body
	res.BodyType = b.Type
	switch {
	case b.Type == "formdata":
		return resolveFormData(r, res, scope)
	case b.File != "":
		path := b.File
		if !filepath.IsAbs(path) && r.Path != "" {
			path = filepath.Join(filepath.Dir(r.Path), path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("body file: %w", err)
		}
		res.Body = data
	default:
		content, err := scope.Interpolate(b.Content)
		if err != nil {
			return fmt.Errorf("body: %w", err)
		}
		res.Body = []byte(content)
	}
	if res.Headers.Get("Content-Type") == "" {
		if ct := defaultContentType(b.Type); ct != "" {
			res.Headers.Set("Content-Type", ct)
			res.HeaderOrigin["Content-Type"] = "request"
		}
	}
	return nil
}

func resolveFormData(r *dsl.Request, res *Resolved, scope *Scope) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, field := range r.Body.FormData {
		if field.Disabled || field.Key == "" {
			continue
		}
		key, err := scope.Interpolate(field.Key)
		if err != nil {
			return fmt.Errorf("form-data key %s: %w", field.Key, err)
		}
		if field.Type == "file" {
			path, err := scope.Interpolate(field.File)
			if err != nil {
				return fmt.Errorf("form-data file %s: %w", field.Key, err)
			}
			if !filepath.IsAbs(path) && r.Path != "" {
				path = filepath.Join(filepath.Dir(r.Path), path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("form-data file %s: %w", field.Key, err)
			}
			part, err := w.CreateFormFile(key, filepath.Base(path))
			if err != nil {
				return fmt.Errorf("form-data file %s: %w", field.Key, err)
			}
			if _, err := part.Write(data); err != nil {
				return fmt.Errorf("form-data file %s: %w", field.Key, err)
			}
			continue
		}
		value, err := scope.Interpolate(field.Value)
		if err != nil {
			return fmt.Errorf("form-data %s: %w", field.Key, err)
		}
		if err := w.WriteField(key, value); err != nil {
			return fmt.Errorf("form-data %s: %w", field.Key, err)
		}
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("form-data: %w", err)
	}
	res.Body = buf.Bytes()
	if res.Headers.Get("Content-Type") == "" {
		res.Headers.Set("Content-Type", w.FormDataContentType())
		res.HeaderOrigin["Content-Type"] = "request"
	}
	return nil
}

func defaultContentType(bodyType string) string {
	switch bodyType {
	case "json":
		return "application/json"
	case "xml":
		return "application/xml"
	case "urlencoded":
		return "application/x-www-form-urlencoded"
	}
	return ""
}

func applyAuth(a *dsl.Auth, res *Resolved, scope *Scope) error {
	interp := func(s string) (string, error) { return scope.Interpolate(s) }
	switch a.Type {
	case "", "none":
		return nil
	case "bearer":
		tok, err := interp(a.Token)
		if err != nil {
			return err
		}
		res.Headers.Set("Authorization", "Bearer "+tok)
		res.HeaderOrigin["Authorization"] = "auth"
	case "basic":
		user, err := interp(a.Username)
		if err != nil {
			return err
		}
		pass, err := interp(a.Password)
		if err != nil {
			return err
		}
		req := http.Request{Header: res.Headers}
		req.SetBasicAuth(user, pass)
		res.HeaderOrigin["Authorization"] = "auth"
	case "apikey":
		val, err := interp(a.Value)
		if err != nil {
			return err
		}
		if a.In == "query" {
			u, err := url.Parse(res.URL)
			if err != nil {
				return err
			}
			q := u.Query()
			q.Set(a.Key, val)
			u.RawQuery = q.Encode()
			res.URL = u.String()
		} else {
			res.Headers.Set(a.Key, val)
			res.HeaderOrigin[http.CanonicalHeaderKey(a.Key)] = "auth"
		}
	default:
		return fmt.Errorf("unsupported auth type %q", a.Type)
	}
	return nil
}
