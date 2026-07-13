// Package terminal is the sole Terminal use-case boundary. REST, WebSocket
// and renderer adapters may call it, but none of them may retain a PTY, a
// process handle or this package's private session map.
package terminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	domainterminal "github.com/lyming99/autoplan/backend/internal/domain/terminal"
	terminalruntime "github.com/lyming99/autoplan/backend/internal/runtime/terminal"
)

const (
	defaultRetention          = 5 * time.Minute
	maximumRetention          = time.Hour
	defaultSubscriptionBuffer = 64
	defaultConnectionGlobal   = 128
	defaultConnectionSession  = 4
	maximumConnectionGlobal   = 1024
	maximumConnectionSession  = 16
)

// ErrConnectionLimit is returned before a transport subscribes to terminal
// output. It is deliberately an application error so every transport shares
// the same global and per-session admission decision.
var ErrConnectionLimit = errors.New("terminal connection limit reached")

type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// RuntimeSession is the intentionally narrow application view of a PTY.
// Platform adapters, exec.Cmd, files, job objects and process groups remain
// inside runtime/terminal.
type RuntimeSession interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Resize(int, int) error
	Kill() error
	Close() error
	Wait(context.Context) (terminalruntime.Exit, error)
}

// Spawner lets this service be tested without creating a real PTY. Production
// uses the adapter below, which is the only bridge to the P002 factory.
type Spawner interface {
	Spawn(context.Context, terminalruntime.SpawnRequest) (RuntimeSession, error)
	CloseProject(int64) int
	Shutdown()
}

type factorySpawner struct{ factory *terminalruntime.Factory }

func (adapter factorySpawner) Spawn(ctx context.Context, request terminalruntime.SpawnRequest) (RuntimeSession, error) {
	return adapter.factory.Spawn(ctx, request)
}

func (adapter factorySpawner) CloseProject(projectID int64) int {
	return adapter.factory.CloseProject(projectID)
}
func (adapter factorySpawner) Shutdown() { adapter.factory.Shutdown() }

// AuditEvent contains only safe metadata. The application never provides raw
// input, output, replay data, cwd, environment, shell command or handles to an
// auditor. Audit failure is advisory and cannot prevent PTY cleanup.
type AuditEvent struct {
	Action    string
	CallerID  string
	ProjectID int64
	SessionID string
	Outcome   string
}

type Auditor interface {
	RecordTerminal(context.Context, AuditEvent) error
}

type Dependencies struct {
	Factory    *terminalruntime.Factory
	Spawner    Spawner
	Authorizer Authorizer
	Workspaces WorkspaceResolver
	Auditor    Auditor
	Profiles   []domainterminal.Profile
	Clock      Clock

	DefaultProfile           string
	DefaultCols              int
	DefaultRows              int
	Retention                time.Duration
	ReplayEntries            int
	ReplayBytes              int
	SubscriptionBuffer       int
	MaxConnectionsGlobal     int
	MaxConnectionsPerSession int
	IDGenerator              func() (string, error)
}

type Service struct {
	mu                 sync.RWMutex
	closed             bool
	records            map[string]*sessionRecord
	profiles           map[string]domainterminal.Profile
	spawner            Spawner
	authorizer         Authorizer
	workspaces         WorkspaceResolver
	auditor            Auditor
	clock              Clock
	defaultProfile     string
	defaultCols        int
	defaultRows        int
	retention          time.Duration
	replay             replayLimits
	queueSize          int
	connectionLimit    ConnectionLimits
	connections        int
	sessionConnections map[string]int
	shutdownDone       chan struct{}
	newID              func() (string, error)
}

// ConnectionLimits bounds independent terminal data-plane attachments while
// their counters stay centralized in Service. The transport supplies values
// copied from validated immutable terminal configuration.
type ConnectionLimits struct {
	Global     int
	PerSession int
}

// ConnectionLease reserves one terminal WebSocket attachment. The lease has
// no process handle and closing it cannot affect the underlying PTY session.
// A transport must release it when its client connection ends.
type ConnectionLease struct {
	service   *Service
	sessionID string
	done      <-chan struct{}
	once      sync.Once
}

