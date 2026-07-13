package eventbus

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"sync"

	domainevents "github.com/lyming99/autoplan/backend/internal/domain/events"
)

var subscriptionIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type SubscribeRequest struct {
	ProjectID   int64
	OperationID string
	LastEventID string
}

func (request SubscribeRequest) valid() bool {
	return request.ProjectID > 0 && (request.OperationID == "" || subscriptionIdentifier.MatchString(request.OperationID))
}

// Delivery identifies whether an event came from the durable replay snapshot.
// Both replay and live entries carry the exact same frozen event envelope.
type Delivery struct {
	Envelope domainevents.Envelope
	Replay   bool
}

// Subscription is intentionally pull-based. Replay is bounded and retained
// locally in order, while live delivery uses a finite channel. Next therefore
// cannot lose the replay/live boundary merely because the caller starts
// reading after Subscribe returns.
type Subscription struct {
	bus           *Bus
	id            uint64
	projectID     int64
	operationID   string
	lastEventID   int64
	lastRevision  int64
	initialCursor int64
	mu            sync.Mutex
	replay        []Delivery
	live          chan Delivery
	done          chan struct{}
	terminal      *Delivery
	closed        bool
}

func newSubscription(bus *Bus, id uint64, request SubscribeRequest, lastEventID, initialRevision int64, buffer int) *Subscription {
	return &Subscription{
		bus: bus, id: id, projectID: request.ProjectID, operationID: request.OperationID,
		lastEventID: lastEventID, lastRevision: initialRevision, initialCursor: lastEventID,
		live: make(chan Delivery, buffer), done: make(chan struct{}),
	}
}

func (subscription *Subscription) ProjectID() int64 {
	if subscription == nil {
		return 0
	}
	return subscription.projectID
}

func (subscription *Subscription) OperationID() string {
	if subscription == nil {
		return ""
	}
	return subscription.operationID
}

func (subscription *Subscription) Done() <-chan struct{} {
	if subscription == nil {
		return nil
	}
	return subscription.done
}

// Next returns the ordered replay first, then waits for a bounded live event.
// A resync_required control envelope is returned once before a closed
// subscription reports ErrSubscriptionClosed.
func (subscription *Subscription) Next(ctx context.Context) (Delivery, error) {
	if subscription == nil {
		return Delivery{}, ErrSubscriptionClosed
	}
	if err := ctx.Err(); err != nil {
		return Delivery{}, err
	}
	for {
		subscription.mu.Lock()
		if subscription.terminal != nil {
			result := *subscription.terminal
			subscription.terminal = nil
			subscription.mu.Unlock()
			return result, nil
		}
		if len(subscription.replay) > 0 {
			result := subscription.replay[0]
			subscription.replay[0] = Delivery{}
			subscription.replay = subscription.replay[1:]
			subscription.mu.Unlock()
			return result, nil
		}
		if subscription.closed {
			subscription.mu.Unlock()
			return Delivery{}, ErrSubscriptionClosed
		}
		live := subscription.live
		done := subscription.done
		subscription.mu.Unlock()

		select {
		case delivery := <-live:
			return delivery, nil
		case <-done:
			// A close can race with the select after terminal was checked.
			// Loop once to observe a terminal resync deterministically.
			continue
		case <-ctx.Done():
			return Delivery{}, ctx.Err()
		}
	}
}

// Close releases an individual transport subscription. It is safe to call
// more than once and never blocks on a slow reader.
func (subscription *Subscription) Close() {
	if subscription == nil || subscription.bus == nil {
		return
	}
	subscription.bus.mu.Lock()
	defer subscription.bus.mu.Unlock()
	if _, exists := subscription.bus.subscriptions[subscription.id]; exists {
		delete(subscription.bus.subscriptions, subscription.id)
	}
	subscription.close()
}

func (subscription *Subscription) appendReplay(envelope domainevents.Envelope) string {
	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	reason, accepted := subscription.acceptPersistentLocked(envelope)
	if reason != "" {
		return reason
	}
	if !accepted {
		return ""
	}
	subscription.replay = append(subscription.replay, Delivery{Envelope: envelope, Replay: true})
	return ""
}

func (subscription *Subscription) offerLive(envelope domainevents.Envelope) string {
	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	reason, accepted := subscription.acceptPersistentLocked(envelope)
	if reason != "" {
		return reason
	}
	if !accepted {
		return ""
	}
	delivery := Delivery{Envelope: envelope}
	select {
	case subscription.live <- delivery:
		return ""
	default:
		return "slow_consumer"
	}
}

func (subscription *Subscription) acceptPersistentLocked(envelope domainevents.Envelope) (string, bool) {
	if envelope.Validate() != nil || !envelope.Persistent() || envelope.EventID == nil || envelope.ProjectRevision == nil ||
		envelope.ProjectID != subscription.projectID {
		return "project_mismatch", false
	}
	eventID := cursorValue(*envelope.EventID)
	if eventID <= 0 {
		return "project_mismatch", false
	}
	if eventID <= subscription.lastEventID {
		return "", false
	}
	if subscription.operationID == "" {
		revision := *envelope.ProjectRevision
		if (subscription.lastRevision > 0 && revision != subscription.lastRevision+1) ||
			(subscription.lastRevision == 0 && subscription.initialCursor == 0 && revision != 1) {
			return "revision_gap", false
		}
		subscription.lastRevision = revision
	}
	subscription.lastEventID = eventID
	return "", true
}

func (subscription *Subscription) resync(reason string) {
	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	subscription.resyncLocked(reason)
}

func (subscription *Subscription) resyncLocked(reason string) {
	if subscription.closed {
		return
	}
	subscription.replay = nil
	subscription.drainLiveLocked()
	control := resyncEnvelope(subscription.projectID, reason, subscription.bus.clock)
	subscription.terminal = &Delivery{Envelope: control}
	subscription.closed = true
	close(subscription.done)
}

func (subscription *Subscription) drainLiveLocked() {
	for {
		select {
		case <-subscription.live:
		default:
			return
		}
	}
}

func (subscription *Subscription) closeLocked() {
	if subscription.closed {
		return
	}
	subscription.replay = nil
	subscription.drainLiveLocked()
	subscription.closed = true
	close(subscription.done)
}

func (subscription *Subscription) close() {
	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	subscription.closeLocked()
}

func resyncEnvelope(projectID int64, reason string, clock Clock) domainevents.Envelope {
	now := clock.Now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	payload, _ := json.Marshal(map[string]string{"reason": reason})
	return domainevents.Envelope{
		SchemaVersion: domainevents.SchemaVersion, Class: domainevents.ClassControl, ProjectID: projectID,
		Type: domainevents.TypeResyncRequired, OccurredAt: now, Payload: payload,
	}
}

func validCursor(value string) bool {
	if value == "" || value == "0" {
		return true
	}
	if len(value) > 19 || value[0] == '0' {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed > 0
}

func cursorValue(value string) int64 {
	if value == "" || value == "0" {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}
