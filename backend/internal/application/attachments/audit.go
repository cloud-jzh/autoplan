package attachments

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

type AuditIssue struct {
	Code         string `json:"code"`
	AttachmentID *int64 `json:"attachment_id,omitempty"`
	OperationID  string `json:"operation_id,omitempty"`
	Location     string `json:"location"`
	Repairable   bool   `json:"repairable"`
}

type AuditReport struct {
	Issues []AuditIssue `json:"issues"`
}

type RepairReport struct {
	Repaired  int64       `json:"repaired"`
	Remaining AuditReport `json:"remaining"`
}

const defaultAuditAge = 24 * time.Hour

func (service *Service) Audit(ctx context.Context) (AuditReport, error) {
	return service.audit(ctx, service.clock.Now().UTC(), defaultAuditAge)
}

func (service *Service) Repair(ctx context.Context) (RepairReport, error) {
	if err := service.ready(ctx); err != nil {
		return RepairReport{}, err
	}
	now := service.clock.Now().UTC()
	if _, err := service.audit(ctx, now, defaultAuditAge); err != nil {
		return RepairReport{}, err
	}
	files, err := service.store.List(ctx)
	if err != nil {
		return RepairReport{}, err
	}
	operations, err := service.allOperations(ctx)
	if err != nil {
		return RepairReport{}, err
	}
	known := make(map[string]struct{}, len(operations)*2)
	for _, operation := range operations {
		if operation.StageKey != "" {
			known[operation.StageKey] = struct{}{}
		}
		if operation.QuarantineKey != "" {
			known[operation.QuarantineKey] = struct{}{}
		}
	}
	var repaired int64
	for _, file := range files {
		if file.Size < 0 || !domainfiles.StorageKeyValid(file.Key) || now.Sub(file.ModifiedAt) < defaultAuditAge || !repairableStorageKey(file.Key) {
			continue
		}
		// A staged/quarantine object with a durable operation is recovered by
		// Recover, not removed by audit repair. Only unreferenced expired files
		// are on the repair whitelist.
		if _, referenced := known[file.Key]; referenced {
			continue
		}
		if err := service.store.Remove(ctx, file.Key); err != nil {
			return RepairReport{}, err
		}
		repaired++
	}
	remaining, err := service.audit(ctx, now, defaultAuditAge)
	if err != nil {
		return RepairReport{}, err
	}
	return RepairReport{Repaired: repaired, Remaining: remaining}, nil
}

