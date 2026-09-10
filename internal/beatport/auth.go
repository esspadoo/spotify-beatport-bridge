package beatport

import (
	"bytes"
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
	// LegacyClientID is the public OAuth client observed in BeatportDL at commit
	// 878d3f61 (2026-06-30). It is overridable because Beatport does not provide
	// a stability guarantee for frontend/first-party client identifiers.
	LegacyClientID      = "ryZ8LuyQVPqbK2mBX2Hwt4qSMtnWuTYSqBPO92yQ"
	FrontendAPIClientID = "1xmvMPWqWYowVmAW9ezqB4Xwvcd7zHYVIG8Celtz"
)

type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type,omitempty"`
	Scope        string `json:"scope,omitempty"`
	IssuedAt     int64  `json:"issued_at"`
}

func (t Token) Valid(now time.Time) bool {
	return t.AccessToken != "" && t.IssuedAt > 0 && now.Unix()+300 < t.IssuedAt+t.ExpiresIn
}

type TokenStore interface {
	Load(context.Context) (Token, error)
	Save(context.Context, Token) error
	Delete(context.Context) error
}

type MemoryTokenStore struct {
	mu      sync.Mutex
	Token   Token
	Present bool
}

func (s *MemoryTokenStore) Load(context.Context) (Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.Present {
		return Token{}, ErrNotAuthenticated
	}
	return s.Token, nil
}

func (s *MemoryTokenStore) Save(_ context.Context, token Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Token, s.Present = token, true
	return nil
}

func (s *MemoryTokenStore) Delete(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Token, s.Present = Token{}, false
	return nil
}

type AuthManager struct {
	mu         sync.Mutex
	httpClient *http.Client
	apiBase    string
	clientID   string
	store      TokenStore
	token      Token
	loaded     bool
	now        func() time.Time
}

type AuthOption func(*AuthManager)

func WithAuthHTTPClient(client *http.Client) AuthOption {
	return func(a *AuthManager) {
		if client != nil {
			a.httpClient = client
		}
	}
}

func WithAuthBaseURL(baseURL string) AuthOption {
	return func(a *AuthManager) {
		if baseURL != "" {
			a.apiBase = strings.TrimRight(baseURL, "/")
		}
	}
}

func WithClientID(clientID string) AuthOption {
	return func(a *AuthManager) {
		if clientID != "" {
			a.clientID = clientID
		}
	}
}

func NewAuthManager(store TokenStore, options ...AuthOption) *AuthManager {
	manager := &AuthManager{
		httpClient: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		apiBase:  DefaultAPIBase,
		clientID: LegacyClientID,
		store:    store,
		now:      time.Now,
	}
	for _, option := range options {
		option(manager)
	}
	return manager
}

func (a *AuthManager) AccessToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.load(ctx); err != nil {
		return "", err
	}
	if a.token.Valid(a.now()) {
		return a.token.AccessToken, nil
	}
	if a.token.RefreshToken == "" {
		return "", ErrNotAuthenticated
	}
	if err := a.refreshLocked(ctx); err != nil {
		return "", err
	}
	return a.token.AccessToken, nil
}

func (a *AuthManager) Invalidate() {
	a.mu.Lock()
	a.token.IssuedAt = 0
	a.mu.Unlock()
}

func (a *AuthManager) Status(ctx context.Context) (authenticated bool, expiresAt time.Time, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.load(ctx); err != nil {
		if errors.Is(err, ErrNotAuthenticated) {
			return false, time.Time{}, nil
		}
		return false, time.Time{}, err
	}
	return a.token.AccessToken != "", time.Unix(a.token.IssuedAt+a.token.ExpiresIn, 0), nil
}

func (a *AuthManager) Login(ctx context.Context, username, password string) error {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return errors.New("Beatport username and password are required")
	}
	payload, _ := json.Marshal(map[string]string{"username": username, "password": password})
	loginResp, err := a.rawRequest(ctx, http.MethodPost, a.apiBase+"/auth/login/", payload, "application/json", "")
	if err != nil {
		return fmt.Errorf("Beatport login: %s", redactKnownValues(err.Error(), password))
	}
	var sessionID string
	for _, cookie := range loginResp.Cookies() {
		if cookie.Name == "sessionid" {
			sessionID = cookie.Value
			break
		}
	}
	loginResp.Body.Close()
	if sessionID == "" {
		return errors.New("Beatport login did not return a session cookie; MFA accounts can use auth beatport --token")
	}

	authorizeURL := a.apiBase + "/auth/o/authorize/?" + url.Values{
		"client_id":     {a.clientID},
		"response_type": {"code"},
	}.Encode()
	authorizeResp, err := a.rawRequest(ctx, http.MethodGet, authorizeURL, nil, "", "sessionid="+sessionID)
	if err != nil {
		return fmt.Errorf("Beatport authorization: %s", redactKnownValues(err.Error(), sessionID))
	}
	location := authorizeResp.Header.Get("Location")
	authorizeResp.Body.Close()
	redirect, err := url.Parse(location)
	if err != nil || redirect.Query().Get("code") == "" {
		return errors.New("Beatport authorization did not return a code; use token import if the account requires MFA")
	}
	form := url.Values{
		"client_id":  {a.clientID},
		"grant_type": {"authorization_code"},
		"code":       {redirect.Query().Get("code")},
	}
	token, err := a.issueToken(ctx, form)
	if err != nil {
		return errors.New(redactKnownValues(err.Error(), password, sessionID, redirect.Query().Get("code")))
	}
	return a.save(ctx, token)
}

