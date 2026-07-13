package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/lyming99/autoplan/backend/internal/application"
	applicationattachments "github.com/lyming99/autoplan/backend/internal/application/attachments"
	applicationintake "github.com/lyming99/autoplan/backend/internal/application/intake"
	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
	domainintake "github.com/lyming99/autoplan/backend/internal/domain/intake"
	platformfilesystem "github.com/lyming99/autoplan/backend/internal/platform/filesystem"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const (
	AttachmentPath        = "/api/v1/attachments/{attachment_id}"
	AttachmentContentPath = AttachmentPath + "/content"
)

type AttachmentService interface {
	Upload(context.Context, applicationattachments.UploadCommand) (applicationattachments.UploadResult, error)
	Open(context.Context, int64, int64) (io.ReadCloser, applicationattachments.AttachmentDTO, error)
	Delete(context.Context, string, int64, int64) (applicationattachments.DeleteResult, error)
}

var _ AttachmentService = (*applicationattachments.Service)(nil)

// AttachmentOwnerReader performs the project-scoped owner existence check
// before the multipart body is consumed. It deliberately shares Intake's
// read use case instead of reaching into persistence from this transport.
type AttachmentOwnerReader interface {
	Get(context.Context, int64, domainintake.Type, int64) (applicationintake.IntakeDTO, error)
}

var _ AttachmentOwnerReader = (*applicationintake.Service)(nil)

func RegisterAttachments(router *Router, security *Security, service AttachmentService, owners AttachmentOwnerReader) error {
	if router == nil || security == nil || service == nil || owners == nil {
		return ErrRouterDependency
	}
	upload := security.Protect(TransportREST, attachmentUploadEndpoint(service, owners, router.BodyLimitBytes()))
	content := security.Protect(TransportREST, attachmentContentEndpoint(service))
	remove := security.Protect(TransportREST, attachmentDeleteEndpoint(service))
	registrations := []struct {
		method   string
		path     string
		endpoint Endpoint
	}{
		{http.MethodPost, RequirementAttachmentsPath, upload},
		{http.MethodPost, FeedbackAttachmentsPath, upload},
		{http.MethodGet, AttachmentContentPath, content},
		{http.MethodHead, AttachmentContentPath, content},
		{http.MethodDelete, AttachmentPath, remove},
	}
	for _, registration := range registrations {
		if err := router.HandlePattern(registration.method, registration.path, registration.endpoint); err != nil {
			return err
		}
	}
	return nil
}

func attachmentUploadEndpoint(service AttachmentService, owners AttachmentOwnerReader, bodyLimit int64) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		projectID, intakeType, intakeID, failure := intakePath(request.URL.Path, "attachments")
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		metadata, failure := mutationRequestContext(request)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		if failure := validateMultipartHeaders(request, bodyLimit); failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		if _, err := owners.Get(request.Context(), projectID, intakeType, intakeID); err != nil {
			writeIntakeServiceError(writer, request, err)
			return
		}
		upload, failure := parseMultipartUpload(writer, request, bodyLimit)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		ownerType := domainfiles.OwnerRequirement
		if intakeType == domainintake.Feedback {
			ownerType = domainfiles.OwnerFeedback
		}
		result, err := service.Upload(request.Context(), applicationattachments.UploadCommand{
			OperationID: attachmentOperationID("upload", metadata, projectID, intakeType, intakeID),
			ProjectID:   projectID, OwnerType: ownerType, OwnerID: intakeID,
			DisplayName: upload.DisplayName, MIMEType: upload.MIMEType, Content: upload.Content,
		})
		if err != nil {
			writeAttachmentServiceError(writer, request, err, bodyLimit)
			return
		}
		WriteResponse(writer, request, http.StatusCreated, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
	}
}

func validateMultipartHeaders(request *http.Request, limit int64) *APIError {
	if request == nil || limit <= 0 {
		failure := NewAPIError(CodeInternal, nil)
		return &failure
	}
	if request.ContentLength > limit {
		failure := NewAPIError(CodeBodyTooLarge, &ErrorDetails{LimitBytes: limit})
		return &failure
	}
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || strings.TrimSpace(parameters["boundary"]) == "" {
		failure := NewAPIError(CodeUnsupportedMediaType, nil)
		return &failure
	}
	return nil
}

