package terminal

import (
	"context"
	"testing"
	"time"
)

func TestSupervisorClosesOnlyTargetProjectAndReleasesLeases(t *testing.T) {
	supervisor, err := NewSupervisor(terminalContractLimits())
	if err != nil {
		t.Fatal(err)
	}
	firstPTY, secondPTY := newContractPTY(), newContractPTY()
	firstLease, err := supervisor.Acquire(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	secondLease, err := supervisor.Acquire(context.Background(), 8)
	if err != nil {
		t.Fatal(err)
	}
	first := newSession(firstPTY, terminalContractLimits())
	second := newSession(secondPTY, terminalContractLimits())
	firstLease.Bind(first)
	secondLease.Bind(second)
	first.start(firstLease.Release)
	second.start(secondLease.Release)

	if closed := supervisor.CloseProject(7); closed != 1 {
		t.Fatalf("closed sessions = %d, want 1", closed)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := first.Wait(ctx); err != nil {
		t.Fatalf("target project wait = %v", err)
	}
	secondPTY.mu.Lock()
	secondKilled := secondPTY.kills
	secondPTY.mu.Unlock()
	if secondKilled != 0 {
		t.Fatal("closing project 7 terminated project 8")
	}
	supervisor.Shutdown()
	if _, err := second.Wait(ctx); err != nil {
		t.Fatalf("shutdown wait = %v", err)
	}
	if active, projects := supervisor.Active(); active != 0 || len(projects) != 0 {
		t.Fatalf("supervisor leaked leases active=%d projects=%#v", active, projects)
	}
}
