package spotify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientCredentialsSourceIssuesToken(t *testing.T) {
	t.Parallel()

	fixedNow := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("method = %q", request.Method)
		}
		clientID, secret, ok := request.BasicAuth()
		if !ok || clientID != "client-id" || secret != "client-secret" {
			t.Errorf("BasicAuth() = %q, %q, %v", clientID, secret, ok)
		}
		if request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", request.Header.Get("Content-Type"))
		}
		if err := request.ParseForm(); err != nil {
			t.Errorf("ParseForm() error = %v", err)
		}
		if request.Form.Get("grant_type") != "client_credentials" {
			t.Errorf("grant_type = %q", request.Form.Get("grant_type"))
		}
		writeJSON(writer, http.StatusOK, `{
			"access_token":"app-access", "token_type":"Bearer", "scope":"", "expires_in":3600
		}`)
	}))
	defer server.Close()

	source := NewClientCredentialsSource("client-id", "client-secret", server.Client())
	source.TokenURL = server.URL
	source.Now = func() time.Time { return fixedNow }
	token, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if token.AccessToken != "app-access" || token.TokenType != "Bearer" {
		t.Fatalf("token = %#v", token)
	}
	if want := fixedNow.Add(time.Hour); !token.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want %v", token.ExpiresAt, want)
	}
}

func TestTokenEndpointErrorsAreTypedAndDoNotLeakSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
		check  func(*testing.T, error)
	}{
		{"OAuth error", http.StatusBadRequest, `{"error":"invalid_client","error_description":"credentials rejected"}`, func(t *testing.T, err error) {
			var oauthError *OAuthError
			if !errors.As(err, &oauthError) || oauthError.Code != "invalid_client" || oauthError.StatusCode != 400 {
				t.Fatalf("error = %#v", err)
			}
		}},
		{"malformed JSON", http.StatusOK, `{"access_token":`, func(t *testing.T, err error) {
			var decodeError *ResponseDecodeError
			if !errors.As(err, &decodeError) {
				t.Fatalf("error type = %T, want *ResponseDecodeError", err)
			}
		}},
		{"missing access token", http.StatusOK, `{"token_type":"Bearer","expires_in":3600}`, func(t *testing.T, err error) {
			var decodeError *ResponseDecodeError
			if !errors.As(err, &decodeError) {
				t.Fatalf("error type = %T, want *ResponseDecodeError", err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writeJSON(writer, test.status, test.body)
			}))
			defer server.Close()
			source := NewClientCredentialsSource("client-id", "super-secret-value", server.Client())
			source.TokenURL = server.URL
			_, err := source.Token(context.Background())
			if err == nil {
				t.Fatal("Token() unexpectedly succeeded")
			}
			if strings.Contains(err.Error(), "super-secret-value") {
				t.Fatalf("error leaked secret: %v", err)
			}
			test.check(t, err)
		})
	}
}

func TestTokenManagerCachesSerializesAndInvalidates(t *testing.T) {
	t.Parallel()

	fixedNow := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	source := &countingTokenSource{expiresAt: fixedNow.Add(time.Hour)}
	manager := NewTokenManager(source)
	manager.now = func() time.Time { return fixedNow }

	const callers = 16
	var waitGroup sync.WaitGroup
	errorsChannel := make(chan error, callers)
	for index := 0; index < callers; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			token, err := manager.Token(context.Background())
			if err == nil && token.AccessToken != "token-1" {
				err = errors.New("unexpected token")
			}
			errorsChannel <- err
		}()
	}
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("Token() error = %v", err)
		}
	}
	if source.calls.Load() != 1 {
		t.Fatalf("source calls = %d, want 1", source.calls.Load())
	}

	manager.Invalidate()
	token, err := manager.Token(context.Background())
	if err != nil || token.AccessToken != "token-2" {
		t.Fatalf("Token() after Invalidate = %#v, %v", token, err)
	}
	if snapshot := manager.Snapshot(); snapshot.AccessToken != "token-2" {
		t.Fatalf("Snapshot() = %#v", snapshot)
	}
}

func TestPKCEGenerationAndRFCChallenge(t *testing.T) {
	t.Parallel()

	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const wantChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := PKCEChallenge(verifier); got != wantChallenge {
		t.Fatalf("PKCEChallenge() = %q, want %q", got, wantChallenge)
	}
	pair, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE() error = %v", err)
	}
	if len(pair.Verifier) < 43 || pair.Challenge != PKCEChallenge(pair.Verifier) {
		t.Fatalf("pair = %#v", pair)
	}
	if err := validateVerifier(pair.Verifier); err != nil {
		t.Fatalf("generated verifier is invalid: %v", err)
	}
	state, err := GenerateState()
	if err != nil || len(state) < 43 || state == pair.Verifier {
		t.Fatalf("GenerateState() = %q, %v", state, err)
	}
}

