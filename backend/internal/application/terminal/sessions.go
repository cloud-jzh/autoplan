package terminal

import (
	"context"
	"strings"
	"sync"
	"time"

	domainterminal "github.com/lyming99/autoplan/backend/internal/domain/terminal"
	terminalruntime "github.com/lyming99/autoplan/backend/internal/runtime/terminal"
)

const outputReadChunkBytes = 64 << 10

type sessionRecord struct {
	mu             sync.Mutex
	projectID      int64
	runtime        RuntimeSession
	cancel         context.CancelFunc
	state          domainterminal.Session
	replay         replayBuffer
	nextSeq        uint64
	subscribers    map[string]*subscriber
	readDone       chan struct{}
	exited         bool
	exit           domainterminal.Exit
	killRequested  bool
	closing        bool
	removed        bool
	retentionTimer *time.Timer
	idleTimer      *time.Timer
}

type subscriber struct {
	events   chan domainterminal.Event
	done     chan error
	finished bool
}

// Subscription keeps raw output inside the authorized Terminal data plane.
// Replay is a fixed snapshot through LastSeq; Events begins strictly after
// that watermark. A closed Done with ErrSlowConsumer means no output was
// retained for that subscriber and it must reattach with a cursor.
type Subscription struct {
	ID      string
	Session domainterminal.Session
	Replay  domainterminal.Replay
	Initial []domainterminal.Event
	Events  <-chan domainterminal.Event
	Done    <-chan error

	service   *Service
	caller    domainterminal.Caller
	projectID int64
	sessionID string
}

func (subscription *Subscription) Detach(ctx context.Context) error {
	if subscription == nil || subscription.service == nil {
		return domainterminal.ErrNotFound
	}
	return subscription.service.Detach(ctx, SessionCommand{
		Caller: subscription.caller, ProjectID: subscription.projectID, SessionID: subscription.sessionID,
	}, subscription.ID)
}

// Reauthorize lets a long-lived data-plane adapter check current project
// visibility before accepting another client frame or forwarding more output.
func (subscription *Subscription) Reauthorize(ctx context.Context) error {
	if subscription == nil || subscription.service == nil {
		return domainterminal.ErrNotFound
	}
	return subscription.service.Authorize(ctx, SessionCommand{
		Caller: subscription.caller, ProjectID: subscription.projectID, SessionID: subscription.sessionID,
	})
}

func (service *Service) Create(ctx context.Context, command CreateCommand) (domainterminal.Session, error) {
	if err := service.ready(ctx); err != nil {
		return domainterminal.Session{}, err
	}
	if command.ProfileID == "" {
		command.ProfileID = service.defaultProfile
	}
	if command.Cols == 0 {
		command.Cols = service.defaultCols
	}
	if command.Rows == 0 {
		command.Rows = service.defaultRows
	}
	if !command.valid() {
		return domainterminal.Session{}, domainterminal.ErrInvalidCommand
	}
	if err := service.authorize(ctx, command.Caller, command.ProjectID); err != nil {
		return domainterminal.Session{}, err
	}
	workspace, err := service.workspace(ctx, command.Caller, command.ProjectID)
	if err != nil {
		return domainterminal.Session{}, err
	}
	profile, found := service.profile(command.ProfileID)
	if !found {
		return domainterminal.Session{}, domainterminal.ErrInvalidCommand
	}
	id, err := service.newID()
	if err != nil || !validSessionID(id) {
		return domainterminal.Session{}, domainterminal.ErrUnavailable
	}
	// PTY lifetime must not inherit an HTTP request cancellation. We preserve
	// request values for Files Policy while the service retains the only cancel.
	lifetime, cancel := context.WithCancel(context.WithoutCancel(contextOrBackground(ctx)))
	environment := copyEnvironment(command.Environment)
	runtimeSession, err := service.spawner.Spawn(lifetime, terminalruntime.SpawnRequest{
		ProjectID: command.ProjectID, Workspace: workspace, WorkingDirectory: command.CWD,
		ProfileID: command.ProfileID, Environment: environment, Cols: command.Cols, Rows: command.Rows,
	})
	clearEnvironment(environment)
	if err != nil || runtimeSession == nil {
		cancel()
		if err != nil {
			return domainterminal.Session{}, err
		}
		return domainterminal.Session{}, domainterminal.ErrUnavailable
	}
	now := service.clock.Now().UTC()
	title := command.Title
	if title == "" {
		title = defaultTitle
	}
	record := &sessionRecord{
		projectID: command.ProjectID, runtime: runtimeSession, cancel: cancel,
		state: domainterminal.Session{
			ID: id, ProjectID: command.ProjectID, Title: title, CWD: command.CWD, Shell: profile.ShellPath,
			Status: domainterminal.StatusRunning, CreatedAt: now, Cols: command.Cols, Rows: command.Rows,
			Profile: profile, Runtime: domainterminal.RuntimeGo,
		},
		replay: newReplayBuffer(service.replay), subscribers: make(map[string]*subscriber), readDone: make(chan struct{}),
	}
	service.mu.Lock()
	if service.closed {
		service.mu.Unlock()
		cancel()
		_ = runtimeSession.Close()
		return domainterminal.Session{}, domainterminal.ErrUnavailable
	}
	if _, duplicate := service.records[id]; duplicate {
		service.mu.Unlock()
		cancel()
		_ = runtimeSession.Close()
		return domainterminal.Session{}, domainterminal.ErrUnavailable
	}
	service.records[id] = record
	service.mu.Unlock()
	record.mu.Lock()
	service.scheduleIdleExpiryLocked(record)
	result := record.state.Copy()
	record.mu.Unlock()
	go service.pumpOutput(record)
	go service.waitForExit(record)
	service.audit(ctx, AuditEvent{Action: "create", CallerID: command.Caller.ID, ProjectID: command.ProjectID, SessionID: id, Outcome: "accepted"})
	return result, nil
}

