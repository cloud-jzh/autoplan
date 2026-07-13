// Package intake provides the shared Intake use cases used by every transport.
package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	applicationidempotency "github.com/lyming99/autoplan/backend/internal/application/idempotency"
	applicationsnapshot "github.com/lyming99/autoplan/backend/internal/application/snapshot"
	domainintake "github.com/lyming99/autoplan/backend/internal/domain/intake"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

var (
	ErrUnavailable       = errors.New("intake application service unavailable")
	ErrInvalidCommand    = errors.New("intake command is invalid")
	ErrStateConflict     = errors.New("intake state conflicts")
	ErrInvalidTransition = errors.New("intake status transition is invalid")
)

type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type PlanRuntime interface {
	StopIntakePlans(context.Context, int64, domainintake.Type, int64) ([]int64, error)
}

type AttachmentDeletion struct {
	OperationID   string
	ProjectID     int64
	IntakeType    domainintake.Type
	IntakeID      int64
	AttachmentIDs []int64
}

type AttachmentCleanup struct {
	Total   int64
	Deleted int64
	Missing int64
	Pending int64
	Code    string
}

type AttachmentWorkflow interface {
	// PrepareIntakeDeletion may persist recovery intent, but must not remove or
	// move final bytes before the caller's database transaction commits.
	PrepareIntakeDeletion(context.Context, int64, domainintake.Type, int64, string) (AttachmentDeletion, error)
	// FinalizeIntakeDeletion uses OperationID as the replay identity; the ID
	// slice is an advisory copy of the rows removed by the committed transaction.
	FinalizeIntakeDeletion(context.Context, AttachmentDeletion) (AttachmentCleanup, error)
}

type Dependencies struct {
	Assembler   *applicationsnapshot.Assembler
	Writer      repository.IntakeTransactional
	Idempotency *applicationidempotency.Service
	Runtime     PlanRuntime
	Attachments AttachmentWorkflow
	Clock       Clock
}

type Service struct {
	assembler   *applicationsnapshot.Assembler
	writer      repository.IntakeTransactional
	idempotency *applicationidempotency.Service
	runtime     PlanRuntime
	attachments AttachmentWorkflow
	clock       Clock
}

// DuplicateError preserves the frozen duplicate identity without exposing
// repository rows or private generation fields to transports.
type DuplicateError struct {
	IntakeType domainintake.Type
	Existing   IntakeDTO
}

func (failure DuplicateError) Error() string {
	label := "需求"
	if failure.IntakeType == domainintake.Feedback {
		label = "反馈"
	}
	return fmt.Sprintf("%s已存在：#%d", label, failure.Existing.ID)
}

func (DuplicateError) Unwrap() error { return repository.ErrDuplicate }

func NewService(dependencies Dependencies) *Service {
	clock := dependencies.Clock
	if clock == nil {
		clock = systemClock{}
	}
	runtime := dependencies.Runtime
	if runtime == nil {
		runtime = noPlanRuntime{}
	}
	attachments := dependencies.Attachments
	if attachments == nil {
		attachments = noAttachmentWorkflow{}
	}
	return &Service{
		assembler: dependencies.Assembler, writer: dependencies.Writer,
		idempotency: dependencies.Idempotency, runtime: runtime,
		attachments: attachments, clock: clock,
	}
}

func (service *Service) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == nil || service.assembler == nil || service.writer == nil ||
		service.idempotency == nil || service.runtime == nil || service.attachments == nil || service.clock == nil {
		return ErrUnavailable
	}
	return service.writer.Check(ctx)
}

func (service *Service) now() time.Time { return service.clock.Now().UTC() }

func formatTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

func nextTimestamp(candidate time.Time, current string) string {
	next := candidate.UTC().Truncate(time.Millisecond)
	if parsed, err := time.Parse(time.RFC3339Nano, current); err == nil && !next.After(parsed) {
		next = parsed.UTC().Truncate(time.Millisecond).Add(time.Millisecond)
	}
	return formatTimestamp(next)
}

func mutationPrepared(prepared applicationidempotency.Prepared, metadata MutationMetadata) applicationidempotency.Prepared {
	if prepared.Enabled || prepared.RequestID != "" {
		return prepared
	}
	requestID := strings.TrimSpace(metadata.RequestID)
	if requestID != "" && len(requestID) <= 256 && !strings.ContainsAny(requestID, "\r\n\x00") {
		prepared.RequestID = requestID
	}
	return prepared
}

func eventIdentity(route, requestID string, projectID, intakeID int64, occurredAt string) (string, int64) {
	digest := sha256.Sum256([]byte(fmt.Sprintf("p06\x00%s\x00%s\x00%d\x00%d\x00%s", route, requestID, projectID, intakeID, occurredAt)))
	sequence := int64(0)
	for index := 0; index < 7; index++ {
		sequence = (sequence << 8) | int64(digest[index])
	}
	return "evt-" + hex.EncodeToString(digest[:16]), sequence
}

func appendEvent(
	ctx context.Context,
	transaction repository.IntakeWriteTransaction,
	prepared applicationidempotency.Prepared,
	route, eventType string,
	projectID, intakeID int64,
	intakeType domainintake.Type,
	message string,
	data map[string]any,
	occurredAt string,
) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return ErrInvalidCommand
	}
	requestID := prepared.RequestID
	if requestID == "" {
		requestID = "local-intake-mutation"
	}
	eventID, sequence := eventIdentity(route, requestID, projectID, intakeID, occurredAt)
	var operationID *string
	if prepared.Enabled {
		value := prepared.OperationID
		operationID = &value
	}
	return transaction.AppendIntakeEvent(ctx, domainintake.PendingEvent{
		EventID: eventID, StreamKey: fmt.Sprintf("project:%d:intake:%s:%d", projectID, intakeType, intakeID),
		Sequence: sequence, Type: eventType, RequestID: requestID, OperationID: operationID,
		ProjectID: projectID, Message: message, DataJSON: string(encoded),
		OccurredAt: occurredAt, CreatedAt: occurredAt,
	})
}

func activeProjectReference(projectID int64) applicationidempotency.Reference {
	value := projectID
	return applicationidempotency.Reference{Kind: "active-project", ProjectID: &value}
}

type noPlanRuntime struct{}

func (noPlanRuntime) StopIntakePlans(context.Context, int64, domainintake.Type, int64) ([]int64, error) {
	return []int64{}, nil
}

type noAttachmentWorkflow struct{}

func (noAttachmentWorkflow) PrepareIntakeDeletion(
	_ context.Context, projectID int64, intakeType domainintake.Type, intakeID int64, operationID string,
) (AttachmentDeletion, error) {
	return AttachmentDeletion{
		OperationID: operationID, ProjectID: projectID, IntakeType: intakeType, IntakeID: intakeID,
	}, nil
}

func (noAttachmentWorkflow) FinalizeIntakeDeletion(
	_ context.Context, deletion AttachmentDeletion,
) (AttachmentCleanup, error) {
	if len(deletion.AttachmentIDs) != 0 {
		count := int64(len(deletion.AttachmentIDs))
		return AttachmentCleanup{
			Total: count, Pending: count, Code: "attachment_workflow_unavailable",
		}, ErrUnavailable
	}
	return AttachmentCleanup{Code: "not_configured"}, nil
}
