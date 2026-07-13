package terminal

import (
	"context"
	"errors"
	"testing"

	domainterminal "github.com/lyming99/autoplan/backend/internal/domain/terminal"
	terminalruntime "github.com/lyming99/autoplan/backend/internal/runtime/terminal"
)

type testSpawner struct{}

func (testSpawner) Spawn(context.Context, terminalruntime.SpawnRequest) (RuntimeSession, error) {
	return nil, errors.New("not used")
}
func (testSpawner) CloseProject(int64) int { return 0 }
func (testSpawner) Shutdown()              {}

func testService() *Service {
	return NewService(Dependencies{
		Spawner: testSpawner{}, Authorizer: AuthorizerFunc(func(context.Context, domainterminal.Caller, int64) error { return nil }),
		IDGenerator: func() (string, error) { return "term_test000", nil },
	})
}

func TestReplayClearPreservesSequenceAndReportsGap(t *testing.T) {
	buffer := newReplayBuffer(replayLimits{entries: 2, bytes: 64})
	buffer.append(domainterminal.Output{Seq: 1, Data: "one"})
	buffer.append(domainterminal.Output{Seq: 2, Data: "two"})
	buffer.clear()
	if _, _, err := buffer.after(1, 2); !errors.Is(err, domainterminal.ErrReplayGap) {
		t.Fatalf("after cleared replay error = %v, want replay gap", err)
	}
	entries, first, err := buffer.after(2, 2)
	if err != nil || first != 0 || len(entries) != 0 {
		t.Fatalf("terminal cursor result = %#v, %d, %v", entries, first, err)
	}
}

func TestSessionIDCannotCrossProjectBoundary(t *testing.T) {
	service := testService()
	service.records["term_test000"] = &sessionRecord{
		projectID: 1,
		state:     domainterminal.Session{ID: "term_test000", ProjectID: 1, Status: domainterminal.StatusRunning},
		replay:    newReplayBuffer(replayLimits{}), subscribers: make(map[string]*subscriber), readDone: make(chan struct{}),
	}
	_, err := service.Get(context.Background(), SessionCommand{
		Caller: domainterminal.Caller{ID: "caller"}, ProjectID: 2, SessionID: "term_test000",
	})
	if !errors.Is(err, domainterminal.ErrNotFound) {
		t.Fatalf("cross-project get error = %v, want not found", err)
	}
}

func TestAttachUsesReplayWatermarkBeforeLiveOutput(t *testing.T) {
	service := testService()
	record := &sessionRecord{
		projectID: 1,
		state:     domainterminal.Session{ID: "term_test000", ProjectID: 1, Status: domainterminal.StatusRunning},
		replay:    newReplayBuffer(replayLimits{}), subscribers: make(map[string]*subscriber), readDone: make(chan struct{}),
		nextSeq: 2,
	}
	record.replay.append(domainterminal.Output{Seq: 1, Data: "one"})
	record.replay.append(domainterminal.Output{Seq: 2, Data: "two"})
	service.records[record.state.ID] = record
	subscription, err := service.Attach(context.Background(), AttachCommand{ReplayCommand: ReplayCommand{SessionCommand: SessionCommand{
		Caller: domainterminal.Caller{ID: "caller"}, ProjectID: 1, SessionID: record.state.ID,
	}, LastSeq: 0}})
	if err != nil {
		t.Fatalf("attach error = %v", err)
	}
	if subscription.Replay.LastSeq != 2 || len(subscription.Replay.Entries) != 2 || subscription.Replay.Entries[0].Seq != 1 || subscription.Replay.Entries[1].Seq != 2 {
		t.Fatalf("replay = %#v", subscription.Replay)
	}
	service.appendOutput(record, "three")
	event := <-subscription.Events
	if event.Type != domainterminal.EventOutput || event.Output == nil || event.Output.Seq != 3 || event.Output.Data != "three" {
		t.Fatalf("live event = %#v", event)
	}
}
