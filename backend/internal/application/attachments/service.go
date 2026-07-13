// Package attachments owns byte-oriented attachment upload, download, and
// deletion workflows. It never accepts a client-controlled storage path.
package attachments

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	applicationintake "github.com/lyming99/autoplan/backend/internal/application/intake"
	applicationsnapshot "github.com/lyming99/autoplan/backend/internal/application/snapshot"
	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
	domainintake "github.com/lyming99/autoplan/backend/internal/domain/intake"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

var ErrUnavailable = errors.New("attachment application service unavailable")

type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type Store interface {
	Stage(context.Context, string, io.Reader, int64) (domainfiles.StagedAttachment, error)
	Promote(context.Context, string, string) error
	Quarantine(context.Context, string, string) error
	Remove(context.Context, string) error
	Open(context.Context, string) (io.ReadCloser, error)
	Inspect(context.Context, string) (domainfiles.StoredAttachmentFile, string, error)
	Sample(context.Context, string, int) ([]byte, error)
	List(context.Context) ([]domainfiles.StoredAttachmentFile, error)
}

type SourcePolicy interface {
	Authorize(context.Context, domainfiles.Operation, string, string, bool) (domainfiles.Decision, error)
}

type attachmentTransaction interface {
	ListProjects(context.Context) ([]repository.Project, error)
	CreateAttachment(context.Context, domainfiles.Attachment) (domainfiles.Attachment, error)
	GetAttachment(context.Context, int64, int64) (domainfiles.Attachment, bool, error)
	ListAttachmentsForOwner(context.Context, int64, domainfiles.AttachmentOwner, int64) ([]domainfiles.Attachment, error)
	ListProjectAttachments(context.Context, int64) ([]domainfiles.Attachment, error)
	DeleteAttachment(context.Context, int64, int64) (domainfiles.Attachment, error)
	UpdateAttachmentStorageKey(context.Context, int64, int64, string) error
	CreateAttachmentOperation(context.Context, domainfiles.AttachmentOperation) error
	UpdateAttachmentOperation(context.Context, domainfiles.AttachmentOperation) error
	GetAttachmentOperation(context.Context, string) (domainfiles.AttachmentOperation, bool, error)
	ListAttachmentOperations(context.Context, int64, bool) ([]domainfiles.AttachmentOperation, error)
	ValidateAttachmentOwner(context.Context, int64, domainfiles.AttachmentOwner, int64) error
}

type Dependencies struct {
	Writer repository.IntakeTransactional
	Store  Store
	Policy SourcePolicy
	Clock  Clock
}

type Service struct {
	writer repository.IntakeTransactional
	store  Store
	policy SourcePolicy
	clock  Clock
}

type UploadCommand struct {
	OperationID string
	ProjectID   int64
	OwnerType   domainfiles.AttachmentOwner
	OwnerID     int64
	DisplayName string
	MIMEType    string
	Content     io.Reader
}

