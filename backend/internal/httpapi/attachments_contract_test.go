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
	applicationintake "github.com/lyming99/autoplan/backend/internal/application/intake"
	"github.com/lyming99/autoplan/backend/internal/config"
	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
	domainintake "github.com/lyming99/autoplan/backend/internal/domain/intake"
	"github.com/lyming99/autoplan/backend/internal/platform/session"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

type p008AttachmentHTTPSpy struct {
	uploadCalls int
	openCalls   int
	deleteCalls int
	uploadErr   error
}

func (spy *p008AttachmentHTTPSpy) Upload(_ context.Context, command applicationattachments.UploadCommand) (applicationattachments.UploadResult, error) {
	spy.uploadCalls++
	if _, err := io.ReadAll(command.Content); err != nil {
		return applicationattachments.UploadResult{}, err
	}
	if spy.uploadErr != nil {
		return applicationattachments.UploadResult{}, spy.uploadErr
	}
	return applicationattachments.UploadResult{
		Attachment: applicationattachments.AttachmentDTO{ID: 8, DisplayName: command.DisplayName, Size: 5, MIMEType: "text/plain", DownloadURL: "/api/v1/attachments/8/content"},
		State:      domainfiles.StateReady,
	}, nil
}

func (spy *p008AttachmentHTTPSpy) Open(_ context.Context, projectID, attachmentID int64) (io.ReadCloser, applicationattachments.AttachmentDTO, error) {
	spy.openCalls++
	if projectID != 1 || attachmentID != 8 {
		return nil, applicationattachments.AttachmentDTO{}, repository.ErrNotFound
	}
	return io.NopCloser(strings.NewReader("hello")), applicationattachments.AttachmentDTO{ID: 8, DisplayName: "fixture.txt", Size: 5, MIMEType: "text/plain"}, nil
}

func (spy *p008AttachmentHTTPSpy) Delete(_ context.Context, _ string, projectID, attachmentID int64) (applicationattachments.DeleteResult, error) {
	spy.deleteCalls++
	if projectID != 1 || attachmentID != 8 {
		return applicationattachments.DeleteResult{}, repository.ErrNotFound
	}
	return applicationattachments.DeleteResult{AttachmentID: 8, State: domainfiles.StateComplete}, nil
}

type p008AttachmentOwnerSpy struct {
	calls int
	err   error
}

func (spy *p008AttachmentOwnerSpy) Get(context.Context, int64, domainintake.Type, int64) (applicationintake.IntakeDTO, error) {
	spy.calls++
	return applicationintake.IntakeDTO{}, spy.err
}

func newP008AttachmentHTTPRouter(t *testing.T, service AttachmentService, owners AttachmentOwnerReader) (*Router, string) {
	t.Helper()
	clock := fixedClock{value: time.Date(2026, 7, 11, 4, 0, 0, 0, time.UTC)}
	manager, err := session.New(bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)))
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
	router, err := NewRouter(RouterOptions{Application: &testApplication{}, Logger: &recordingLogger{}, Clock: clock, RequestIDs: fixedRequestIDs{}, BodyLimitBytes: 512})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterAttachments(router, security, service, owners); err != nil {
		t.Fatal(err)
	}
	return router, string(manager.CredentialCopy())
}

