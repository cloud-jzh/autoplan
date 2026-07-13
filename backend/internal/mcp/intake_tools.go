package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	applicationattachments "github.com/lyming99/autoplan/backend/internal/application/attachments"
	applicationintake "github.com/lyming99/autoplan/backend/internal/application/intake"
	applicationprojects "github.com/lyming99/autoplan/backend/internal/application/projects"
	"github.com/lyming99/autoplan/backend/internal/domain/contracts"
	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
	domainintake "github.com/lyming99/autoplan/backend/internal/domain/intake"
	domainproject "github.com/lyming99/autoplan/backend/internal/domain/project"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

// ToolError is intentionally code-only. MCP callers can make stable decisions
// without receiving database, path, token, body, or implementation detail.
type ToolError struct{ Code string }

func (failure ToolError) Error() string { return failure.Code }

type IntakeApplication interface {
	List(context.Context, applicationintake.ListQuery) ([]applicationintake.IntakeDTO, error)
	Get(context.Context, int64, domainintake.Type, int64) (applicationintake.IntakeDTO, error)
	Create(context.Context, applicationintake.CreateCommand, domainproject.Visibility) (applicationintake.MutationResult, error)
	Update(context.Context, applicationintake.UpdateCommand, domainproject.Visibility) (applicationintake.MutationResult, error)
	SetAcceptance(context.Context, applicationintake.AcceptanceCommand, domainproject.Visibility) (applicationintake.MutationResult, error)
	Links(context.Context, int64, domainintake.Type, int64) ([]applicationintake.LinkedPlanDTO, error)
	ReplaceLinks(context.Context, applicationintake.ReplaceLinksCommand, domainproject.Visibility) (applicationintake.MutationResult, error)
	Delete(context.Context, applicationintake.DeleteCommand, domainproject.Visibility) (applicationintake.MutationResult, error)
}

type AttachmentApplication interface {
	Upload(context.Context, applicationattachments.UploadCommand) (applicationattachments.UploadResult, error)
	UploadFromAuthorizedPath(context.Context, string, string, applicationattachments.UploadCommand) (applicationattachments.UploadResult, error)
	Open(context.Context, int64, int64) (io.ReadCloser, applicationattachments.AttachmentDTO, error)
	Delete(context.Context, string, int64, int64) (applicationattachments.DeleteResult, error)
}

type ProjectApplication interface {
	Get(context.Context, int64, domainproject.Visibility) (contracts.Project, error)
}

var (
	_ IntakeApplication     = (*applicationintake.Service)(nil)
	_ AttachmentApplication = (*applicationattachments.Service)(nil)
	_ ProjectApplication    = (*applicationprojects.Service)(nil)
)

type Dependencies struct {
	Intake      IntakeApplication
	Attachments AttachmentApplication
	Projects    ProjectApplication
}

// ToolContext contains server-authenticated caller facts, never raw MCP or
// local session credentials. LocalCaller must only be asserted by a trusted
// in-process/stdio host, never by tool JSON arguments.
type ToolContext struct {
	CallerScope    string
	IdempotencyKey string
	RequestID      string
	LocalCaller    bool
}

type IntakeTools struct {
	intake      IntakeApplication
	attachments AttachmentApplication
	projects    ProjectApplication
}

func NewIntakeTools(dependencies Dependencies) *IntakeTools {
	return &IntakeTools{
		intake: dependencies.Intake, attachments: dependencies.Attachments, projects: dependencies.Projects,
	}
}

type ListRequest struct {
	ProjectID int64
	Status    *domainintake.Status
	Limit     int
	Offset    int
}

type CreateRequest struct {
	ProjectID      int64
	RequirementID  *int64
	Title          string
	Body           string
	Status         domainintake.Status
	AgentCLI       domainintake.AgentCLIConfig
	PlanGeneration domainintake.PlanGenerationConfig
}

type UpdateRequest struct {
	ProjectID         int64
	ID                int64
	ExpectedUpdatedAt string
	RequirementID     applicationintake.NullableInt64
	Title             *string
	Body              *string
	Status            *domainintake.Status
	AgentCLI          *domainintake.AgentCLIConfig
	PlanGeneration    *domainintake.PlanGenerationConfig
}

type ItemRequest struct {
	ProjectID int64
	ID        int64
}

type ReplaceLinksRequest struct {
	ProjectID int64
	ID        int64
	Links     []domainintake.PlanLinkInput
}

