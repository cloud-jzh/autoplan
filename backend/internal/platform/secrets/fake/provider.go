// Package fake offers a deterministic, explicitly injected secret provider for
// tests. It is never selected by production preference wiring.
package fake

import (
	"context"
	"fmt"
	"sync"

	domainsecrets "github.com/lyming99/autoplan/backend/internal/domain/secrets"
	platformsecrets "github.com/lyming99/autoplan/backend/internal/platform/secrets"
)

type Operation string

const (
	OperationCapabilities Operation = "capabilities"
	OperationPut          Operation = "put"
	OperationGet          Operation = "get"
	OperationDelete       Operation = "delete"
	OperationExists       Operation = "exists"
)

type Options struct {
	Name       string
	Capability platformsecrets.Capability
	Available  *bool
}

type Provider struct {
	mu         sync.Mutex
	name       string
	capability platformsecrets.Capability
	records    map[string]record
	failures   map[Operation]error
	next       uint64
}

type record struct {
	binding domainsecrets.Binding
	value   []byte
}

func New(options Options) *Provider {
	name := options.Name
	if name == "" {
		name = "fake-secrets-v1"
	}
	capability := options.Capability
	if options.Available != nil {
		capability.Available = *options.Available
	} else if capability == (platformsecrets.Capability{}) {
		capability.Available = true
	}
	return &Provider{name: name, capability: capability, records: make(map[string]record), failures: make(map[Operation]error)}
}

func (provider *Provider) Name() string {
	if provider == nil {
		return ""
	}
	return provider.name
}

func (provider *Provider) SetFailure(operation Operation, err error) {
	if provider == nil {
		return
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err == nil {
		delete(provider.failures, operation)
		return
	}
	provider.failures[operation] = err
}

func (provider *Provider) Capabilities(ctx context.Context) (platformsecrets.Capability, error) {
	if provider == nil {
		return platformsecrets.Capability{}, platformsecrets.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return platformsecrets.Capability{}, err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err := provider.failure(OperationCapabilities); err != nil {
		return platformsecrets.Capability{}, err
	}
	return provider.capability, nil
}

func (provider *Provider) Put(ctx context.Context, binding domainsecrets.Binding, value []byte) (string, error) {
	if provider == nil || platformsecrets.ValidateBinding(binding) != nil || domainsecrets.ValidateSecret(value) != nil {
		return "", platformsecrets.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err := provider.failure(OperationPut); err != nil {
		return "", err
	}
	provider.next++
	reference := fmt.Sprintf("fake_%024d", provider.next)
	provider.records[reference] = record{binding: binding, value: append([]byte(nil), value...)}
	return reference, nil
}

func (provider *Provider) Get(ctx context.Context, binding domainsecrets.Binding, reference string) ([]byte, error) {
	if provider == nil || platformsecrets.ValidateBinding(binding) != nil || platformsecrets.ValidateReference(reference) != nil {
		return nil, platformsecrets.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err := provider.failure(OperationGet); err != nil {
		return nil, err
	}
	record, found := provider.records[reference]
	if !found {
		return nil, platformsecrets.ErrNotFound
	}
	if record.binding != binding {
		return nil, platformsecrets.ErrDenied
	}
	return append([]byte(nil), record.value...), nil
}

func (provider *Provider) Delete(ctx context.Context, binding domainsecrets.Binding, reference string) error {
	if provider == nil || platformsecrets.ValidateBinding(binding) != nil || platformsecrets.ValidateReference(reference) != nil {
		return platformsecrets.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err := provider.failure(OperationDelete); err != nil {
		return err
	}
	record, found := provider.records[reference]
	if !found {
		return platformsecrets.ErrNotFound
	}
	if record.binding != binding {
		return platformsecrets.ErrDenied
	}
	clear(record.value)
	delete(provider.records, reference)
	return nil
}

func (provider *Provider) Exists(ctx context.Context, binding domainsecrets.Binding, reference string) (bool, error) {
	if provider == nil || platformsecrets.ValidateBinding(binding) != nil || platformsecrets.ValidateReference(reference) != nil {
		return false, platformsecrets.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err := provider.failure(OperationExists); err != nil {
		return false, err
	}
	record, found := provider.records[reference]
	return found && record.binding == binding, nil
}

func (provider *Provider) failure(operation Operation) error {
	return provider.failures[operation]
}

var _ platformsecrets.Provider = (*Provider)(nil)
