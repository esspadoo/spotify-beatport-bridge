//go:build !windows

package credentials

import (
	"context"
	"fmt"
)

// CredentialManagerStore exists on non-Windows platforms so shared code can
// compile, but it never falls back to plaintext or silently ephemeral storage.
type CredentialManagerStore struct{}

func NewCredentialManagerStore(string) (*CredentialManagerStore, error) {
	return nil, ErrUnsupported
}

func newPlatformStore(string) (Store, error) {
	return nil, ErrUnsupported
}

func (*CredentialManagerStore) Get(context.Context, string) ([]byte, error) {
	return nil, ErrUnsupported
}

func (*CredentialManagerStore) Set(context.Context, string, []byte) error {
	return ErrUnsupported
}

func (*CredentialManagerStore) Delete(context.Context, string) error {
	return fmt.Errorf("delete credential: %w", ErrUnsupported)
}
