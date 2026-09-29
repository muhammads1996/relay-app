// Package engine sends resolved requests over net/http and captures a
// timing breakdown via httptrace.
package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"time"

	"github.com/muhaymien96/relay/internal/vars"
)

// Timing is the phase breakdown for one exchange. Phases that did not occur
// (e.g. TLS on plain HTTP, DNS on a reused connection) are zero.
type Timing struct {
	DNS      time.Duration
	Connect  time.Duration
	TLS      time.Duration
	TTFB     time.Duration // request written → first response byte
	Download time.Duration
	Total    time.Duration
}

// Result describes a response with a bounded preview in Body. ActualBytes
// counts bytes read, and BodyComplete distinguishes a full body from a partial
// transfer. ContentLength is -1 when the server did not send a known length.
// Size remains a compatibility alias for BufferedBytes.
type Result struct {
	Status        int
	StatusText    string
	Proto         string
	Headers       http.Header
	Body          []byte
	Size          int64 // compatibility alias for BufferedBytes
	ActualBytes   int64
	BufferedBytes int64
	ContentLength int64
	Truncated     bool
	BodyComplete  bool
	SavedTo       string
	Timing        Timing
}

// Options configure the client per send. Requests use
// http.ProxyFromEnvironment (HTTP_PROXY, HTTPS_PROXY, NO_PROXY).
type Options struct {
	Timeout         time.Duration  // default 30s
	FollowRedirects bool           // default true via NewOptions
	Insecure        bool           // skip TLS verification
	MaxBodyBytes    int64          // cap on buffered response body, default 50MB
	Cookies         *CookieSession // optional reusable cookie session; nil is isolated per send
	RootCAPEM       []byte         // optional additional trusted root certificate PEM
	ClientCertPEM   []byte         // optional client certificate PEM for mTLS
	ClientKeyPEM    []byte         // private key matching ClientCertPEM
}

// NewOptions returns the default options.
func NewOptions() Options {
	return Options{Timeout: 30 * time.Second, FollowRedirects: true, MaxBodyBytes: 50 << 20}
}

// Send performs the exchange.
func Send(ctx context.Context, r *vars.Resolved, opts Options) (*Result, error) {
	opts = normalizeOptions(opts)
	req, err := newRequest(ctx, r)
	if err != nil {
		return nil, err
	}

	var t traceTimes
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), t.trace()))

	client, err := newClient(opts)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	firstByte := time.Now()
	data, actual, complete, err := streamPreview(resp.Body, opts.MaxBodyBytes, io.Discard)
	end := time.Now()
	result := responseResult(resp, data, actual, complete, start, firstByte, end, &t)
	if err != nil {
		return result, fmt.Errorf("reading response: %w", err)
	}
	return result, nil
}

// SendToFile streams the complete body to a temporary file beside destination,
// retaining only a bounded preview in Result.Body. It fails if destination
// already exists and publishes the file only after the transfer completes.
func SendToFile(ctx context.Context, r *vars.Resolved, opts Options, destination string) (*Result, error) {
	if destination == "" {
		return nil, fmt.Errorf("download destination is required")
	}
	opts = normalizeOptions(opts)
	req, err := newRequest(ctx, r)
	if err != nil {
		return nil, err
	}
	t := &traceTimes{}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), t.trace()))
	start := time.Now()
	client, err := newClient(opts)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	temp, err := os.CreateTemp(filepath.Dir(destination), "."+filepath.Base(destination)+"-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("creating download file: %w", err)
	}
	tempName := temp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempName)
		}
	}()
	firstByte := time.Now()
	preview, actual, complete, readErr := streamPreview(resp.Body, opts.MaxBodyBytes, temp)
	end := time.Now()
	result := responseResult(resp, preview, actual, complete, start, firstByte, end, t)
	if readErr != nil {
		_ = temp.Close()
		return result, fmt.Errorf("downloading response: %w", readErr)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return result, fmt.Errorf("syncing download: %w", err)
	}
	if err := temp.Close(); err != nil {
		return result, fmt.Errorf("closing download: %w", err)
	}
	// Linking atomically publishes the completed file and fails if destination
	// already exists, avoiding platform-specific replacement behavior.
	if err := os.Link(tempName, destination); err != nil {
		return result, fmt.Errorf("committing download: %w", err)
	}
	committed = true
	result.SavedTo = destination
	if err := os.Remove(tempName); err != nil {
		return result, fmt.Errorf("download committed to %s but temporary link cleanup failed: %w", destination, err)
	}
	return result, nil
}

func normalizeOptions(opts Options) Options {
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = 50 << 20
	}
	return opts
}

func newRequest(ctx context.Context, r *vars.Resolved) (*http.Request, error) {
	var body io.Reader
	if len(r.Body) > 0 {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, body)
	if err != nil {
		return nil, err
	}
	orderedHeaders := make(map[string]bool, len(r.HeaderEntries))
	for _, entry := range r.HeaderEntries {
		orderedHeaders[http.CanonicalHeaderKey(entry.Key)] = true
	}
	for k, v := range r.Headers {
		if !orderedHeaders[http.CanonicalHeaderKey(k)] {
			req.Header[k] = v
		}
	}
	for _, entry := range r.HeaderEntries {
		req.Header.Add(entry.Key, entry.Value)
	}
	return req, nil
}

