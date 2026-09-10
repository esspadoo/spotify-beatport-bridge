package spotify

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const DefaultAuthorizationURL = "https://accounts.spotify.com/authorize"

// PKCEPair is an RFC 7636 verifier and its S256 challenge.
type PKCEPair struct {
	Verifier  string
	Challenge string
}

// GeneratePKCE creates a cryptographically strong 43-character verifier.
func GeneratePKCE() (PKCEPair, error) {
	verifier, err := randomBase64URL(32)
	if err != nil {
		return PKCEPair{}, fmt.Errorf("generate Spotify PKCE verifier: %w", err)
	}
	return PKCEPair{Verifier: verifier, Challenge: PKCEChallenge(verifier)}, nil
}

// PKCEChallenge returns the RFC 7636 S256 challenge for a verifier.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// GenerateState creates a cryptographically strong authorization state value.
func GenerateState() (string, error) {
	state, err := randomBase64URL(32)
	if err != nil {
		return "", fmt.Errorf("generate Spotify OAuth state: %w", err)
	}
	return state, nil
}

func randomBase64URL(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// OAuthConfig configures the secretless Authorization Code with PKCE flow.
type OAuthConfig struct {
	ClientID         string
	AuthorizationURL string
	TokenURL         string
	HTTPClient       *http.Client
	Now              func() time.Time
}

// OAuthClient builds authorization URLs and exchanges/refreshes PKCE tokens.
// It never needs or accepts a Spotify client secret.
type OAuthClient struct {
	clientID         string
	authorizationURL string
	tokenURL         string
	httpClient       *http.Client
	now              func() time.Time
}

func NewOAuthClient(config OAuthConfig) (*OAuthClient, error) {
	if strings.TrimSpace(config.ClientID) == "" {
		return nil, errors.New("Spotify OAuth client ID is required")
	}
	authorizationURL := config.AuthorizationURL
	if authorizationURL == "" {
		authorizationURL = DefaultAuthorizationURL
	}
	tokenURL := config.TokenURL
	if tokenURL == "" {
		tokenURL = DefaultAccountsTokenURL
	}
	for label, rawURL := range map[string]string{"authorization": authorizationURL, "token": tokenURL} {
		if err := validateSecureEndpoint(rawURL); err != nil {
			return nil, fmt.Errorf("invalid Spotify %s endpoint", label)
		}
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &OAuthClient{
		clientID:         config.ClientID,
		authorizationURL: authorizationURL,
		tokenURL:         tokenURL,
		httpClient:       httpClient,
		now:              now,
	}, nil
}

// AuthorizationURL constructs the user authorization URL. The caller should
// generate state with GenerateState and verify it in a LoopbackReceiver.
func (c *OAuthClient) AuthorizationURL(redirectURI, state, challenge string, scopes []string) (string, error) {
	if state == "" {
		return "", errors.New("Spotify OAuth state is required")
	}
	if challenge == "" {
		return "", errors.New("Spotify PKCE challenge is required")
	}
	if err := validateRedirectURI(redirectURI); err != nil {
		return "", err
	}
	endpoint, err := url.Parse(c.authorizationURL)
	if err != nil {
		return "", fmt.Errorf("parse Spotify authorization endpoint: %w", err)
	}
	values := endpoint.Query()
	values.Set("client_id", c.clientID)
	values.Set("response_type", "code")
	values.Set("redirect_uri", redirectURI)
	values.Set("state", state)
	values.Set("code_challenge_method", "S256")
	values.Set("code_challenge", challenge)
	if len(scopes) != 0 {
		values.Set("scope", strings.Join(scopes, " "))
	}
	endpoint.RawQuery = values.Encode()
	return endpoint.String(), nil
}

// Exchange trades a callback code and verifier for access/refresh tokens.
func (c *OAuthClient) Exchange(ctx context.Context, redirectURI, code, verifier string) (Token, error) {
	if code == "" {
		return Token{}, errors.New("Spotify authorization code is required")
	}
	if err := validateVerifier(verifier); err != nil {
		return Token{}, err
	}
	if err := validateRedirectURI(redirectURI); err != nil {
		return Token{}, err
	}
	values := url.Values{
		"client_id":     {c.clientID},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	return c.postToken(ctx, values)
}

// Refresh obtains a new PKCE access token. Spotify may omit refresh_token in
// the response; RefreshTokenSource preserves the previous value in that case.
func (c *OAuthClient) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	if refreshToken == "" {
		return Token{}, errors.New("Spotify refresh token is required")
	}
	values := url.Values{
		"client_id":     {c.clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	return c.postToken(ctx, values)
}

func (c *OAuthClient) postToken(ctx context.Context, values url.Values) (Token, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return Token{}, fmt.Errorf("create Spotify OAuth request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	return performTokenRequest(c.httpClient, request, c.now())
}

func validateVerifier(verifier string) error {
	if len(verifier) < 43 || len(verifier) > 128 {
		return errors.New("Spotify PKCE verifier must be between 43 and 128 characters")
	}
	for _, character := range verifier {
		if !((character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("-._~", character)) {
			return errors.New("Spotify PKCE verifier contains an invalid character")
		}
	}
	return nil
}

// RefreshTokenSource adapts OAuthClient.Refresh to TokenSource. It retains
// rotating refresh tokens only in memory and exposes Snapshot for the CLI's
// secure-store layer.
type RefreshTokenSource struct {
	mu           sync.Mutex
	oauth        *OAuthClient
	refreshToken string
	last         Token
}

func NewRefreshTokenSource(oauth *OAuthClient, initial Token) *RefreshTokenSource {
	return &RefreshTokenSource{oauth: oauth, refreshToken: initial.RefreshToken, last: initial}
}

func (s *RefreshTokenSource) Token(ctx context.Context) (Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.oauth == nil {
		return Token{}, errors.New("Spotify refresh source has no OAuth client")
	}
	if s.refreshToken == "" {
		return Token{}, errors.New("Spotify refresh token is required; authorize again")
	}
	token, err := s.oauth.Refresh(ctx, s.refreshToken)
	if err != nil {
		return Token{}, err
	}
	if token.RefreshToken == "" {
		token.RefreshToken = s.refreshToken
	}
	s.refreshToken = token.RefreshToken
	s.last = token
	return token, nil
}

func (s *RefreshTokenSource) Snapshot() Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}
