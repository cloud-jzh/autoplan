package runtime_test

import (
	"testing"
	"time"

	domainoperation "github.com/lyming99/autoplan/backend/internal/domain/operation"
)

func TestP11RecoveryNeverRelaunchesRunningAndClaimsOnlyFreshQueuedWork(t *testing.T) {
	now := time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)
	running, err := domainoperation.DecideRecovery(domainoperation.StatusRunning, now.Add(-time.Minute).Format(time.RFC3339Nano), now, time.Minute)
	if err != nil || running.Action != domainoperation.RecoveryInterrupt || running.Code != "RECOVERY_INTERRUPTED" {
		t.Fatalf("running recovery=%#v err=%v", running, err)
	}
	fresh, err := domainoperation.DecideRecovery(domainoperation.StatusQueued, now.Add(-30*time.Second).Format(time.RFC3339Nano), now, time.Minute)
	if err != nil || fresh.Action != domainoperation.RecoveryAwaitClaim || fresh.Code != "" {
		t.Fatalf("fresh queued recovery=%#v err=%v", fresh, err)
	}
	expired, err := domainoperation.DecideRecovery(domainoperation.StatusQueued, now.Add(-time.Minute).Format(time.RFC3339Nano), now, time.Minute)
	if err != nil || expired.Action != domainoperation.RecoveryInterrupt || expired.Code != "RECOVERY_EXPIRED" {
		t.Fatalf("expired queued recovery=%#v err=%v", expired, err)
	}
	zeroLease, err := domainoperation.DecideRecovery(domainoperation.StatusQueued, now.Format(time.RFC3339Nano), now, 0)
	if err != nil || zeroLease.Action != domainoperation.RecoveryInterrupt {
		t.Fatalf("zero lease recovery=%#v err=%v", zeroLease, err)
	}
}

func TestP11RecoveryKeepsTerminalStatesTerminal(t *testing.T) {
	now := time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)
	for _, status := range []domainoperation.Status{
		domainoperation.StatusSucceeded,
		domainoperation.StatusFailed,
		domainoperation.StatusCancelled,
		domainoperation.StatusInterrupted,
	} {
		decision, err := domainoperation.DecideRecovery(status, now.Format(time.RFC3339Nano), now, time.Minute)
		if err != nil || decision.Action != domainoperation.RecoveryNone {
			t.Fatalf("terminal status %q decision=%#v err=%v", status, decision, err)
		}
	}
}
