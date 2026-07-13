// Package secrets coordinates secret_refs metadata with platform secret
// providers. It never returns secret bytes, opaque references, or providers in
// ordinary results.
package secrets

import (
	"context"
	"errors"
	"time"

	domainsecrets "github.com/lyming99/autoplan/backend/internal/domain/secrets"
	platformsecrets "github.com/lyming99/autoplan/backend/internal/platform/secrets"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

var (
	ErrUnavailable   = errors.New("secret application service unavailable")
	ErrInvalid       = errors.New("secret command is invalid")
	ErrConflict      = errors.New("secret reference conflicts")
	ErrNotFound      = errors.New("secret reference is not found")
	ErrProvider      = errors.New("secret provider operation failed")
	ErrCompensation  = errors.New("secret provider compensation is required")
	ErrSecretMissing = errors.New("secret value is unavailable")
)

type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type Dependencies struct {
	Writer   repository.Transactional
	Provider platformsecrets.Provider
	Clock    Clock
}

type Service struct {
	writer   repository.Transactional
	provider platformsecrets.Provider
	clock    Clock
}

type CreateRequest struct {
	Kind  domainsecrets.Kind
	Owner domainsecrets.Owner
	Value []byte
}

type ReplaceRequest struct {
	Kind            domainsecrets.Kind
	Owner           domainsecrets.Owner
	ExpectedVersion int64
	Value           []byte
}

type DeleteRequest struct {
	Kind            domainsecrets.Kind
	Owner           domainsecrets.Owner
	ExpectedVersion int64
}

// Status is safe to expose to ordinary callers. It deliberately omits the
// opaque reference, provider, and all secret bytes.
type Status struct {
	Kind     domainsecrets.Kind `json:"kind"`
	HasValue bool               `json:"has_value"`
	Version  int64              `json:"version"`
}

// EnvironmentBindingRequest is an internal runtime request. It deliberately
// names the destination environment variable without accepting a plaintext
// value, provider name, or opaque reference from an API caller.
type EnvironmentBindingRequest struct {
	Name  string
	Kind  domainsecrets.Kind
	Owner domainsecrets.Owner
}

type secretRefTransaction interface {
	GetSecretRef(context.Context, domainsecrets.Kind, domainsecrets.Owner) (domainsecrets.Ref, bool, error)
	ListRetiredSecretRefs(context.Context, domainsecrets.Owner) ([]domainsecrets.Ref, error)
	CreateSecretRef(context.Context, domainsecrets.Create) (domainsecrets.Ref, error)
	ReplaceSecretRef(context.Context, domainsecrets.Replace) (domainsecrets.Ref, error)
	RetireSecretRef(context.Context, domainsecrets.Retire) (domainsecrets.Ref, error)
	PurgeSecretRef(context.Context, domainsecrets.Delete) error
	PurgeRetiredSecretRef(context.Context, domainsecrets.Ref) error
}

func NewService(dependencies Dependencies) *Service {
	clock := dependencies.Clock
	if clock == nil {
		clock = systemClock{}
	}
	return &Service{writer: dependencies.Writer, provider: dependencies.Provider, clock: clock}
}

func (service *Service) Create(ctx context.Context, request CreateRequest) (Status, error) {
	if err := service.ready(ctx); err != nil {
		return Status{}, err
	}
	if domainsecrets.ValidateScope(request.Kind, request.Owner) != nil || domainsecrets.ValidateSecret(request.Value) != nil {
		return Status{}, ErrInvalid
	}
	if err := service.cleanupPending(ctx, request.Owner); err != nil {
		return Status{}, ErrCompensation
	}
	current, found, err := service.getRef(ctx, request.Kind, request.Owner)
	if err != nil {
		return Status{}, mapRepositoryError(err)
	}
	if found && current.HasValue {
		return Status{}, ErrConflict
	}
	if found {
		if err := service.cleanupActiveTombstone(ctx, current); err != nil {
			return Status{}, ErrCompensation
		}
	}
	binding := domainsecrets.Binding{Kind: request.Kind, Owner: request.Owner, Version: 1}
	stored, err := service.put(ctx, binding, request.Value)
	if err != nil {
		return Status{}, mapProviderError(err)
	}
	created, err := service.createRef(ctx, domainsecrets.Create{
		Binding: binding, Provider: stored.Provider, Reference: stored.Reference, CreatedAt: service.timestamp(),
	})
	if err != nil {
		if cleanupErr := service.compensateDelete(stored.Provider, binding, stored.Reference); cleanupErr != nil {
			return Status{}, ErrCompensation
		}
		return Status{}, mapRepositoryError(err)
	}
	return status(created), nil
}

func (service *Service) Replace(ctx context.Context, request ReplaceRequest) (Status, error) {
	if err := service.ready(ctx); err != nil {
		return Status{}, err
	}
	if domainsecrets.ValidateScope(request.Kind, request.Owner) != nil || request.ExpectedVersion <= 0 ||
		domainsecrets.ValidateSecret(request.Value) != nil {
		return Status{}, ErrInvalid
	}
	if err := service.cleanupPending(ctx, request.Owner); err != nil {
		return Status{}, ErrCompensation
	}
	current, found, err := service.getRef(ctx, request.Kind, request.Owner)
	if err != nil {
		return Status{}, mapRepositoryError(err)
	}
	if !found {
		return Status{}, ErrNotFound
	}
	if !current.HasValue || current.Version != request.ExpectedVersion {
		return Status{}, ErrConflict
	}
	nextBinding := current.Binding
	nextBinding.Version = current.Version + 1
	stored, err := service.put(ctx, nextBinding, request.Value)
	if err != nil {
		return Status{}, mapProviderError(err)
	}
	updated, err := service.replaceRef(ctx, domainsecrets.Replace{
		Binding: nextBinding, ExpectedVersion: request.ExpectedVersion, Provider: stored.Provider,
		Reference: stored.Reference, UpdatedAt: service.timestamp(current.UpdatedAt),
	})
	if err != nil {
		if cleanupErr := service.compensateDelete(stored.Provider, nextBinding, stored.Reference); cleanupErr != nil {
			return Status{}, ErrCompensation
		}
		return Status{}, mapRepositoryError(err)
	}
	if err := service.cleanupRetirement(ctx, current); err != nil {
		// The active row has already switched. Returning a compensating error is
		// safer than pretending that retired provider material was deleted.
		return status(updated), ErrCompensation
	}
	return status(updated), nil
}

func (service *Service) Delete(ctx context.Context, request DeleteRequest) error {
	if err := service.ready(ctx); err != nil {
		return err
	}
	if domainsecrets.ValidateScope(request.Kind, request.Owner) != nil || request.ExpectedVersion <= 0 {
		return ErrInvalid
	}
	if err := service.cleanupPending(ctx, request.Owner); err != nil {
		return ErrCompensation
	}
	current, found, err := service.getRef(ctx, request.Kind, request.Owner)
	if err != nil {
		return mapRepositoryError(err)
	}
	if !found {
		return ErrNotFound
	}
	if current.Version != request.ExpectedVersion {
		return ErrConflict
	}
	retired, err := service.retireRef(ctx, domainsecrets.Retire{
		Binding: current.Binding, ExpectedVersion: request.ExpectedVersion, UpdatedAt: service.timestamp(current.UpdatedAt),
	})
	if err != nil {
		return mapRepositoryError(err)
	}
	if err := service.cleanupActiveTombstone(ctx, retired); err != nil {
		return ErrCompensation
	}
	return nil
}

func (service *Service) Status(ctx context.Context, kind domainsecrets.Kind, owner domainsecrets.Owner) (Status, error) {
	if err := service.ready(ctx); err != nil {
		return Status{}, err
	}
	if domainsecrets.ValidateScope(kind, owner) != nil {
		return Status{}, ErrInvalid
	}
	value, found, err := service.getRef(ctx, kind, owner)
	if err != nil {
		return Status{}, mapRepositoryError(err)
	}
	if !found {
		return Status{}, ErrNotFound
	}
	return status(value), nil
}

// Reconcile retries only durable tombstone cleanup. It does not read or return
// plaintext, references, provider names, or other locator details.
func (service *Service) Reconcile(ctx context.Context, owner domainsecrets.Owner) error {
	if err := service.ready(ctx); err != nil {
		return err
	}
	if domainsecrets.ValidateOwner(owner) != nil {
		return ErrInvalid
	}
	if err := service.cleanupPending(ctx, owner); err != nil {
		return ErrCompensation
	}
	return nil
}

// Exists verifies platform availability without reading plaintext. It is kept
// for future authorized runtime consumers and is not an ordinary DTO path.
func (service *Service) Exists(ctx context.Context, kind domainsecrets.Kind, owner domainsecrets.Owner) (bool, error) {
	if err := service.ready(ctx); err != nil {
		return false, err
	}
	value, found, err := service.getRef(ctx, kind, owner)
	if err != nil {
		return false, mapRepositoryError(err)
	}
	if !found {
		return false, ErrNotFound
	}
	if !value.HasValue {
		return false, ErrSecretMissing
	}
	provider, ok := service.providerFor(value.Provider)
	if !ok {
		return false, ErrProvider
	}
	exists, err := provider.Exists(ctx, value.Binding, value.Reference)
	if err != nil {
		return false, mapProviderError(err)
	}
	if !exists {
		return false, ErrSecretMissing
	}
	return true, nil
}

// ResolveEnvironmentBinding returns an opaque platform binding for an
// authorized runtime consumer. The secret is never read by this service and
// must be resolved only by the Process Runner immediately before spawn.
func (service *Service) ResolveEnvironmentBinding(
	ctx context.Context,
	request EnvironmentBindingRequest,
) (platformsecrets.EnvironmentBinding, error) {
	if err := service.ready(ctx); err != nil {
		return platformsecrets.EnvironmentBinding{}, err
	}
	if !validEnvironmentName(request.Name) || domainsecrets.ValidateScope(request.Kind, request.Owner) != nil {
		return platformsecrets.EnvironmentBinding{}, ErrInvalid
	}
	value, found, err := service.getRef(ctx, request.Kind, request.Owner)
	if err != nil {
		return platformsecrets.EnvironmentBinding{}, mapRepositoryError(err)
	}
	if !found {
		return platformsecrets.EnvironmentBinding{}, ErrNotFound
	}
	if !value.HasValue {
		return platformsecrets.EnvironmentBinding{}, ErrSecretMissing
	}
	provider, ok := service.providerFor(value.Provider)
	if !ok {
		return platformsecrets.EnvironmentBinding{}, ErrProvider
	}
	exists, err := provider.Exists(ctx, value.Binding, value.Reference)
	if err != nil {
		return platformsecrets.EnvironmentBinding{}, mapProviderError(err)
	}
	if !exists {
		return platformsecrets.EnvironmentBinding{}, ErrSecretMissing
	}
	return platformsecrets.EnvironmentBinding{
		Name: request.Name, Binding: value.Binding, Reference: value.Reference,
	}, nil
}

func (service *Service) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == nil || service.writer == nil || service.provider == nil || service.clock == nil {
		return ErrUnavailable
	}
	if err := service.writer.Check(ctx); err != nil {
		return err
	}
	capability, err := service.provider.Capabilities(ctx)
	if err != nil || !capability.Available {
		return ErrProvider
	}
	return nil
}

