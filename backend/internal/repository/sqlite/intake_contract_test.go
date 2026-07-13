package sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
	domainintake "github.com/lyming99/autoplan/backend/internal/domain/intake"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

func TestP008RepositoryIntakeRejectsCrossProjectPlanBeforeLinkWrites(t *testing.T) {
	backend := &scriptBackend{steps: []scriptStep{
		queryStep("FROM requirements WHERE project_id", intakeTestColumns(),
			intakeTestValues(domainintake.Requirement, 3, 1, nil, "Requirement", "Body", "open", nil)),
		queryStep("SELECT project_id FROM plans", []string{"project_id"}, []driver.Value{int64(2)}),
	}}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()
	err := writer.TransactIntake(context.Background(), func(transaction repository.IntakeWriteTransaction) error {
		_, err := transaction.ReplacePlanLinks(context.Background(), 1, domainintake.Requirement, 3,
			[]domainintake.PlanLinkInput{{PlanID: 8, PhaseIndex: 1, PhaseTitle: "Foreign plan"}}, intakeTestTime)
		return err
	})
	if !errors.Is(err, repository.ErrProjectMismatch) {
		t.Fatalf("cross-project plan error = %v", err)
	}
	backend.assertFinished(t, 0, 1)
}

func TestP008RepositoryAttachmentOwnerBoundaryRejectsBeforeInsert(t *testing.T) {
	backend := &scriptBackend{steps: []scriptStep{
		queryStep("SELECT project_id FROM requirements", []string{"project_id"}, []driver.Value{int64(2)}),
	}}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()
	err := writer.TransactIntake(context.Background(), func(transaction repository.IntakeWriteTransaction) error {
		attachment, ok := transaction.(interface {
			CreateAttachment(context.Context, domainfiles.Attachment) (domainfiles.Attachment, error)
		})
		if !ok {
			return errors.New("attachment transaction contract missing")
		}
		_, err := attachment.CreateAttachment(context.Background(), domainfiles.Attachment{
			ProjectID: 1, OwnerType: domainfiles.OwnerRequirement, OwnerID: 4,
			DisplayName: "fixture.txt", StoredKey: "ready/4.blob", MIMEType: "text/plain", Size: 5,
			SHA256: strings.Repeat("a", 64), CreatedAt: intakeTestTime,
		})
		return err
	})
	if !errors.Is(err, repository.ErrProjectMismatch) {
		t.Fatalf("cross-project attachment owner error = %v", err)
	}
	backend.assertFinished(t, 0, 1)
}

func TestP008RepositoryOperationPayloadIsStrictAndDoesNotAcceptUnknownFields(t *testing.T) {
	valid := `{"ID":"upload-1","ProjectID":1,"Kind":"upload","State":"staged","StoredKey":"ready/1.blob","StageKey":"staged/upload-1.blob","Size":5,"SHA256":"` + strings.Repeat("b", 64) + `","MIMEType":"text/plain","CreatedAt":"2026-07-11T00:00:00.000Z","UpdatedAt":"2026-07-11T00:00:00.000Z"}`
	if _, err := decodeAttachmentOperation(valid); err != nil {
		t.Fatalf("valid attachment operation rejected: %v", err)
	}
	if _, err := decodeAttachmentOperation(strings.TrimSuffix(valid, "}") + `,"stored_path":"C:/private"}`); !errors.Is(err, repository.ErrTransaction) {
		t.Fatalf("unknown/private operation field error = %v", err)
	}
}