func attachmentContentEndpoint(service AttachmentService) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		projectID, failure := attachmentProjectID(request.URL)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		attachmentID, failure := attachmentIDFromPath(request.URL.Path, true)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		content, attachment, err := service.Open(request.Context(), projectID, attachmentID)
		if err != nil {
			writeAttachmentServiceError(writer, request, err, 0)
			return
		}
		defer content.Close()
		start, length, partial, valid := parseAttachmentRange(request.Header.Get("Range"), attachment.Size)
		if !valid {
			writer.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(attachment.Size, 10))
			WriteError(writer, request, NewAPIError(CodeRangeNotSatisfiable, nil))
			return
		}
		end := start + length - 1
		writer.Header().Set("Content-Type", attachment.MIMEType)
		writer.Header().Set("Content-Disposition", attachmentDisposition(attachment.DisplayName))
		writer.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		writer.Header().Set("Accept-Ranges", "bytes")
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		if partial {
			writer.Header().Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(end, 10)+"/"+strconv.FormatInt(attachment.Size, 10))
			writer.WriteHeader(http.StatusPartialContent)
		} else {
			writer.WriteHeader(http.StatusOK)
		}
		if request.Method == http.MethodHead {
			return
		}
		if start > 0 {
			if _, err := io.CopyN(io.Discard, content, start); err != nil {
				return
			}
		}
		_, _ = io.CopyN(writer, content, length)
	}
}

func attachmentDeleteEndpoint(service AttachmentService) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		attachmentID, failure := attachmentIDFromPath(request.URL.Path, false)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		metadata, failure := mutationRequestContext(request)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		projectID, failure := attachmentProjectID(request.URL)
		if failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		result, err := service.Delete(request.Context(), attachmentOperationID("delete", metadata, projectID, "", attachmentID), projectID, attachmentID)
		if err != nil {
			writeAttachmentServiceError(writer, request, err, 0)
			return
		}
		WriteResponse(writer, request, http.StatusOK, responseEnvelope{Data: result, RequestID: RequestID(request.Context())})
	}
}

func attachmentIDFromPath(path string, content bool) (int64, *APIError) {
	prefix := "/api/v1/attachments/"
	value := strings.TrimPrefix(path, prefix)
	if content {
		value = strings.TrimSuffix(value, "/content")
	}
	if !strings.HasPrefix(path, prefix) || value == "" || strings.Contains(value, "/") {
		failure := NewAPIError(CodeNotFound, nil)
		return 0, &failure
	}
	attachmentID, valid := parseCanonicalPositiveID(value)
	if !valid {
		failure := NewAPIError(CodeInvalidAttachment, &ErrorDetails{Field: "attachment_id"})
		return 0, &failure
	}
	return attachmentID, nil
}

func attachmentProjectID(location *url.URL) (int64, *APIError) {
	if location == nil {
		failure := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
		return 0, &failure
	}
	values, err := url.ParseQuery(location.RawQuery)
	if err != nil || len(values) != 1 || len(values["project_id"]) != 1 || values["project_id"][0] == "" {
		failure := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
		return 0, &failure
	}
	projectID, valid := parseCanonicalPositiveID(values["project_id"][0])
	if !valid {
		failure := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
		return 0, &failure
	}
	return projectID, nil
}

func attachmentOperationID(kind string, metadata mutationContext, projectID int64, intakeType domainintake.Type, resourceID int64) string {
	identity := metadata.IdempotencyKey
	if identity == "" {
		identity = metadata.RequestID
	}
	digest := sha256.Sum256([]byte("autoplan-p06-attachment\x00" + kind + "\x00" + metadata.CallerScope + "\x00" + identity + "\x00" + strconv.FormatInt(projectID, 10) + "\x00" + string(intakeType) + "\x00" + strconv.FormatInt(resourceID, 10)))
	return "file-" + kind + "-" + hex.EncodeToString(digest[:16])
}

