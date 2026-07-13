package attachments

import (
	"context"
	"errors"
	"testing"
	"time"

	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
)

func p008Operation(kind domainfiles.OperationKind, state domainfiles.OperationState, id string, attachmentID *int64) domainfiles.AttachmentOperation {
	return domainfiles.AttachmentOperation{
		ID: id, ProjectID: 1, Kind: kind, State: state, AttachmentID: attachmentID,
		StoredKey: "ready/1.blob", StageKey: "staged/" + id + ".blob", QuarantineKey: "quarantine/" + id + ".blob",
		Size: 7, SHA256: p008Digest([]byte("fixture")), MIMEType: "text/plain", CreatedAt: p008AttachmentTime, UpdatedAt: p008AttachmentTime,
	}
}

func TestP008RecoveryCompletesInterruptedUploadAndDeleteIdempotently(t *testing.T) {
	t.Run("staged upload promotes and verifies bytes", func(t *testing.T) {
		transaction := newP008AttachmentTransaction()
		store := newP008AttachmentStore()
		attachmentID := int64(1)
		operation := p008Operation(domainfiles.OperationUpload, domainfiles.StateStaged, "recover-upload", &attachmentID)
		store.files[operation.StageKey] = p008StoredFile{bytes: []byte("fixture"), modifiedAt: time.Unix(0, 0).UTC()}
		transaction.operations[operation.ID] = operation
		service := newP008AttachmentService(transaction, store)

		report, err := service.Recover(context.Background())
		updated := transaction.operations[operation.ID]
		if err != nil || report.Recovered != 1 || report.Complete != 1 || report.Pending != 0 || updated.State != domainfiles.StateReady {
			t.Fatalf("upload recovery drifted: report=%#v err=%v operation=%#v", report, err, updated)
		}
		if _, found := store.files[operation.StoredKey]; !found {
			t.Fatal("upload recovery did not promote staged bytes")
		}
		second, err := service.Recover(context.Background())
		if err != nil || second.Recovered != 0 || second.Complete != 0 || second.Pending != 0 {
			t.Fatalf("upload recovery replay drifted: report=%#v err=%v", second, err)
		}
	})

	t.Run("deleting attachment quarantines then removes bytes", func(t *testing.T) {
		transaction := newP008AttachmentTransaction()
		store := newP008AttachmentStore()
		attachmentID := int64(1)
		operation := p008Operation(domainfiles.OperationDelete, domainfiles.StateDeleting, "recover-delete", &attachmentID)
		store.files[operation.StoredKey] = p008StoredFile{bytes: []byte("fixture"), modifiedAt: time.Unix(0, 0).UTC()}
		transaction.operations[operation.ID] = operation
		service := newP008AttachmentService(transaction, store)

		report, err := service.Recover(context.Background())
		updated := transaction.operations[operation.ID]
		if err != nil || report.Recovered != 1 || report.Complete != 1 || updated.State != domainfiles.StateComplete || len(store.files) != 0 {
			t.Fatalf("delete recovery drifted: report=%#v err=%v operation=%#v files=%d", report, err, updated, len(store.files))
		}
	})
}

func TestP008RecoveryReportsUnrecoverableFaultInsteadOfSilentlyOpening(t *testing.T) {
	transaction := newP008AttachmentTransaction()
	store := newP008AttachmentStore()
	store.removeErr = errors.New("synthetic unlink fault")
	attachmentID := int64(1)
	operation := p008Operation(domainfiles.OperationDelete, domainfiles.StateQuarantined, "recover-unlink", &attachmentID)
	store.files[operation.QuarantineKey] = p008StoredFile{bytes: []byte("fixture"), modifiedAt: time.Unix(0, 0).UTC()}
	transaction.operations[operation.ID] = operation
	service := newP008AttachmentService(transaction, store)

	report, err := service.Recover(context.Background())
	updated := transaction.operations[operation.ID]
	if !errors.Is(err, domainfiles.ErrAttachmentRecovery) || report.Pending != 1 || report.Failed != 1 || updated.State != domainfiles.StateFailed {
		t.Fatalf("unrecoverable fault must remain reported: report=%#v err=%v operation=%#v", report, err, updated)
	}
}

func TestP008AuditAndRepairAreDeterministicAndNeverDeleteReadySentinels(t *testing.T) {
	transaction := newP008AttachmentTransaction()
	store := newP008AttachmentStore()
	old := time.Date(2026, 7, 9, 9, 0, 0, 0, time.UTC)
	store.files["staged/expired.blob"] = p008StoredFile{bytes: []byte("staged"), modifiedAt: old}
	store.files["quarantine/expired.blob"] = p008StoredFile{bytes: []byte("quarantine"), modifiedAt: old}
	store.files["ready/sentinel.blob"] = p008StoredFile{bytes: []byte("sentinel"), modifiedAt: old}
	service := newP008AttachmentService(transaction, store)

	report, err := service.Audit(context.Background())
	if err != nil || len(report.Issues) != 3 || report.Issues[0].Code != "quarantine_expired" ||
		report.Issues[1].Code != "staged_expired" || report.Issues[2].Code != "storage_orphan" {
		t.Fatalf("audit order/content drifted: report=%#v err=%v", report, err)
	}
	repair, err := service.Repair(context.Background())
	if err != nil || repair.Repaired != 2 || len(repair.Remaining.Issues) != 1 || repair.Remaining.Issues[0].Code != "storage_orphan" {
		t.Fatalf("repair result drifted: report=%#v err=%v", repair, err)
	}
	if _, found := store.files["ready/sentinel.blob"]; !found {
		t.Fatal("repair deleted ready sentinel outside its staged/quarantine whitelist")
	}
	second, err := service.Repair(context.Background())
	if err != nil || second.Repaired != 0 || len(second.Remaining.Issues) != 1 {
		t.Fatalf("repair replay must be idempotent: report=%#v err=%v", second, err)
	}
}
