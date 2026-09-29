package engine

import (
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// CookieSession is a reusable cookie jar owned by one UI workspace or one
// explicitly shared runner. A nil *CookieSession on engine.Options gives each
// send a fresh, isolated client with no retained cookies.
type CookieSession struct {
	mu      sync.Mutex
	jar     http.CookieJar
	records map[string]map[string]*http.Cookie // source origin -> cookie identity -> cookie
	enabled bool
}

// NewCookieSession creates an enabled, empty cookie session.
func NewCookieSession() *CookieSession {
	jar, _ := cookiejar.New(nil)
	return &CookieSession{jar: jar, records: make(map[string]map[string]*http.Cookie), enabled: true}
}

func (s *CookieSession) Cookies(u *url.URL) []*http.Cookie {
	if s == nil || u == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return nil
	}
	return cloneCookies(s.jar.Cookies(u))
}

// Inspect returns stored cookies that apply to u, including their original
// attributes. Unlike Cookies, inspection remains available when sending is
// disabled; net/http's jar only exposes a reduced cookie view.
func (s *CookieSession) Inspect(u *url.URL) []*http.Cookie {
	if s == nil || u == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	byIdentity := make(map[string]*http.Cookie)
	for origin, records := range s.records {
		source, err := url.Parse(origin)
		if err != nil {
			continue
		}
		for id, cookie := range records {
			if !cookieApplies(cookie, source, u, now) {
				continue
			}
			byIdentity[id] = cloneCookie(cookie)
		}
	}
	out := make([]*http.Cookie, 0, len(byIdentity))
	for _, cookie := range byIdentity {
		out = append(out, cookie)
	}
	return out
}

func cookieApplies(cookie *http.Cookie, source, target *url.URL, now time.Time) bool {
	if cookie.MaxAge < 0 || (!cookie.Expires.IsZero() && !cookie.Expires.After(now)) {
		return false
	}
	if cookie.Secure && target.Scheme != "https" {
		return false
	}
	host := strings.ToLower(target.Hostname())
	domain := strings.TrimPrefix(strings.ToLower(cookie.Domain), ".")
	if domain == "" {
		if host != strings.ToLower(source.Hostname()) {
			return false
		}
	} else if host != domain && !strings.HasSuffix(host, "."+domain) {
		return false
	}
	path := cookie.Path
	if path == "" || path[0] != '/' {
		path = "/"
	}
	requestPath := target.Path
	if requestPath == "" {
		requestPath = "/"
	}
	return requestPath == path || strings.HasPrefix(requestPath, path) && (strings.HasSuffix(path, "/") || len(requestPath) > len(path) && requestPath[len(path)] == '/')
}

func (s *CookieSession) SetCookies(u *url.URL, cookies []*http.Cookie) {
	if s == nil || u == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return
	}
	key := cookieOrigin(u)
	if s.records[key] == nil {
		s.records[key] = make(map[string]*http.Cookie)
	}
	for _, cookie := range cookies {
		if cookie == nil {
			continue
		}
		id := cookieIdentity(cookie)
		if cookie.MaxAge < 0 || (!cookie.Expires.IsZero() && !cookie.Expires.After(time.Now())) {
			delete(s.records[key], id)
		} else {
			s.records[key][id] = cloneCookie(cookie)
		}
	}
	s.jar.SetCookies(u, cookies)
}

// Enabled reports whether the session currently sends and accepts cookies.
func (s *CookieSession) Enabled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

// SetEnabled controls cookie use without discarding the retained session.
func (s *CookieSession) SetEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.enabled = enabled
	s.mu.Unlock()
}

// Clear removes cookies associated with an exact origin. An empty origin clears
// the whole session. Origin must be an absolute URL.
func (s *CookieSession) Clear(origin string) error {
	if s == nil {
		return nil
	}
	key := ""
	if strings.TrimSpace(origin) != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return errors.New("origin must be an absolute URL")
		}
		key = cookieOrigin(u)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == "" {
		s.records = make(map[string]map[string]*http.Cookie)
	} else {
		delete(s.records, key)
	}
	return s.rebuildLocked()
}

func (s *CookieSession) rebuildLocked() error {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	for origin, records := range s.records {
		u, err := url.Parse(origin)
		if err != nil {
			continue
		}
		cookies := make([]*http.Cookie, 0, len(records))
		for _, cookie := range records {
			cookies = append(cookies, cloneCookie(cookie))
		}
		jar.SetCookies(u, cookies)
	}
	s.jar = jar
	return nil
}

func cookieOrigin(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

func cookieIdentity(c *http.Cookie) string {
	return strings.ToLower(c.Domain) + "\x00" + c.Path + "\x00" + c.Name
}

func cloneCookies(in []*http.Cookie) []*http.Cookie {
	out := make([]*http.Cookie, len(in))
	for i, cookie := range in {
		out[i] = cloneCookie(cookie)
	}
	return out
}

func cloneCookie(c *http.Cookie) *http.Cookie {
	copy := *c
	return &copy
}
