//go:build windows

package credentials

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/danieljoos/wincred"
)

// Windows Credential Manager limits a generic credential blob to 2560 bytes
// on supported modern Windows versions.
const maxCredentialBlobBytes = 2560

type CredentialManagerStore struct {
	service string
}

func NewCredentialManagerStore(service string) (*CredentialManagerStore, error) {
	service = strings.TrimSpace(service)
	if service == "" || len(service) > 128 || strings.ContainsAny(service, "\x00\r\n") {
		return nil, errors.New("credential service name is invalid")
	}
	return &CredentialManagerStore{service: service}, nil
}

func newPlatformStore(service string) (Store, error) {
	return NewCredentialManagerStore(service)
}

func (s *CredentialManagerStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	target, err := s.target(key)
	if err != nil {
		return nil, err
	}
	credential, err := wincred.GetGenericCredential(target)
	if err != nil {
		if errors.Is(err, wincred.ErrElementNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, fmt.Errorf("read Windows credential %q: %w", key, err)
	}
	return append([]byte(nil), credential.CredentialBlob...), nil
}

func (s *CredentialManagerStore) Set(ctx context.Context, key string, value []byte) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	target, err := s.target(key)
	if err != nil {
		return err
	}
	if value == nil {
		return errors.New("credential value cannot be nil")
	}
	if len(value) > maxCredentialBlobBytes {
		return fmt.Errorf("%w: got %d bytes, maximum is %d", ErrTooLarge, len(value), maxCredentialBlobBytes)
	}
	credential := wincred.NewGenericCredential(target)
	credential.UserName = s.service
	credential.CredentialBlob = append([]byte(nil), value...)
	if err := credential.Write(); err != nil {
		return fmt.Errorf("write Windows credential %q: %w", key, err)
	}
	return nil
}

func (s *CredentialManagerStore) Delete(ctx context.Context, key string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	target, err := s.target(key)
	if err != nil {
		return err
	}
	credential, err := wincred.GetGenericCredential(target)
	if err != nil {
		if errors.Is(err, wincred.ErrElementNotFound) {
			return nil
		}
		return fmt.Errorf("read Windows credential %q for deletion: %w", key, err)
	}
	if err := credential.Delete(); err != nil && !errors.Is(err, wincred.ErrElementNotFound) {
		return fmt.Errorf("delete Windows credential %q: %w", key, err)
	}
	return nil
}

func (s *CredentialManagerStore) target(key string) (string, error) {
	if s == nil || s.service == "" {
		return "", errors.New("Windows credential store is not initialized")
	}
	if err := validateKey(key); err != nil {
		return "", err
	}
	return s.service + "/" + key, nil
}
