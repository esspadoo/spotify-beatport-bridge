package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	DefaultAccountsTokenURL = "https://accounts.spotify.com/api/token"
	defaultTokenExpirySkew  = 30 * time.Second
	maxOAuthResponseBytes   = 1 << 20
)

// Token contains OAuth token material. Callers that persist it must use secure
// credential storage; this package only keeps token values in memory.
type Token struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
}

// ValidAt reports whether the token is usable beyond the supplied safety skew.
func (t Token) ValidAt(now time.Time, skew time.Duration) bool {
	if t.AccessToken == "" {
		return false
	}
	return t.ExpiresAt.IsZero() || now.Add(skew).Before(t.ExpiresAt)
}

// TokenSource obtains a new token and performs no persistence. It is typically
// wrapped in TokenManager to add in-memory caching and refresh serialization.
type TokenSource interface {
	Token(context.Context) (Token, error)
}

// TokenManager caches a token in memory, refreshes it early, and serializes
// concurrent refresh calls. Invalidate lets Client recover once from a 401.
type TokenManager struct {
	mu     sync.Mutex
	source TokenSource
	token  Token
	skew   time.Duration
	now    func() time.Time
}

// NewTokenManager creates an in-memory manager. Pass one optional initial token
// loaded by the CLI from its secure credential store.
func NewTokenManager(source TokenSource, initial ...Token) *TokenManager {
	manager := &TokenManager{
		source: source,
		skew:   defaultTokenExpirySkew,
		now:    time.Now,
	}
	if len(initial) != 0 {
		manager.token = initial[0]
	}
	return manager
}

// Token returns a cached token or obtains a replacement from the source.
func (m *TokenManager) Token(ctx context.Context) (Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.token.ValidAt(m.now(), m.skew) {
		return m.token, nil
	}
	if m.source == nil {
		return Token{}, errors.New("Spotify token manager has no token source")
	}
	token, err := m.source.Token(ctx)
	if err != nil {
		return Token{}, err
	}
	if token.AccessToken == "" {
		return Token{}, errors.New("Spotify token source returned an empty access token")
	}
	if token.TokenType == "" {
		token.TokenType = "Bearer"
	}
	m.token = token
	return token, nil
}

// Invalidate forgets the access token while preserving refresh-token material
// in the manager's snapshot. The configured source is used on the next call.
func (m *TokenManager) Invalidate() {
	m.mu.Lock()
	m.token.AccessToken = ""
	m.token.ExpiresAt = time.Time{}
	m.mu.Unlock()
}

// SetToken replaces the in-memory token, for example after an interactive PKCE
// exchange. It does not persist it.
func (m *TokenManager) SetToken(token Token) {
	m.mu.Lock()
	m.token = token
	m.mu.Unlock()
}

// Snapshot returns the current in-memory value so the CLI can save it using
// its own secure credential abstraction.
func (m *TokenManager) Snapshot() Token {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.token
}

// StaticTokenSource is useful for already-managed user tokens and tests. Its
// Token value should be replaced externally when it expires.
type StaticTokenSource struct {
	TokenValue Token
}

func (s StaticTokenSource) Token(context.Context) (Token, error) {
	if s.TokenValue.AccessToken == "" {
		return Token{}, errors.New("static Spotify access token is empty")
	}
	return s.TokenValue, nil
}

// ClientCredentialsSource issues application tokens using the official OAuth
// Client Credentials flow. It does not cache or persist the client secret.
type ClientCredentialsSource struct {
	ClientID     string
	ClientSecret string
	TokenURL     string
	HTTPClient   *http.Client
	Now          func() time.Time
}

func NewClientCredentialsSource(clientID, clientSecret string, client *http.Client) *ClientCredentialsSource {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &ClientCredentialsSource{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     DefaultAccountsTokenURL,
		HTTPClient:   client,
		Now:          time.Now,
	}
}

func (s *ClientCredentialsSource) Token(ctx context.Context) (Token, error) {
	if strings.TrimSpace(s.ClientID) == "" || s.ClientSecret == "" {
		return Token{}, errors.New("Spotify client ID and client secret are required")
	}
	if err := validateSecureEndpoint(s.tokenURL()); err != nil {
		return Token{}, err
	}
	values := url.Values{"grant_type": {"client_credentials"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL(), strings.NewReader(values.Encode()))
	if err != nil {
		return Token{}, fmt.Errorf("create Spotify token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.SetBasicAuth(s.ClientID, s.ClientSecret)
	return performTokenRequest(s.httpClient(), request, s.now())
}

func (s *ClientCredentialsSource) tokenURL() string {
	if s.TokenURL != "" {
		return s.TokenURL
	}
	return DefaultAccountsTokenURL
}

func (s *ClientCredentialsSource) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (s *ClientCredentialsSource) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type tokenWire struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	RefreshToken     string `json:"refresh_token"`
	Scope            string `json:"scope"`
	ExpiresIn        int64  `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func performTokenRequest(client *http.Client, request *http.Request, now time.Time) (Token, error) {
	response, err := client.Do(request)
	if err != nil {
		if request.Context().Err() != nil {
			return Token{}, request.Context().Err()
		}
		return Token{}, fmt.Errorf("Spotify OAuth request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxOAuthResponseBytes+1))
	if err != nil {
		return Token{}, fmt.Errorf("read Spotify OAuth response: %w", err)
	}
	if len(body) > maxOAuthResponseBytes {
		return Token{}, fmt.Errorf("Spotify OAuth response exceeds %d bytes", maxOAuthResponseBytes)
	}

	var wire tokenWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return Token{}, &ResponseDecodeError{Endpoint: request.URL.Path, Cause: err}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		code := wire.Error
		if code == "" {
			code = http.StatusText(response.StatusCode)
		}
		return Token{}, &OAuthError{
			StatusCode:  response.StatusCode,
			Code:        code,
			Description: wire.ErrorDescription,
		}
	}
	if wire.AccessToken == "" {
		return Token{}, &ResponseDecodeError{Endpoint: request.URL.Path, Cause: errors.New("access_token is missing")}
	}
	expiresAt := time.Time{}
	if wire.ExpiresIn > 0 {
		expiresAt = now.Add(time.Duration(wire.ExpiresIn) * time.Second)
	}
	tokenType := wire.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}
	return Token{
		AccessToken:  wire.AccessToken,
		TokenType:    tokenType,
		RefreshToken: wire.RefreshToken,
		Scope:        wire.Scope,
		ExpiresAt:    expiresAt,
	}, nil
}
