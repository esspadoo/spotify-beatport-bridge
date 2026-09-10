package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultIsValidAndSecretFree(t *testing.T) {
	settings := Default()
	if err := settings.Validate(); err != nil {
		t.Fatalf("Default().Validate() = %v", err)
	}
	encoded := strings.ToLower(mustTOML(t, settings))
	for _, forbidden := range []string{"client_secret", "access_token", "refresh_token", "password"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("default config contains secret field %q", forbidden)
		}
	}
}

func TestLoadMergesDefaultsFileAndEnvironment(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.toml")
	contents := []byte(`version = 1

[spotify]
market = "gb"

[matching]
match_threshold = 90
ambiguous_threshold = 72
prefer_extended_mix = false
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	values := map[string]string{
		"SPOTIFY_CLIENT_ID":            " spotify-id ",
		"BEATPORT_CLIENT_ID":           "frontend-id",
		"BPBRIDGE_MATCH_THRESHOLD":     "91.5",
		"BPBRIDGE_REQUEST_TIMEOUT":     "45s",
		"BPBRIDGE_PREFER_EXTENDED_MIX": "true",
	}
	settings, err := load(path, func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if settings.Spotify.ClientID != "spotify-id" || settings.Spotify.Market != "GB" {
		t.Fatalf("unexpected Spotify settings: %+v", settings.Spotify)
	}
	if settings.Beatport.ClientID != "frontend-id" {
		t.Fatalf("Beatport client ID = %q", settings.Beatport.ClientID)
	}
	if settings.Matching.MatchThreshold != 91.5 || !settings.Matching.PreferExtendedMix {
		t.Fatalf("unexpected matching settings: %+v", settings.Matching)
	}
	if settings.Network.RequestTimeout.Value() != 45*time.Second {
		t.Fatalf("request timeout = %s", settings.Network.RequestTimeout)
	}
	if settings.Cache.TTL.Value() != 24*time.Hour {
		t.Fatalf("default cache TTL was not retained: %s", settings.Cache.TTL)
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	settings, err := load(filepath.Join(t.TempDir(), "missing.toml"), func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if settings != Default() {
		t.Fatalf("Load missing file = %#v, want defaults %#v", settings, Default())
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	want := Default()
	want.Spotify.ClientID = "public-client-id"
	want.Spotify.Market = "IT"
	want.Cache.TTL = Duration(12 * time.Hour)
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := load(path, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
}

func TestValidationRejectsUnsafeOrInconsistentValues(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{"HTTP base URL", func(c *Config) { c.Beatport.APIBaseURL = "http://api.beatport.com/v4" }},
		{"threshold order", func(c *Config) { c.Matching.AmbiguousThreshold = c.Matching.MatchThreshold + 1 }},
		{"bad market", func(c *Config) { c.Spotify.Market = "ITA" }},
		{"bad visibility", func(c *Config) { c.UI.DefaultPlaylistVisibility = "friends" }},
		{"zero TTL", func(c *Config) { c.Cache.TTL = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := Default()
			test.change(&settings)
			if err := settings.Validate(); err == nil {
				t.Fatal("Validate() unexpectedly succeeded")
			}
		})
	}
}

func TestEnvironmentParseErrorNamesVariable(t *testing.T) {
	_, err := load(filepath.Join(t.TempDir(), "missing.toml"), func(name string) (string, bool) {
		if name == "BPBRIDGE_CACHE_TTL" {
			return "tomorrow", true
		}
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), "BPBRIDGE_CACHE_TTL") {
		t.Fatalf("load error = %v", err)
	}
}

func mustTOML(t *testing.T, settings Config) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(path, settings); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
