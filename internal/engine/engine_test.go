package engine

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muhaymien96/relay/internal/dsl"
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

func TestSendPreservesOrderedDuplicates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "%s\n%s", r.URL.RawQuery, strings.Join(r.Header.Values("X-Repeat"), ","))
	}))
	defer srv.Close()
	resolved := &vars.Resolved{
		Method: "GET", URL: srv.URL + "/echo?base=one",
		Headers:       http.Header{"X-Repeat": []string{"old"}, "X-Keep": []string{"yes"}},
		HeaderEntries: []dsl.Entry{{Key: "X-Repeat", Value: "first"}, {Key: "X-Repeat", Value: "second"}},
	}
	result, err := Send(context.Background(), resolved, NewOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result.Body), "base=one\nfirst,second"; got != want {
		t.Errorf("echo = %q, want %q", got, want)
	}
}

func TestSendResolvedOrderedQueryEntries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, r.URL.RawQuery)
	}))
	defer srv.Close()
	resolved, err := vars.Resolve(&dsl.Request{
		Method: "GET", URL: srv.URL + "/echo?base=one",
		QueryEntries: []dsl.Entry{
			{Key: "tag", Value: "first value"},
			{Key: "tag", Value: "ignored", Disabled: true},
			{Key: "tag", Value: "last"},
		},
	}, nil, vars.NewScope(nil))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Send(context.Background(), resolved, NewOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result.Body), "base=one&tag=first+value&tag=last"; got != want {
		t.Errorf("echo = %q, want %q", got, want)
	}
}

func TestCookieSessionMatchingOriginAndIsolation(t *testing.T) {
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "alpha", Path: "/"})
			return
		}
		if r.URL.Path == "/check" {
			_, _ = w.Write([]byte(r.Header.Get("Cookie")))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer login.Close()
	otherURL := strings.Replace(login.URL, "127.0.0.1", "localhost", 1)
	first, second := NewCookieSession(), NewCookieSession()
	firstOpts := NewOptions()
	firstOpts.Cookies = first
	if _, err := Send(context.Background(), &vars.Resolved{Method: "GET", URL: login.URL + "/login", Headers: http.Header{}}, firstOpts); err != nil {
		t.Fatal(err)
	}
	res, err := Send(context.Background(), &vars.Resolved{Method: "GET", URL: login.URL + "/check", Headers: http.Header{}}, firstOpts)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res.Body); got != "session=alpha" {
		t.Errorf("matching origin cookie = %q", got)
	}
	res, err = Send(context.Background(), &vars.Resolved{Method: "GET", URL: otherURL + "/check", Headers: http.Header{}}, firstOpts)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res.Body); got != "" {
		t.Errorf("unrelated host received cookie %q", got)
	}
	secondOpts := NewOptions()
	secondOpts.Cookies = second
	res, err = Send(context.Background(), &vars.Resolved{Method: "GET", URL: login.URL + "/check", Headers: http.Header{}}, secondOpts)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res.Body); got != "" {
		t.Errorf("unrelated workspace received cookie %q", got)
	}
}