func parseAttachmentRange(value string, size int64) (int64, int64, bool, bool) {
	if size <= 0 {
		return 0, 0, false, false
	}
	if value == "" {
		return 0, size, false, true
	}
	if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return 0, 0, false, false
	}
	bounds := strings.Split(strings.TrimPrefix(value, "bytes="), "-")
	if len(bounds) != 2 || (bounds[0] == "" && bounds[1] == "") {
		return 0, 0, false, false
	}
	if bounds[0] == "" {
		suffix, err := strconv.ParseInt(bounds[1], 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, false, false
		}
		if suffix > size {
			suffix = size
		}
		return size - suffix, suffix, true, true
	}
	start, err := strconv.ParseInt(bounds[0], 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false, false
	}
	end := size - 1
	if bounds[1] != "" {
		end, err = strconv.ParseInt(bounds[1], 10, 64)
		if err != nil || end < start {
			return 0, 0, false, false
		}
		if end >= size {
			end = size - 1
		}
	}
	return start, end - start + 1, true, true
}

func attachmentDisposition(displayName string) string {
	name := strings.NewReplacer("\\", "_", "\"", "_", "\r", "_", "\n", "_").Replace(displayName)
	return "attachment; filename=\"" + name + "\""
}

func writeAttachmentServiceError(writer http.ResponseWriter, request *http.Request, err error, limit int64) {
	code := CodeInternal
	var maximum *http.MaxBytesError
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = CodeRequestTimeout
	case errors.Is(err, errMultipartExtraPart):
		code = CodeInvalidAttachment
	case errors.As(err, &maximum):
		details := &ErrorDetails{LimitBytes: limit}
		if limit <= 0 {
			details = nil
		}
		WriteError(writer, request, NewAPIError(CodeBodyTooLarge, details))
		return
	case errors.Is(err, domainfiles.ErrAttachmentTooLarge):
		code = CodeBodyTooLarge
	case errors.Is(err, domainfiles.ErrAttachmentContentType):
		code = CodeUnsupportedMediaType
	case errors.Is(err, domainfiles.ErrInvalidAttachment), errors.Is(err, domainfiles.ErrAttachmentLimit), errors.Is(err, domainfiles.ErrAttachmentContent):
		code = CodeInvalidAttachment
	case errors.Is(err, domainfiles.ErrAttachmentState), errors.Is(err, domainfiles.ErrAttachmentRecovery):
		code = CodeAttachmentRecovery
	case errors.Is(err, repository.ErrNotFound), errors.Is(err, repository.ErrProjectMismatch):
		code = CodeAttachmentNotFound
	case errors.Is(err, repository.ErrIdempotencyKeyReuse):
		code = CodeIdempotencyKeyReused
	case errors.Is(err, repository.ErrDuplicate):
		code = CodeRequestInProgress
	case errors.Is(err, repository.ErrTransaction), errors.Is(err, repository.ErrCommit), errors.Is(err, repository.ErrRollback):
		code = CodeRepositoryBusy
	case errors.Is(err, repository.ErrSchemaDrift):
		code = CodeRepositorySchemaDrift
	case errors.Is(err, platformfilesystem.ErrAttachmentStoreInvalid), errors.Is(err, platformfilesystem.ErrAttachmentStoreUnsafe),
		errors.Is(err, platformfilesystem.ErrAttachmentStoreExists):
		code = CodeInsufficientStorage
	case errors.Is(err, applicationattachments.ErrUnavailable), errors.Is(err, repository.ErrNotConfigured),
		errors.Is(err, repository.ErrUnsafePath), errors.Is(err, repository.ErrInvalidStore),
		errors.Is(err, repository.ErrSourceChanged), errors.Is(err, repository.ErrClosed), errors.Is(err, repository.ErrWriterUnauthorized):
		code = CodeRepositoryUnavailable
	}
	WriteError(writer, request, NewAPIError(code, nil))
}
