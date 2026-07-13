package mcp

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	applicationattachments "github.com/lyming99/autoplan/backend/internal/application/attachments"
	applicationintake "github.com/lyming99/autoplan/backend/internal/application/intake"
	"github.com/lyming99/autoplan/backend/internal/domain/contracts"
	domainintake "github.com/lyming99/autoplan/backend/internal/domain/intake"
	domainproject "github.com/lyming99/autoplan/backend/internal/domain/project"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

type intakeFixture struct {
	listQuery applicationintake.ListQuery
	list      []applicationintake.IntakeDTO
	create    applicationintake.CreateCommand
}

func (fixture *intakeFixture) List(_ context.Context, query applicationintake.ListQuery) ([]applicationintake.IntakeDTO, error) {
	fixture.listQuery = query
	return fixture.list, nil
}

func (fixture *intakeFixture) Get(context.Context, int64, domainintake.Type, int64) (applicationintake.IntakeDTO, error) {
	return applicationintake.IntakeDTO{ID: 9, ProjectID: 4, Type: domainintake.Requirement, LinkedPlans: []applicationintake.LinkedPlanDTO{}}, nil
}

func (fixture *intakeFixture) Create(_ context.Context, command applicationintake.CreateCommand, _ domainproject.Visibility) (applicationintake.MutationResult, error) {
	fixture.create = command
	return applicationintake.MutationResult{}, nil
}

func (fixture *intakeFixture) Update(context.Context, applicationintake.UpdateCommand, domainproject.Visibility) (applicationintake.MutationResult, error) {
	return applicationintake.MutationResult{}, nil
}

func (fixture *intakeFixture) SetAcceptance(context.Context, applicationintake.AcceptanceCommand, domainproject.Visibility) (applicationintake.MutationResult, error) {
	return applicationintake.MutationResult{}, nil
}

func (fixture *intakeFixture) Links(context.Context, int64, domainintake.Type, int64) ([]applicationintake.LinkedPlanDTO, error) {
	return []applicationintake.LinkedPlanDTO{}, nil
}

func (fixture *intakeFixture) ReplaceLinks(context.Context, applicationintake.ReplaceLinksCommand, domainproject.Visibility) (applicationintake.MutationResult, error) {
	return applicationintake.MutationResult{}, nil
}

func (fixture *intakeFixture) Delete(context.Context, applicationintake.DeleteCommand, domainproject.Visibility) (applicationintake.MutationResult, error) {
	return applicationintake.MutationResult{}, nil
}

type attachmentFixture struct {
	upload applicationattachments.UploadCommand
}

func (fixture *attachmentFixture) Upload(_ context.Context, command applicationattachments.UploadCommand) (applicationattachments.UploadResult, error) {
	fixture.upload = command
	if _, err := io.ReadAll(command.Content); err != nil {
		return applicationattachments.UploadResult{}, err
	}
	return applicationattachments.UploadResult{Attachment: applicationattachments.AttachmentDTO{ID: 8, DisplayName: command.DisplayName, MIMEType: "text/plain", Size: 4, DownloadURL: "/api/v1/attachments/8/content"}}, nil
}

func (fixture *attachmentFixture) UploadFromAuthorizedPath(context.Context, string, string, applicationattachments.UploadCommand) (applicationattachments.UploadResult, error) {
	return applicationattachments.UploadResult{}, errors.New("path upload must not be called")
}

func (fixture *attachmentFixture) Open(context.Context, int64, int64) (io.ReadCloser, applicationattachments.AttachmentDTO, error) {
	return io.NopCloser(strings.NewReader("safe")), applicationattachments.AttachmentDTO{ID: 8, DisplayName: "safe.txt", Size: 4, MIMEType: "text/plain", DownloadURL: "/api/v1/attachments/8/content"}, nil
}

func (fixture *attachmentFixture) Delete(context.Context, string, int64, int64) (applicationattachments.DeleteResult, error) {
	return applicationattachments.DeleteResult{}, nil
}

type projectFixture struct{}

func (projectFixture) Get(context.Context, int64, domainproject.Visibility) (contracts.Project, error) {
	return contracts.Project{ID: 4, WorkspacePath: "safe-workspace"}, nil
}

func TestIntakeToolsUseSharedApplicationsAndSafeAttachmentInput(t *testing.T) {
	intakes := &intakeFixture{list: []applicationintake.IntakeDTO{{ID: 7, ProjectID: 4, Type: domainintake.Requirement, LinkedPlans: []applicationintake.LinkedPlanDTO{}}}}
	attachments := &attachmentFixture{}
	tools := NewIntakeTools(Dependencies{Intake: intakes, Attachments: attachments, Projects: projectFixture{}})
	status := domainintake.StatusOpen
	items, err := tools.ListRequirements(context.Background(), ListRequest{ProjectID: 4, Status: &status, Limit: 10})
	if err != nil || len(items) != 1 || intakes.listQuery.ProjectID != 4 || intakes.listQuery.Type != domainintake.Requirement {
		t.Fatalf("list=%#v query=%#v err=%v", items, intakes.listQuery, err)
	}
	_, err = tools.CreateFeedback(context.Background(), ToolContext{CallerScope: "mcp-test", IdempotencyKey: "create-1", RequestID: "mcp-test-1"}, CreateRequest{ProjectID: 4, Title: "Title", Body: "Body", Status: domainintake.StatusOpen})
	if err != nil || intakes.create.Metadata.CallerScope != "mcp-test" || intakes.create.Metadata.IdempotencyKey != "create-1" {
		t.Fatalf("create=%#v err=%v", intakes.create, err)
	}
	uploaded, err := tools.UploadRequirementAttachment(context.Background(), ToolContext{CallerScope: "mcp-test", RequestID: "upload-1"}, ItemRequest{ProjectID: 4, ID: 7}, AttachmentInput{Name: "safe.txt", Bytes: []byte("safe")})
	if err != nil || uploaded.Attachment.ID != 8 || attachments.upload.OwnerID != 7 || attachments.upload.Content == nil {
		t.Fatalf("uploaded=%#v command=%#v err=%v", uploaded, attachments.upload, err)
	}
}

func TestIntakeToolsRejectNonLocalPathWithoutOpeningIt(t *testing.T) {
	tools := NewIntakeTools(Dependencies{Intake: &intakeFixture{}, Attachments: &attachmentFixture{}, Projects: projectFixture{}})
	_, err := tools.UploadFeedbackAttachment(context.Background(), ToolContext{CallerScope: "remote"}, ItemRequest{ProjectID: 4, ID: 9}, AttachmentInput{Name: "safe.txt", Path: "C:\\secret\\safe.txt"})
	var failure ToolError
	if !errors.As(err, &failure) || failure.Code != "attachment_path_denied" || strings.Contains(err.Error(), "secret") {
		t.Fatalf("path failure=%v", err)
	}
}

func TestToolErrorsNeverExposeRepositoryDetail(t *testing.T) {
	err := mapError(repository.ErrProjectMismatch)
	var failure ToolError
	if !errors.As(err, &failure) || failure.Code != "not_found" || strings.Contains(err.Error(), "repository") {
		t.Fatalf("mapped error=%v", err)
	}
}

var _ IntakeApplication = (*intakeFixture)(nil)
var _ AttachmentApplication = (*attachmentFixture)(nil)
var _ ProjectApplication = projectFixture{}