func TestCookieSessionDisableInspectAndClear(t *testing.T) {
	session := NewCookieSession()
	u, _ := url.Parse("https://example.test/path")
	session.SetCookies(u, []*http.Cookie{{Name: "session", Value: "one", Path: "/"}})
	if got := session.Cookies(u); len(got) != 1 || got[0].Value != "one" {
		t.Fatalf("inspect cookies = %#v", got)
	}
	session.SetEnabled(false)
	if got := session.Cookies(u); len(got) != 0 {
		t.Fatalf("disabled cookies = %#v", got)
	}
	if got := session.Inspect(u); len(got) != 1 || got[0].Value != "one" || got[0].Path != "/" {
		t.Fatalf("disabled inspect cookies = %#v", got)
	}
	session.SetEnabled(true)
	if err := session.Clear(u.Scheme + "://" + u.Host); err != nil {
		t.Fatal(err)
	}
	if got := session.Cookies(u); len(got) != 0 {
		t.Fatalf("cleared cookies = %#v", got)
	}
	session.SetCookies(u, []*http.Cookie{{Name: "session", Value: "two", Path: "/"}})
	if err := session.Clear(""); err != nil {
		t.Fatal(err)
	}
	if got := session.Cookies(u); len(got) != 0 {
		t.Fatalf("clear all cookies = %#v", got)
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
	if res.ActualBytes != 1024 || res.BufferedBytes != 100 || !res.Truncated || !res.BodyComplete {
		t.Errorf("response byte accounting = actual %d buffered %d truncated %t complete %t", res.ActualBytes, res.BufferedBytes, res.Truncated, res.BodyComplete)
	}
}

func TestSendBodyAccountingKnownAndUnknownLength(t *testing.T) {
	const size = 256 << 10
	for _, known := range []bool{true, false} {
		t.Run(map[bool]string{true: "known", false: "unknown"}[known], func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if known {
					w.Header().Set("Content-Length", fmt.Sprint(size))
				}
				chunk := bytes.Repeat([]byte("x"), 8192)
				for written := 0; written < size; written += len(chunk) {
					if _, err := w.Write(chunk); err != nil {
						return
					}
					if !known {
						w.(http.Flusher).Flush()
					}
				}
			}))
			defer srv.Close()
			opts := NewOptions()
			opts.MaxBodyBytes = 1024
			res, err := Send(context.Background(), &vars.Resolved{Method: "GET", URL: srv.URL, Headers: http.Header{}}, opts)
			if err != nil {
				t.Fatal(err)
			}
			if res.ActualBytes != size || res.BufferedBytes != opts.MaxBodyBytes || !res.Truncated || !res.BodyComplete {
				t.Errorf("byte accounting = actual %d buffered %d truncated %t complete %t", res.ActualBytes, res.BufferedBytes, res.Truncated, res.BodyComplete)
			}
			if known && res.ContentLength != size {
				t.Errorf("content length = %d, want %d", res.ContentLength, size)
			}
			if !known && res.ContentLength != -1 {
				t.Errorf("unknown content length reported as %d", res.ContentLength)
			}
		})
	}
}

func TestSendReportsEarlyEOFAsPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
	}))
	defer srv.Close()
	opts := NewOptions()
	opts.MaxBodyBytes = 20
	res, err := Send(context.Background(), &vars.Resolved{Method: "GET", URL: srv.URL, Headers: http.Header{}}, opts)
	if err == nil {
		t.Fatal("expected early EOF error")
	}
	if res == nil || res.ActualBytes != 5 || res.BufferedBytes != 5 || res.ContentLength != 100 || res.BodyComplete || !res.Truncated {
		t.Fatalf("partial response accounting = %+v", res)
	}
}

func TestSendToFileStreamsLargeResponse(t *testing.T) {
	const size int64 = 100 << 20
	chunk := bytes.Repeat([]byte("relay-download-"), 2048)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(size))
		for written := int64(0); written < size; {
			part := chunk
			if remain := size - written; remain < int64(len(part)) {
				part = part[:remain]
			}
			n, err := w.Write(part)
			if err != nil {
				return
			}
			written += int64(n)
		}
	}))
	defer srv.Close()
	destination := filepath.Join(t.TempDir(), "download.bin")
	opts := NewOptions()
	opts.MaxBodyBytes = 4096
	res, err := SendToFile(context.Background(), &vars.Resolved{Method: "GET", URL: srv.URL, Headers: http.Header{}}, opts, destination)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != size {
		t.Fatalf("download size = %d, want %d", info.Size(), size)
	}
	if res.ActualBytes != size || res.BufferedBytes != opts.MaxBodyBytes || !res.Truncated || !res.BodyComplete || res.SavedTo != destination {
		t.Errorf("download result = actual %d buffered %d truncated %t complete %t saved %q", res.ActualBytes, res.BufferedBytes, res.Truncated, res.BodyComplete, res.SavedTo)
	}
	file, err := os.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	first := make([]byte, len(chunk))
	if _, err := file.ReadAt(first, 0); err != nil {
		t.Fatal(err)
	}
	last := make([]byte, 1)
	if _, err := file.ReadAt(last, size-1); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, chunk) || last[0] != chunk[(size-1)%int64(len(chunk))] {
		t.Error("download contents did not match response stream")
	}
}

func TestSendToFileCancelCleansTemporaryFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		_, _ = w.Write(bytes.Repeat([]byte("x"), 1024))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	dir := t.TempDir()
	destination := filepath.Join(dir, "cancelled.bin")
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(25*time.Millisecond, cancel)
	defer timer.Stop()
	_, err := SendToFile(ctx, &vars.Resolved{Method: "GET", URL: srv.URL, Headers: http.Header{}}, NewOptions(), destination)
	if err == nil {
		t.Fatal("expected canceled download error")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("cancelled destination exists or stat failed: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, ".cancelled.bin-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("temporary downloads remain: %v", files)
	}
}

