// Package keyring adapts the current platform credential store to the common
// secrets Provider contract.
package keyring

import (
	"context"
	"errors"
	"strings"

	domainsecrets "github.com/lyming99/autoplan/backend/internal/domain/secrets"
	platformsecrets "github.com/lyming99/autoplan/backend/internal/platform/secrets"
)

const providerName = "os-keyring-v1"

var ErrInvalidOptions = errors.New("keyring options are invalid")

type Backend interface {
	Capabilities(context.Context) (platformsecrets.Capability, error)
	Put(context.Context, string, string, []byte) error
	Get(context.Context, string, string) ([]byte, error)
	Delete(context.Context, string, string) error
	Exists(context.Context, string, string) (bool, error)
}

type Options struct {
	Service string
	Backend Backend
}

type Provider struct {
	service string
	backend Backend
}

func New(options Options) (*Provider, error) {
	service := strings.TrimSpace(options.Service)
	if service == "" || len(service) > 120 || strings.ContainsAny(service, "\\/\x00") {
		return nil, ErrInvalidOptions
	}
	backend := options.Backend
	if backend == nil {
		backend = newSystemBackend()
	}
	if backend == nil {
		return nil, ErrInvalidOptions
	}
	return &Provider{service: service, backend: backend}, nil
}

func (provider *Provider) Name() string { return providerName }

func (provider *Provider) Capabilities(ctx context.Context) (platformsecrets.Capability, error) {
	if provider == nil || provider.backend == nil {
		return platformsecrets.Capability{}, platformsecrets.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return platformsecrets.Capability{}, err
	}
	return provider.backend.Capabilities(ctx)
}

func (provider *Provider) Put(ctx context.Context, binding domainsecrets.Binding, value []byte) (string, error) {
	if provider == nil || provider.backend == nil || platformsecrets.ValidateBinding(binding) != nil || domainsecrets.ValidateSecret(value) != nil {
		return "", platformsecrets.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	reference, err := platformsecrets.NewOpaqueReference()
	if err != nil {
		return "", err
	}
	copy := append([]byte(nil), value...)
	defer clear(copy)
	if err := provider.backend.Put(ctx, provider.service, reference, copy); err != nil {
		return "", err
	}
	return reference, nil
}

func (provider *Provider) Get(ctx context.Context, binding domainsecrets.Binding, reference string) ([]byte, error) {
	if provider == nil || provider.backend == nil || platformsecrets.ValidateBinding(binding) != nil || platformsecrets.ValidateReference(reference) != nil {
		return nil, platformsecrets.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	value, err := provider.backend.Get(ctx, provider.service, reference)
	if err != nil {
		return nil, err
	}
	defer clear(value)
	if domainsecrets.ValidateSecret(value) != nil {
		return nil, platformsecrets.ErrCorrupt
	}
	return append([]byte(nil), value...), nil
}

func (provider *Provider) Delete(ctx context.Context, binding domainsecrets.Binding, reference string) error {
	if provider == nil || provider.backend == nil || platformsecrets.ValidateBinding(binding) != nil || platformsecrets.ValidateReference(reference) != nil {
		return platformsecrets.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return provider.backend.Delete(ctx, provider.service, reference)
}

func (provider *Provider) Exists(ctx context.Context, binding domainsecrets.Binding, reference string) (bool, error) {
	if provider == nil || provider.backend == nil || platformsecrets.ValidateBinding(binding) != nil || platformsecrets.ValidateReference(reference) != nil {
		return false, platformsecrets.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return provider.backend.Exists(ctx, provider.service, reference)
}

var _ platformsecrets.Provider = (*Provider)(nil)