func TestP008AttachmentAuthorizationAndOwnerChecksHappenBeforeBytePersistence(t *testing.T) {
	service := &p008AttachmentHTTPSpy{}
	owners := &p008AttachmentOwnerSpy{}
	router, credential := newP008AttachmentHTTPRouter(t, service, owners)
	path := "/api/v1/projects/1/requirements/2/attachments"
	for _, item := range []struct {
		name    string
		prepare func(*http.Request)
		status  int
		code    ErrorCode
	}{
		{
			name:    "missing session",
			prepare: func(request *http.Request) { request.Header.Set("Origin", testOrigin) },
			status:  http.StatusUnauthorized, code: CodeUnauthorized,
		},
		{
			name: "forged origin",
			prepare: func(request *http.Request) {
				request.Header.Set("Origin", "http://127.0.0.1:43125")
				request.Header.Set(session.HeaderName, credential)
			},
			status: http.StatusForbidden, code: CodeOriginForbidden,
		},
		{
			name: "forged host",
			prepare: func(request *http.Request) {
				request.Host = "127.0.0.1:43124"
				request.Header.Set("Origin", testOrigin)
				request.Header.Set(session.HeaderName, credential)
			},
			status: http.StatusForbidden, code: CodeOriginForbidden,
		},
		{
			name: "forwarded host",
			prepare: func(request *http.Request) {
				request.Header.Set("Origin", testOrigin)
				request.Header.Set(session.HeaderName, credential)
				request.Header.Set("X-Forwarded-Host", "private.example")
			},
			status: http.StatusForbidden, code: CodeOriginForbidden,
		},
	} {
		t.Run(item.name, func(t *testing.T) {
			body, contentType := p008Multipart(t, "fixture.txt", "hello", false)
			request := httptest.NewRequest(http.MethodPost, "http://"+testAuthority+path, body)
			request.Header.Set("Content-Type", contentType)
			item.prepare(request)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assertContractError(t, response, item.status, string(item.code), false)
		})
	}
	if service.uploadCalls != 0 || owners.calls != 0 {
		t.Fatalf("unauthorized attachment request reached owner=%d service=%d", owners.calls, service.uploadCalls)
	}

	owners.err = repository.ErrNotFound
	response := p008AttachmentRequest(t, router, credential, http.MethodPost, path, "fixture.txt", "hello", "owner-not-found")
	assertContractError(t, response, http.StatusNotFound, string(CodeIntakeNotFound), false)
	if owners.calls != 1 || service.uploadCalls != 0 {
		t.Fatalf("missing owner must reject before attachment persistence: owner=%d service=%d", owners.calls, service.uploadCalls)
	}

	owners.err = repository.ErrWriterUnauthorized
	response = p008AttachmentRequest(t, router, credential, http.MethodPost, path, "fixture.txt", "hello", "owner-unavailable")
	assertContractError(t, response, http.StatusServiceUnavailable, string(CodeRepositoryUnavailable), true)
	if owners.calls != 2 || service.uploadCalls != 0 {
		t.Fatalf("owner unavailability must reject before attachment persistence: owner=%d service=%d", owners.calls, service.uploadCalls)
	}

	contentRequest := httptest.NewRequest(http.MethodGet, "http://"+testAuthority+"/api/v1/attachments/8/content?project_id=1", nil)
	contentRequest.Header.Set("Origin", testOrigin)
	contentResponse := httptest.NewRecorder()
	router.ServeHTTP(contentResponse, contentRequest)
	assertContractError(t, contentResponse, http.StatusUnauthorized, string(CodeUnauthorized), false)
	deleteRequest := httptest.NewRequest(http.MethodDelete, "http://"+testAuthority+"/api/v1/attachments/8?project_id=1", nil)
	deleteRequest.Header.Set("Origin", testOrigin)
	deleteResponse := httptest.NewRecorder()
	router.ServeHTTP(deleteResponse, deleteRequest)
	assertContractError(t, deleteResponse, http.StatusUnauthorized, string(CodeUnauthorized), false)
	if service.openCalls != 0 || service.deleteCalls != 0 {
		t.Fatalf("unauthorized attachment content reached open=%d delete=%d", service.openCalls, service.deleteCalls)
	}
}