func newClient(opts Options) (*http.Client, error) {
	transport := &http.Transport{ForceAttemptHTTP2: true, Proxy: http.ProxyFromEnvironment}
	if opts.Insecure || len(opts.RootCAPEM) > 0 || len(opts.ClientCertPEM) > 0 || len(opts.ClientKeyPEM) > 0 {
		tlsConfig := &tls.Config{InsecureSkipVerify: opts.Insecure}
		if len(opts.RootCAPEM) > 0 {
			roots, err := x509.SystemCertPool()
			if err != nil || roots == nil {
				roots = x509.NewCertPool()
			}
			if ok := roots.AppendCertsFromPEM(opts.RootCAPEM); !ok {
				return nil, fmt.Errorf("root CA PEM contains no valid certificates")
			}
			tlsConfig.RootCAs = roots
		}
		if len(opts.ClientCertPEM) == 0 != (len(opts.ClientKeyPEM) == 0) {
			return nil, fmt.Errorf("client certificate and key must be provided together")
		}
		if len(opts.ClientCertPEM) > 0 {
			cert, err := tls.X509KeyPair(opts.ClientCertPEM, opts.ClientKeyPEM)
			if err != nil {
				return nil, fmt.Errorf("loading client certificate and key: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
		}
		transport.TLSClientConfig = tlsConfig
	}
	client := &http.Client{Transport: transport, Timeout: opts.Timeout, Jar: opts.Cookies}
	if !opts.FollowRedirects {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return client, nil
}

func streamPreview(source io.Reader, limit int64, destination io.Writer) ([]byte, int64, bool, error) {
	const chunkSize = 32 << 10
	preview := make([]byte, 0, minInt64(limit, chunkSize))
	chunk := make([]byte, chunkSize)
	var actual int64
	for {
		n, readErr := source.Read(chunk)
		if n > 0 {
			actual += int64(n)
			written, err := destination.Write(chunk[:n])
			if err != nil {
				return preview, actual, false, err
			}
			if written != n {
				return preview, actual, false, io.ErrShortWrite
			}
			keep := minInt64(limit-int64(len(preview)), int64(n))
			if keep > 0 {
				preview = append(preview, chunk[:int(keep)]...)
			}
		}
		if readErr == io.EOF {
			return preview, actual, true, nil
		}
		if readErr != nil {
			return preview, actual, false, readErr
		}
	}
}

func responseResult(resp *http.Response, preview []byte, actual int64, complete bool, start, firstByte, end time.Time, trace *traceTimes) *Result {
	buffered := int64(len(preview))
	return &Result{
		Status: resp.StatusCode, StatusText: resp.Status, Proto: resp.Proto,
		Headers: resp.Header, Body: preview, Size: buffered,
		ActualBytes: actual, BufferedBytes: buffered, ContentLength: resp.ContentLength,
		Truncated: !complete || actual > buffered, BodyComplete: complete,
		Timing: trace.timing(start, firstByte, end),
	}
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

type traceTimes struct {
	dnsStart, dnsDone   time.Time
	connStart, connDone time.Time
	tlsStart, tlsDone   time.Time
	wroteRequest        time.Time
	gotFirstByte        time.Time
}

func (t *traceTimes) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart:             func(httptrace.DNSStartInfo) { t.dnsStart = time.Now() },
		DNSDone:              func(httptrace.DNSDoneInfo) { t.dnsDone = time.Now() },
		ConnectStart:         func(string, string) { t.connStart = time.Now() },
		ConnectDone:          func(string, string, error) { t.connDone = time.Now() },
		TLSHandshakeStart:    func() { t.tlsStart = time.Now() },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { t.tlsDone = time.Now() },
		WroteRequest:         func(httptrace.WroteRequestInfo) { t.wroteRequest = time.Now() },
		GotFirstResponseByte: func() { t.gotFirstByte = time.Now() },
	}
}

func (t *traceTimes) timing(start, firstByte, end time.Time) Timing {
	tm := Timing{Total: end.Sub(start)}
	if !t.dnsDone.IsZero() {
		tm.DNS = t.dnsDone.Sub(t.dnsStart)
	}
	if !t.connDone.IsZero() {
		tm.Connect = t.connDone.Sub(t.connStart)
	}
	if !t.tlsDone.IsZero() {
		tm.TLS = t.tlsDone.Sub(t.tlsStart)
	}
	if !t.wroteRequest.IsZero() && !t.gotFirstByte.IsZero() {
		tm.TTFB = t.gotFirstByte.Sub(t.wroteRequest)
	}
	if !t.gotFirstByte.IsZero() {
		tm.Download = end.Sub(t.gotFirstByte)
	} else {
		tm.Download = end.Sub(firstByte)
	}
	return tm
}
