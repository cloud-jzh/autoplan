package events

import (
	"encoding/json"
	"strings"
	"testing"

	domainevent "github.com/lyming99/autoplan/backend/internal/domain/event"
)

func TestAuditSnapshotSanitizesHistoricalMetadataAndPreservesPublicFields(t *testing.T) {
	metadata := `{"target_type":"plan","path":"/private/fixture","nested":{"session":"opaque","kept":true}}`
	safe := domainevent.SanitizeMetaJSON(&metadata)
	if safe == nil || strings.Contains(*safe, "private") || strings.Contains(*safe, "session") {
		t.Fatalf("historical metadata was not redacted: %v", safe)
	}
	snapshot, err := EventSnapshot(domainevent.Event{
		ID: 4, ProjectID: 2, Type: "plan.accepted", Message: "accepted", MetaJSON: safe,
		CreatedAt: "2026-07-11T00:00:00.000Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(snapshot["meta"], &meta); err != nil || meta["target_type"] != "plan" {
		t.Fatalf("audit meta=%#v error=%v", meta, err)
	}
	if _, exists := meta["path"]; exists {
		t.Fatalf("path escaped audit snapshot: %#v", meta)
	}
}

func TestAuditPendingEventRejectsSensitiveMetadataBeforePersistence(t *testing.T) {
	metadata := `{"credential":"not-public"}`
	err := domainevent.ValidatePending(domainevent.PendingEvent{
		EventID: "event-audit-fixture", StreamKey: "project:2", Sequence: 1, Type: "plan.accepted",
		RequestID: "request-audit-fixture", ProjectID: 2, Message: "accepted", MetaJSON: &metadata,
		OccurredAt: "2026-07-11T00:00:00.000Z", CreatedAt: "2026-07-11T00:00:00.000Z",
	})
	if err == nil {
		t.Fatal("sensitive event metadata was accepted")
	}
}
