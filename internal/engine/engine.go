// Package engine sends resolved requests over net/http and captures a
// timing breakdown via httptrace.
package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
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

// Result is one completed exchange.
type Result struct {
	Status     int
	StatusText string
	Proto      string
	Headers    http.Header
	Body       []byte
	Size       int64
	Timing     Timing
}

// Options configure the client per send.
type Options struct {
	Timeout         time.Duration // default 30s
	FollowRedirects bool          // default true via NewOptions
	Insecure        bool          // skip TLS verification
	MaxBodyBytes    int64         // cap on buffered response body, default 50MB
}

// NewOptions returns the default options.
func NewOptions() Options {
	return Options{Timeout: 30 * time.Second, FollowRedirects: true, MaxBodyBytes: 50 << 20}
}

// Send performs the exchange.
func Send(ctx context.Context, r *vars.Resolved, opts Options) (*Result, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = 50 << 20
	}

	var body io.Reader
	if len(r.Body) > 0 {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, body)
	if err != nil {
		return nil, err
	}
	for k, v := range r.Headers {
		req.Header[k] = v
	}

	var t traceTimes
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), t.trace()))

	transport := &http.Transport{
		ForceAttemptHTTP2: true,
		Proxy:             http.ProxyFromEnvironment,
	}
	if opts.Insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	client := &http.Client{Transport: transport, Timeout: opts.Timeout}
	if !opts.FollowRedirects {
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	firstByte := time.Now()
	data, err := io.ReadAll(io.LimitReader(resp.Body, opts.MaxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	end := time.Now()

	return &Result{
		Status:     resp.StatusCode,
		StatusText: resp.Status,
		Proto:      resp.Proto,
		Headers:    resp.Header,
		Body:       data,
		Size:       int64(len(data)),
		Timing:     t.timing(start, firstByte, end),
	}, nil
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
