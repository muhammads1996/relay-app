// Package oauth implements the OAuth 2.0 client-credentials and authorization
// code with PKCE token flows used by Relay integrations.
package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Grant identifies a supported token acquisition flow.
type Grant string

const (
	GrantClientCredentials Grant = "client_credentials"
	GrantAuthorizationCode Grant = "authorization_code"
)

const (
	refreshSkew   = 30 * time.Second
	stateLifetime = 10 * time.Minute
	maxPending    = 16
	maxTokenBytes = 1 << 20
)

// Config contains OAuth endpoints and client settings. ClientSecret is
// optional for public authorization-code clients; client-credentials
// deployments normally require one.
type Config struct {
	Grant            Grant
	ClientID         string
	ClientSecret     string
	AuthorizationURL string
	TokenURL         string
	RedirectURL      string
	Scopes           []string
	HTTPClient       *http.Client
}

// Authorization contains the browser URL and state for one PKCE authorization
// attempt. The verifier is intentionally retained inside Manager.
type Authorization struct {
	URL           string
	State         string
	CodeChallenge string
}

// Token is an OAuth token response. It contains credential material; callers
// must avoid logging or persisting it in plaintext.
type Token struct {
	AccessToken  string
	TokenType    string
	RefreshToken string
	Scope        string
	Expiry       time.Time
}

// Manager acquires and refreshes tokens for one OAuth client configuration.
type Manager struct {
	mu      sync.Mutex
	config  Config
	token   Token
	pending map[string]pendingAuthorization
}

type pendingAuthorization struct {
	verifier string
	expires  time.Time
}

// NewManager validates the endpoint configuration and creates a token manager.
func NewManager(config Config) (*Manager, error) {
	if strings.TrimSpace(config.ClientID) == "" {
		return nil, errors.New("oauth client ID is required")
	}
	if config.Grant != GrantClientCredentials && config.Grant != GrantAuthorizationCode {
		return nil, fmt.Errorf("unsupported oauth grant %q", config.Grant)
	}
	if config.Grant == GrantClientCredentials && strings.TrimSpace(config.ClientSecret) == "" {
		return nil, errors.New("client credentials grant requires a client secret")
	}
	if config.Grant == GrantAuthorizationCode && strings.TrimSpace(config.RedirectURL) == "" {
		return nil, errors.New("authorization code grant requires a redirect URL")
	}
	tokenEndpoint, err := endpointURL(config.TokenURL)
	if err != nil {
		return nil, fmt.Errorf("token URL: %w", err)
	}
	config.TokenURL = tokenEndpoint.String()
	if config.Grant == GrantAuthorizationCode {
		authorizationEndpoint, err := endpointURL(config.AuthorizationURL)
		if err != nil {
			return nil, fmt.Errorf("authorization URL: %w", err)
		}
		config.AuthorizationURL = authorizationEndpoint.String()
		redirect, err := redirectURL(config.RedirectURL)
		if err != nil {
			return nil, fmt.Errorf("redirect URL: %w", err)
		}
		config.RedirectURL = redirect.String()
	}
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	return &Manager{config: config, pending: make(map[string]pendingAuthorization)}, nil
}

// StartAuthorization creates an authorization URL using PKCE S256 and a
// cryptographically random state. The manager keeps the verifier for the
// subsequent ExchangeAuthorizationCode call.
func (m *Manager) StartAuthorization() (*Authorization, error) {
	if m.config.Grant != GrantAuthorizationCode {
		return nil, errors.New("authorization URL requires the authorization-code grant")
	}
	verifier, err := randomURLToken(32)
	if err != nil {
		return nil, fmt.Errorf("creating PKCE verifier: %w", err)
	}
	state, err := randomURLToken(32)
	if err != nil {
		return nil, fmt.Errorf("creating OAuth state: %w", err)
	}
	challengeDigest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeDigest[:])
	endpoint, _ := url.Parse(m.config.AuthorizationURL) // validated by NewManager
	query := endpoint.Query()
	query.Set("response_type", "code")
	query.Set("client_id", m.config.ClientID)
	query.Set("redirect_uri", m.config.RedirectURL)
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	if len(m.config.Scopes) > 0 {
		query.Set("scope", strings.Join(m.config.Scopes, " "))
	}
	endpoint.RawQuery = query.Encode()
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for oldState, pending := range m.pending {
		if !pending.expires.After(now) {
			delete(m.pending, oldState)
		}
	}
	if len(m.pending) >= maxPending {
		return nil, errors.New("too many pending OAuth authorizations")
	}
	m.pending[state] = pendingAuthorization{verifier: verifier, expires: now.Add(stateLifetime)}
	return &Authorization{URL: endpoint.String(), State: state, CodeChallenge: challenge}, nil
}

