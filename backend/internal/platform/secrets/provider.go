// Package secrets provides platform secret storage without exposing its
// locators to ordinary application responses.
package secrets

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"unicode/utf8"

	domainsecrets "github.com/lyming99/autoplan/backend/internal/domain/secrets"
)

var (
	ErrUnavailable = errors.New("secret provider is unavailable")
	ErrLocked      = errors.New("secret provider is locked")
	ErrDenied      = errors.New("secret provider access is denied")
	ErrCorrupt     = errors.New("secret provider data is invalid")
	ErrNotFound    = errors.New("secret is not found")
	ErrInvalid     = errors.New("secret provider input is invalid")
	ErrTooLarge    = errors.New("secret exceeds provider capacity")
)

type Capability struct {
	Available       bool
	ProtectedAtRest bool
	Fallback        bool
}

// Provider is deliberately small: it can only manage opaque references and
// secret bytes. Business ownership remains in secret_refs.
type Provider interface {
	Name() string
	Capabilities(context.Context) (Capability, error)
	Put(context.Context, domainsecrets.Binding, []byte) (string, error)
	Get(context.Context, domainsecrets.Binding, string) ([]byte, error)
	Delete(context.Context, domainsecrets.Binding, string) error
	Exists(context.Context, domainsecrets.Binding, string) (bool, error)
}

// EnvironmentBinding is an opaque secret lookup requested by the Process
// Runner. The environment name is metadata; the material stays in memory only
// long enough to assemble the child process environment.
type EnvironmentBinding struct {
	Name      string
	Binding   domainsecrets.Binding
	Reference string
}

// StdinBinding is one opaque secret value delivered to a child standard input.
// It intentionally has no name or plaintext field, so transports cannot turn
// it into an environment map, log field or request override.
type StdinBinding struct {
	Binding   domainsecrets.Binding
	Reference string
}

// ResolveEnvironment obtains a finite set of opaque secret references for a
// child process. It never serializes, logs, or returns provider references;
// callers receive only transient environment values and must not retain them.
func ResolveEnvironment(ctx context.Context, provider Provider, bindings []EnvironmentBinding) (map[string]string, error) {
	return ResolveEnvironmentBounded(ctx, provider, bindings, 64, 64<<10, 64<<10)
}