func (a *AuthManager) ImportJSON(ctx context.Context, data []byte) error {
	var token Token
	if err := json.Unmarshal(data, &token); err != nil {
		return fmt.Errorf("invalid Beatport token JSON: %w", err)
	}
	if token.AccessToken == "" {
		return errors.New("Beatport token JSON has no access_token")
	}
	if token.ExpiresIn <= 0 {
		token.ExpiresIn = 3600
	}
	if token.IssuedAt <= 0 {
		token.IssuedAt = a.now().Unix()
	}
	return a.save(ctx, token)
}

func (a *AuthManager) Logout(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.token, a.loaded = Token{}, true
	if a.store == nil {
		return nil
	}
	return a.store.Delete(ctx)
}

func (a *AuthManager) load(ctx context.Context) error {
	if a.loaded {
		if a.token.AccessToken == "" {
			return ErrNotAuthenticated
		}
		return nil
	}
	a.loaded = true
	if a.store == nil {
		return ErrNotAuthenticated
	}
	token, err := a.store.Load(ctx)
	if err != nil {
		return err
	}
	a.token = token
	return nil
}

func (a *AuthManager) save(ctx context.Context, token Token) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.saveLocked(ctx, token)
}

func (a *AuthManager) saveLocked(ctx context.Context, token Token) error {
	if token.IssuedAt <= 0 {
		token.IssuedAt = a.now().Unix()
	}
	a.token, a.loaded = token, true
	if a.store != nil {
		return a.store.Save(ctx, token)
	}
	return nil
}

func (a *AuthManager) refreshLocked(ctx context.Context) error {
	form := url.Values{
		"client_id":     {a.clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {a.token.RefreshToken},
	}
	oldRefresh := a.token.RefreshToken
	token, err := a.issueToken(ctx, form)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && (apiErr.Status == http.StatusBadRequest || apiErr.Status == http.StatusUnauthorized) {
			return fmt.Errorf("%w: %s", ErrInvalidRefresh, redactKnownValues(apiErr.Detail, a.token.RefreshToken))
		}
		return errors.New(redactKnownValues(err.Error(), a.token.RefreshToken))
	}
	if token.RefreshToken == "" {
		token.RefreshToken = oldRefresh
	}
	return a.saveLocked(ctx, token)
}

func (a *AuthManager) issueToken(ctx context.Context, form url.Values) (Token, error) {
	var token Token
	resp, err := a.rawRequest(ctx, http.MethodPost, a.apiBase+"/auth/o/token/", []byte(form.Encode()), "application/x-www-form-urlencoded", "")
	if err != nil {
		return token, fmt.Errorf("Beatport token exchange: %w", err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&token); err != nil {
		return token, fmt.Errorf("decode Beatport token response: %w", err)
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return token, errors.New("decode Beatport token response: access_token is missing")
	}
	if token.ExpiresIn <= 0 {
		token.ExpiresIn = 3600
	}
	token.IssuedAt = a.now().Unix()
	return token, nil
}

func redactKnownValues(message string, values ...string) string {
	for _, value := range values {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[REDACTED]")
		}
	}
	return message
}

func (a *AuthManager) rawRequest(ctx context.Context, method, endpoint string, body []byte, contentType, cookie string) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "bpbridge/1")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if (resp.StatusCode >= 200 && resp.StatusCode < 300) || resp.StatusCode == http.StatusFound {
		return resp, nil
	}
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	return nil, makeAPIError(resp, method, endpoint, payload)
}

func (t Token) MarshalSafeStatus() map[string]any {
	return map[string]any{
		"authenticated": t.AccessToken != "",
		"has_refresh":   t.RefreshToken != "",
		"expires_at":    time.Unix(t.IssuedAt+t.ExpiresIn, 0),
		"scope":         t.Scope,
	}
}
