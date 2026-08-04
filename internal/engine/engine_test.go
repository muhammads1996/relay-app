package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/muhaymien96/relay/internal/vars"
)

func TestSendCapturesTiming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("X-Probe") != "1" {
			t.Errorf("got %s, X-Probe=%q", r.Method, r.Header.Get("X-Probe"))
		}
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	res, err := Send(context.Background(), &vars.Resolved{
		Method:  "POST",
		URL:     srv.URL,
		Headers: http.Header{"X-Probe": []string{"1"}},
		Body:    []byte(`{}`),
	}, NewOptions())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 201 {
		t.Errorf("status = %d", res.Status)
	}
	if string(res.Body) != `{"ok":true}` {
		t.Errorf("body = %s", res.Body)
	}
	if res.Timing.Total < 20*time.Millisecond {
		t.Errorf("total = %v, expected >= 20ms", res.Timing.Total)
	}
	if res.Timing.TTFB <= 0 {
		t.Errorf("ttfb = %v", res.Timing.TTFB)
	}
}

func TestRedirectPolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusFound)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	req := &vars.Resolved{Method: "GET", URL: srv.URL + "/old", Headers: http.Header{}}

	res, err := Send(context.Background(), req, NewOptions())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 200 {
		t.Errorf("follow: status = %d", res.Status)
	}

	opts := NewOptions()
	opts.FollowRedirects = false
	res, err = Send(context.Background(), req, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 302 {
		t.Errorf("no-follow: status = %d", res.Status)
	}
}

func TestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	opts := NewOptions()
	opts.Timeout = 50 * time.Millisecond
	_, err := Send(context.Background(), &vars.Resolved{Method: "GET", URL: srv.URL, Headers: http.Header{}}, opts)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestMaxBodyBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 1024))
	}))
	defer srv.Close()

	opts := NewOptions()
	opts.MaxBodyBytes = 100
	res, err := Send(context.Background(), &vars.Resolved{Method: "GET", URL: srv.URL, Headers: http.Header{}}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Size != 100 {
		t.Errorf("size = %d, want capped at 100", res.Size)
	}
}