type AttachmentDTO struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"display_name"`
	Size        int64  `json:"size"`
	MIMEType    string `json:"mime_type"`
	DownloadURL string `json:"download_url"`
}

type UploadResult struct {
	Attachment       AttachmentDTO              `json:"attachment"`
	State            domainfiles.OperationState `json:"state"`
	RecoveryRequired bool                       `json:"recovery_required"`
}

type DeleteResult struct {
	AttachmentID     int64                      `json:"attachment_id"`
	State            domainfiles.OperationState `json:"state"`
	RecoveryRequired bool                       `json:"recovery_required"`
}

func NewService(dependencies Dependencies) *Service {
	clock := dependencies.Clock
	if clock == nil {
		clock = systemClock{}
	}
	return &Service{writer: dependencies.Writer, store: dependencies.Store, policy: dependencies.Policy, clock: clock}
}

func (service *Service) Upload(ctx context.Context, command UploadCommand) (UploadResult, error) {
	if err := service.ready(ctx); err != nil {
		return UploadResult{}, err
	}
	if command.ProjectID <= 0 || command.OwnerID <= 0 || !command.OwnerType.Valid() || command.Content == nil ||
		!validOperationID(command.OperationID) {
		return UploadResult{}, domainfiles.ErrInvalidAttachment
	}
	name, err := domainfiles.NormalizeDisplayName(command.DisplayName)
	if err != nil {
		return UploadResult{}, err
	}
	mimeType := domainfiles.NormalizeMIMEType(command.MIMEType)
	nameMIME := mimeForName(name)
	if mimeType == "" {
		mimeType = nameMIME
	}
	if nameMIME != "" && mimeType != nameMIME && !(nameMIME == "image/png" && mimeType == "image/apng") {
		return UploadResult{}, domainfiles.ErrAttachmentContentType
	}
	if !domainfiles.AllowedMIMEType(mimeType) {
		return UploadResult{}, domainfiles.ErrAttachmentContentType
	}
	if existing, found, lookupErr := service.operation(ctx, command.OperationID); lookupErr != nil {
		return UploadResult{}, lookupErr
	} else if found {
		if existing.ProjectID != command.ProjectID || existing.Kind != domainfiles.OperationUpload {
			return UploadResult{}, repository.ErrIdempotencyKeyReuse
		}
		state, recoverErr := service.recoverOperation(ctx, existing)
		if recoverErr != nil && !errors.Is(recoverErr, domainfiles.ErrAttachmentRecovery) {
			return UploadResult{}, recoverErr
		}
		return service.uploadResult(ctx, state, recoverErr != nil)
	}
	staged, err := service.store.Stage(ctx, command.OperationID, command.Content, domainfiles.MaximumAttachmentBytes)
	if err != nil {
		return UploadResult{}, err
	}
	if err := validateContent(mimeType, staged.Sample); err != nil {
		_ = service.store.Remove(ctx, staged.StageKey)
		return UploadResult{}, err
	}
	now := service.timestamp()
	operation := domainfiles.AttachmentOperation{
		ID: command.OperationID, ProjectID: command.ProjectID, Kind: domainfiles.OperationUpload, State: domainfiles.StateStaged,
		StoredKey: staged.ReadyKey, StageKey: staged.StageKey, Size: staged.Size, SHA256: staged.SHA256,
		MIMEType: mimeType, CreatedAt: now, UpdatedAt: now,
	}
	var attachment domainfiles.Attachment
	err = service.transact(ctx, func(transaction attachmentTransaction) error {
		existing, listErr := transaction.ListAttachmentsForOwner(ctx, command.ProjectID, command.OwnerType, command.OwnerID)
		if listErr != nil {
			return listErr
		}
		if err := enforceOwnerLimits(existing, staged.Size); err != nil {
			return err
		}
		attachment, err = transaction.CreateAttachment(ctx, domainfiles.Attachment{
			ProjectID: command.ProjectID, OwnerType: command.OwnerType, OwnerID: command.OwnerID,
			DisplayName: name, StoredKey: staged.ReadyKey, MIMEType: mimeType, Size: staged.Size,
			SHA256: staged.SHA256, CreatedAt: now,
		})
		if err != nil {
			return err
		}
		readyKey := readyKey(attachment.ID)
		operation.AttachmentID = copyID(attachment.ID)
		operation.StoredKey = readyKey
		if err := transaction.CreateAttachmentOperation(ctx, operation); err != nil {
			return err
		}
		attachment.StoredKey = readyKey
		if err := transaction.UpdateAttachmentStorageKey(ctx, command.ProjectID, attachment.ID, readyKey); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		operation.AttachmentID = nil
		operation.StoredKey = ""
		operation.State = domainfiles.StateFailed
		operation.UpdatedAt = service.timestamp()
		// A failed metadata transaction never promotes the staged bytes. When
		// cleanup cannot finish immediately, this independent record is the
		// recovery/audit anchor for the still-staged object.
		_ = service.createOperation(ctx, operation)
		cleanupErr := service.store.Remove(ctx, staged.StageKey)
		if cleanupErr != nil {
			return UploadResult{}, domainfiles.ErrAttachmentRecovery
		}
		return UploadResult{}, err
	}
	operation.StoredKey = readyKey(attachment.ID)
	state, recoverErr := service.recoverOperation(ctx, operation)
	if recoverErr != nil && !errors.Is(recoverErr, domainfiles.ErrAttachmentRecovery) {
		return UploadResult{}, recoverErr
	}
	return service.uploadResult(ctx, state, recoverErr != nil)
}

func (service *Service) UploadFromAuthorizedPath(
	ctx context.Context,
	workspaceRoot string,
	sourcePath string,
	command UploadCommand,
) (UploadResult, error) {
	if err := service.ready(ctx); err != nil {
		return UploadResult{}, err
	}
	if service.policy == nil {
		return UploadResult{}, domainfiles.ErrInvalidPolicy
	}
	decision, err := service.policy.Authorize(ctx, domainfiles.OperationAttachment, workspaceRoot, sourcePath, false)
	if err != nil || !decision.Allowed {
		return UploadResult{}, err
	}
	file, err := os.Open(decision.ResolvedTarget)
	if err != nil {
		return UploadResult{}, domainfiles.ErrAttachmentContent
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return UploadResult{}, domainfiles.ErrAttachmentContent
	}
	rechecked, err := service.policy.Authorize(ctx, domainfiles.OperationAttachment, workspaceRoot, sourcePath, false)
	if err != nil || !rechecked.Allowed || rechecked.ResolvedTarget != decision.ResolvedTarget {
		return UploadResult{}, domainfiles.ErrAttachmentContent
	}
	after, err := os.Stat(rechecked.ResolvedTarget)
	if err != nil || !os.SameFile(before, after) {
		return UploadResult{}, domainfiles.ErrAttachmentContent
	}
	command.Content = file
	return service.Upload(ctx, command)
}

func (service *Service) Open(ctx context.Context, projectID, attachmentID int64) (io.ReadCloser, AttachmentDTO, error) {
	if err := service.ready(ctx); err != nil {
		return nil, AttachmentDTO{}, err
	}
	attachment, err := service.attachment(ctx, projectID, attachmentID)
	if err != nil {
		return nil, AttachmentDTO{}, err
	}
	if !domainfiles.StorageKeyValid(attachment.StoredKey) || !strings.HasPrefix(attachment.StoredKey, "ready/") {
		return nil, AttachmentDTO{}, domainfiles.ErrAttachmentRecovery
	}
	file, err := service.store.Open(ctx, attachment.StoredKey)
	if err != nil {
		return nil, AttachmentDTO{}, domainfiles.ErrAttachmentRecovery
	}
	return file, attachmentDTO(attachment), nil
}

func (service *Service) Delete(ctx context.Context, operationID string, projectID, attachmentID int64) (DeleteResult, error) {
	if err := service.ready(ctx); err != nil {
		return DeleteResult{}, err
	}
	if projectID <= 0 || attachmentID <= 0 || !validOperationID(operationID) {
		return DeleteResult{}, domainfiles.ErrInvalidAttachment
	}
	if existing, found, err := service.operation(ctx, operationID); err != nil {
		return DeleteResult{}, err
	} else if found {
		if existing.ProjectID != projectID || existing.Kind != domainfiles.OperationDelete {
			return DeleteResult{}, repository.ErrIdempotencyKeyReuse
		}
		state, recoverErr := service.recoverOperation(ctx, existing)
		return DeleteResult{AttachmentID: attachmentID, State: state.State, RecoveryRequired: recoverErr != nil}, recoverErr
	}
	now := service.timestamp()
	var operation domainfiles.AttachmentOperation
	err := service.transact(ctx, func(transaction attachmentTransaction) error {
		attachment, found, getErr := transaction.GetAttachment(ctx, projectID, attachmentID)
		if getErr != nil {
			return getErr
		}
		if !found {
			return repository.ErrNotFound
		}
		operation = domainfiles.AttachmentOperation{
			ID: operationID, ProjectID: projectID, Kind: domainfiles.OperationDelete, State: domainfiles.StateDeleting,
			AttachmentID: copyID(attachment.ID), StoredKey: attachment.StoredKey,
			QuarantineKey: quarantineKey(operationID, attachment.ID), Size: attachment.Size, SHA256: attachment.SHA256,
			MIMEType: attachment.MIMEType, CreatedAt: now, UpdatedAt: now,
		}
		if err := transaction.CreateAttachmentOperation(ctx, operation); err != nil {
			return err
		}
		_, err := transaction.DeleteAttachment(ctx, projectID, attachmentID)
		return err
	})
	if err != nil {
		return DeleteResult{}, err
	}
	state, recoverErr := service.recoverOperation(ctx, operation)
	return DeleteResult{AttachmentID: attachmentID, State: state.State, RecoveryRequired: recoverErr != nil}, recoverErr
}

func (service *Service) ListAttachmentSnapshots(ctx context.Context, projectID int64) ([]applicationsnapshot.AttachmentSnapshot, error) {
	if err := service.ready(ctx); err != nil {
		return nil, err
	}
	var records []domainfiles.Attachment
	err := service.transact(ctx, func(transaction attachmentTransaction) error {
		var listErr error
		records, listErr = transaction.ListProjectAttachments(ctx, projectID)
		return listErr
	})
	if err != nil {
		return nil, err
	}
	result := make([]applicationsnapshot.AttachmentSnapshot, 0, len(records))
	for _, record := range records {
		if !domainfiles.StorageKeyValid(record.StoredKey) || !strings.HasPrefix(record.StoredKey, "ready/") {
			continue
		}
		file, digest, inspectErr := service.store.Inspect(ctx, record.StoredKey)
		if inspectErr != nil || file.Size != record.Size || digest != record.SHA256 {
			continue
		}
		result = append(result, applicationsnapshot.AttachmentSnapshot{
			ID: record.ID, DisplayName: record.DisplayName, Size: record.Size, MIMEType: record.MIMEType,
			DownloadURL: "/api/v1/attachments/" + decimalID(record.ID) + "/content",
		})
	}
	return result, nil
}

func (service *Service) PrepareIntakeDeletion(
	ctx context.Context,
	projectID int64,
	intakeType domainintake.Type,
	intakeID int64,
	operationID string,
) (applicationintake.AttachmentDeletion, error) {
	if err := service.ready(ctx); err != nil {
		return applicationintake.AttachmentDeletion{}, err
	}
	if projectID <= 0 || intakeID <= 0 || !intakeType.Valid() || !validOperationID(operationID) {
		return applicationintake.AttachmentDeletion{}, domainfiles.ErrInvalidAttachment
	}
	// P003 calls this while its own Intake transaction is open. The durable
	// per-file records are created during Finalize after that transaction has
	// committed; before commit this method intentionally has no file side effect.
	return applicationintake.AttachmentDeletion{
		OperationID: operationID, ProjectID: projectID, IntakeType: intakeType, IntakeID: intakeID,
	}, nil
}

func (service *Service) FinalizeIntakeDeletion(
	ctx context.Context,
	deletion applicationintake.AttachmentDeletion,
) (applicationintake.AttachmentCleanup, error) {
	if err := service.ready(ctx); err != nil {
		return applicationintake.AttachmentCleanup{}, err
	}
	result := applicationintake.AttachmentCleanup{Total: int64(len(deletion.AttachmentIDs))}
	for _, attachmentID := range deletion.AttachmentIDs {
		if attachmentID <= 0 {
			result.Pending++
			continue
		}
		operationID := detachedDeleteOperationID(deletion.OperationID, attachmentID)
		operation, found, err := service.operation(ctx, operationID)
		if err != nil {
			result.Pending++
			continue
		}
		if !found {
			now := service.timestamp()
			operation = domainfiles.AttachmentOperation{
				ID: operationID, ProjectID: deletion.ProjectID, Kind: domainfiles.OperationDelete, State: domainfiles.StateDeleting,
				AttachmentID: copyID(attachmentID), StoredKey: readyKey(attachmentID),
				QuarantineKey: quarantineKey(operationID, attachmentID), CreatedAt: now, UpdatedAt: now,
			}
			if createErr := service.createOperation(ctx, operation); createErr != nil && !errors.Is(createErr, repository.ErrDuplicate) {
				result.Pending++
				continue
			}
		}
		state, recoverErr := service.recoverOperation(ctx, operation)
		if recoverErr != nil || state.State != domainfiles.StateComplete {
			result.Pending++
			continue
		}
		result.Deleted++
	}
	if result.Pending > 0 {
		result.Code = "attachment_cleanup_pending"
		return result, domainfiles.ErrAttachmentRecovery
	}
	return result, nil
}

func (service *Service) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == nil || service.writer == nil || service.store == nil || service.clock == nil {
		return ErrUnavailable
	}
	return service.writer.Check(ctx)
}

func (service *Service) transact(ctx context.Context, operation func(attachmentTransaction) error) error {
	if operation == nil {
		return repository.ErrTransaction
	}
	return service.writer.TransactIntake(ctx, func(transaction repository.IntakeWriteTransaction) error {
		attachments, ok := transaction.(attachmentTransaction)
		if !ok {
			return repository.ErrTransaction
		}
		return operation(attachments)
	})
}

func (service *Service) operation(ctx context.Context, operationID string) (domainfiles.AttachmentOperation, bool, error) {
	var result domainfiles.AttachmentOperation
	var found bool
	err := service.transact(ctx, func(transaction attachmentTransaction) error {
		var lookupErr error
		result, found, lookupErr = transaction.GetAttachmentOperation(ctx, operationID)
		return lookupErr
	})
	return result, found, err
}

func (service *Service) attachment(ctx context.Context, projectID, attachmentID int64) (domainfiles.Attachment, error) {
	var result domainfiles.Attachment
	err := service.transact(ctx, func(transaction attachmentTransaction) error {
		value, found, err := transaction.GetAttachment(ctx, projectID, attachmentID)
		if err != nil {
			return err
		}
		if !found {
			return repository.ErrNotFound
		}
		result = value
		return nil
	})
	return result, err
}

func (service *Service) uploadResult(
	ctx context.Context,
	operation domainfiles.AttachmentOperation,
	recoveryRequired bool,
) (UploadResult, error) {
	if operation.AttachmentID == nil || *operation.AttachmentID <= 0 {
		return UploadResult{}, domainfiles.ErrAttachmentRecovery
	}
	attachment, err := service.attachment(ctx, operation.ProjectID, *operation.AttachmentID)
	if err != nil {
		return UploadResult{}, err
	}
	return UploadResult{
		Attachment: attachmentDTO(attachment), State: operation.State, RecoveryRequired: recoveryRequired,
	}, nil
}

func (service *Service) createOperation(ctx context.Context, operation domainfiles.AttachmentOperation) error {
	return service.transact(ctx, func(transaction attachmentTransaction) error {
		return transaction.CreateAttachmentOperation(ctx, operation)
	})
}

func (service *Service) updateOperation(ctx context.Context, operation domainfiles.AttachmentOperation) error {
	return service.transact(ctx, func(transaction attachmentTransaction) error {
		return transaction.UpdateAttachmentOperation(ctx, operation)
	})
}

func (service *Service) timestamp() string {
	return service.clock.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func enforceOwnerLimits(existing []domainfiles.Attachment, incoming int64) error {
	if len(existing) >= domainfiles.MaximumAttachmentCount {
		return domainfiles.ErrAttachmentLimit
	}
	total := incoming
	for _, attachment := range existing {
		if attachment.Size < 0 || attachment.Size > domainfiles.MaximumAttachmentTotal-total {
			return domainfiles.ErrAttachmentLimit
		}
		total += attachment.Size
	}
	if total > domainfiles.MaximumAttachmentTotal {
		return domainfiles.ErrAttachmentLimit
	}
	return nil
}

func validateContent(mimeType string, sample []byte) error {
	if len(sample) == 0 {
		return domainfiles.ErrAttachmentContent
	}
	switch mimeType {
	case "image/png", "image/apng":
		if !bytes.HasPrefix(sample, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}) {
			return domainfiles.ErrAttachmentContent
		}
	case "image/jpeg":
		if len(sample) < 3 || sample[0] != 0xff || sample[1] != 0xd8 || sample[2] != 0xff {
			return domainfiles.ErrAttachmentContent
		}
	case "image/gif":
		if !bytes.HasPrefix(sample, []byte("GIF87a")) && !bytes.HasPrefix(sample, []byte("GIF89a")) {
			return domainfiles.ErrAttachmentContent
		}
	case "image/webp":
		if len(sample) < 12 || !bytes.Equal(sample[:4], []byte("RIFF")) || !bytes.Equal(sample[8:12], []byte("WEBP")) {
			return domainfiles.ErrAttachmentContent
		}
	case "image/bmp":
		if !bytes.HasPrefix(sample, []byte("BM")) {
			return domainfiles.ErrAttachmentContent
		}
	case "image/avif":
		if len(sample) < 12 || !bytes.Equal(sample[4:8], []byte("ftyp")) || !bytes.Contains(sample[8:], []byte("avif")) {
			return domainfiles.ErrAttachmentContent
		}
	case "application/pdf":
		if !bytes.HasPrefix(sample, []byte("%PDF-")) {
			return domainfiles.ErrAttachmentContent
		}
	case "text/plain", "application/json":
		if !utf8.Valid(sample) || bytes.IndexByte(sample, 0) >= 0 {
			return domainfiles.ErrAttachmentContent
		}
		if mimeType == "application/json" {
			trimmed := bytes.TrimSpace(sample)
			if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
				return domainfiles.ErrAttachmentContent
			}
		}
	default:
		return domainfiles.ErrAttachmentContentType
	}
	return nil
}

func attachmentDTO(value domainfiles.Attachment) AttachmentDTO {
	return AttachmentDTO{
		ID: value.ID, DisplayName: value.DisplayName, Size: value.Size, MIMEType: value.MIMEType,
		DownloadURL: "/api/v1/attachments/" + decimalID(value.ID) + "/content",
	}
}

func mimeForName(value string) string {
	lower := strings.ToLower(value)
	switch {
	case strings.HasSuffix(lower, ".pdf"):
		return "application/pdf"
	case strings.HasSuffix(lower, ".json"):
		return "application/json"
	case strings.HasSuffix(lower, ".txt"):
		return "text/plain"
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".gif"):
		return "image/gif"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	case strings.HasSuffix(lower, ".bmp"):
		return "image/bmp"
	case strings.HasSuffix(lower, ".avif"):
		return "image/avif"
	case strings.HasSuffix(lower, ".apng"):
		return "image/apng"
	default:
		return ""
	}
}

func readyKey(attachmentID int64) string { return "ready/" + decimalID(attachmentID) + ".blob" }

func quarantineKey(operationID string, attachmentID int64) string {
	return "quarantine/" + operationID + "-" + decimalID(attachmentID) + ".blob"
}

func detachedDeleteOperationID(parent string, attachmentID int64) string {
	digest := sha256.Sum256([]byte("attachment-delete\x00" + parent + "\x00" + decimalID(attachmentID)))
	return "file-delete-" + hex.EncodeToString(digest[:16])
}

func copyID(value int64) *int64 { return &value }

func validOperationID(value string) bool {
	if value == "" || len(value) > 160 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func decimalID(value int64) string {
	if value <= 0 {
		return "0"
	}
	buffer := [20]byte{}
	position := len(buffer)
	for value > 0 {
		position--
		buffer[position] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[position:])
}

var _ applicationintake.AttachmentWorkflow = (*Service)(nil)
var _ applicationsnapshot.AttachmentSnapshotSource = (*Service)(nil)
