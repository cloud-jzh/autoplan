package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lyming99/autoplan/backend/internal/config"
	"github.com/lyming99/autoplan/backend/internal/domain/contracts"
	domainevents "github.com/lyming99/autoplan/backend/internal/domain/events"
	"github.com/lyming99/autoplan/backend/internal/platform/session"
	"github.com/lyming99/autoplan/backend/internal/runtime/eventbus"
)

func TestProjectSSEReplaysCommittedEnvelopeAndReleasesOnClientCancel(t *testing.T) {
	store := &sseHTTPStore{events: []domainevents.Envelope{sseOperationEvent("1", 1)}}
	clock := operationHTTPClock{value: time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)}
	bus := eventbus.NewBus(eventbus.Options{Store: store, Clock: clock, SubscriptionBuffer: 2, ReplayLimit: 10})
	projects := &projectServiceFixture{project: contracts.Project{ID: 7, Name: "fixture"}}
	operations := &operationHTTPService{operation: operationHTTPFixture(7, "operation-7", 1)}
	router, credential := newSSEHTTPRouter(t, projects, operations, bus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptestNewSSERequest(ctx, http.MethodGet, "/api/v1/projects/7/events", credential)
	writer := &cancelAfterSSEWriter{cancel: cancel, marker: "event: operation.running"}
	router.ServeHTTP(writer, request)

	body := writer.Body.String()
	if writer.status != http.StatusOK || writer.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" ||
		writer.Header().Get("Cache-Control") != "no-store" || !strings.Contains(body, "retry: 3000") ||
		!strings.Contains(body, "id: 1\n") || !strings.Contains(body, "event: operation.running\n") {
		t.Fatalf("SSE replay contract drifted: status=%d headers=%v body=%q", writer.status, writer.Header(), body)
	}
	if strings.Contains(strings.ToLower(body), "stdout") || strings.Contains(strings.ToLower(body), "credential") {
		t.Fatal("SSE payload exposed forbidden runtime material")
	}
}

func TestOperationSSEChecksScopeAndReturnsCursorResync(t *testing.T) {
	store := &sseHTTPStore{}
	clock := operationHTTPClock{value: time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)}
	bus := eventbus.NewBus(eventbus.Options{Store: store, Clock: clock, SubscriptionBuffer: 1, ReplayLimit: 10})
	projects := &projectServiceFixture{project: contracts.Project{ID: 7, Name: "fixture"}}
	operations := &operationHTTPService{operation: operationHTTPFixture(7, "operation-7", 1)}
	router, credential := newSSEHTTPRouter(t, projects, operations, bus)

	missingScope := httptestNewSSERequest(context.Background(), http.MethodGet, "/api/v1/operations/operation-7/events", credential)
	missingWriter := newSSEWriter()
	router.ServeHTTP(missingWriter, missingScope)
	assertContractError(t, missingWriter.recorder(), http.StatusBadRequest, string(CodeInvalidProjectID), false)
	if operations.getCalls != 0 {
		t.Fatal("operation event endpoint reached service without project scope")
	}

	resync := httptestNewSSERequest(context.Background(), http.MethodGet,
		"/api/v1/operations/operation-7/events?project_id=7", credential)
	resync.Header.Set("Last-Event-ID", "not-a-cursor")
	resyncWriter := newSSEWriter()
	router.ServeHTTP(resyncWriter, resync)
	body := resyncWriter.Body.String()
	if resyncWriter.status != http.StatusOK || !strings.Contains(body, "event: resync_required\n") ||
		!strings.Contains(body, `"reason":"last_event_id_invalid"`) || strings.Contains(body, "id:") {
		t.Fatalf("cursor resync contract drifted: status=%d body=%q", resyncWriter.status, body)
	}
}