func (lease *ConnectionLease) Close() {
	if lease == nil || lease.service == nil {
		return
	}
	lease.once.Do(func() { lease.service.releaseConnection(lease.sessionID) })
}

// Done closes when the shared service begins shutdown, allowing transports to
// send their protocol-specific restart signal without polling private state.
func (lease *ConnectionLease) Done() <-chan struct{} {
	if lease == nil {
		return nil
	}
	return lease.done
}

func NewService(dependencies Dependencies) *Service {
	clock := dependencies.Clock
	if clock == nil {
		clock = systemClock{}
	}
	spawner := dependencies.Spawner
	if spawner == nil && dependencies.Factory != nil {
		spawner = factorySpawner{factory: dependencies.Factory}
	}
	retention := dependencies.Retention
	if retention <= 0 || retention > maximumRetention {
		retention = defaultRetention
	}
	limits := replayLimits{entries: dependencies.ReplayEntries, bytes: dependencies.ReplayBytes}
	if limits.entries <= 0 || limits.entries > defaultReplayEntries {
		limits.entries = defaultReplayEntries
	}
	if limits.bytes <= 0 || limits.bytes > defaultReplayBytes {
		limits.bytes = defaultReplayBytes
	}
	queueSize := dependencies.SubscriptionBuffer
	if queueSize <= 0 || queueSize > defaultSubscriptionBuffer {
		queueSize = defaultSubscriptionBuffer
	}
	connectionLimit := normalizeConnectionLimits(ConnectionLimits{
		Global: dependencies.MaxConnectionsGlobal, PerSession: dependencies.MaxConnectionsPerSession,
	})
	profiles := make(map[string]domainterminal.Profile, len(dependencies.Profiles))
	for _, profile := range dependencies.Profiles {
		if profile.Valid() {
			profiles[profile.ID] = profile.Copy()
		}
	}
	newID := dependencies.IDGenerator
	if newID == nil {
		newID = randomSessionID
	}
	defaultProfile := dependencies.DefaultProfile
	if defaultProfile == "" {
		defaultProfile = "default"
	}
	defaultCols, defaultRows := dependencies.DefaultCols, dependencies.DefaultRows
	if defaultCols == 0 {
		defaultCols = 80
	}
	if defaultRows == 0 {
		defaultRows = 24
	}
	return &Service{
		records: make(map[string]*sessionRecord), profiles: profiles, spawner: spawner,
		authorizer: dependencies.Authorizer, workspaces: dependencies.Workspaces, auditor: dependencies.Auditor, clock: clock,
		defaultProfile: defaultProfile, defaultCols: defaultCols, defaultRows: defaultRows,
		retention: retention, replay: limits, queueSize: queueSize,
		connectionLimit: connectionLimit, sessionConnections: make(map[string]int), shutdownDone: make(chan struct{}),
		newID: newID,
	}
}

func randomSessionID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "term_" + hex.EncodeToString(raw), nil
}

func (service *Service) ready(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == nil || service.spawner == nil || service.clock == nil || service.newID == nil {
		return domainterminal.ErrUnavailable
	}
	service.mu.RLock()
	closed := service.closed
	service.mu.RUnlock()
	if closed {
		return domainterminal.ErrUnavailable
	}
	return nil
}

func (service *Service) profile(id string) (domainterminal.Profile, bool) {
	service.mu.RLock()
	profile, found := service.profiles[id]
	service.mu.RUnlock()
	if !found {
		return domainterminal.Profile{}, false
	}
	return profile.Copy(), true
}

func (service *Service) audit(ctx context.Context, event AuditEvent) {
	if service == nil || service.auditor == nil {
		return
	}
	_ = service.auditor.RecordTerminal(ctx, event)
}