type AttachmentRequest struct {
	ProjectID    int64
	AttachmentID int64
}

type IntakeMutation struct {
	Mutation applicationintake.MutationResult `json:"mutation"`
}

func (tools *IntakeTools) ListRequirements(ctx context.Context, input ListRequest) ([]applicationintake.IntakeDTO, error) {
	return tools.list(ctx, domainintake.Requirement, input)
}

func (tools *IntakeTools) ListFeedback(ctx context.Context, input ListRequest) ([]applicationintake.IntakeDTO, error) {
	return tools.list(ctx, domainintake.Feedback, input)
}

func (tools *IntakeTools) GetRequirement(ctx context.Context, input ItemRequest) (applicationintake.IntakeDTO, error) {
	return tools.get(ctx, domainintake.Requirement, input)
}

func (tools *IntakeTools) GetFeedback(ctx context.Context, input ItemRequest) (applicationintake.IntakeDTO, error) {
	return tools.get(ctx, domainintake.Feedback, input)
}

func (tools *IntakeTools) CreateRequirement(ctx context.Context, request ToolContext, input CreateRequest) (IntakeMutation, error) {
	return tools.create(ctx, request, domainintake.Requirement, input)
}

func (tools *IntakeTools) CreateFeedback(ctx context.Context, request ToolContext, input CreateRequest) (IntakeMutation, error) {
	return tools.create(ctx, request, domainintake.Feedback, input)
}

func (tools *IntakeTools) UpdateRequirement(ctx context.Context, request ToolContext, input UpdateRequest) (applicationintake.MutationResult, error) {
	return tools.update(ctx, request, domainintake.Requirement, input)
}

func (tools *IntakeTools) UpdateFeedback(ctx context.Context, request ToolContext, input UpdateRequest) (applicationintake.MutationResult, error) {
	return tools.update(ctx, request, domainintake.Feedback, input)
}

func (tools *IntakeTools) DeleteRequirement(ctx context.Context, request ToolContext, input ItemRequest) (applicationintake.MutationResult, error) {
	return tools.delete(ctx, request, domainintake.Requirement, input)
}

func (tools *IntakeTools) DeleteFeedback(ctx context.Context, request ToolContext, input ItemRequest) (applicationintake.MutationResult, error) {
	return tools.delete(ctx, request, domainintake.Feedback, input)
}

func (tools *IntakeTools) SetRequirementAcceptance(ctx context.Context, request ToolContext, input ItemRequest, accepted bool) (applicationintake.MutationResult, error) {
	return tools.setAcceptance(ctx, request, domainintake.Requirement, input, accepted)
}

func (tools *IntakeTools) SetFeedbackAcceptance(ctx context.Context, request ToolContext, input ItemRequest, accepted bool) (applicationintake.MutationResult, error) {
	return tools.setAcceptance(ctx, request, domainintake.Feedback, input, accepted)
}

func (tools *IntakeTools) RequirementLinks(ctx context.Context, input ItemRequest) ([]applicationintake.LinkedPlanDTO, error) {
	return tools.links(ctx, domainintake.Requirement, input)
}

func (tools *IntakeTools) FeedbackLinks(ctx context.Context, input ItemRequest) ([]applicationintake.LinkedPlanDTO, error) {
	return tools.links(ctx, domainintake.Feedback, input)
}

func (tools *IntakeTools) ReplaceRequirementLinks(ctx context.Context, request ToolContext, input ReplaceLinksRequest) (applicationintake.MutationResult, error) {
	return tools.replaceLinks(ctx, request, domainintake.Requirement, input)
}

func (tools *IntakeTools) ReplaceFeedbackLinks(ctx context.Context, request ToolContext, input ReplaceLinksRequest) (applicationintake.MutationResult, error) {
	return tools.replaceLinks(ctx, request, domainintake.Feedback, input)
}

func (tools *IntakeTools) OpenAttachment(ctx context.Context, input AttachmentRequest) (io.ReadCloser, applicationattachments.AttachmentDTO, error) {
	if tools == nil || tools.attachments == nil {
		return nil, applicationattachments.AttachmentDTO{}, ToolError{Code: "service_unavailable"}
	}
	content, attachment, err := tools.attachments.Open(ctx, input.ProjectID, input.AttachmentID)
	if err != nil {
		return nil, applicationattachments.AttachmentDTO{}, mapError(err)
	}
	return content, attachment, nil
}

