//go:build !windows

package keyring

import (
	"context"

	platformsecrets "github.com/lyming99/autoplan/backend/internal/platform/secrets"
)

type unavailableBackend struct{}

func newSystemBackend() Backend { return unavailableBackend{} }

func (unavailableBackend) Capabilities(context.Context) (platformsecrets.Capability, error) {
	return platformsecrets.Capability{}, platformsecrets.ErrUnavailable
}

func (unavailableBackend) Put(context.Context, string, string, []byte) error {
	return platformsecrets.ErrUnavailable
}

func (unavailableBackend) Get(context.Context, string, string) ([]byte, error) {
	return nil, platformsecrets.ErrUnavailable
}

func (unavailableBackend) Delete(context.Context, string, string) error {
	return platformsecrets.ErrUnavailable
}

func (unavailableBackend) Exists(context.Context, string, string) (bool, error) {
	return false, platformsecrets.ErrUnavailable
}
