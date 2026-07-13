package intake

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	domainintake "github.com/lyming99/autoplan/backend/internal/domain/intake"
	domainproject "github.com/lyming99/autoplan/backend/internal/domain/project"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

func TestP008GoldenSnapshotPreservesCompatibilityShapeAndSanitizesPrivateFields(t *testing.T) {
	store := newMemoryStore()
	privatePath := "C:/private/fixture.txt"
	privateDigest := "synthetic-private-digest"
	store.seedIntake(domainintake.Intake{
		ID: 10, ProjectID: 1, Type: domainintake.Requirement, Title: "Requirement", Body: "Body",
		Status: domainintake.StatusOpen, CreatedAt: applicationTestTime, UpdatedAt: applicationTestTime,
		SourceRef: &privatePath, SourceDigest: &privateDigest,
	})
	requirementID := int64(10)
	store.seedIntake(domainintake.Intake{
		ID: 11, ProjectID: 1, Type: domainintake.Feedback, RequirementID: &requirementID,
		Title: "Feedback", Body: "Feedback body", Status: domainintake.StatusCompleted,
		CreatedAt: applicationTestTime, UpdatedAt: applicationTestTime,
	})
	service := newTestService(store, nil, nil)
	snapshot, err := service.Snapshot(context.Background(), 1, domainproject.Visibility{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var contract map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &contract); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"activeProjectId", "activeProject", "requirements", "feedback", "attachments", "plans", "activeOperations"} {
		if _, exists := contract[key]; !exists {
			t.Fatalf("snapshot compatibility key %q is missing", key)
		}
	}
	if len(snapshot.Requirements) != 1 || len(snapshot.Feedback) != 1 {
		t.Fatalf("snapshot intake collections drifted: requirements=%#v feedback=%#v", snapshot.Requirements, snapshot.Feedback)
	}
	for _, forbidden := range []string{privatePath, privateDigest, "source_path", "source_hash", "stored_path", "file://"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("snapshot leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestP008GoldenIntakeFailuresPreserveStableDomainBoundaries(t *testing.T) {
	store := newMemoryStore()
	store.seedIntake(domainintake.Intake{
		ID: 8, ProjectID: 2, Type: domainintake.Requirement, Title: "Other", Body: "Body",
		Status: domainintake.StatusOpen, CreatedAt: applicationTestTime, UpdatedAt: applicationTestTime,
	})
	service := newTestService(store, nil, nil)
	requirementID := int64(8)
	_, err := service.Create(context.Background(), CreateCommand{
		ProjectID: 1, Type: domainintake.Feedback, RequirementID: &requirementID, Title: "Feedback", Body: "Body",
		Metadata: MutationMetadata{CallerScope: "golden", IdempotencyKey: "cross-project", RequestID: "request-cross-project"},
	}, domainproject.Visibility{})
	if !errors.Is(err, repository.ErrProjectMismatch) || len(store.intakes) != 1 {
		t.Fatalf("cross-project feedback contract drifted: err=%v intakes=%d", err, len(store.intakes))
	}

	store.seedIntake(domainintake.Intake{
		ID: 9, ProjectID: 1, Type: domainintake.Requirement, Title: "Duplicate", Body: "Line one\nLine two",
		Status: domainintake.StatusOpen, CreatedAt: applicationTestTime, UpdatedAt: applicationTestTime,
	})
	_, err = service.Create(context.Background(), CreateCommand{
		ProjectID: 1, Type: domainintake.Requirement, Title: " Duplicate ", Body: "Line one\r\nLine two ",
		Metadata: MutationMetadata{CallerScope: "golden", IdempotencyKey: "duplicate", RequestID: "request-duplicate"},
	}, domainproject.Visibility{})
	var duplicate DuplicateError
	if !errors.As(err, &duplicate) || !errors.Is(err, repository.ErrDuplicate) || duplicate.Existing.ID != 9 {
		t.Fatalf("normalized duplicate contract drifted: %#v", err)
	}
}
