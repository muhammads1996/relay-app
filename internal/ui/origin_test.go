package ui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func prepareLocalRequest(r *http.Request) *http.Request {
	r.Host = "127.0.0.1:7717"
	r.RemoteAddr = "127.0.0.1:54321"
	return r
}

func TestLocalOriginGuard(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	guard := localOriginGuard(next)
	tests := []struct {
		name, host, origin, remote string
		want                       int
	}{
		{"same origin", "127.0.0.1:7717", "http://127.0.0.1:7717", "127.0.0.1:5000", 204},
		{"wails windows", "wails.localhost", "http://wails.localhost", "", 204},
		{"wails native", "wails.localhost", "wails://wails.localhost", "", 204},
		{"null", "127.0.0.1:7717", "null", "127.0.0.1:5000", 403},
		{"foreign origin", "127.0.0.1:7717", "http://evil.example", "127.0.0.1:5000", 403},
		{"bad host", "relay.example:7717", "http://relay.example:7717", "127.0.0.1:5000", 403},
		{"originless local", "localhost:7717", "", "127.0.0.1:5000", 204},
		{"originless remote", "127.0.0.1:7717", "", "192.0.2.1:5000", 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://"+tt.host+"/api/requests", nil)
			r.Host, r.RemoteAddr = tt.host, tt.remote
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			w := httptest.NewRecorder()
			guard.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d", w.Code, tt.want)
			}
		})
	}
}
