// Package config loads BPBridge's non-secret settings.
//
// OAuth tokens, passwords, and client secrets intentionally have no fields in
// Config. They belong in internal/credentials instead.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const (
	SchemaVersion = 1
	AppDirectory  = "BPBridge"
	FileName      = "config.toml"

	defaultBeatportAPIBaseURL = "https://api.beatport.com/v4"
	// This is the public authorization-code client identifier verified in
	// BeatportDL in June 2026. Beatport's separate current frontend API4 client
	// has code grant disabled and is therefore not the safe default for the
	// legacy interactive login flow. This identifier is not a user secret and
	// is always overridable through BEATPORT_CLIENT_ID.
	defaultBeatportClientID = "ryZ8LuyQVPqbK2mBX2Hwt4qSMtnWuTYSqBPO92yQ"
)

// Duration is a human-readable TOML duration such as "30s" or "24h".
type Duration time.Duration

func (d Duration) String() string {
	return time.Duration(d).String()
}

// Value converts d to the standard-library duration type.
func (d Duration) Value() time.Duration {
	return time.Duration(d)
}

func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
}

func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(strings.TrimSpace(string(text)))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", text, err)
	}
	*d = Duration(parsed)
	return nil
}

type Spotify struct {
	ClientID string `toml:"client_id" json:"client_id"`
	Market   string `toml:"market" json:"market"`
}

type Beatport struct {
	APIBaseURL        string `toml:"api_base_url" json:"api_base_url"`
	ClientID          string `toml:"client_id" json:"client_id"`
	SearchResultCount int    `toml:"search_result_count" json:"search_result_count"`
}

type Matching struct {
	MatchThreshold     float64 `toml:"match_threshold" json:"match_threshold"`
	AmbiguousThreshold float64 `toml:"ambiguous_threshold" json:"ambiguous_threshold"`
	PreferExtendedMix  bool    `toml:"prefer_extended_mix" json:"prefer_extended_mix"`
}

type Network struct {
	RequestTimeout Duration `toml:"request_timeout" json:"request_timeout"`
	MaxRetries     int      `toml:"max_retries" json:"max_retries"`
}

type Cache struct {
	TTL Duration `toml:"ttl" json:"ttl"`
}

type UI struct {
	DefaultPlaylistVisibility string `toml:"default_playlist_visibility" json:"default_playlist_visibility"`
	LogLevel                  string `toml:"log_level" json:"log_level"`
}

// Config contains only values safe to write to config.toml.
type Config struct {
	Version  int      `toml:"version" json:"version"`
	Spotify  Spotify  `toml:"spotify" json:"spotify"`
	Beatport Beatport `toml:"beatport" json:"beatport"`
	Matching Matching `toml:"matching" json:"matching"`
	Network  Network  `toml:"network" json:"network"`
	Cache    Cache    `toml:"cache" json:"cache"`
	UI       UI       `toml:"ui" json:"ui"`
}

// Default returns conservative production defaults. Authentication identifiers
// that are private to the user are deliberately absent.
func Default() Config {
	return Config{
		Version: SchemaVersion,
		Spotify: Spotify{},
		Beatport: Beatport{
			APIBaseURL:        defaultBeatportAPIBaseURL,
			ClientID:          defaultBeatportClientID,
			SearchResultCount: 25,
		},
		Matching: Matching{
			MatchThreshold:     85,
			AmbiguousThreshold: 70,
			PreferExtendedMix:  true,
		},
		Network: Network{
			RequestTimeout: Duration(30 * time.Second),
			MaxRetries:     3,
		},
		Cache: Cache{TTL: Duration(24 * time.Hour)},
		UI: UI{
			DefaultPlaylistVisibility: "private",
			LogLevel:                  "info",
		},
	}
}

// DataDir returns the directory used for BPBridge's non-secret local data.
// Windows uses %LOCALAPPDATA% as required by the CLI contract.
func DataDir() (string, error) {
	if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
		return filepath.Join(local, AppDirectory), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user configuration directory: %w", err)
	}
	return filepath.Join(base, AppDirectory), nil
}