// AcquireConnection authorizes the session and atomically enforces the shared
// WebSocket limits. It intentionally owns no subscription: callers acquire
// before the HTTP upgrade and attach only after admission succeeds.
func (service *Service) AcquireConnection(ctx context.Context, command SessionCommand, limits ConnectionLimits) (*ConnectionLease, error) {
	if err := service.ready(ctx); err != nil {
		return nil, err
	}
	if err := service.Authorize(ctx, command); err != nil {
		return nil, err
	}
	if limits.Global == 0 && limits.PerSession == 0 {
		limits = service.connectionLimit
	}
	limits = normalizeConnectionLimits(limits)
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		return nil, domainterminal.ErrUnavailable
	}
	if service.connections >= limits.Global || service.sessionConnections[command.SessionID] >= limits.PerSession {
		return nil, ErrConnectionLimit
	}
	service.connections++
	service.sessionConnections[command.SessionID]++
	return &ConnectionLease{service: service, sessionID: command.SessionID, done: service.shutdownDone}, nil
}

func normalizeConnectionLimits(value ConnectionLimits) ConnectionLimits {
	if value.Global <= 0 || value.Global > maximumConnectionGlobal {
		value.Global = defaultConnectionGlobal
	}
	if value.PerSession <= 0 || value.PerSession > maximumConnectionSession {
		value.PerSession = defaultConnectionSession
	}
	if value.PerSession > value.Global {
		value.PerSession = value.Global
	}
	return value
}

func (service *Service) releaseConnection(sessionID string) {
	if service == nil || sessionID == "" {
		return
	}
	service.mu.Lock()
	if service.connections > 0 {
		service.connections--
	}
	if count := service.sessionConnections[sessionID]; count <= 1 {
		delete(service.sessionConnections, sessionID)
	} else {
		service.sessionConnections[sessionID] = count - 1
	}
	service.mu.Unlock()
}

// DetachSubscription releases a server-created subscription capability during
// transport teardown. Unlike the caller-facing Detach command it deliberately
// does not reauthorize: a permission revocation must stop subsequent reads and
// writes, but must never leave its already-created subscriber queued forever.
// The pointer is only returned by Attach and is checked against this service.
func (service *Service) DetachSubscription(subscription *Subscription) {
	if service == nil || subscription == nil || subscription.service != service || subscription.ID == "" || subscription.sessionID == "" {
		return
	}
	service.mu.RLock()
	record := service.records[subscription.sessionID]
	service.mu.RUnlock()
	if record == nil {
		return
	}
	record.mu.Lock()
	if subscriber, found := record.subscribers[subscription.ID]; found {
		delete(record.subscribers, subscription.ID)
		finishSubscriberLocked(subscriber, nil)
		service.scheduleIdleExpiryLocked(record)
	}
	record.mu.Unlock()
}

// Shutdown stops admissions and asks every runtime session to terminate before
// its existing read/wait path emits the final exit/status/closed sequence. The
// P002 supervisor enforces the bounded grace period; completion then releases
// replay/subscription memory. It has no repository or event-bus dependency.
func (service *Service) Shutdown(ctx context.Context) {
	if service == nil {
		return
	}
	service.mu.Lock()
	if service.closed {
		service.mu.Unlock()
		return
	}
	service.closed = true
	close(service.shutdownDone)
	records := make([]*sessionRecord, 0, len(service.records))
	for _, record := range service.records {
		records = append(records, record)
	}
	service.mu.Unlock()
	for _, record := range records {
		service.initiateClose(record)
	}
	if service.spawner != nil {
		service.spawner.Shutdown()
	}
}

// CloseProject is for trusted project lifecycle code (such as project
// deletion), not a transport action. Normal callers always use Close with
// caller/project authorization. It closes only this runtime's project records.
func (service *Service) CloseProject(projectID int64) int {
	if service == nil || projectID <= 0 {
		return 0
	}
	service.mu.RLock()
	records := make([]*sessionRecord, 0)
	for _, record := range service.records {
		if record.projectID == projectID {
			records = append(records, record)
		}
	}
	service.mu.RUnlock()
	for _, record := range records {
		service.initiateClose(record)
	}
	if service.spawner != nil {
		service.spawner.CloseProject(projectID)
	}
	return len(records)
}