// ExchangeAuthorizationCode validates state and exchanges a one-time code
// using the matching PKCE verifier.
func (m *Manager) ExchangeAuthorizationCode(ctx context.Context, code, state string) (*Token, error) {
	if m.config.Grant != GrantAuthorizationCode {
		return nil, errors.New("authorization code exchange requires the authorization-code grant")
	}
	if strings.TrimSpace(code) == "" || strings.TrimSpace(state) == "" {
		return nil, errors.New("authorization code and state are required")
	}
	m.mu.Lock()
	var verifier string
	for candidate, pending := range m.pending {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(state)) == 1 && pending.expires.After(time.Now()) {
			verifier = pending.verifier
			delete(m.pending, candidate)
			break
		}
	}
	m.mu.Unlock()
	if verifier == "" {
		return nil, errors.New("OAuth state is invalid or expired")
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {m.config.RedirectURL},
		"code_verifier": {verifier},
	}
	if m.config.ClientSecret == "" {
		form.Set("client_id", m.config.ClientID)
	}
	token, err := m.requestToken(ctx, form)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.token = *token
	m.mu.Unlock()
	return tokenCopy(token), nil
}

// AccessToken returns a current access token, acquiring or refreshing it when
// needed. Client-credentials tokens are reacquired; authorization-code tokens
// use a refresh token when the server provided one.
func (m *Manager) AccessToken(ctx context.Context) (*Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tokenUsable(m.token, time.Now()) {
		return tokenCopy(&m.token), nil
	}
	var form url.Values
	switch {
	case m.config.Grant == GrantClientCredentials:
		form = url.Values{"grant_type": {"client_credentials"}}
		if len(m.config.Scopes) > 0 {
			form.Set("scope", strings.Join(m.config.Scopes, " "))
		}
	case m.token.RefreshToken != "":
		form = url.Values{"grant_type": {"refresh_token"}, "refresh_token": {m.token.RefreshToken}}
		if m.config.ClientSecret == "" {
			form.Set("client_id", m.config.ClientID)
		}
	default:
		return nil, errors.New("OAuth authorization is required before requesting an access token")
	}
	token, err := m.requestToken(ctx, form)
	if err != nil {
		return nil, err
	}
	if token.RefreshToken == "" {
		token.RefreshToken = m.token.RefreshToken
	}
	m.token = *token
	return tokenCopy(token), nil
}

// Current returns the cached token without performing network I/O.
func (m *Manager) Current() (*Token, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token.AccessToken == "" {
		return nil, false
	}
	return tokenCopy(&m.token), true
}

func (m *Manager) requestToken(ctx context.Context, form url.Values) (*Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.config.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating OAuth token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if m.config.ClientSecret != "" {
		credentials := url.QueryEscape(m.config.ClientID) + ":" + url.QueryEscape(m.config.ClientSecret)
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credentials)))
	}
	resp, err := m.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OAuth token request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("OAuth token endpoint returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading OAuth token response: %w", err)
	}
	if len(body) > maxTokenBytes {
		return nil, errors.New("OAuth token response is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var wire tokenResponse
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("decoding OAuth token response: %w", err)
	}
	if wire.AccessToken == "" {
		return nil, errors.New("OAuth token response has no access_token")
	}
	if wire.TokenType == "" {
		return nil, errors.New("OAuth token response has no token_type")
	}
	token := &Token{AccessToken: wire.AccessToken, TokenType: wire.TokenType, RefreshToken: wire.RefreshToken, Scope: wire.Scope}
	if wire.ExpiresIn != "" {
		seconds, err := wire.ExpiresIn.Int64()
		if err != nil || seconds < 0 {
			return nil, errors.New("OAuth token response has invalid expires_in")
		}
		if seconds > int64((1<<63-1)/int64(time.Second)) {
			return nil, errors.New("OAuth token response has invalid expires_in")
		}
		token.Expiry = time.Now().Add(time.Duration(seconds) * time.Second)
	}
	return token, nil
}

type tokenResponse struct {
	AccessToken  string      `json:"access_token"`
	TokenType    string      `json:"token_type"`
	RefreshToken string      `json:"refresh_token"`
	Scope        string      `json:"scope"`
	ExpiresIn    json.Number `json:"expires_in"`
}

func tokenUsable(token Token, now time.Time) bool {
	return token.AccessToken != "" && (token.Expiry.IsZero() || token.Expiry.After(now.Add(refreshSkew)))
}

func tokenCopy(token *Token) *Token {
	copy := *token
	return &copy
}

func randomURLToken(bytesCount int) (string, error) {
	data := make([]byte, bytesCount)
	if _, err := io.ReadFull(rand.Reader, data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func endpointURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("must be an absolute HTTP(S) URL without user info or fragment")
	}
	if u.Scheme == "http" && !loopbackHost(u.Hostname()) {
		return nil, errors.New("must use HTTPS except for loopback test or callback endpoints")
	}
	return u, nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func redirectURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("must be an absolute URI without user info or fragment")
	}
	return u, nil
}