func (tools *IntakeTools) DeleteAttachment(ctx context.Context, request ToolContext, input AttachmentRequest) (applicationattachments.DeleteResult, error) {
	if tools == nil || tools.attachments == nil {
		return applicationattachments.DeleteResult{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.attachments.Delete(ctx, attachmentOperationID("delete", request, input.ProjectID, "", input.AttachmentID), input.ProjectID, input.AttachmentID)
	if err != nil {
		return applicationattachments.DeleteResult{}, mapError(err)
	}
	return result, nil
}

func (tools *IntakeTools) UploadRequirementAttachment(ctx context.Context, request ToolContext, owner ItemRequest, input AttachmentInput) (applicationattachments.UploadResult, error) {
	return tools.uploadAttachment(ctx, request, domainintake.Requirement, owner, input)
}

func (tools *IntakeTools) UploadFeedbackAttachment(ctx context.Context, request ToolContext, owner ItemRequest, input AttachmentInput) (applicationattachments.UploadResult, error) {
	return tools.uploadAttachment(ctx, request, domainintake.Feedback, owner, input)
}

func (tools *IntakeTools) list(ctx context.Context, intakeType domainintake.Type, input ListRequest) ([]applicationintake.IntakeDTO, error) {
	if tools == nil || tools.intake == nil {
		return nil, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.intake.List(ctx, applicationintake.ListQuery{
		ProjectID: input.ProjectID, Type: intakeType, Status: input.Status, Limit: input.Limit, Offset: input.Offset,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return result, nil
}

func (tools *IntakeTools) get(ctx context.Context, intakeType domainintake.Type, input ItemRequest) (applicationintake.IntakeDTO, error) {
	if tools == nil || tools.intake == nil {
		return applicationintake.IntakeDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.intake.Get(ctx, input.ProjectID, intakeType, input.ID)
	if err != nil {
		return applicationintake.IntakeDTO{}, mapError(err)
	}
	return result, nil
}

func (tools *IntakeTools) create(ctx context.Context, request ToolContext, intakeType domainintake.Type, input CreateRequest) (IntakeMutation, error) {
	if tools == nil || tools.intake == nil {
		return IntakeMutation{}, ToolError{Code: "service_unavailable"}
	}
	if intakeType == domainintake.Requirement && input.RequirementID != nil {
		return IntakeMutation{}, ToolError{Code: "invalid_intake"}
	}
	result, err := tools.intake.Create(ctx, applicationintake.CreateCommand{
		ProjectID: input.ProjectID, Type: intakeType, RequirementID: input.RequirementID, Title: input.Title, Body: input.Body,
		Status: input.Status, AgentCLI: input.AgentCLI, PlanGeneration: input.PlanGeneration, Metadata: mutationMetadata(request),
	}, domainproject.Visibility{WorkspacePath: request.LocalCaller})
	if err != nil {
		return IntakeMutation{}, mapError(err)
	}
	return IntakeMutation{Mutation: result}, nil
}

func (tools *IntakeTools) update(ctx context.Context, request ToolContext, intakeType domainintake.Type, input UpdateRequest) (applicationintake.MutationResult, error) {
	if tools == nil || tools.intake == nil {
		return applicationintake.MutationResult{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.intake.Update(ctx, applicationintake.UpdateCommand{
		ProjectID: input.ProjectID, Type: intakeType, ID: input.ID, ExpectedUpdatedAt: input.ExpectedUpdatedAt,
		RequirementID: input.RequirementID, Title: input.Title, Body: input.Body, Status: input.Status,
		AgentCLI: input.AgentCLI, PlanGeneration: input.PlanGeneration, Metadata: mutationMetadata(request),
	}, domainproject.Visibility{WorkspacePath: request.LocalCaller})
	if err != nil {
		return applicationintake.MutationResult{}, mapError(err)
	}
	return result, nil
}

func (tools *IntakeTools) delete(ctx context.Context, request ToolContext, intakeType domainintake.Type, input ItemRequest) (applicationintake.MutationResult, error) {
	if tools == nil || tools.intake == nil {
		return applicationintake.MutationResult{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.intake.Delete(ctx, applicationintake.DeleteCommand{
		ProjectID: input.ProjectID, Type: intakeType, ID: input.ID, Metadata: mutationMetadata(request),
	}, domainproject.Visibility{WorkspacePath: request.LocalCaller})
	if err != nil {
		return applicationintake.MutationResult{}, mapError(err)
	}
	return result, nil
}

func (tools *IntakeTools) setAcceptance(ctx context.Context, request ToolContext, intakeType domainintake.Type, input ItemRequest, accepted bool) (applicationintake.MutationResult, error) {
	if tools == nil || tools.intake == nil {
		return applicationintake.MutationResult{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.intake.SetAcceptance(ctx, applicationintake.AcceptanceCommand{
		ProjectID: input.ProjectID, Type: intakeType, ID: input.ID, Accept: accepted, Metadata: mutationMetadata(request),
	}, domainproject.Visibility{WorkspacePath: request.LocalCaller})
	if err != nil {
		return applicationintake.MutationResult{}, mapError(err)
	}
	return result, nil
}

func (tools *IntakeTools) links(ctx context.Context, intakeType domainintake.Type, input ItemRequest) ([]applicationintake.LinkedPlanDTO, error) {
	if tools == nil || tools.intake == nil {
		return nil, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.intake.Links(ctx, input.ProjectID, intakeType, input.ID)
	if err != nil {
		return nil, mapError(err)
	}
	return result, nil
}

func (tools *IntakeTools) replaceLinks(ctx context.Context, request ToolContext, intakeType domainintake.Type, input ReplaceLinksRequest) (applicationintake.MutationResult, error) {
	if tools == nil || tools.intake == nil {
		return applicationintake.MutationResult{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.intake.ReplaceLinks(ctx, applicationintake.ReplaceLinksCommand{
		ProjectID: input.ProjectID, Type: intakeType, ID: input.ID, Links: input.Links, Metadata: mutationMetadata(request),
	}, domainproject.Visibility{WorkspacePath: request.LocalCaller})
	if err != nil {
		return applicationintake.MutationResult{}, mapError(err)
	}
	return result, nil
}

func (tools *IntakeTools) uploadAttachment(ctx context.Context, request ToolContext, intakeType domainintake.Type, owner ItemRequest, input AttachmentInput) (applicationattachments.UploadResult, error) {
	if tools == nil || tools.attachments == nil {
		return applicationattachments.UploadResult{}, ToolError{Code: "service_unavailable"}
	}
	ownerType := domainfiles.OwnerRequirement
	if intakeType == domainintake.Feedback {
		ownerType = domainfiles.OwnerFeedback
	}
	decoded, err := decodeAttachmentInput(input, request.LocalCaller)
	if err != nil {
		return applicationattachments.UploadResult{}, mapError(err)
	}
	command := applicationattachments.UploadCommand{
		OperationID: attachmentOperationID("upload", request, owner.ProjectID, intakeType, owner.ID),
		ProjectID:   owner.ProjectID, OwnerType: ownerType, OwnerID: owner.ID,
		DisplayName: decoded.Name, MIMEType: decoded.MIMEType,
	}
	if decoded.Path != "" {
		if tools.projects == nil {
			return applicationattachments.UploadResult{}, ToolError{Code: "service_unavailable"}
		}
		project, projectErr := tools.projects.Get(ctx, owner.ProjectID, domainproject.Visibility{WorkspacePath: true})
		if projectErr != nil || strings.TrimSpace(project.WorkspacePath) == "" {
			if projectErr != nil {
				return applicationattachments.UploadResult{}, mapError(projectErr)
			}
			return applicationattachments.UploadResult{}, ToolError{Code: "attachment_path_denied"}
		}
		result, uploadErr := tools.attachments.UploadFromAuthorizedPath(ctx, project.WorkspacePath, decoded.Path, command)
		if uploadErr != nil {
			return applicationattachments.UploadResult{}, mapError(uploadErr)
		}
		return result, nil
	}
	command.Content = decoded.Content
	result, uploadErr := tools.attachments.Upload(ctx, command)
	if uploadErr != nil {
		return applicationattachments.UploadResult{}, mapError(uploadErr)
	}
	return result, nil
}

func mutationMetadata(request ToolContext) applicationintake.MutationMetadata {
	caller := strings.TrimSpace(request.CallerScope)
	if caller == "" {
		caller = "mcp-local"
	}
	requestID := strings.TrimSpace(request.RequestID)
	if requestID == "" {
		requestID = "mcp-request"
	}
	return applicationintake.MutationMetadata{
		CallerScope: caller, IdempotencyKey: strings.TrimSpace(request.IdempotencyKey), RequestID: requestID,
	}
}

func attachmentOperationID(kind string, request ToolContext, projectID int64, intakeType domainintake.Type, resourceID int64) string {
	identity := strings.TrimSpace(request.IdempotencyKey)
	if identity == "" {
		identity = strings.TrimSpace(request.RequestID)
	}
	if identity == "" {
		buffer := make([]byte, 16)
		if _, err := io.ReadFull(rand.Reader, buffer); err == nil {
			identity = hex.EncodeToString(buffer)
		} else {
			identity = "local"
		}
	}
	digest := sha256.Sum256([]byte("autoplan-p06-mcp-attachment\x00" + kind + "\x00" + strings.TrimSpace(request.CallerScope) + "\x00" + identity + "\x00" + decimal(projectID) + "\x00" + string(intakeType) + "\x00" + decimal(resourceID)))
	return "mcp-" + kind + "-" + hex.EncodeToString(digest[:16])
}

func decimal(value int64) string {
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

func mapError(err error) error {
	if err == nil {
		return nil
	}
	var tool ToolError
	if errors.As(err, &tool) {
		return tool
	}
	var duplicate applicationintake.DuplicateError
	switch {
	case errors.Is(err, ErrInvalidAttachmentInput), errors.Is(err, ErrAttachmentTooLarge):
		return ToolError{Code: "invalid_attachment"}
	case errors.Is(err, ErrAttachmentPathDenied):
		return ToolError{Code: "attachment_path_denied"}
	case errors.Is(err, domainfiles.ErrInvalidPath), errors.Is(err, domainfiles.ErrOutsideScope),
		errors.Is(err, domainfiles.ErrResolutionFailed), errors.Is(err, domainfiles.ErrSymlinkEscape),
		errors.Is(err, domainfiles.ErrRaceDetected), errors.Is(err, domainfiles.ErrControlledTarget),
		errors.Is(err, domainfiles.ErrInvalidPolicy):
		return ToolError{Code: "attachment_path_denied"}
	case errors.As(err, &duplicate):
		return ToolError{Code: "duplicate_intake"}
	case errors.Is(err, applicationintake.ErrInvalidCommand), errors.Is(err, applicationintake.ErrInvalidTransition),
		errors.Is(err, domainintake.ErrInvalid), errors.Is(err, domainintake.ErrInvalidLink), errors.Is(err, repository.ErrInvalidIntake):
		return ToolError{Code: "invalid_intake"}
	case errors.Is(err, applicationintake.ErrStateConflict):
		return ToolError{Code: "precondition_failed"}
	case errors.Is(err, domainproject.ErrNotFound), errors.Is(err, repository.ErrNotFound), errors.Is(err, repository.ErrProjectMismatch), errors.Is(err, repository.ErrRequirementMissing), errors.Is(err, repository.ErrPlanMissing):
		return ToolError{Code: "not_found"}
	case errors.Is(err, repository.ErrLinkConflict):
		return ToolError{Code: "relation_conflict"}
	case errors.Is(err, repository.ErrIdempotencyKeyReuse):
		return ToolError{Code: "idempotency_key_reused"}
	case errors.Is(err, repository.ErrDuplicate):
		return ToolError{Code: "request_in_progress"}
	case errors.Is(err, domainfiles.ErrAttachmentTooLarge), errors.Is(err, domainfiles.ErrAttachmentLimit), errors.Is(err, domainfiles.ErrInvalidAttachment), errors.Is(err, domainfiles.ErrAttachmentContent):
		return ToolError{Code: "invalid_attachment"}
	case errors.Is(err, domainfiles.ErrAttachmentContentType):
		return ToolError{Code: "unsupported_media_type"}
	case errors.Is(err, domainfiles.ErrAttachmentState), errors.Is(err, domainfiles.ErrAttachmentRecovery):
		return ToolError{Code: "attachment_recovery_required"}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return ToolError{Code: "request_timeout"}
	case errors.Is(err, applicationintake.ErrUnavailable), errors.Is(err, applicationattachments.ErrUnavailable), errors.Is(err, domainproject.ErrUnavailable), errors.Is(err, repository.ErrNotConfigured):
		return ToolError{Code: "service_unavailable"}
	default:
		return ToolError{Code: "internal_error"}
	}
}