func (service *Service) audit(ctx context.Context, now time.Time, maximumAge time.Duration) (AuditReport, error) {
	if err := service.ready(ctx); err != nil {
		return AuditReport{}, err
	}
	projects, err := service.projects(ctx)
	if err != nil {
		return AuditReport{}, err
	}
	expected := make(map[string]domainfiles.Attachment)
	issues := make([]AuditIssue, 0)
	for _, project := range projects {
		attachments, listErr := service.projectAttachments(ctx, project.ID)
		if listErr != nil {
			return AuditReport{}, listErr
		}
		operations, operationErr := service.operations(ctx, project.ID, true)
		if operationErr != nil {
			return AuditReport{}, operationErr
		}
		for _, operation := range operations {
			if operation.State.Recoverable() {
				issues = append(issues, AuditIssue{Code: "operation_pending", OperationID: operation.ID, Location: operationLocation(operation.State)})
			}
		}
		for _, attachment := range attachments {
			id := attachment.ID
			location := "<attachment-root>/ready"
			if !domainfiles.StorageKeyValid(attachment.StoredKey) || !strings.HasPrefix(attachment.StoredKey, "ready/") {
				issues = append(issues, AuditIssue{Code: "unsafe_storage_key", AttachmentID: &id, Location: location})
				continue
			}
			expected[attachment.StoredKey] = attachment
			if ownerErr := service.validateOwner(ctx, attachment); ownerErr != nil {
				code := "owner_missing"
				if errors.Is(ownerErr, repository.ErrProjectMismatch) {
					code = "owner_project_mismatch"
				}
				issues = append(issues, AuditIssue{Code: code, AttachmentID: &id, Location: location})
			}
			file, digest, inspectErr := service.store.Inspect(ctx, attachment.StoredKey)
			if inspectErr != nil {
				issues = append(issues, AuditIssue{Code: "database_file_missing", AttachmentID: &id, Location: location})
				continue
			}
			if file.Size != attachment.Size {
				issues = append(issues, AuditIssue{Code: "size_mismatch", AttachmentID: &id, Location: location})
			}
			if digest != attachment.SHA256 {
				issues = append(issues, AuditIssue{Code: "hash_mismatch", AttachmentID: &id, Location: location})
			}
			sample, sampleErr := service.store.Sample(ctx, attachment.StoredKey, 512)
			if sampleErr != nil || validateContent(attachment.MIMEType, sample) != nil {
				issues = append(issues, AuditIssue{Code: "mime_mismatch", AttachmentID: &id, Location: location})
			}
		}
	}
	files, err := service.store.List(ctx)
	if err != nil {
		return AuditReport{}, err
	}
	for _, file := range files {
		if file.Size < 0 || !domainfiles.StorageKeyValid(file.Key) {
			issues = append(issues, AuditIssue{Code: "unknown_storage_entry", Location: storageLocation(file.Key)})
			continue
		}
		switch {
		case strings.HasPrefix(file.Key, "ready/"):
			if _, exists := expected[file.Key]; !exists {
				issues = append(issues, AuditIssue{Code: "storage_orphan", Location: "<attachment-root>/ready"})
			}
		case strings.HasPrefix(file.Key, "staged/") && now.Sub(file.ModifiedAt) >= maximumAge:
			issues = append(issues, AuditIssue{Code: "staged_expired", Location: "<attachment-root>/staged", Repairable: true})
		case strings.HasPrefix(file.Key, "quarantine/") && now.Sub(file.ModifiedAt) >= maximumAge:
			issues = append(issues, AuditIssue{Code: "quarantine_expired", Location: "<attachment-root>/quarantine", Repairable: true})
		}
	}
	sort.SliceStable(issues, func(left, right int) bool {
		if issues[left].Code != issues[right].Code {
			return issues[left].Code < issues[right].Code
		}
		leftID, rightID := int64(0), int64(0)
		if issues[left].AttachmentID != nil {
			leftID = *issues[left].AttachmentID
		}
		if issues[right].AttachmentID != nil {
			rightID = *issues[right].AttachmentID
		}
		if leftID != rightID {
			return leftID < rightID
		}
		return issues[left].OperationID < issues[right].OperationID
	})
	return AuditReport{Issues: issues}, nil
}

func (service *Service) projects(ctx context.Context) ([]repository.Project, error) {
	var result []repository.Project
	err := service.transact(ctx, func(transaction attachmentTransaction) error {
		var listErr error
		result, listErr = transaction.ListProjects(ctx)
		return listErr
	})
	return result, err
}

func (service *Service) projectAttachments(ctx context.Context, projectID int64) ([]domainfiles.Attachment, error) {
	var result []domainfiles.Attachment
	err := service.transact(ctx, func(transaction attachmentTransaction) error {
		var listErr error
		result, listErr = transaction.ListProjectAttachments(ctx, projectID)
		return listErr
	})
	return result, err
}

func (service *Service) validateOwner(ctx context.Context, attachment domainfiles.Attachment) error {
	return service.transact(ctx, func(transaction attachmentTransaction) error {
		return transaction.ValidateAttachmentOwner(ctx, attachment.ProjectID, attachment.OwnerType, attachment.OwnerID)
	})
}

func (service *Service) allOperations(ctx context.Context) ([]domainfiles.AttachmentOperation, error) {
	projects, err := service.projects(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]domainfiles.AttachmentOperation, 0)
	for _, project := range projects {
		operations, listErr := service.operations(ctx, project.ID, true)
		if listErr != nil {
			return nil, listErr
		}
		result = append(result, operations...)
	}
	return result, nil
}

func repairableStorageKey(key string) bool {
	return strings.HasPrefix(key, "staged/") || strings.HasPrefix(key, "quarantine/")
}

func operationLocation(state domainfiles.OperationState) string {
	if state == domainfiles.StateQuarantined {
		return "<attachment-root>/quarantine"
	}
	return "<attachment-root>/staged"
}

func storageLocation(key string) string {
	switch {
	case strings.HasPrefix(key, "staged/"):
		return "<attachment-root>/staged"
	case strings.HasPrefix(key, "quarantine/"):
		return "<attachment-root>/quarantine"
	default:
		return "<attachment-root>/ready"
	}
}
