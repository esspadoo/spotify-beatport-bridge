package credentials

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type sampleToken struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
}

func TestMemoryStoreCopiesValuesAndDeleteIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	original := []byte("secret")
	if err := store.Set(ctx, KeySpotifyClientSecret, original); err != nil {
		t.Fatal(err)
	}
	original[0] = 'X'
	first, err := store.Get(ctx, KeySpotifyClientSecret)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != "secret" {
		t.Fatalf("Get() = %q", first)
	}
	first[0] = 'Y'
	second, _ := store.Get(ctx, KeySpotifyClientSecret)
	if string(second) != "secret" {
		t.Fatal("caller mutated the stored buffer")
	}
	if err := store.Delete(ctx, KeySpotifyClientSecret); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, KeySpotifyClientSecret); err != nil {
		t.Fatalf("second Delete() = %v", err)
	}
	if _, err := store.Get(ctx, KeySpotifyClientSecret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", err)
	}
}

func TestJSONStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	want := sampleToken{Access: "access", Refresh: "refresh"}
	store := JSONStore[sampleToken]{Store: NewMemoryStore(), Key: KeyBeatportToken}
	if err := store.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
	if err := store.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load after delete = %v", err)
	}
}

func TestMemoryStoreHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := NewMemoryStore()
	if err := store.Set(ctx, "key", []byte("value")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Set() = %v, want context.Canceled", err)
	}
}

func TestRedactSecrets(t *testing.T) {
	input := `Authorization: Bearer abc.def access_token=visible refresh-token:also-visible {"client_secret":"json-secret","password":"pw"} Cookie: sessionid=cookie-value; theme=dark`
	got := RedactSecrets(input)
	for _, secret := range []string{"abc.def", "visible", "also-visible", "json-secret", `"pw"`, "cookie-value"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redaction leaked %q in %q", secret, got)
		}
	}
	if count := strings.Count(got, Redacted); count < 6 {
		t.Fatalf("redaction marker count = %d in %q", count, got)
	}
	if RedactSecret("") != "" || RedactSecret("anything") != Redacted {
		t.Fatal("RedactSecret returned an unexpected value")
	}
}

func TestInvalidKeys(t *testing.T) {
	store := NewMemoryStore()
	for _, key := range []string{"", " padded", "line\nbreak"} {
		if err := store.Set(context.Background(), key, []byte("value")); err == nil {
			t.Fatalf("Set(%q) unexpectedly succeeded", key)
		}
	}
}