func DefaultPath() (string, error) {
	directory, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, FileName), nil
}

// Load reads a TOML file on top of Default, then applies environment
// overrides. A missing file is a valid first-run state. Passing an empty path
// selects DefaultPath.
func Load(path string) (Config, error) {
	return load(path, os.LookupEnv)
}

type envLookup func(string) (string, bool)

func load(path string, lookup envLookup) (Config, error) {
	settings := Default()
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return Config{}, err
		}
	}

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := toml.Unmarshal(data, &settings); err != nil {
			return Config{}, fmt.Errorf("decode config %q: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// First run: defaults plus environment overrides are sufficient.
	default:
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}

	if err := applyEnvironment(&settings, lookup); err != nil {
		return Config{}, err
	}
	normalize(&settings)
	if err := settings.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config: %w", err)
	}
	return settings, nil
}

// Save validates and atomically writes a secret-free TOML file. Passing an
// empty path selects DefaultPath.
func Save(path string, settings Config) error {
	normalize(&settings)
	if err := settings.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return err
		}
	}
	data, err := toml.Marshal(settings)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := writeAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("write config %q: %w", path, err)
	}
	return nil
}

func normalize(settings *Config) {
	settings.Spotify.ClientID = strings.TrimSpace(settings.Spotify.ClientID)
	settings.Spotify.Market = strings.ToUpper(strings.TrimSpace(settings.Spotify.Market))
	settings.Beatport.APIBaseURL = strings.TrimRight(strings.TrimSpace(settings.Beatport.APIBaseURL), "/")
	settings.Beatport.ClientID = strings.TrimSpace(settings.Beatport.ClientID)
	settings.UI.DefaultPlaylistVisibility = strings.ToLower(strings.TrimSpace(settings.UI.DefaultPlaylistVisibility))
	settings.UI.LogLevel = strings.ToLower(strings.TrimSpace(settings.UI.LogLevel))
}

// Validate checks values without requiring authentication to have been set up.
func (settings Config) Validate() error {
	var problems []error
	if settings.Version != SchemaVersion {
		problems = append(problems, fmt.Errorf("unsupported config version %d (want %d)", settings.Version, SchemaVersion))
	}
	if market := settings.Spotify.Market; market != "" && !isASCIICountryCode(market) {
		problems = append(problems, fmt.Errorf("Spotify market must be a two-letter country code, got %q", market))
	}
	if err := validateHTTPSURL("Beatport API base URL", settings.Beatport.APIBaseURL); err != nil {
		problems = append(problems, err)
	}
	if settings.Beatport.ClientID == "" {
		problems = append(problems, errors.New("Beatport client ID cannot be empty"))
	}
	if settings.Beatport.SearchResultCount < 1 || settings.Beatport.SearchResultCount > 100 {
		problems = append(problems, errors.New("Beatport search result count must be between 1 and 100"))
	}
	if settings.Matching.MatchThreshold <= 0 || settings.Matching.MatchThreshold > 100 {
		problems = append(problems, errors.New("match threshold must be greater than zero and at most 100"))
	}
	if settings.Matching.AmbiguousThreshold <= 0 || settings.Matching.AmbiguousThreshold > 100 {
		problems = append(problems, errors.New("ambiguous threshold must be greater than zero and at most 100"))
	}
	if settings.Matching.AmbiguousThreshold > settings.Matching.MatchThreshold {
		problems = append(problems, errors.New("ambiguous threshold cannot exceed match threshold"))
	}
	timeout := settings.Network.RequestTimeout.Value()
	if timeout < time.Second || timeout > 5*time.Minute {
		problems = append(problems, errors.New("request timeout must be between 1s and 5m"))
	}
	if settings.Network.MaxRetries < 0 || settings.Network.MaxRetries > 10 {
		problems = append(problems, errors.New("max retries must be between 0 and 10"))
	}
	ttl := settings.Cache.TTL.Value()
	if ttl <= 0 || ttl > 365*24*time.Hour {
		problems = append(problems, errors.New("cache TTL must be greater than zero and at most 8760h"))
	}
	if !oneOf(settings.UI.DefaultPlaylistVisibility, "private", "public") {
		problems = append(problems, errors.New("default playlist visibility must be private or public"))
	}
	if !oneOf(settings.UI.LogLevel, "error", "warn", "info", "debug") {
		problems = append(problems, errors.New("log level must be error, warn, info, or debug"))
	}
	return errors.Join(problems...)
}