func (service *Service) timestamp(after ...string) string {
	next := service.clock.Now().UTC().Truncate(time.Millisecond)
	for _, value := range after {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err == nil && !next.After(parsed) {
			next = parsed.Add(time.Millisecond)
		}
	}
	return next.UTC().Format("2006-01-02T15:04:05.000Z")
}

func (service *Service) put(ctx context.Context, binding domainsecrets.Binding, value []byte) (platformsecrets.WriteResult, error) {
	if detailed, ok := service.provider.(platformsecrets.DetailedPutter); ok {
		return detailed.PutWithProvider(ctx, binding, value)
	}
	reference, err := service.provider.Put(ctx, binding, value)
	if err != nil {
		return platformsecrets.WriteResult{}, err
	}
	return platformsecrets.WriteResult{Provider: service.provider.Name(), Reference: reference}, nil
}

func (service *Service) providerFor(name string) (platformsecrets.Provider, bool) {
	if resolver, ok := service.provider.(platformsecrets.Resolver); ok {
		return resolver.ProviderFor(name)
	}
	if service.provider.Name() == name {
		return service.provider, true
	}
	return nil, false
}

func (service *Service) deleteStored(ctx context.Context, value domainsecrets.Ref) error {
	provider, ok := service.providerFor(value.Provider)
	if !ok {
		return ErrProvider
	}
	err := provider.Delete(ctx, value.Binding, value.Reference)
	if errors.Is(err, platformsecrets.ErrNotFound) {
		return nil
	}
	return err
}