// ResolveEnvironmentBounded resolves secret references immediately before
// spawn. All limits are mandatory so a malformed provider cannot create an
// unbounded environment or leave partial secret material in the caller map.
func ResolveEnvironmentBounded(
	ctx context.Context,
	provider Provider,
	bindings []EnvironmentBinding,
	maximumEntries int,
	maximumValueBytes int,
	maximumTotalBytes int,
) (map[string]string, error) {
	if provider == nil || maximumEntries <= 0 || maximumValueBytes <= 0 || maximumTotalBytes <= 0 || len(bindings) > maximumEntries {
		return nil, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make(map[string]string, len(bindings))
	totalBytes := 0
	for _, item := range bindings {
		if !validEnvironmentName(item.Name) || ValidateBinding(item.Binding) != nil || ValidateReference(item.Reference) != nil {
			clearEnvironment(result)
			return nil, ErrInvalid
		}
		if _, duplicate := result[item.Name]; duplicate {
			clearEnvironment(result)
			return nil, ErrInvalid
		}
		value, err := provider.Get(ctx, item.Binding, item.Reference)
		if err != nil {
			clearEnvironment(result)
			return nil, err
		}
		if len(value) > maximumValueBytes {
			clearBytes(value)
			clearEnvironment(result)
			return nil, ErrTooLarge
		}
		if len(value) == 0 || containsNUL(value) || containsLineBreak(value) || !utf8.Valid(value) {
			clearBytes(value)
			clearEnvironment(result)
			return nil, ErrInvalid
		}
		totalBytes += len(item.Name) + len(value) + 1
		if totalBytes > maximumTotalBytes {
			clearBytes(value)
			clearEnvironment(result)
			return nil, ErrTooLarge
		}
		result[item.Name] = string(value)
		clearBytes(value)
	}
	return result, nil
}

// ResolveStdin returns an owned byte slice for direct command.Stdin use. The
// caller must clear it after the child is reaped. Newlines are allowed here so
// key material can use stdin, unlike environment values where line breaks make
// safe streaming redaction ambiguous.
func ResolveStdin(ctx context.Context, provider Provider, binding StdinBinding, maximumBytes int) ([]byte, error) {
	if provider == nil || maximumBytes <= 0 || ValidateBinding(binding.Binding) != nil || ValidateReference(binding.Reference) != nil {
		return nil, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	value, err := provider.Get(ctx, binding.Binding, binding.Reference)
	if err != nil {
		return nil, err
	}
	if len(value) > maximumBytes {
		clearBytes(value)
		return nil, ErrTooLarge
	}
	if len(value) == 0 || containsNUL(value) || !utf8.Valid(value) {
		clearBytes(value)
		return nil, ErrInvalid
	}
	return value, nil
}

func validEnvironmentName(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if index == 0 && !(character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z') {
			return false
		}
		if index > 0 && !(character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}

func containsNUL(value []byte) bool {
	for _, current := range value {
		if current == 0 {
			return true
		}
	}
	return false
}

func containsLineBreak(value []byte) bool {
	for _, current := range value {
		if current == '\r' || current == '\n' {
			return true
		}
	}
	return false
}

func clearEnvironment(values map[string]string) {
	for name := range values {
		values[name] = ""
		delete(values, name)
	}
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

// WriteResult identifies which provider accepted a newly created opaque
// reference. It is for persistence metadata only and must not cross ordinary
// API, event, or logging boundaries.
type WriteResult struct {
	Provider  string
	Reference string
}

// DetailedPutter and Resolver are optional extensions used by the application
// service to retain a fallback provider choice across later OS-keyring state
// changes without widening the base Provider contract.
type DetailedPutter interface {
	PutWithProvider(context.Context, domainsecrets.Binding, []byte) (WriteResult, error)
}

type Resolver interface {
	ProviderFor(string) (Provider, bool)
}

type PreferenceOptions struct {
	Primary       Provider
	Fallback      Provider
	AllowFallback bool
}

// Preferred uses the OS provider whenever it is available. Fallback is only
// considered for explicit unavailability, never for locked, denied, corrupt,
// or malformed platform storage.
type Preferred struct {
	primary       Provider
	fallback      Provider
	allowFallback bool
}

func NewPreferred(options PreferenceOptions) (*Preferred, error) {
	if options.Primary == nil {
		return nil, ErrInvalid
	}
	if options.AllowFallback && options.Fallback == nil {
		return nil, ErrInvalid
	}
	return &Preferred{primary: options.Primary, fallback: options.Fallback, allowFallback: options.AllowFallback}, nil
}

func (provider *Preferred) Name() string {
	if provider == nil || provider.primary == nil {
		return ""
	}
	return provider.primary.Name()
}

func (provider *Preferred) Capabilities(ctx context.Context) (Capability, error) {
	selected, err := provider.selectProvider(ctx)
	if err != nil {
		return Capability{}, err
	}
	return selected.Capabilities(ctx)
}

func (provider *Preferred) Put(ctx context.Context, binding domainsecrets.Binding, value []byte) (string, error) {
	result, err := provider.PutWithProvider(ctx, binding, value)
	return result.Reference, err
}

func (provider *Preferred) PutWithProvider(ctx context.Context, binding domainsecrets.Binding, value []byte) (WriteResult, error) {
	selected, err := provider.selectProvider(ctx)
	if err != nil {
		return WriteResult{}, err
	}
	reference, err := selected.Put(ctx, binding, value)
	if errors.Is(err, ErrUnavailable) && provider.canFallback(selected) {
		reference, err = provider.fallback.Put(ctx, binding, value)
		if err != nil {
			return WriteResult{}, err
		}
		return WriteResult{Provider: provider.fallback.Name(), Reference: reference}, nil
	}
	if err != nil {
		return WriteResult{}, err
	}
	return WriteResult{Provider: selected.Name(), Reference: reference}, nil
}

func (provider *Preferred) Get(ctx context.Context, binding domainsecrets.Binding, reference string) ([]byte, error) {
	selected, err := provider.selectProvider(ctx)
	if err != nil {
		return nil, err
	}
	value, err := selected.Get(ctx, binding, reference)
	if errors.Is(err, ErrUnavailable) && provider.canFallback(selected) {
		return provider.fallback.Get(ctx, binding, reference)
	}
	return value, err
}

func (provider *Preferred) Delete(ctx context.Context, binding domainsecrets.Binding, reference string) error {
	selected, err := provider.selectProvider(ctx)
	if err != nil {
		return err
	}
	err = selected.Delete(ctx, binding, reference)
	if errors.Is(err, ErrUnavailable) && provider.canFallback(selected) {
		return provider.fallback.Delete(ctx, binding, reference)
	}
	return err
}

func (provider *Preferred) Exists(ctx context.Context, binding domainsecrets.Binding, reference string) (bool, error) {
	selected, err := provider.selectProvider(ctx)
	if err != nil {
		return false, err
	}
	exists, err := selected.Exists(ctx, binding, reference)
	if errors.Is(err, ErrUnavailable) && provider.canFallback(selected) {
		return provider.fallback.Exists(ctx, binding, reference)
	}
	return exists, err
}

func (provider *Preferred) selectProvider(ctx context.Context) (Provider, error) {
	if provider == nil || provider.primary == nil {
		return nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	capability, err := provider.primary.Capabilities(ctx)
	if err == nil && capability.Available {
		return provider.primary, nil
	}
	if !errors.Is(err, ErrUnavailable) && err != nil {
		return nil, err
	}
	if provider.allowFallback && provider.fallback != nil {
		return provider.fallback, nil
	}
	return nil, ErrUnavailable
}

func (provider *Preferred) canFallback(selected Provider) bool {
	return provider != nil && provider.allowFallback && provider.fallback != nil && selected == provider.primary
}

func (provider *Preferred) ProviderFor(name string) (Provider, bool) {
	if provider == nil {
		return nil, false
	}
	if provider.primary != nil && provider.primary.Name() == name {
		return provider.primary, true
	}
	if provider.fallback != nil && provider.fallback.Name() == name {
		return provider.fallback, true
	}
	return nil, false
}

func NewOpaqueReference() (string, error) {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		return "", ErrUnavailable
	}
	return "sref_" + base64.RawURLEncoding.EncodeToString(buffer), nil
}

func ValidateBinding(binding domainsecrets.Binding) error {
	if !binding.Valid() {
		return ErrInvalid
	}
	return nil
}

func ValidateReference(reference string) error {
	if len(reference) < 20 || len(reference) > 200 {
		return ErrInvalid
	}
	for _, character := range reference {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') && character != '_' && character != '-' {
			return ErrInvalid
		}
	}
	return nil
}