func applyEnvironment(settings *Config, lookup envLookup) error {
	stringOverride(lookup, "SPOTIFY_CLIENT_ID", &settings.Spotify.ClientID)
	stringOverride(lookup, "BPBRIDGE_MARKET", &settings.Spotify.Market)
	stringOverride(lookup, "BEATPORT_CLIENT_ID", &settings.Beatport.ClientID)
	stringOverride(lookup, "BPBRIDGE_BEATPORT_API_BASE_URL", &settings.Beatport.APIBaseURL)
	stringOverride(lookup, "BPBRIDGE_DEFAULT_PLAYLIST_VISIBILITY", &settings.UI.DefaultPlaylistVisibility)
	stringOverride(lookup, "BPBRIDGE_LOG_LEVEL", &settings.UI.LogLevel)

	var problems []error
	parseIntEnv(lookup, "BPBRIDGE_BEATPORT_SEARCH_RESULT_COUNT", &settings.Beatport.SearchResultCount, &problems)
	parseIntEnv(lookup, "BPBRIDGE_MAX_RETRIES", &settings.Network.MaxRetries, &problems)
	parseFloatEnv(lookup, "BPBRIDGE_MATCH_THRESHOLD", &settings.Matching.MatchThreshold, &problems)
	parseFloatEnv(lookup, "BPBRIDGE_AMBIGUOUS_THRESHOLD", &settings.Matching.AmbiguousThreshold, &problems)
	parseBoolEnv(lookup, "BPBRIDGE_PREFER_EXTENDED_MIX", &settings.Matching.PreferExtendedMix, &problems)
	parseDurationEnv(lookup, "BPBRIDGE_REQUEST_TIMEOUT", &settings.Network.RequestTimeout, &problems)
	parseDurationEnv(lookup, "BPBRIDGE_CACHE_TTL", &settings.Cache.TTL, &problems)
	if err := errors.Join(problems...); err != nil {
		return fmt.Errorf("apply environment configuration: %w", err)
	}
	return nil
}

func stringOverride(lookup envLookup, name string, destination *string) {
	if value, ok := lookup(name); ok {
		*destination = strings.TrimSpace(value)
	}
}

func parseIntEnv(lookup envLookup, name string, destination *int, problems *[]error) {
	value, ok := lookup(name)
	if !ok {
		return
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		*problems = append(*problems, fmt.Errorf("%s must be an integer: %w", name, err))
		return
	}
	*destination = parsed
}

func parseFloatEnv(lookup envLookup, name string, destination *float64, problems *[]error) {
	value, ok := lookup(name)
	if !ok {
		return
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		*problems = append(*problems, fmt.Errorf("%s must be a number: %w", name, err))
		return
	}
	*destination = parsed
}

func parseBoolEnv(lookup envLookup, name string, destination *bool, problems *[]error) {
	value, ok := lookup(name)
	if !ok {
		return
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		*problems = append(*problems, fmt.Errorf("%s must be a boolean: %w", name, err))
		return
	}
	*destination = parsed
}

func parseDurationEnv(lookup envLookup, name string, destination *Duration, problems *[]error) {
	value, ok := lookup(name)
	if !ok {
		return
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		*problems = append(*problems, fmt.Errorf("%s must be a duration: %w", name, err))
		return
	}
	*destination = Duration(parsed)
}

func validateHTTPSURL(label, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("%s must be an absolute HTTPS URL without user information", label)
	}
	return nil
}

func isASCIICountryCode(value string) bool {
	if len(value) != 2 {
		return false
	}
	return value[0] >= 'A' && value[0] <= 'Z' && value[1] >= 'A' && value[1] <= 'Z'
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func writeAtomic(path string, data []byte, mode os.FileMode) (err error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".bpbridge-config-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err = temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err = temporary.Write(data); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}