func newSSEHTTPRouter(t *testing.T, projects ProjectService, operations OperationService, bus *eventbus.Bus) (*Router, string) {
	t.Helper()
	clock := operationHTTPClock{value: time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)}
	manager, err := session.New(bytes.NewReader(bytes.Repeat([]byte{0x5b}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	origins, err := config.NewOriginSet([]string{operationTestOrigin})
	if err != nil {
		t.Fatal(err)
	}
	logger := operationHTTPLogger{}
	security, err := NewSecurity(SecurityOptions{
		Sessions: manager, Origins: origins, ExpectedHost: config.DefaultListenHost, ExpectedPort: 43123,
		Logger: logger, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterOptions{
		Application: operationHTTPBoundary{}, Logger: logger, Clock: clock,
		RequestIDs: operationHTTPRequestIDs{}, BodyLimitBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterEvents(router, security, projects, operations, bus); err != nil {
		t.Fatal(err)
	}
	return router, string(manager.CredentialCopy())
}

func httptestNewSSERequest(ctx context.Context, method, target, credential string) *http.Request {
	request := httptest.NewRequest(method, "http://"+operationTestAuthority+target, nil).WithContext(ctx)
	request.Header.Set("Origin", operationTestOrigin)
	request.Header.Set(session.HeaderName, credential)
	request.Header.Set(RequestIDHeader, "req_sse_fixture")
	request.Header.Set("Accept", "text/event-stream")
	return request
}

type cancelAfterSSEWriter struct {
	header http.Header
	Body   bytes.Buffer
	status int
	cancel context.CancelFunc
	marker string
}

func (writer *cancelAfterSSEWriter) Header() http.Header {
	if writer.header == nil {
		writer.header = make(http.Header)
	}
	return writer.header
}

func (writer *cancelAfterSSEWriter) WriteHeader(status int) {
	if writer.status == 0 {
		writer.status = status
	}
}

func (writer *cancelAfterSSEWriter) Write(value []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	count, err := writer.Body.Write(value)
	if writer.cancel != nil && strings.Contains(writer.Body.String(), writer.marker) {
		writer.cancel()
		writer.cancel = nil
	}
	return count, err
}

func (writer *cancelAfterSSEWriter) Flush() {}

type sseWriter struct {
	header http.Header
	Body   bytes.Buffer
	status int
}

func newSSEWriter() *sseWriter                { return &sseWriter{header: make(http.Header)} }
func (writer *sseWriter) Header() http.Header { return writer.header }
func (writer *sseWriter) WriteHeader(status int) {
	if writer.status == 0 {
		writer.status = status
	}
}
func (writer *sseWriter) Write(value []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	return writer.Body.Write(value)
}
func (writer *sseWriter) Flush() {}
func (writer *sseWriter) recorder() *httptest.ResponseRecorder {
	result := httptest.NewRecorder()
	for name, values := range writer.header {
		result.Header()[name] = append([]string(nil), values...)
	}
	result.Code = writer.status
	_, _ = result.Body.Write(writer.Body.Bytes())
	return result
}

type sseHTTPStore struct {
	mu     sync.Mutex
	events []domainevents.Envelope
}

func (store *sseHTTPStore) Check(ctx context.Context) error { return ctx.Err() }

func (store *sseHTTPStore) ListProjects(context.Context) ([]int64, error) { return []int64{7}, nil }

func (store *sseHTTPStore) Transact(ctx context.Context, operation func(eventbus.Transaction) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation(sseHTTPTransaction{store: store})
}

type sseHTTPTransaction struct{ store *sseHTTPStore }

func (transaction sseHTTPTransaction) ListOutbox(_ context.Context, query eventbus.OutboxQuery) ([]domainevents.Envelope, error) {
	transaction.store.mu.Lock()
	defer transaction.store.mu.Unlock()
	items := make([]domainevents.Envelope, 0)
	for _, event := range transaction.store.events {
		if event.ProjectID != query.ProjectID || event.EventID == nil || eventCursorNumber(*event.EventID) <= eventCursorNumber(query.AfterEventID) ||
			(query.OperationID != "" && (event.OperationID == nil || *event.OperationID != query.OperationID)) {
			continue
		}
		items = append(items, event)
	}
	sort.Slice(items, func(left, right int) bool {
		return eventCursorNumber(*items[left].EventID) < eventCursorNumber(*items[right].EventID)
	})
	if query.Limit > 0 && len(items) > query.Limit {
		items = items[:query.Limit]
	}
	return items, nil
}

func (transaction sseHTTPTransaction) RequiresResync(context.Context, int64, string) (bool, error) {
	return false, nil
}
func (transaction sseHTTPTransaction) MarkPublished(context.Context, string, string) (bool, error) {
	return false, nil
}
func (transaction sseHTTPTransaction) Prune(context.Context, eventbus.RetentionPolicy) (eventbus.RetentionResult, error) {
	return eventbus.RetentionResult{DeletedThrough: map[int64]string{}}, nil
}

func sseOperationEvent(eventID string, revision int64) domainevents.Envelope {
	operationID := "operation-7"
	requestID := "request-operation"
	return domainevents.Envelope{
		SchemaVersion: domainevents.SchemaVersion, Class: domainevents.ClassOperation, EventID: &eventID,
		ProjectID: 7, ProjectRevision: &revision, Type: domainevents.TypeOperationRunning,
		OperationID: &operationID, RequestID: &requestID, OccurredAt: "2026-07-12T08:00:00Z",
		Payload: json.RawMessage(`{"status":"running"}`),
	}
}

func eventCursorNumber(value string) int64 {
	if value == "" {
		return 0
	}
	var result int64
	for _, character := range value {
		result = result*10 + int64(character-'0')
	}
	return result
}

var _ eventbus.Store = (*sseHTTPStore)(nil)
