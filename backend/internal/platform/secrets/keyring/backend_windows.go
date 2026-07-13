//go:build windows

package keyring

import (
	"context"
	"errors"
	"syscall"
	"unsafe"

	platformsecrets "github.com/lyming99/autoplan/backend/internal/platform/secrets"
)

const (
	credentialTypeGeneric    = 1
	credentialPersistLocal   = 2
	errorAccessDenied        = syscall.Errno(5)
	errorNotFound            = syscall.Errno(1168)
	errorCredentialNotFound  = syscall.Errno(1312)
	errorOperationCancelled  = syscall.Errno(1223)
	maximumCredentialBlobLen = 2500
)

var (
	advapi32         = syscall.NewLazyDLL("advapi32.dll")
	credentialRead   = advapi32.NewProc("CredReadW")
	credentialWrite  = advapi32.NewProc("CredWriteW")
	credentialDelete = advapi32.NewProc("CredDeleteW")
	credentialFree   = advapi32.NewProc("CredFree")
)

type credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWrittenLow     uint32
	LastWrittenHigh    uint32
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

type windowsBackend struct{}

func newSystemBackend() Backend { return windowsBackend{} }

func (windowsBackend) Capabilities(ctx context.Context) (platformsecrets.Capability, error) {
	if err := ctx.Err(); err != nil {
		return platformsecrets.Capability{}, err
	}
	return platformsecrets.Capability{Available: true, ProtectedAtRest: true}, nil
}

func (windowsBackend) Put(ctx context.Context, service, reference string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(value) == 0 || len(value) > maximumCredentialBlobLen {
		return platformsecrets.ErrTooLarge
	}
	target, err := syscall.UTF16PtrFromString(targetName(service, reference))
	if err != nil {
		return platformsecrets.ErrInvalid
	}
	user, err := syscall.UTF16PtrFromString(service)
	if err != nil {
		return platformsecrets.ErrInvalid
	}
	copy := append([]byte(nil), value...)
	defer clear(copy)
	entry := credential{Type: credentialTypeGeneric, TargetName: target, CredentialBlobSize: uint32(len(copy)),
		CredentialBlob: &copy[0], Persist: credentialPersistLocal, UserName: user}
	result, _, callErr := credentialWrite.Call(uintptr(unsafe.Pointer(&entry)), 0)
	if result == 0 {
		return mapWindowsError(callErr)
	}
	return ctx.Err()
}

func (windowsBackend) Get(ctx context.Context, service, reference string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := syscall.UTF16PtrFromString(targetName(service, reference))
	if err != nil {
		return nil, platformsecrets.ErrInvalid
	}
	var pointer *credential
	result, _, callErr := credentialRead.Call(uintptr(unsafe.Pointer(target)), credentialTypeGeneric, 0, uintptr(unsafe.Pointer(&pointer)))
	if result == 0 {
		return nil, mapWindowsError(callErr)
	}
	if pointer == nil {
		return nil, platformsecrets.ErrCorrupt
	}
	defer credentialFree.Call(uintptr(unsafe.Pointer(pointer)))
	if pointer.Type != credentialTypeGeneric || pointer.CredentialBlob == nil || pointer.CredentialBlobSize == 0 || pointer.CredentialBlobSize > maximumCredentialBlobLen {
		return nil, platformsecrets.ErrCorrupt
	}
	return append([]byte(nil), unsafe.Slice(pointer.CredentialBlob, pointer.CredentialBlobSize)...), nil
}

func (windowsBackend) Delete(ctx context.Context, service, reference string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := syscall.UTF16PtrFromString(targetName(service, reference))
	if err != nil {
		return platformsecrets.ErrInvalid
	}
	result, _, callErr := credentialDelete.Call(uintptr(unsafe.Pointer(target)), credentialTypeGeneric, 0)
	if result == 0 {
		return mapWindowsError(callErr)
	}
	return ctx.Err()
}

func (backend windowsBackend) Exists(ctx context.Context, service, reference string) (bool, error) {
	value, err := backend.Get(ctx, service, reference)
	if err == nil {
		clear(value)
		return true, nil
	}
	if errors.Is(err, platformsecrets.ErrNotFound) {
		return false, nil
	}
	return false, err
}

func targetName(service, reference string) string { return service + ":" + reference }

func mapWindowsError(err error) error {
	if err == nil {
		return platformsecrets.ErrUnavailable
	}
	switch {
	case errors.Is(err, errorNotFound), errors.Is(err, errorCredentialNotFound):
		return platformsecrets.ErrNotFound
	case errors.Is(err, errorAccessDenied):
		return platformsecrets.ErrDenied
	case errors.Is(err, errorOperationCancelled):
		return platformsecrets.ErrLocked
	default:
		return platformsecrets.ErrUnavailable
	}
}