func TestSendClosesIdleConnections(t *testing.T) {
	var closed atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			closed.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()
	for i := 0; i < 5; i++ {
		if _, err := Send(context.Background(), &vars.Resolved{Method: "GET", URL: srv.URL, Headers: http.Header{}}, NewOptions()); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for closed.Load() < 5 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := closed.Load(); got < 5 {
		t.Errorf("closed connections = %d, want at least 5", got)
	}
}

func TestSendToFileDoesNotOverwriteDestination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("new")) }))
	defer srv.Close()
	destination := filepath.Join(t.TempDir(), "existing.bin")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := SendToFile(context.Background(), &vars.Resolved{Method: "GET", URL: srv.URL, Headers: http.Header{}}, NewOptions(), destination)
	if err == nil {
		t.Fatal("expected existing destination to be rejected")
	}
	got, readErr := os.ReadFile(destination)
	if readErr != nil || string(got) != "old" {
		t.Fatalf("existing destination changed: contents=%q error=%v", got, readErr)
	}
}

func TestCustomRootCAPreservesSystemTrustAndInsecureMode(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("trusted")) }))
	defer srv.Close()
	rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if systemRoots, err := x509.SystemCertPool(); err == nil && systemRoots != nil {
		client, err := newClient(Options{RootCAPEM: rootPEM})
		if err != nil {
			t.Fatal(err)
		}
		defer client.CloseIdleConnections()
		got := client.Transport.(*http.Transport).TLSClientConfig.RootCAs.Subjects()
		if len(got) < len(systemRoots.Subjects()) {
			t.Errorf("custom root replaced system roots: got %d subjects, want at least %d", len(got), len(systemRoots.Subjects()))
		}
	}
	request := &vars.Resolved{Method: "GET", URL: srv.URL, Headers: http.Header{}}
	if _, err := Send(context.Background(), request, NewOptions()); err == nil {
		t.Fatal("expected server certificate to be untrusted without custom root")
	}
	opts := NewOptions()
	opts.RootCAPEM = rootPEM
	result, err := Send(context.Background(), request, opts)
	if err != nil {
		t.Fatalf("custom root CA request failed: %v", err)
	}
	if string(result.Body) != "trusted" {
		t.Errorf("body = %q", result.Body)
	}
	bad := NewOptions()
	bad.RootCAPEM = []byte("not a certificate")
	if _, err := Send(context.Background(), request, bad); err == nil {
		t.Error("expected invalid root CA PEM to be rejected")
	}
	insecure := NewOptions()
	insecure.Insecure = true
	if _, err := Send(context.Background(), request, insecure); err != nil {
		t.Errorf("Insecure compatibility failed: %v", err)
	}
}

func TestMutualTLSClientCertificate(t *testing.T) {
	clientCertPEM, clientKeyPEM, clientCAPool := makeClientCertificate(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			http.Error(w, "client was not verified", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("mTLS"))
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientCAPool}
	srv.StartTLS()
	defer srv.Close()
	rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	request := &vars.Resolved{Method: "GET", URL: srv.URL, Headers: http.Header{}}
	if _, err := Send(context.Background(), request, Options{RootCAPEM: rootPEM}); err == nil {
		t.Fatal("expected request without client certificate to fail")
	}
	opts := NewOptions()
	opts.RootCAPEM = rootPEM
	opts.ClientCertPEM = clientCertPEM
	opts.ClientKeyPEM = clientKeyPEM
	result, err := Send(context.Background(), request, opts)
	if err != nil {
		t.Fatalf("mTLS request failed: %v", err)
	}
	if string(result.Body) != "mTLS" {
		t.Errorf("body = %q", result.Body)
	}
	missingKey := NewOptions()
	missingKey.ClientCertPEM = clientCertPEM
	if _, err := Send(context.Background(), request, missingKey); err == nil {
		t.Error("expected incomplete client certificate configuration to fail")
	}
}

func makeClientCertificate(t *testing.T) ([]byte, []byte, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Relay test client"},
		NotBefore:    now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	pool := x509.NewCertPool()
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool.AddCert(cert)
	return certPEM, privateKeyPEM, pool
}
