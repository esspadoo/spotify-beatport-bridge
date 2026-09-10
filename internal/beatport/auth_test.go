package beatport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoginAuthorizationCodeFlow(t *testing.T) {
	t.Parallel()
	store := &MemoryTokenStore{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/login/":
			var credentials map[string]string
			if err := json.NewDecoder(r.Body).Decode(&credentials); err != nil {
				t.Fatal(err)
			}
			if credentials["username"] != "user@example.com" || credentials["password"] != "secret" {
				t.Fatalf("credentials = %#v", credentials)
			}
			http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "session"})
			_, _ = w.Write([]byte(`{}`))
		case "/auth/o/authorize/":
			if cookie, err := r.Cookie("sessionid"); err != nil || cookie.Value != "session" {
				t.Fatalf("session cookie missing: %v %#v", err, cookie)
			}
			if r.URL.Query().Get("client_id") != "client" {
				t.Fatalf("client_id=%q", r.URL.Query().Get("client_id"))
			}
			w.Header().Set("Location", "https://callback.invalid/?code=authorization-code")
			w.WriteHeader(http.StatusFound)
		case "/auth/o/token/":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "authorization-code" {
				t.Fatalf("form = %v", r.Form)
			}
			_ = json.NewEncoder(w).Encode(Token{AccessToken: "access", RefreshToken: "refresh", ExpiresIn: 3600})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	manager := NewAuthManager(store, WithAuthBaseURL(server.URL), WithClientID("client"))
	if err := manager.Login(context.Background(), "user@example.com", "secret"); err != nil {
		t.Fatal(err)
	}
	if !store.Present || store.Token.AccessToken != "access" || store.Token.IssuedAt == 0 {
		t.Fatalf("stored token = %#v", store.Token)
	}
}

func TestRefreshAndInvalidRefresh(t *testing.T) {
	t.Parallel()
	t.Run("refresh retains old refresh token", func(t *testing.T) {
		store := &MemoryTokenStore{Present: true, Token: Token{AccessToken: "expired", RefreshToken: "old-refresh", ExpiresIn: 1, IssuedAt: 1}}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("refresh_token") != "old-refresh" {
				t.Fatalf("form=%v", r.Form)
			}
			_ = json.NewEncoder(w).Encode(Token{AccessToken: "new-access", ExpiresIn: 3600})
		}))
		defer server.Close()
		manager := NewAuthManager(store, WithAuthBaseURL(server.URL), WithClientID("client"))
		token, err := manager.AccessToken(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if token != "new-access" || store.Token.RefreshToken != "old-refresh" {
			t.Fatalf("token=%q stored=%#v", token, store.Token)
		}
	})

	t.Run("invalid refresh", func(t *testing.T) {
		store := &MemoryTokenStore{Present: true, Token: Token{AccessToken: "expired", RefreshToken: "bad", ExpiresIn: 1, IssuedAt: 1}}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"detail":"invalid_grant"}`))
		}))
		defer server.Close()
		manager := NewAuthManager(store, WithAuthBaseURL(server.URL))
		_, err := manager.AccessToken(context.Background())
		if !errors.Is(err, ErrInvalidRefresh) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestTokenImportAndLogout(t *testing.T) {
	t.Parallel()
	store := &MemoryTokenStore{}
	manager := NewAuthManager(store)
	manager.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	if err := manager.ImportJSON(context.Background(), []byte(`{"access_token":"a","refresh_token":"r","expires_in":60}`)); err != nil {
		t.Fatal(err)
	}
	if store.Token.IssuedAt != 1_700_000_000 {
		t.Fatalf("issued_at=%d", store.Token.IssuedAt)
	}
	if err := manager.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.Present {
		t.Fatal("token still present after logout")
	}
}

func TestAuthenticationErrorsDoNotEchoKnownSecrets(t *testing.T) {
	t.Parallel()
	const password = "unmistakable-password-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"password was unmistakable-password-value"}`))
	}))
	defer server.Close()

	manager := NewAuthManager(&MemoryTokenStore{}, WithAuthBaseURL(server.URL))
	err := manager.Login(context.Background(), "user@example.com", password)
	if err == nil || strings.Contains(err.Error(), password) {
		t.Fatalf("error leaked password: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("error did not retain a redaction marker: %v", err)
	}
}

func TestTokenExchangeRequiresAccessToken(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"refresh_token":"refresh","expires_in":3600}`))
	}))
	defer server.Close()

	store := &MemoryTokenStore{Present: true, Token: Token{AccessToken: "expired", RefreshToken: "refresh", ExpiresIn: 1, IssuedAt: 1}}
	manager := NewAuthManager(store, WithAuthBaseURL(server.URL))
	_, err := manager.AccessToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "access_token is missing") {
		t.Fatalf("error = %v, want missing access token", err)
	}
	if store.Token.AccessToken != "expired" {
		t.Fatalf("malformed token was persisted: %#v", store.Token)
	}
}
