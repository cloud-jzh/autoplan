package terminal

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	domainterminal "github.com/lyming99/autoplan/backend/internal/domain/terminal"
	terminalruntime "github.com/lyming99/autoplan/backend/internal/runtime/terminal"
)

type integrationRuntime struct {
	mu       sync.Mutex
	writes   []string
	resizes  [][2]int
	wait     chan struct{}
	waitOnce sync.Once
}

func newIntegrationRuntime() *integrationRuntime {
	return &integrationRuntime{wait: make(chan struct{})}
}
func (runtime *integrationRuntime) Read([]byte) (int, error) { return 0, io.EOF }
func (runtime *integrationRuntime) Write(data []byte) (int, error) {
	runtime.mu.Lock()
	runtime.writes = append(runtime.writes, string(data))
	runtime.mu.Unlock()
	return len(data), nil
}
func (runtime *integrationRuntime) Resize(cols, rows int) error {
	runtime.mu.Lock()
	runtime.resizes = append(runtime.resizes, [2]int{cols, rows})
	runtime.mu.Unlock()
	return nil
}
func (runtime *integrationRuntime) Kill() error {
	runtime.waitOnce.Do(func() { close(runtime.wait) })
	return nil
}
func (runtime *integrationRuntime) Close() error { return runtime.Kill() }
func (runtime *integrationRuntime) Wait(ctx context.Context) (terminalruntime.Exit, error) {
	select {
	case <-ctx.Done():
		return terminalruntime.Exit{}, ctx.Err()
	case <-runtime.wait:
		return terminalruntime.Exit{Code: 0, EndedAt: time.Now().UTC()}, nil
	}
}

type integrationSpawner struct {
	runtime  *integrationRuntime
	requests []terminalruntime.SpawnRequest
}

func (spawner *integrationSpawner) Spawn(_ context.Context, request terminalruntime.SpawnRequest) (RuntimeSession, error) {
	spawner.requests = append(spawner.requests, request)
	return spawner.runtime, nil
}
func (*integrationSpawner) CloseProject(int64) int { return 0 }
func (*integrationSpawner) Shutdown()              {}

type integrationWorkspace struct{}

func (integrationWorkspace) TerminalWorkspace(context.Context, domainterminal.Caller, int64) (string, error) {
	return "/fixture-workspace", nil
}

func newIntegrationService(runtime *integrationRuntime, spawner *integrationSpawner, allow bool) *Service {
	sequence := 0
	return NewService(Dependencies{
		Spawner: spawner,
		Authorizer: AuthorizerFunc(func(context.Context, domainterminal.Caller, int64) error {
			if allow {
				return nil
			}
			return domainterminal.ErrForbidden
		}),
		Workspaces:     integrationWorkspace{},
		Profiles:       []domainterminal.Profile{{ID: "default", Name: "Default", Kind: "default", ShellPath: "/controlled/sh"}},
		DefaultProfile: "default", DefaultCols: 80, DefaultRows: 24, Retention: time.Hour,
		ReplayEntries: 8, ReplayBytes: 256, SubscriptionBuffer: 4, MaxConnectionsGlobal: 2, MaxConnectionsPerSession: 1,
		IDGenerator: func() (string, error) { sequence++; return fmt.Sprintf("term_fixture%03d", sequence), nil },
	})
}

func TestTerminalApplicationLifecyclePreservesIsolationAndDataPlane(t *testing.T) {
	runtime := newIntegrationRuntime()
	spawner := &integrationSpawner{runtime: runtime}
	service := newIntegrationService(runtime, spawner, true)
	defer service.Shutdown(context.Background())
	caller := domainterminal.Caller{ID: "caller"}
	session, err := service.Create(context.Background(), CreateCommand{
		Caller: caller, ProjectID: 7, CWD: "/fixture-workspace", ProfileID: "default", Cols: 80, Rows: 24,
		Environment: map[string]string{"TERM": "xterm-256color"},
	})
	if err != nil || session.Runtime != domainterminal.RuntimeGo || session.Profile.ID != "default" || session.Profile.ShellPath != "/controlled/sh" {
		t.Fatalf("create result = %#v, %v", session, err)
	}
	if len(spawner.requests) != 1 || spawner.requests[0].Environment["TERM"] != "xterm-256color" {
		t.Fatalf("controlled spawn request = %#v", spawner.requests)
	}
	if _, err := service.Write(context.Background(), WriteCommand{SessionCommand: SessionCommand{Caller: caller, ProjectID: 7, SessionID: session.ID}, Data: "echo fixture\n"}); err != nil {
		t.Fatalf("write = %v", err)
	}
	if err := service.Resize(context.Background(), ResizeCommand{SessionCommand: SessionCommand{Caller: caller, ProjectID: 7, SessionID: session.ID}, Cols: 120, Rows: 40}); err != nil {
		t.Fatalf("resize = %v", err)
	}
	subscription, err := service.Attach(context.Background(), AttachCommand{ReplayCommand: ReplayCommand{SessionCommand: SessionCommand{Caller: caller, ProjectID: 7, SessionID: session.ID}}})
	if err != nil {
		t.Fatalf("attach = %v", err)
	}
	record := service.records[session.ID]
	service.appendOutput(record, "safe-output")
	select {
	case event := <-subscription.Events:
		if event.Type != domainterminal.EventOutput || event.Output == nil || event.Output.Seq != 1 || event.Output.Data != "safe-output" {
			t.Fatalf("output event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal output was not delivered to its authorized subscription")
	}
	lease, err := service.AcquireConnection(context.Background(), SessionCommand{Caller: caller, ProjectID: 7, SessionID: session.ID}, ConnectionLimits{Global: 1, PerSession: 1})
	if err != nil {
		t.Fatalf("connection lease = %v", err)
	}
	if _, err := service.AcquireConnection(context.Background(), SessionCommand{Caller: caller, ProjectID: 7, SessionID: session.ID}, ConnectionLimits{Global: 1, PerSession: 1}); err != ErrConnectionLimit {
		t.Fatalf("second connection error = %v, want ErrConnectionLimit", err)
	}
	lease.Close()
	if _, err := service.Kill(context.Background(), SessionCommand{Caller: caller, ProjectID: 7, SessionID: session.ID}); err != nil {
		t.Fatalf("kill = %v", err)
	}
}

func TestTerminalApplicationRejectsUnauthorizedCreateBeforeSpawn(t *testing.T) {
	runtime := newIntegrationRuntime()
	spawner := &integrationSpawner{runtime: runtime}
	service := newIntegrationService(runtime, spawner, false)
	_, err := service.Create(context.Background(), CreateCommand{Caller: domainterminal.Caller{ID: "caller"}, ProjectID: 7, CWD: "/fixture-workspace", ProfileID: "default", Cols: 80, Rows: 24})
	if err != domainterminal.ErrForbidden || len(spawner.requests) != 0 {
		t.Fatalf("unauthorized create err=%v spawn-count=%d", err, len(spawner.requests))
	}
}