func (service *Service) List(ctx context.Context, caller domainterminal.Caller, projectID int64) ([]domainterminal.Session, error) {
	if err := service.ready(ctx); err != nil {
		return nil, err
	}
	if !caller.Valid() || projectID <= 0 {
		return nil, domainterminal.ErrInvalidCommand
	}
	if err := service.authorize(ctx, caller, projectID); err != nil {
		return nil, err
	}
	service.mu.RLock()
	records := make([]*sessionRecord, 0, len(service.records))
	for _, record := range service.records {
		records = append(records, record)
	}
	service.mu.RUnlock()
	result := make([]domainterminal.Session, 0, len(records))
	for _, record := range records {
		record.mu.Lock()
		if !record.removed && !record.closing && record.projectID == projectID {
			result = append(result, record.state.Copy())
		}
		record.mu.Unlock()
	}
	return result, nil
}

func (service *Service) Get(ctx context.Context, command SessionCommand) (domainterminal.Session, error) {
	record, err := service.record(ctx, command, false)
	if err != nil {
		return domainterminal.Session{}, err
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	return record.state.Copy(), nil
}

func (service *Service) Write(ctx context.Context, command WriteCommand) (int, error) {
	if !command.valid() {
		return 0, domainterminal.ErrInvalidCommand
	}
	record, err := service.record(ctx, command.SessionCommand, false)
	if err != nil {
		return 0, err
	}
	written, err := record.runtime.Write([]byte(command.Data))
	service.audit(ctx, AuditEvent{Action: "write", CallerID: command.Caller.ID, ProjectID: command.ProjectID, SessionID: command.SessionID, Outcome: outcome(err)})
	return written, err
}

func (service *Service) Resize(ctx context.Context, command ResizeCommand) error {
	if !command.valid() {
		return domainterminal.ErrInvalidCommand
	}
	record, err := service.record(ctx, command.SessionCommand, false)
	if err != nil {
		return err
	}
	if err := record.runtime.Resize(command.Cols, command.Rows); err != nil {
		service.audit(ctx, AuditEvent{Action: "resize", CallerID: command.Caller.ID, ProjectID: command.ProjectID, SessionID: command.SessionID, Outcome: outcome(err)})
		return err
	}
	record.mu.Lock()
	if !record.removed && !record.closing {
		record.state.Cols, record.state.Rows = command.Cols, command.Rows
	}
	record.mu.Unlock()
	service.audit(ctx, AuditEvent{Action: "resize", CallerID: command.Caller.ID, ProjectID: command.ProjectID, SessionID: command.SessionID, Outcome: "accepted"})
	return nil
}

func (service *Service) Kill(ctx context.Context, command SessionCommand) (domainterminal.Session, error) {
	record, err := service.record(ctx, command, false)
	if err != nil {
		return domainterminal.Session{}, err
	}
	record.mu.Lock()
	if record.exited {
		result := record.state.Copy()
		record.mu.Unlock()
		return result, nil
	}
	record.killRequested = true
	record.mu.Unlock()
	if err := record.runtime.Kill(); err != nil {
		service.audit(ctx, AuditEvent{Action: "kill", CallerID: command.Caller.ID, ProjectID: command.ProjectID, SessionID: command.SessionID, Outcome: outcome(err)})
		return domainterminal.Session{}, err
	}
	record.mu.Lock()
	result := record.state.Copy()
	record.mu.Unlock()
	service.audit(ctx, AuditEvent{Action: "kill", CallerID: command.Caller.ID, ProjectID: command.ProjectID, SessionID: command.SessionID, Outcome: "accepted"})
	return result, nil
}

func (service *Service) Close(ctx context.Context, command SessionCommand) (domainterminal.Session, error) {
	record, err := service.record(ctx, command, true)
	if err != nil {
		return domainterminal.Session{}, err
	}
	service.initiateClose(record)
	record.mu.Lock()
	exited := record.exited
	result := record.state.Copy()
	result.Closed = true
	record.mu.Unlock()
	if exited {
		service.finalizeRecord(record)
	}
	service.audit(ctx, AuditEvent{Action: "close", CallerID: command.Caller.ID, ProjectID: command.ProjectID, SessionID: command.SessionID, Outcome: "accepted"})
	return result, nil
}

func (service *Service) Rename(ctx context.Context, command RenameCommand) (domainterminal.Session, error) {
	if !command.valid() {
		return domainterminal.Session{}, domainterminal.ErrInvalidCommand
	}
	record, err := service.record(ctx, command.SessionCommand, false)
	if err != nil {
		return domainterminal.Session{}, err
	}
	record.mu.Lock()
	record.state.Title = command.Title
	result := record.state.Copy()
	record.mu.Unlock()
	service.audit(ctx, AuditEvent{Action: "rename", CallerID: command.Caller.ID, ProjectID: command.ProjectID, SessionID: command.SessionID, Outcome: "accepted"})
	return result, nil
}

func (service *Service) Clear(ctx context.Context, command SessionCommand) error {
	record, err := service.record(ctx, command, false)
	if err != nil {
		return err
	}
	record.mu.Lock()
	record.replay.clear()
	record.mu.Unlock()
	service.audit(ctx, AuditEvent{Action: "clear", CallerID: command.Caller.ID, ProjectID: command.ProjectID, SessionID: command.SessionID, Outcome: "accepted"})
	return nil
}

func (service *Service) Replay(ctx context.Context, command ReplayCommand) (domainterminal.Replay, error) {
	if !command.valid() {
		return domainterminal.Replay{}, domainterminal.ErrInvalidCommand
	}
	record, err := service.record(ctx, command.SessionCommand, false)
	if err != nil {
		return domainterminal.Replay{}, err
	}
	record.mu.Lock()
	entries, first, err := record.replay.after(command.LastSeq, record.nextSeq)
	result := domainterminal.Replay{Session: record.state.Copy(), FirstSeq: first, LastSeq: record.nextSeq, Entries: entries}
	record.mu.Unlock()
	if err != nil {
		return domainterminal.Replay{}, err
	}
	return result.Copy(), nil
}

func (service *Service) Attach(ctx context.Context, command AttachCommand) (*Subscription, error) {
	if !command.valid() {
		return nil, domainterminal.ErrInvalidCommand
	}
	record, err := service.record(ctx, command.SessionCommand, false)
	if err != nil {
		return nil, err
	}
	id, err := service.newID()
	if err != nil || !validSessionID(id) {
		return nil, domainterminal.ErrUnavailable
	}
	record.mu.Lock()
	entries, first, err := record.replay.after(command.LastSeq, record.nextSeq)
	if err != nil {
		record.mu.Unlock()
		return nil, err
	}
	subscription := &subscriber{events: make(chan domainterminal.Event, service.queueSize), done: make(chan error, 1)}
	record.subscribers[id] = subscription
	if record.idleTimer != nil {
		record.idleTimer.Stop()
		record.idleTimer = nil
	}
	session := record.state.Copy()
	replay := domainterminal.Replay{Session: session.Copy(), FirstSeq: first, LastSeq: record.nextSeq, Entries: entries}
	initial := terminalEventsLocked(record)
	record.mu.Unlock()
	return &Subscription{
		ID: id, Session: session, Replay: replay.Copy(), Initial: initial,
		Events: subscription.events, Done: subscription.done,
		service: service, caller: command.Caller, projectID: command.ProjectID, sessionID: command.SessionID,
	}, nil
}

func (service *Service) Detach(ctx context.Context, command SessionCommand, subscriptionID string) error {
	if !command.valid() || !validSessionID(subscriptionID) {
		return domainterminal.ErrInvalidCommand
	}
	record, err := service.record(ctx, command, true)
	if err != nil {
		return err
	}
	record.mu.Lock()
	if subscriber, found := record.subscribers[subscriptionID]; found {
		delete(record.subscribers, subscriptionID)
		finishSubscriberLocked(subscriber, nil)
		service.scheduleIdleExpiryLocked(record)
	}
	record.mu.Unlock()
	return nil
}

// Authorize performs a fresh caller/project/session check without exposing the
// record. Long-lived transports use it after authentication changes or before
// accepting a new data-plane action.
func (service *Service) Authorize(ctx context.Context, command SessionCommand) error {
	_, err := service.record(ctx, command, false)
	return err
}

func (service *Service) record(ctx context.Context, command SessionCommand, allowClosing bool) (*sessionRecord, error) {
	if err := service.ready(ctx); err != nil {
		return nil, err
	}
	if !command.valid() {
		return nil, domainterminal.ErrInvalidCommand
	}
	if err := service.authorize(ctx, command.Caller, command.ProjectID); err != nil {
		return nil, err
	}
	service.mu.RLock()
	record := service.records[command.SessionID]
	service.mu.RUnlock()
	if record == nil {
		return nil, domainterminal.ErrNotFound
	}
	record.mu.Lock()
	visible := !record.removed && record.projectID == command.ProjectID && (allowClosing || !record.closing)
	record.mu.Unlock()
	if !visible {
		// Project mismatch deliberately maps to not-found after authorization,
		// so a guessed ID reveals neither session existence nor its metadata.
		return nil, domainterminal.ErrNotFound
	}
	return record, nil
}

func (service *Service) pumpOutput(record *sessionRecord) {
	defer close(record.readDone)
	buffer := make([]byte, outputReadChunkBytes)
	for {
		read, err := record.runtime.Read(buffer)
		if read > 0 {
			service.appendOutput(record, strings.ToValidUTF8(string(buffer[:read]), "�"))
		}
		if err != nil {
			return
		}
	}
}

func (service *Service) appendOutput(record *sessionRecord, data string) {
	if data == "" {
		return
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if record.removed {
		return
	}
	record.nextSeq++
	output := domainterminal.Output{Seq: record.nextSeq, Data: data}
	record.replay.append(output)
	service.broadcastLocked(record, domainterminal.Event{Type: domainterminal.EventOutput, Output: &output})
}

func (service *Service) waitForExit(record *sessionRecord) {
	exit, err := record.runtime.Wait(context.Background())
	<-record.readDone
	service.completeExit(record, exit, err)
}

func (service *Service) completeExit(record *sessionRecord, runtimeExit terminalruntime.Exit, waitErr error) {
	record.mu.Lock()
	if record.removed || record.exited {
		record.mu.Unlock()
		return
	}
	if record.idleTimer != nil {
		record.idleTimer.Stop()
		record.idleTimer = nil
	}
	service.completeExitLocked(record, runtimeExit, waitErr)
	closing := record.closing
	if !closing {
		record.retentionTimer = time.AfterFunc(service.retention, func() { service.finalizeRecord(record) })
	}
	record.mu.Unlock()
	if closing {
		service.finalizeRecord(record)
	}
}

func (service *Service) completeExitLocked(record *sessionRecord, runtimeExit terminalruntime.Exit, waitErr error) {
	if record.exited {
		return
	}
	record.exited = true
	ended := runtimeExit.EndedAt.UTC()
	if ended.IsZero() {
		ended = service.clock.Now().UTC()
	}
	record.state.EndedAt = &ended
	if waitErr != nil {
		record.state.Status = domainterminal.StatusError
	} else if record.killRequested || record.closing || runtimeExit.TimedOut {
		record.state.Status = domainterminal.StatusKilled
	} else {
		record.state.Status = domainterminal.StatusExited
	}
	if waitErr == nil {
		code := runtimeExit.Code
		record.state.ExitCode = &code
	}
	record.exit = domainterminal.Exit{Code: copyInt(record.state.ExitCode), Signal: runtimeExit.Signal, TimedOut: runtimeExit.TimedOut}
	exit := record.exit.Copy()
	state := record.state.Copy()
	service.broadcastLocked(record, domainterminal.Event{Type: domainterminal.EventExit, Exit: &exit})
	service.broadcastLocked(record, domainterminal.Event{Type: domainterminal.EventStatus, Session: &state})
}

func (service *Service) initiateClose(record *sessionRecord) {
	if record == nil {
		return
	}
	record.mu.Lock()
	if record.removed || record.closing {
		record.mu.Unlock()
		return
	}
	record.closing = true
	if record.retentionTimer != nil {
		record.retentionTimer.Stop()
		record.retentionTimer = nil
	}
	if record.idleTimer != nil {
		record.idleTimer.Stop()
		record.idleTimer = nil
	}
	cancel := record.cancel
	runtime := record.runtime
	record.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if runtime != nil {
		_ = runtime.Close()
	}
}

func (service *Service) finalizeRecord(record *sessionRecord) {
	if record == nil {
		return
	}
	record.mu.Lock()
	if record.removed || !record.exited {
		record.mu.Unlock()
		return
	}
	record.removed = true
	record.closing = true
	if record.retentionTimer != nil {
		record.retentionTimer.Stop()
		record.retentionTimer = nil
	}
	if record.idleTimer != nil {
		record.idleTimer.Stop()
		record.idleTimer = nil
	}
	state := record.state.Copy()
	state.Closed = true
	record.state.Closed = true
	service.broadcastLocked(record, domainterminal.Event{Type: domainterminal.EventClosed, Session: &state})
	for id, subscriber := range record.subscribers {
		delete(record.subscribers, id)
		finishSubscriberLocked(subscriber, nil)
	}
	record.replay.clear()
	cancel := record.cancel
	runtime := record.runtime
	record.mu.Unlock()
	service.mu.Lock()
	if service.records[state.ID] == record {
		delete(service.records, state.ID)
	}
	service.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if runtime != nil {
		_ = runtime.Close()
	}
}

func (service *Service) broadcastLocked(record *sessionRecord, event domainterminal.Event) {
	for id, subscriber := range record.subscribers {
		if subscriber.finished {
			delete(record.subscribers, id)
			continue
		}
		select {
		case subscriber.events <- event.Copy():
		default:
			delete(record.subscribers, id)
			finishSubscriberLocked(subscriber, domainterminal.ErrSlowConsumer)
		}
	}
	service.scheduleIdleExpiryLocked(record)
}

// scheduleIdleExpiryLocked gives a detached, still-running PTY one bounded
// reconnect window. It is called with record.mu held. Any Attach atomically
// stops this timer before exposing the replay/live subscription.
func (service *Service) scheduleIdleExpiryLocked(record *sessionRecord) {
	if record == nil || record.removed || record.exited || record.closing || len(record.subscribers) != 0 || record.idleTimer != nil {
		return
	}
	record.idleTimer = time.AfterFunc(service.retention, func() { service.expireDetached(record) })
}

func (service *Service) expireDetached(record *sessionRecord) {
	if record == nil {
		return
	}
	record.mu.Lock()
	record.idleTimer = nil
	if record.removed || record.exited || record.closing || len(record.subscribers) != 0 {
		record.mu.Unlock()
		return
	}
	record.closing = true
	cancel := record.cancel
	runtime := record.runtime
	record.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if runtime != nil {
		_ = runtime.Close()
	}
}

func finishSubscriberLocked(subscriber *subscriber, reason error) {
	if subscriber == nil || subscriber.finished {
		return
	}
	subscriber.finished = true
	if reason != nil {
		select {
		case subscriber.done <- reason:
		default:
		}
	}
	close(subscriber.events)
	close(subscriber.done)
}

func terminalEventsLocked(record *sessionRecord) []domainterminal.Event {
	if record == nil || !record.exited {
		return []domainterminal.Event{}
	}
	exit := record.exit.Copy()
	state := record.state.Copy()
	return []domainterminal.Event{
		{Type: domainterminal.EventExit, Exit: &exit},
		{Type: domainterminal.EventStatus, Session: &state},
	}
}

func copyEnvironment(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for name, value := range input {
		result[name] = value
	}
	return result
}

func clearEnvironment(values map[string]string) {
	for name := range values {
		values[name] = ""
		delete(values, name)
	}
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func outcome(err error) string {
	if err == nil {
		return "accepted"
	}
	return "rejected"
}

func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