func TestOAuthAuthorizationURLIsPKCEAndSecretless(t *testing.T) {
	t.Parallel()

	oauth, err := NewOAuthClient(OAuthConfig{ClientID: "client-id"})
	if err != nil {
		t.Fatalf("NewOAuthClient() error = %v", err)
	}
	authorizationURL, err := oauth.AuthorizationURL(
		"http://127.0.0.1:7777/callback",
		"strong-state",
		"challenge",
		[]string{"playlist-read-private", "user-read-private"},
	)
	if err != nil {
		t.Fatalf("AuthorizationURL() error = %v", err)
	}
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	query := parsed.Query()
	wants := map[string]string{
		"client_id":             "client-id",
		"response_type":         "code",
		"redirect_uri":          "http://127.0.0.1:7777/callback",
		"state":                 "strong-state",
		"code_challenge":        "challenge",
		"code_challenge_method": "S256",
		"scope":                 "playlist-read-private user-read-private",
	}
	for key, want := range wants {
		if got := query.Get(key); got != want {
			t.Errorf("query[%q] = %q, want %q", key, got, want)
		}
	}
	if query.Has("client_secret") || strings.Contains(authorizationURL, "secret") {
		t.Fatalf("authorization URL includes a secret: %s", authorizationURL)
	}
}

func TestOAuthExchangeAndRefreshTokenSource(t *testing.T) {
	t.Parallel()

	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	var refreshCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Errorf("ParseForm() error = %v", err)
		}
		if request.Form.Has("client_secret") || request.Header.Get("Authorization") != "" {
			t.Errorf("PKCE token request unexpectedly used a secret")
		}
		if request.Form.Get("client_id") != "client-id" {
			t.Errorf("client_id = %q", request.Form.Get("client_id"))
		}
		switch request.Form.Get("grant_type") {
		case "authorization_code":
			if request.Form.Get("code") != "auth-code" || request.Form.Get("code_verifier") != verifier || request.Form.Get("redirect_uri") != "http://127.0.0.1:7777/callback" {
				t.Errorf("exchange form = %#v", request.Form)
			}
			writeJSON(writer, http.StatusOK, `{"access_token":"access-1","token_type":"Bearer","refresh_token":"refresh-1","expires_in":3600}`)
		case "refresh_token":
			call := refreshCalls.Add(1)
			if request.Form.Get("refresh_token") != "refresh-1" {
				t.Errorf("refresh_token = %q", request.Form.Get("refresh_token"))
			}
			if call == 1 {
				writeJSON(writer, http.StatusOK, `{"access_token":"access-2","token_type":"Bearer","expires_in":3600}`)
			} else {
				writeJSON(writer, http.StatusOK, `{"access_token":"access-3","token_type":"Bearer","refresh_token":"refresh-2","expires_in":3600}`)
			}
		default:
			writeJSON(writer, http.StatusBadRequest, `{"error":"unsupported_grant_type"}`)
		}
	}))
	defer server.Close()

	oauth, err := NewOAuthClient(OAuthConfig{
		ClientID:         "client-id",
		AuthorizationURL: server.URL + "/authorize",
		TokenURL:         server.URL + "/token",
		HTTPClient:       server.Client(),
	})
	if err != nil {
		t.Fatalf("NewOAuthClient() error = %v", err)
	}
	initial, err := oauth.Exchange(context.Background(), "http://127.0.0.1:7777/callback", "auth-code", verifier)
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if initial.AccessToken != "access-1" || initial.RefreshToken != "refresh-1" {
		t.Fatalf("exchange token = %#v", initial)
	}

	source := NewRefreshTokenSource(oauth, initial)
	refreshed, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("first refresh error = %v", err)
	}
	if refreshed.AccessToken != "access-2" || refreshed.RefreshToken != "refresh-1" {
		t.Fatalf("first refresh = %#v", refreshed)
	}
	rotated, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("second refresh error = %v", err)
	}
	if rotated.AccessToken != "access-3" || rotated.RefreshToken != "refresh-2" || source.Snapshot().RefreshToken != "refresh-2" {
		t.Fatalf("rotated refresh = %#v, snapshot = %#v", rotated, source.Snapshot())
	}
}

