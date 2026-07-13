package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

type p12RecoveryStore struct {
	input  ProcessRecoveryInput
	calls  int
	result ProcessRecoveryResult
}

func (store *p12RecoveryStore) RecoverProcessOwnership(_ context.Context, input ProcessRecoveryInput) (ProcessRecoveryResult, error) {
	store.calls++
	store.input = input
	return store.result, nil
}

func TestP12RecoveryClearsOnlyLocalOwnershipAndNeverRelaunches(t *testing.T) {
	registry := NewOwnershipRegistry()
	resource := ProcessResource{ProjectID: 7, Kind: ProcessResourceExecutor, ID: 21}
	if err := registry.Claim(ProcessOwner{Resource: resource, OperationID: "operation-p12", RegisteredAt: time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	store := &p12RecoveryStore{result: ProcessRecoveryResult{InterruptedOperations: 1, InterruptedExecutors: 1}}
	input := DefaultProcessRecoveryInput(time.Date(2026, 7, 12, 9, 1, 0, 0, time.UTC))
	result, err := RecoverProcessOwnership(context.Background(), registry, store, input)
	if err != nil || result.InterruptedOperations != 1 || store.calls != 1 || store.input != input {
		t.Fatalf("recovery result=%#v store=%#v err=%v", result, store, err)
	}
	if _, found := registry.Lookup(resource); found {
		t.Fatal("recovery retained a stale local process owner")
	}
}

func TestP12RecoveryRejectsInvalidInputBeforeMutatingOwnership(t *testing.T) {
	registry := NewOwnershipRegistry()
	resource := ProcessResource{ProjectID: 7, Kind: ProcessResourceScript, ID: 11}
	if err := registry.Claim(ProcessOwner{Resource: resource, OperationID: "operation-p12", RegisteredAt: time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	store := &p12RecoveryStore{}
	input := DefaultProcessRecoveryInput(time.Now().UTC())
	input.MaximumRecords = MaximumProcessRecoveryRecords + 1
	if _, err := RecoverProcessOwnership(context.Background(), registry, store, input); !errors.Is(err, ErrProcessRecovery) || store.calls != 0 {
		t.Fatalf("invalid recovery error=%v calls=%d", err, store.calls)
	}
	if _, found := registry.Lookup(resource); !found {
		t.Fatal("invalid recovery discarded a live owner")
	}
}
