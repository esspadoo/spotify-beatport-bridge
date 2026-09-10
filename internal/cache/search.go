// Package cache implements BPBridge's durable Beatport search cache.
package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bpbridge/bpbridge/internal/config"
	"github.com/bpbridge/bpbridge/internal/model"
)

const (
	SchemaVersion = 3
	FileName      = "beatport-search.json"
	maxCacheBytes = 64 << 20
)

type entry struct {
	StoredAt time.Time             `json:"stored_at"`
	Tracks   []model.BeatportTrack `json:"tracks"`
}

type fileData struct {
	Version int              `json:"version"`
	Entries map[string]entry `json:"entries"`
}

type Option func(*SearchCache)

// WithClock supplies a clock for deterministic callers and tests.
func WithClock(now func() time.Time) Option {
	return func(cache *SearchCache) {
		if now != nil {
			cache.now = now
		}
	}
}

// SearchCache is safe for concurrent use. The in-memory index is loaded once
// by Open, and each Put durably replaces the on-disk snapshot.
type SearchCache struct {
	mu      sync.RWMutex
	path    string
	ttl     time.Duration
	now     func() time.Time
	entries map[string]entry
}

func DefaultPath() (string, error) {
	directory, err := config.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "cache", FileName), nil
}

// Open loads a search cache. Passing an empty path selects DefaultPath. An
// older/newer schema is treated as an empty cache so incompatible entries are
// never returned.
func Open(path string, ttl time.Duration, options ...Option) (*SearchCache, error) {
	if ttl <= 0 {
		return nil, errors.New("cache TTL must be greater than zero")
	}
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	cache := &SearchCache{
		path:    path,
		ttl:     ttl,
		now:     time.Now,
		entries: make(map[string]entry),
	}
	for _, option := range options {
		option(cache)
	}
	if err := cache.load(); err != nil {
		return nil, err
	}
	return cache, nil
}

func (cache *SearchCache) Path() string {
	if cache == nil {
		return ""
	}
	return cache.path
}

// Get returns a defensive copy of a non-expired search result.
func (cache *SearchCache) Get(query string) ([]model.BeatportTrack, bool) {
	if cache == nil {
		return nil, false
	}
	key := normalizeKey(query)
	if key == "" {
		return nil, false
	}
	cache.mu.RLock()
	value, ok := cache.entries[key]
	now := cache.now()
	ttl := cache.ttl
	cache.mu.RUnlock()
	if !ok || value.StoredAt.IsZero() || now.Sub(value.StoredAt) >= ttl {
		return nil, false
	}
	return cloneTracks(value.Tracks), true
}

// Put replaces a query's result and atomically persists the complete cache.
// Empty result slices are cached as valid negative searches.
func (cache *SearchCache) Put(query string, tracks []model.BeatportTrack) error {
	if cache == nil {
		return errors.New("cache is nil")
	}
	key := normalizeKey(query)
	if key == "" {
		return errors.New("cache query cannot be empty")
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.entries[key] = entry{StoredAt: cache.now().UTC(), Tracks: cloneTracks(tracks)}
	cache.pruneExpiredLocked()
	if err := cache.persistLocked(); err != nil {
		return fmt.Errorf("persist search cache: %w", err)
	}
	return nil
}

// Clear removes all in-memory entries and the persisted cache. It is
// idempotent and is the implementation hook for `bpbridge cache clear`.
func (cache *SearchCache) Clear() error {
	if cache == nil {
		return nil
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.entries = make(map[string]entry)
	if err := os.Remove(cache.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove search cache: %w", err)
	}
	return nil
}

func (cache *SearchCache) Len() int {
	if cache == nil {
		return 0
	}
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	count := 0
	now := cache.now()
	for _, value := range cache.entries {
		if !value.StoredAt.IsZero() && now.Sub(value.StoredAt) < cache.ttl {
			count++
		}
	}
	return count
}

func (cache *SearchCache) load() error {
	file, err := os.Open(cache.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open search cache %q: %w", cache.path, err)
	}
	defer file.Close()

	var stored fileData
	decoder := json.NewDecoder(io.LimitReader(file, maxCacheBytes+1))
	if err := decoder.Decode(&stored); err != nil {
		return fmt.Errorf("decode search cache %q: %w", cache.path, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("decode search cache %q: %w", cache.path, err)
	}
	if stored.Version != SchemaVersion {
		return nil
	}
	for key, value := range stored.Entries {
		normalized := normalizeKey(key)
		if normalized == "" || value.StoredAt.IsZero() {
			continue
		}
		cache.entries[normalized] = entry{
			StoredAt: value.StoredAt,
			Tracks:   cloneTracks(value.Tracks),
		}
	}
	cache.pruneExpiredLocked()
	return nil
}

func (cache *SearchCache) persistLocked() error {
	stored := fileData{Version: SchemaVersion, Entries: cache.entries}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeAtomic(cache.path, data, 0o600)
}

func (cache *SearchCache) pruneExpiredLocked() {
	now := cache.now()
	for key, value := range cache.entries {
		if value.StoredAt.IsZero() || now.Sub(value.StoredAt) >= cache.ttl {
			delete(cache.entries, key)
		}
	}
}

func normalizeKey(query string) string {
	return strings.ToLower(strings.Join(strings.Fields(query), " "))
}

func cloneTracks(source []model.BeatportTrack) []model.BeatportTrack {
	if source == nil {
		return nil
	}
	result := make([]model.BeatportTrack, len(source))
	copy(result, source)
	for index := range result {
		result[index].Artists = append([]model.Artist(nil), source[index].Artists...)
		result[index].Remixers = append([]model.Artist(nil), source[index].Remixers...)
	}
	return result
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("cache contains multiple JSON values")
}

func writeAtomic(path string, data []byte, mode os.FileMode) (err error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".bpbridge-cache-*")
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
	return os.Rename(temporaryPath, path)
}