func TestOAuthInvalidRefreshIsTypedAndNotRetried(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writeJSON(writer, http.StatusBadRequest, `{"error":"invalid_grant","error_description":"Refresh token revoked"}`)
	}))
	defer server.Close()
	oauth, err := NewOAuthClient(OAuthConfig{ClientID: "client-id", TokenURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewOAuthClient() error = %v", err)
	}
	_, err = oauth.Refresh(context.Background(), "invalid-refresh")
	var oauthError *OAuthError
	if !errors.As(err, &oauthError) || oauthError.Code != "invalid_grant" {
		t.Fatalf("error = %#v", err)
	}
	if !errors.Is(err, ErrReauthorizationRequired) {
		t.Fatalf("error = %v, want ErrReauthorizationRequired", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestClient401RefreshesPKCETokenThroughManager(t *testing.T) {
	t.Parallel()

	var apiCalls atomic.Int32
	var refreshCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			refreshCalls.Add(1)
			if err := request.ParseForm(); err != nil {
				t.Errorf("ParseForm() error = %v", err)
			}
			if request.Form.Get("refresh_token") != "stored-refresh" {
				t.Errorf("refresh_token = %q", request.Form.Get("refresh_token"))
			}
			writeJSON(writer, http.StatusOK, `{"access_token":"fresh-access","token_type":"Bearer","expires_in":3600}`)
		case "/v1/tracks/" + testTrackID:
			apiCalls.Add(1)
			if request.Header.Get("Authorization") == "Bearer stale-access" {
				writeJSON(writer, http.StatusUnauthorized, `{"error":{"status":401,"message":"expired"}}`)
				return
			}
			writeJSON(writer, http.StatusOK, trackDocument(testTrackID, "Refreshed", "track", false))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	oauth, err := NewOAuthClient(OAuthConfig{ClientID: "client-id", TokenURL: server.URL + "/token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewOAuthClient() error = %v", err)
	}
	initial := Token{AccessToken: "stale-access", TokenType: "Bearer", RefreshToken: "stored-refresh", ExpiresAt: time.Now().Add(time.Hour)}
	refreshSource := NewRefreshTokenSource(oauth, initial)
	manager := NewTokenManager(refreshSource, initial)
	client := NewClient(manager, WithHTTPClient(server.Client()), WithAPIBaseURL(server.URL+"/v1"))
	track, err := client.FetchTrack(context.Background(), testTrackID)
	if err != nil {
		t.Fatalf("FetchTrack() error = %v", err)
	}
	if track.Title != "Refreshed" || apiCalls.Load() != 2 || refreshCalls.Load() != 1 {
		t.Fatalf("track = %#v, API calls = %d, refresh calls = %d", track, apiCalls.Load(), refreshCalls.Load())
	}
	if snapshot := refreshSource.Snapshot(); snapshot.AccessToken != "fresh-access" || snapshot.RefreshToken != "stored-refresh" {
		t.Fatalf("refresh snapshot = %#v", snapshot)
	}
}

func TestClient401PropagatesInvalidPKCERefresh(t *testing.T) {
	t.Parallel()

	var apiCalls atomic.Int32
	var refreshCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			refreshCalls.Add(1)
			writeJSON(writer, http.StatusBadRequest, `{"error":"invalid_grant","error_description":"Refresh token expired"}`)
			return
		}
		apiCalls.Add(1)
		writeJSON(writer, http.StatusUnauthorized, `{"error":{"status":401,"message":"expired"}}`)
	}))
	defer server.Close()

	oauth, err := NewOAuthClient(OAuthConfig{ClientID: "client-id", TokenURL: server.URL + "/token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewOAuthClient() error = %v", err)
	}
	initial := Token{AccessToken: "stale-access", RefreshToken: "expired-refresh", ExpiresAt: time.Now().Add(time.Hour)}
	manager := NewTokenManager(NewRefreshTokenSource(oauth, initial), initial)
	client := NewClient(manager, WithHTTPClient(server.Client()), WithAPIBaseURL(server.URL+"/v1"))
	_, err = client.FetchTrack(context.Background(), testTrackID)
	if !errors.Is(err, ErrReauthorizationRequired) {
		t.Fatalf("error = %v, want ErrReauthorizationRequired", err)
	}
	var oauthError *OAuthError
	if !errors.As(err, &oauthError) || oauthError.Code != "invalid_grant" {
		t.Fatalf("error = %#v", err)
	}
	if apiCalls.Load() != 1 || refreshCalls.Load() != 1 {
		t.Fatalf("API calls = %d, refresh calls = %d", apiCalls.Load(), refreshCalls.Load())
	}
}

type countingTokenSource struct {
	calls     atomic.Int32
	expiresAt time.Time
}

func (s *countingTokenSource) Token(context.Context) (Token, error) {
	call := s.calls.Add(1)
	return Token{AccessToken: "token-" + string(rune('0'+call)), ExpiresAt: s.expiresAt}, nil
}
