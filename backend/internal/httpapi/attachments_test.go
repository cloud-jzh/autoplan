package httpapi

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	applicationattachments "github.com/lyming99/autoplan/backend/internal/application/attachments"
	"github.com/lyming99/autoplan/backend/internal/config"
	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
	"github.com/lyming99/autoplan/backend/internal/platform/session"
)

type p005AttachmentFixture struct {
	uploadCalls int
	deleteCalls int
	lastUpload  applicationattachments.UploadCommand
	content     string
}

func (fixture *p005AttachmentFixture) Upload(_ context.Context, command applicationattachments.UploadCommand) (applicationattachments.UploadResult, error) {
	fixture.uploadCalls++
	fixture.lastUpload = command
	if _, err := io.ReadAll(command.Content); err != nil {
		return applicationattachments.UploadResult{}, err
	}
	return applicationattachments.UploadResult{Attachment: applicationattachments.AttachmentDTO{ID: 8, DisplayName: command.DisplayName, Size: 5, MIMEType: "text/plain", DownloadURL: "/api/v1/attachments/8/content"}, State: domainfiles.StateReady}, nil
}

func (fixture *p005AttachmentFixture) Open(_ context.Context, projectID, attachmentID int64) (io.ReadCloser, applicationattachments.AttachmentDTO, error) {
	if projectID != 4 || attachmentID != 8 {
		return nil, applicationattachments.AttachmentDTO{}, domainfiles.ErrInvalidAttachment
	}
	return io.NopCloser(strings.NewReader(fixture.content)), applicationattachments.AttachmentDTO{ID: 8, DisplayName: "fixture.txt", Size: int64(len(fixture.content)), MIMEType: "text/plain"}, nil
}

func (fixture *p005AttachmentFixture) Delete(_ context.Context, _ string, projectID, attachmentID int64) (applicationattachments.DeleteResult, error) {
	fixture.deleteCalls++
	if projectID != 4 || attachmentID != 8 {
		return applicationattachments.DeleteResult{}, domainfiles.ErrInvalidAttachment
	}
	return applicationattachments.DeleteResult{AttachmentID: attachmentID, State: domainfiles.StateComplete}, nil
}

func TestP005AttachmentMultipartAndProjectScopedContent(t *testing.T) {
	service := &p005AttachmentFixture{content: "hello"}
	router, credential := newP005AttachmentRouter(t, service)
	body, contentType := p005MultipartBody(t, "fixture.txt", "hello", false)
	upload := httptest.NewRequest(http.MethodPost, "http://"+testAuthority+"/api/v1/projects/4/requirements/2/attachments", body)
	upload.Header.Set("Origin", testOrigin)
	upload.Header.Set(session.HeaderName, credential)
	upload.Header.Set("Content-Type", contentType)
	upload.Header.Set(IdempotencyKeyHeader, "upload-p005")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, upload)
	if response.Code != http.StatusCreated || service.uploadCalls != 1 || service.lastUpload.OwnerType != domainfiles.OwnerRequirement || service.lastUpload.OwnerID != 2 || service.lastUpload.OperationID == "" {
		t.Fatalf("upload status=%d command=%#v body=%s", response.Code, service.lastUpload, response.Body.String())
	}

	missingProject := serveP005AttachmentRequest(router, credential, http.MethodGet, "/api/v1/attachments/8/content", nil, "")
	assertContractError(t, missingProject, http.StatusBadRequest, string(CodeInvalidProjectID), false)

	content := serveP005AttachmentRequest(router, credential, http.MethodGet, "/api/v1/attachments/8/content?project_id=4", nil, "")
	if content.Code != http.StatusOK || content.Body.String() != "hello" || content.Header().Get("Content-Type") != "text/plain" || content.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("content status=%d headers=%v body=%q", content.Code, content.Header(), content.Body.String())
	}
	ranged := httptest.NewRequest(http.MethodGet, "http://"+testAuthority+"/api/v1/attachments/8/content?project_id=4", nil)
	ranged.Header.Set("Origin", testOrigin)
	ranged.Header.Set(session.HeaderName, credential)
	ranged.Header.Set("Range", "bytes=1-3")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, ranged)
	if response.Code != http.StatusPartialContent || response.Body.String() != "ell" || response.Header().Get("Content-Range") != "bytes 1-3/5" {
		t.Fatalf("range status=%d range=%q body=%q", response.Code, response.Header().Get("Content-Range"), response.Body.String())
	}
}

func TestP005AttachmentRejectsExtraMultipartPartsBeforePersistence(t *testing.T) {
	service := &p005AttachmentFixture{content: "hello"}
	router, credential := newP005AttachmentRouter(t, service)
	body, contentType := p005MultipartBody(t, "fixture.txt", "hello", true)
	request := httptest.NewRequest(http.MethodPost, "http://"+testAuthority+"/api/v1/projects/4/feedback/2/attachments", body)
	request.Header.Set("Origin", testOrigin)
	request.Header.Set(session.HeaderName, credential)
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertContractError(t, response, http.StatusUnprocessableEntity, string(CodeInvalidAttachment), false)
	if service.uploadCalls != 1 {
		t.Fatal("multipart content must be checked by the staging reader before persistence")
	}
}

func newP005AttachmentRouter(t *testing.T, service AttachmentService) (*Router, string) {
	t.Helper()
	clock := fixedClock{value: time.Date(2026, 7, 11, 4, 0, 0, 0, time.UTC)}
	manager, err := session.New(bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	origins, err := config.NewOriginSet([]string{testOrigin})
	if err != nil {
		t.Fatal(err)
	}
	security, err := NewSecurity(SecurityOptions{Sessions: manager, Origins: origins, ExpectedHost: config.DefaultListenHost, ExpectedPort: 43123, Logger: &recordingLogger{}, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterOptions{Application: &testApplication{}, Logger: &recordingLogger{}, Clock: clock, RequestIDs: fixedRequestIDs{}, BodyLimitBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterAttachments(router, security, service, &p005IntakeFixture{}); err != nil {
		t.Fatal(err)
	}
	return router, string(manager.CredentialCopy())
}

func p005MultipartBody(t *testing.T, filename, value string, extra bool) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, value); err != nil {
		t.Fatal(err)
	}
	if extra {
		part, err = writer.CreateFormFile("other", "extra.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, "extra"); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, writer.FormDataContentType()
}

func serveP005AttachmentRequest(router http.Handler, credential, method, target string, body io.Reader, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://"+testAuthority+target, body)
	request.Header.Set("Origin", testOrigin)
	request.Header.Set(session.HeaderName, credential)
	if key != "" {
		request.Header.Set(IdempotencyKeyHeader, key)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
