package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lyming99/autoplan/backend/internal/domain/contracts"
	domainevents "github.com/lyming99/autoplan/backend/internal/domain/events"
	"github.com/lyming99/autoplan/backend/internal/runtime/eventbus"
)

func TestSSEReconnectUsesCursorWithoutDuplicatingCommittedEvent(t *testing.T) {
	first := sseOperationEvent("1", 1)
	second := sseOperationEvent("2", 2)
	store := &sseHTTPStore{events: []domainevents.Envelope{first, second}}
	clock := operationHTTPClock{value: time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)}
	bus := eventbus.NewBus(eventbus.Options{Store: store, Clock: clock, SubscriptionBuffer: 2, ReplayLimit: 10})
	projects := &projectServiceFixture{project: contracts.Project{ID: 7, Name: "fixture"}}
	operations := &operationHTTPService{operation: operationHTTPFixture(7, "operation-7", 1)}
	router, credential := newSSEHTTPRouter(t, projects, operations, bus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptestNewSSERequest(ctx, http.MethodGet, "/api/v1/projects/7/events", credential)
	request.Header.Set("Last-Event-ID", "1")
	writer := &cancelAfterSSEWriter{cancel: cancel, marker: "id: 2\n"}
	router.ServeHTTP(writer, request)
	body := writer.Body.String()
	if writer.status != http.StatusOK || strings.Contains(body, "id: 1\n") || !strings.Contains(body, "id: 2\n") ||
		strings.Count(body, "event: operation.running\n") != 1 {
		t.Fatalf("cursor reconnect body=%q status=%d", body, writer.status)
	}
}

func TestOperationSSEOperationFilterDoesNotLeakOtherOperationEvents(t *testing.T) {
	match := sseOperationEvent("1", 1)
	other := sseOperationEvent("2", 2)
	otherOperation := "operation-other"
	other.OperationID = &otherOperation
	store := &sseHTTPStore{events: []domainevents.Envelope{match, other}}
	clock := operationHTTPClock{value: time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)}
	bus := eventbus.NewBus(eventbus.Options{Store: store, Clock: clock, SubscriptionBuffer: 2, ReplayLimit: 10})
	projects := &projectServiceFixture{project: contracts.Project{ID: 7, Name: "fixture"}}
	operations := &operationHTTPService{operation: operationHTTPFixture(7, "operation-7", 1)}
	router, credential := newSSEHTTPRouter(t, projects, operations, bus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptestNewSSERequest(ctx, http.MethodGet, "/api/v1/operations/operation-7/events?project_id=7", credential)
	writer := &cancelAfterSSEWriter{cancel: cancel, marker: "id: 1\n"}
	router.ServeHTTP(writer, request)
	body := writer.Body.String()
	if writer.status != http.StatusOK || !strings.Contains(body, "id: 1\n") || strings.Contains(body, "id: 2\n") ||
		strings.Contains(body, "operation-other") {
		t.Fatalf("operation SSE filter leaked body=%q status=%d", body, writer.status)
	}
}
