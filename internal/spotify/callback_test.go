package spotify

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestLoopbackReceiverIgnoresBadStateThenReturnsCode(t *testing.T) {
	t.Parallel()

	receiver, err := NewLoopbackReceiver("http://127.0.0.1:0/callback", "expected-state")
	if err != nil {
		t.Fatalf("NewLoopbackReceiver() error = %v", err)
	}
	defer receiver.Close()

	badResponse := callbackRequest(t, receiver.RedirectURI(), url.Values{"state": {"wrong"}, "code": {"attacker-code"}})
	if badResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad-state status = %d", badResponse.StatusCode)
	}
	badResponse.Body.Close()

	goodResponse := callbackRequest(t, receiver.RedirectURI(), url.Values{"state": {"expected-state"}, "code": {"authorization-code"}})
	if goodResponse.StatusCode != http.StatusOK {
		t.Fatalf("valid callback status = %d", goodResponse.StatusCode)
	}
	goodResponse.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	code, err := receiver.Wait(ctx)
	if err != nil || code != "authorization-code" {
		t.Fatalf("Wait() = %q, %v", code, err)
	}
}

func TestLoopbackReceiverReturnsAuthorizationDenial(t *testing.T) {
	t.Parallel()

	receiver, err := NewLoopbackReceiver("http://127.0.0.1:0/callback", "state")
	if err != nil {
		t.Fatalf("NewLoopbackReceiver() error = %v", err)
	}
	defer receiver.Close()
	response := callbackRequest(t, receiver.RedirectURI(), url.Values{
		"state":             {"state"},
		"error":             {"access_denied"},
		"error_description": {"User declined"},
	})
	response.Body.Close()
	_, err = receiver.Wait(context.Background())
	var callbackError *CallbackError
	if !errors.As(err, &callbackError) || callbackError.Code != "access_denied" {
		t.Fatalf("error = %#v", err)
	}
}

func TestLoopbackReceiverHonorsCancellation(t *testing.T) {
	t.Parallel()

	receiver, err := NewLoopbackReceiver("http://127.0.0.1:0/", "state")
	if err != nil {
		t.Fatalf("NewLoopbackReceiver() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = receiver.Wait(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v", err)
	}
}

func TestLoopbackReceiverRejectsUnsafeRedirects(t *testing.T) {
	t.Parallel()

	redirects := []string{
		"http://localhost:7777/callback",
		"http://192.0.2.10:7777/callback",
		"https://127.0.0.1:7777/callback",
		"http://127.0.0.1/callback",
		"http://user@127.0.0.1:7777/callback",
		"http://127.0.0.1:7777/callback?code=preloaded",
	}
	for _, redirect := range redirects {
		t.Run(redirect, func(t *testing.T) {
			t.Parallel()
			if receiver, err := NewLoopbackReceiver(redirect, "state"); err == nil {
				receiver.Close()
				t.Fatal("NewLoopbackReceiver() unexpectedly succeeded")
			}
		})
	}
}

func callbackRequest(t *testing.T, redirectURI string, values url.Values) *http.Response {
	t.Helper()
	parsed, err := url.Parse(redirectURI)
	if err != nil {
		t.Fatalf("parse redirect URI: %v", err)
	}
	parsed.RawQuery = values.Encode()
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(parsed.String())
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	return response
}
