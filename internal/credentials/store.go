// Package credentials provides secure, replaceable storage for BPBridge's
// secret values. Production Windows builds use Credential Manager; tests can
// use MemoryStore without touching the user's vault.
package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

const DefaultService = "BPBridge"

const (
	KeySpotifyClientSecret = "spotify.client_secret"
	KeySpotifyToken        = "spotify.token_json"
	KeyBeatportToken       = "beatport.token_json"
)

var (
	ErrNotFound    = errors.New("credential not found")
	ErrUnsupported = errors.New("secure credential storage is unsupported on this platform")
	ErrTooLarge    = errors.New("credential exceeds the platform storage limit")
)

// Store is the minimum interface needed for setup, token persistence, and
// logout. Implementations must return copies rather than mutable internal
// buffers.
type Store interface {
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte) error
	Delete(context.Context, string) error
}

// NewDefaultStore opens the operating system's secure credential store.
func NewDefaultStore() (Store, error) {
	return newPlatformStore(DefaultService)
}

// MemoryStore is concurrency-safe and intended for tests and short-lived
// program state. It does not persist secrets.
type MemoryStore struct {
	mu     sync.RWMutex
	values map[string][]byte
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{values: make(map[string][]byte)}
}

func (s *MemoryStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := validateKey(key); err != nil {
		return nil, err
	}
	s.mu.RLock()
	value, ok := s.values[key]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return append([]byte(nil), value...), nil
}

func (s *MemoryStore) Set(ctx context.Context, key string, value []byte) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}
	if value == nil {
		return errors.New("credential value cannot be nil")
	}
	s.mu.Lock()
	if s.values == nil {
		s.values = make(map[string][]byte)
	}
	s.values[key] = append([]byte(nil), value...)
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) Delete(ctx context.Context, key string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.values, key)
	s.mu.Unlock()
	return nil
}

// JSONStore adapts Store to provider-specific token-store interfaces. For
// example, JSONStore[beatport.Token] implements Beatport's Load/Save/Delete
// contract without either package depending on the other.
type JSONStore[T any] struct {
	Store Store
	Key   string
}

func (s JSONStore[T]) Load(ctx context.Context) (T, error) {
	var value T
	if s.Store == nil {
		return value, errors.New("credential JSON store has no backing store")
	}
	data, err := s.Store.Get(ctx, s.Key)
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return value, fmt.Errorf("decode credential %q: %w", s.Key, err)
	}
	return value, nil
}

func (s JSONStore[T]) Save(ctx context.Context, value T) error {
	if s.Store == nil {
		return errors.New("credential JSON store has no backing store")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode credential %q: %w", s.Key, err)
	}
	if err := s.Store.Set(ctx, s.Key, data); err != nil {
		return fmt.Errorf("save credential %q: %w", s.Key, err)
	}
	return nil
}

func (s JSONStore[T]) Delete(ctx context.Context) error {
	if s.Store == nil {
		return errors.New("credential JSON store has no backing store")
	}
	return s.Store.Delete(ctx, s.Key)
}

func validateKey(key string) error {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return errors.New("credential key cannot be empty")
	}
	if trimmed != key || len(key) > 128 || strings.ContainsAny(key, "\x00\r\n") {
		return fmt.Errorf("invalid credential key %q", key)
	}
	return nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("credential operation requires a context")
	}
	return ctx.Err()
}
