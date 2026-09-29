package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientCredentialsCachesToken(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte(url.QueryEscape("client id")+":"+url.QueryEscape("secret:part")))
		if got := r.Header.Get("Authorization"); got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("scope") != "read write" {
			t.Errorf("form = %v", r.Form)
		}
		_, _ = w.Write([]byte(`{"access_token":"cc-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()
	manager, err := NewManager(Config{Grant: GrantClientCredentials, ClientID: "client id", ClientSecret: "secret:part", TokenURL: srv.URL, Scopes: []string{"read", "write"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		token, err := manager.AccessToken(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if token.AccessToken != "cc-token" || token.TokenType != "Bearer" || time.Until(token.Expiry) <= 0 {
			t.Errorf("token = %+v", token)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("token endpoint calls = %d, want 1", calls.Load())
	}
}

func TestAuthorizationCodePKCEExchangeRefreshAndState(t *testing.T) {
	var exchangeCalls, refreshCalls atomic.Int32
	expectedChallenge := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			exchangeCalls.Add(1)
			if r.Form.Get("client_id") != "public-client" || r.Form.Get("code") != "auth-code" || r.Form.Get("redirect_uri") != "relay://callback" {
				t.Errorf("code exchange form = %v", r.Form)
			}
			verifier := r.Form.Get("code_verifier")
			challenge := sha256.Sum256([]byte(verifier))
			wantChallenge := <-expectedChallenge
			if verifier == "" || base64.RawURLEncoding.EncodeToString(challenge[:]) != wantChallenge {
				t.Errorf("code verifier did not match authorization challenge")
			}
			_, _ = w.Write([]byte(`{"access_token":"initial","token_type":"Bearer","expires_in":0,"refresh_token":"refresh-one"}`))
		case "refresh_token":
			refreshCalls.Add(1)
			if r.Form.Get("refresh_token") != "refresh-one" {
				t.Errorf("refresh form = %v", r.Form)
			}
			_, _ = w.Write([]byte(`{"access_token":"refreshed","token_type":"Bearer","expires_in":3600,"refresh_token":"refresh-two"}`))
		default:
			t.Errorf("unexpected grant_type %q", r.Form.Get("grant_type"))
			http.Error(w, "bad grant", http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	manager, err := NewManager(Config{
		Grant: GrantAuthorizationCode, ClientID: "public-client", AuthorizationURL: srv.URL + "/authorize",
		TokenURL: srv.URL + "/token", RedirectURL: "relay://callback", Scopes: []string{"profile", "email"},
	})
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := manager.StartAuthorization()
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(authorization.URL)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	if query.Get("response_type") != "code" || query.Get("client_id") != "public-client" || query.Get("redirect_uri") != "relay://callback" || query.Get("scope") != "profile email" {
		t.Errorf("authorization URL query = %v", query)
	}
	if query.Get("state") != authorization.State || query.Get("code_challenge") != authorization.CodeChallenge || query.Get("code_challenge_method") != "S256" {
		t.Errorf("PKCE state/challenge mismatch: %#v", authorization)
	}
	if _, err := manager.ExchangeAuthorizationCode(context.Background(), "auth-code", "wrong-state"); err == nil {
		t.Fatal("expected incorrect state to fail")
	}
	expectedChallenge <- authorization.CodeChallenge
	token, err := manager.ExchangeAuthorizationCode(context.Background(), "auth-code", authorization.State)
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "initial" || token.RefreshToken != "refresh-one" {
		t.Errorf("exchange token = %+v", token)
	}
	refreshed, err := manager.AccessToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken != "refreshed" || refreshed.RefreshToken != "refresh-two" {
		t.Errorf("refreshed token = %+v", refreshed)
	}
	if _, err := manager.ExchangeAuthorizationCode(context.Background(), "auth-code", authorization.State); err == nil {
		t.Error("authorization state was reusable")
	}
	if exchangeCalls.Load() != 1 || refreshCalls.Load() != 1 {
		t.Errorf("endpoint calls exchange=%d refresh=%d", exchangeCalls.Load(), refreshCalls.Load())
	}
}

func TestOAuthTokenEndpointErrorDoesNotExposeResponseBody(t *testing.T) {
	const secretEcho = "server-echoed-access-token-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, secretEcho, http.StatusBadRequest)
	}))
	defer srv.Close()
	manager, err := NewManager(Config{Grant: GrantClientCredentials, ClientID: "client", ClientSecret: "secret", TokenURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.AccessToken(context.Background())
	if err == nil || strings.Contains(err.Error(), secretEcho) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("token endpoint error leaked response content or secret: %v", err)
	}
}

func TestOAuthTokenRequestCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	defer srv.Close()
	manager, err := NewManager(Config{Grant: GrantClientCredentials, ClientID: "client", ClientSecret: "secret", TokenURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := manager.AccessToken(ctx)
		done <- err
	}()
	<-started
	cancel()
	close(release)
	if err := <-done; err == nil {
		t.Fatal("expected cancelled token request")
	}
}

func TestStartAuthorizationUsesPKCES256Challenge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	manager, err := NewManager(Config{Grant: GrantAuthorizationCode, ClientID: "client", AuthorizationURL: server.URL, TokenURL: server.URL, RedirectURL: "http://127.0.0.1/callback"})
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := manager.StartAuthorization()
	if err != nil {
		t.Fatal(err)
	}
	if len(authorization.State) < 43 || len(authorization.CodeChallenge) != 43 {
		t.Errorf("state/challenge lengths = %d/%d", len(authorization.State), len(authorization.CodeChallenge))
	}
}

func TestTokenResponseRejectsMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"access_token":`)) }))
	defer srv.Close()
	manager, err := NewManager(Config{Grant: GrantClientCredentials, ClientID: "client", ClientSecret: "secret", TokenURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AccessToken(context.Background()); err == nil {
		t.Fatal("expected malformed token response error")
	}
	if _, ok := manager.Current(); ok {
		t.Error("malformed response installed a token")
	}
}