func TestP008AttachmentMultipartAndDownloadContractRejectsUnsafeInput(t *testing.T) {
	service := &p008AttachmentHTTPSpy{}
	owners := &p008AttachmentOwnerSpy{}
	router, credential := newP008AttachmentHTTPRouter(t, service, owners)
	path := "/api/v1/projects/1/feedback/2/attachments"

	extraBody, extraType := p008Multipart(t, "fixture.txt", "hello", true)
	extraRequest := httptest.NewRequest(http.MethodPost, "http://"+testAuthority+path, extraBody)
	extraRequest.Header.Set("Origin", testOrigin)
	extraRequest.Header.Set(session.HeaderName, credential)
	extraRequest.Header.Set("Content-Type", extraType)
	extraResponse := httptest.NewRecorder()
	router.ServeHTTP(extraResponse, extraRequest)
	assertContractError(t, extraResponse, http.StatusUnprocessableEntity, string(CodeInvalidAttachment), false)

	service.uploadErr = domainfiles.ErrAttachmentContentType
	response := p008AttachmentRequest(t, router, credential, http.MethodPost, path, "fixture.png", "not-a-png", "mime-mismatch")
	assertContractError(t, response, http.StatusUnsupportedMediaType, string(CodeUnsupportedMediaType), false)

	service.uploadErr = domainfiles.ErrInvalidAttachment
	response = p008AttachmentRequest(t, router, credential, http.MethodPost, path, "../dangerous.txt", "hello", "dangerous-name")
	assertContractError(t, response, http.StatusUnprocessableEntity, string(CodeInvalidAttachment), false)

	largeBody, largeType := p008Multipart(t, "fixture.txt", strings.Repeat("x", 2048), false)
	largeRequest := httptest.NewRequest(http.MethodPost, "http://"+testAuthority+path, largeBody)
	largeRequest.ContentLength = -1 // exercise the chunked/unknown-length path guarded by MaxBytesReader.
	largeRequest.Header.Set("Origin", testOrigin)
	largeRequest.Header.Set(session.HeaderName, credential)
	largeRequest.Header.Set("Content-Type", largeType)
	largeRequest.Header.Set(IdempotencyKeyHeader, "chunked-large")
	largeResponse := httptest.NewRecorder()
	router.ServeHTTP(largeResponse, largeRequest)
	assertContractError(t, largeResponse, http.StatusRequestEntityTooLarge, string(CodeBodyTooLarge), false)

	content := p008AttachmentContentRequest(router, credential, http.MethodGet, "/api/v1/attachments/8/content?project_id=1")
	if content.Code != http.StatusOK || content.Body.String() != "hello" || content.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("controlled download drifted: status=%d headers=%v body=%q", content.Code, content.Header(), content.Body.String())
	}
	rangedRequest := httptest.NewRequest(http.MethodGet, "http://"+testAuthority+"/api/v1/attachments/8/content?project_id=1", nil)
	rangedRequest.Header.Set("Origin", testOrigin)
	rangedRequest.Header.Set(session.HeaderName, credential)
	rangedRequest.Header.Set("Range", "bytes=1-3")
	ranged := httptest.NewRecorder()
	router.ServeHTTP(ranged, rangedRequest)
	if ranged.Code != http.StatusPartialContent || ranged.Body.String() != "ell" || ranged.Header().Get("Content-Range") != "bytes 1-3/5" {
		t.Fatalf("controlled range download drifted: status=%d range=%q body=%q", ranged.Code, ranged.Header().Get("Content-Range"), ranged.Body.String())
	}
	deleted := p008AttachmentContentRequest(router, credential, http.MethodDelete, "/api/v1/attachments/8?project_id=1")
	if deleted.Code != http.StatusOK || service.deleteCalls != 1 {
		t.Fatalf("attachment delete drifted: status=%d calls=%d body=%s", deleted.Code, service.deleteCalls, deleted.Body.String())
	}
}

func p008Multipart(t *testing.T, filename, contents string, extra bool) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, contents); err != nil {
		t.Fatal(err)
	}
	if extra {
		part, err = writer.CreateFormFile("second", "second.txt")
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

func p008AttachmentRequest(t *testing.T, router http.Handler, credential, method, target, filename, contents, key string) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := p008Multipart(t, filename, contents, false)
	request := httptest.NewRequest(method, "http://"+testAuthority+target, body)
	request.Header.Set("Origin", testOrigin)
	request.Header.Set(session.HeaderName, credential)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set(IdempotencyKeyHeader, key)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func p008AttachmentContentRequest(router http.Handler, credential, method, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://"+testAuthority+target, nil)
	request.Header.Set("Origin", testOrigin)
	request.Header.Set(session.HeaderName, credential)
	if method == http.MethodDelete {
		request.Header.Set(IdempotencyKeyHeader, "delete-contract")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
