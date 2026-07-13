// Package runtime owns process-lifetime coordination that is shared by the
// Script and Executor application services. Ownership is deliberately
// in-memory: it proves only what this Go process started itself and can never
// be reconstructed from a PID, a client-provided identifier, or a stale
// database record after a restart.
package runtime

import (
	"errors"
	"sync"
	"time"
)

var (
	ErrInvalidProcessOwner = errors.New("runtime process owner is invalid")
	ErrProcessOwned        = errors.New("runtime process is already owned")
)

type ProcessResourceKind string

const (
	ProcessResourceScript   ProcessResourceKind = "script"
	ProcessResourceExecutor ProcessResourceKind = "executor"
)

type ProcessResource struct {
	ProjectID int64
	Kind      ProcessResourceKind
	ID        int64
}

func (value ProcessResource) Valid() bool {
	return value.ProjectID > 0 && value.ID > 0 &&
		(value.Kind == ProcessResourceScript || value.Kind == ProcessResourceExecutor)
}

// ProcessOwner records a live Go-owned process tree. It deliberately has no
// pid or command field: neither is safe to persist, expose, or use as an
// adoption token after the owning runtime exits.
type ProcessOwner struct {
	Resource     ProcessResource
	OperationID  string
	RegisteredAt time.Time
}

func (value ProcessOwner) Valid() bool {
	return value.Resource.Valid() && validOwnerID(value.OperationID) &&
		!value.RegisteredAt.IsZero() && value.RegisteredAt.Location() == time.UTC
}

// OwnershipRegistry gives one live Go runtime exclusive ownership of a
// project/resource pair. It is intentionally not serializable; restart
// recovery clears it and marks stale durable state interrupted instead of
// trying to adopt an unknown process tree.
type OwnershipRegistry struct {
	mu          sync.Mutex
	byResource  map[ProcessResource]ProcessOwner
	byOperation map[string]ProcessResource
}

func NewOwnershipRegistry() *OwnershipRegistry {
	return &OwnershipRegistry{
		byResource:  make(map[ProcessResource]ProcessOwner),
		byOperation: make(map[string]ProcessResource),
	}
}

func (registry *OwnershipRegistry) Claim(owner ProcessOwner) error {
	if registry == nil || !owner.Valid() {
		return ErrInvalidProcessOwner
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if existing, found := registry.byResource[owner.Resource]; found && existing.OperationID != owner.OperationID {
		return ErrProcessOwned
	}
	if resource, found := registry.byOperation[owner.OperationID]; found && resource != owner.Resource {
		return ErrProcessOwned
	}
	registry.byResource[owner.Resource] = owner
	registry.byOperation[owner.OperationID] = owner.Resource
	return nil
}

// Release is idempotent for an already-released operation. A mismatched
// resource is rejected so a caller cannot clear another operation's owner.
func (registry *OwnershipRegistry) Release(resource ProcessResource, operationID string) error {
	if registry == nil || !resource.Valid() || !validOwnerID(operationID) {
		return ErrInvalidProcessOwner
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	existing, found := registry.byResource[resource]
	if !found {
		return nil
	}
	if existing.OperationID != operationID {
		return ErrProcessOwned
	}
	delete(registry.byResource, resource)
	delete(registry.byOperation, operationID)
	return nil
}

func (registry *OwnershipRegistry) Lookup(resource ProcessResource) (ProcessOwner, bool) {
	if registry == nil || !resource.Valid() {
		return ProcessOwner{}, false
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	owner, found := registry.byResource[resource]
	return owner, found
}

// Reset discards every live registration. It is used only at startup, where
// retaining a process owner from an earlier Go process would be an unsafe
// implicit adoption of an unknown OS process.
func (registry *OwnershipRegistry) Reset() {
	if registry == nil {
		return
	}
	registry.mu.Lock()
	registry.byResource = make(map[ProcessResource]ProcessOwner)
	registry.byOperation = make(map[string]ProcessResource)
	registry.mu.Unlock()
}

func validOwnerID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if !(character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' || (index > 0 && (character == '.' || character == '_' || character == ':' || character == '-'))) {
			return false
		}
	}
	return true
}
