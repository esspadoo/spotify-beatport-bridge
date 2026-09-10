package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bpbridge/bpbridge/internal/model"
)

func TestPutGetPersistsAndCopies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "search.json")
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	first, err := Open(path, time.Hour, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	tracks := []model.BeatportTrack{{
		ID:      42,
		Name:    "Track",
		Artists: []model.Artist{{ID: 7, Name: "Artist"}},
	}}
	if err := first.Put("  ARTIST   Track ", tracks); err != nil {
		t.Fatal(err)
	}
	tracks[0].Name = "mutated"
	tracks[0].Artists[0].Name = "mutated"

	got, ok := first.Get("artist track")
	if !ok || got[0].Name != "Track" || got[0].Artists[0].Name != "Artist" {
		t.Fatalf("Get() = %#v, %v", got, ok)
	}
	got[0].Artists[0].Name = "caller mutation"
	again, _ := first.Get("artist track")
	if again[0].Artists[0].Name != "Artist" {
		t.Fatal("Get returned mutable cache internals")
	}

	second, err := Open(path, time.Hour, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := second.Get("Artist Track")
	if !ok || len(persisted) != 1 || persisted[0].ID != 42 {
		t.Fatalf("persisted Get() = %#v, %v", persisted, ok)
	}
}

func TestExpirationAndNegativeResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "search.json")
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	cache, err := Open(path, time.Hour, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Put("no results", []model.BeatportTrack{}); err != nil {
		t.Fatal(err)
	}
	if got, ok := cache.Get("no results"); !ok || len(got) != 0 {
		t.Fatalf("negative cache Get() = %#v, %v", got, ok)
	}
	now = now.Add(time.Hour)
	if _, ok := cache.Get("no results"); ok {
		t.Fatal("expired entry was returned")
	}
}

func TestSchemaMismatchInvalidatesEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "search.json")
	data := []byte(`{"version":99,"entries":{"query":{"stored_at":"2026-09-01T12:00:00Z","tracks":[{"id":42,"name":"Track"}]}}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cache, err := Open(path, time.Hour, WithClock(func() time.Time {
		return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cache.Len() != 0 {
		t.Fatalf("Len() = %d after schema mismatch", cache.Len())
	}
}

func TestClearIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "search.json")
	cache, err := Open(path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Put("query", nil); err != nil {
		t.Fatal(err)
	}
	if err := cache.Clear(); err != nil {
		t.Fatal(err)
	}
	if err := cache.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cache file still exists: %v", err)
	}
}

func TestMalformedCacheReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "search.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, time.Hour); err == nil {
		t.Fatal("Open() unexpectedly accepted malformed JSON")
	}
}