func (service *Service) cleanupActiveTombstone(ctx context.Context, value domainsecrets.Ref) error {
	if value.HasValue {
		return ErrProvider
	}
	if err := service.deleteStored(ctx, value); err != nil {
		return err
	}
	return service.purgeRef(ctx, domainsecrets.Delete{Binding: value.Binding, ExpectedVersion: value.Version})
}

func (service *Service) cleanupRetirement(ctx context.Context, current domainsecrets.Ref) error {
	if err := service.deleteStored(ctx, current); err != nil {
		return err
	}
	return service.purgeRetirement(ctx, current)
}

func (service *Service) cleanupPending(ctx context.Context, owner domainsecrets.Owner) error {
	retirements, err := service.listRetirements(ctx, owner)
	if err != nil {
		return err
	}
	for _, retirement := range retirements {
		binding, valid := domainsecrets.ParseRetirementBinding(retirement.Binding.Kind, owner)
		if !valid {
			return ErrProvider
		}
		active := retirement
		active.Binding = binding
		if err := service.deleteStored(ctx, active); err != nil {
			return err
		}
		if err := service.purgeRef(ctx, domainsecrets.Delete{Binding: retirement.Binding, ExpectedVersion: retirement.Version}); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) compensateDelete(providerName string, binding domainsecrets.Binding, reference string) error {
	provider, ok := service.providerFor(providerName)
	if !ok {
		return ErrProvider
	}
	cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := provider.Delete(cleanupContext, binding, reference)
	if errors.Is(err, platformsecrets.ErrNotFound) {
		return nil
	}
	return err
}

func (service *Service) getRef(ctx context.Context, kind domainsecrets.Kind, owner domainsecrets.Owner) (domainsecrets.Ref, bool, error) {
	var result domainsecrets.Ref
	var found bool
	err := service.writer.Transact(ctx, func(transaction repository.WriteTransaction) error {
		store, ok := transaction.(secretRefTransaction)
		if !ok {
			return repository.ErrTransaction
		}
		var err error
		result, found, err = store.GetSecretRef(ctx, kind, owner)
		return err
	})
	return result, found, err
}

func (service *Service) listRetirements(ctx context.Context, owner domainsecrets.Owner) ([]domainsecrets.Ref, error) {
	var result []domainsecrets.Ref
	err := service.writer.Transact(ctx, func(transaction repository.WriteTransaction) error {
		store, ok := transaction.(secretRefTransaction)
		if !ok {
			return repository.ErrTransaction
		}
		var err error
		result, err = store.ListRetiredSecretRefs(ctx, owner)
		return err
	})
	return result, err
}

func (service *Service) createRef(ctx context.Context, input domainsecrets.Create) (domainsecrets.Ref, error) {
	var result domainsecrets.Ref
	err := service.writer.Transact(ctx, func(transaction repository.WriteTransaction) error {
		store, ok := transaction.(secretRefTransaction)
		if !ok {
			return repository.ErrTransaction
		}
		var err error
		result, err = store.CreateSecretRef(ctx, input)
		return err
	})
	return result, err
}

func (service *Service) replaceRef(ctx context.Context, input domainsecrets.Replace) (domainsecrets.Ref, error) {
	var result domainsecrets.Ref
	err := service.writer.Transact(ctx, func(transaction repository.WriteTransaction) error {
		store, ok := transaction.(secretRefTransaction)
		if !ok {
			return repository.ErrTransaction
		}
		var err error
		result, err = store.ReplaceSecretRef(ctx, input)
		return err
	})
	return result, err
}

func (service *Service) retireRef(ctx context.Context, input domainsecrets.Retire) (domainsecrets.Ref, error) {
	var result domainsecrets.Ref
	err := service.writer.Transact(ctx, func(transaction repository.WriteTransaction) error {
		store, ok := transaction.(secretRefTransaction)
		if !ok {
			return repository.ErrTransaction
		}
		var err error
		result, err = store.RetireSecretRef(ctx, input)
		return err
	})
	return result, err
}

func (service *Service) purgeRef(ctx context.Context, input domainsecrets.Delete) error {
	return service.writer.Transact(ctx, func(transaction repository.WriteTransaction) error {
		store, ok := transaction.(secretRefTransaction)
		if !ok {
			return repository.ErrTransaction
		}
		return store.PurgeSecretRef(ctx, input)
	})
}

func (service *Service) purgeRetirement(ctx context.Context, current domainsecrets.Ref) error {
	return service.writer.Transact(ctx, func(transaction repository.WriteTransaction) error {
		store, ok := transaction.(secretRefTransaction)
		if !ok {
			return repository.ErrTransaction
		}
		return store.PurgeRetiredSecretRef(ctx, current)
	})
}

func status(value domainsecrets.Ref) Status {
	return Status{Kind: value.Binding.Kind, HasValue: value.HasValue, Version: value.Version}
}

func mapRepositoryError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, repository.ErrNotFound) {
		return ErrNotFound
	}
	if errors.Is(err, repository.ErrVersionConflict) || errors.Is(err, repository.ErrDuplicate) {
		return ErrConflict
	}
	if errors.Is(err, repository.ErrInvalidAutomation) || errors.Is(err, domainsecrets.ErrInvalidSecretRef) ||
		errors.Is(err, domainsecrets.ErrInvalidSecret) {
		return ErrInvalid
	}
	return err
}

func mapProviderError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrProvider
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
