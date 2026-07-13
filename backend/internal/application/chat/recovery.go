package chat

import (
	"context"
	"sort"

	domainchat "github.com/lyming99/autoplan/backend/internal/domain/chat"
)

type RecoveryResult struct {
	Interrupted int
	Queues      []QueueDTO
}

// Recover finalizes only work that this daemon can no longer control. It does
// not claim or restart queued work: a later provider/runtime owner may pump
// the durable FIFO, but recovery itself never repeats an external side effect.
func (service *Service) Recover(ctx context.Context) (RecoveryResult, error) {
	if !service.Configured() {
		return RecoveryResult{}, nil
	}
	occurredAt := service.timestamp()
	result := RecoveryResult{Queues: make([]QueueDTO, 0)}
	err := service.queueWriter.TransactChatQueue(ctx, func(transaction domainchat.QueueTransaction) error {
		interrupted, err := transaction.InterruptActiveMessages(ctx, occurredAt)
		if err != nil {
			return err
		}
		seen := make(map[conversationKey]struct{})
		for _, message := range interrupted {
			result.Interrupted++
			key := conversationKey{ProjectID: message.ProjectID, ConversationID: message.ConversationID}
			seen[key] = struct{}{}
		}
		keys := make([]conversationKey, 0, len(seen))
		for key := range seen {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(left, right int) bool {
			if keys[left].ProjectID != keys[right].ProjectID {
				return keys[left].ProjectID < keys[right].ProjectID
			}
			return keys[left].ConversationID < keys[right].ConversationID
		})
		queueCounts := make(map[conversationKey]int, len(keys))
		for _, key := range keys {
			queue, queueErr := transaction.ListQueue(ctx, key.ProjectID, key.ConversationID)
			if queueErr != nil {
				return queueErr
			}
			queueCounts[key] = len(queue)
			result.Queues = append(result.Queues, queueDTO(key.ProjectID, key.ConversationID, queue))
		}
		for _, message := range interrupted {
			key := conversationKey{ProjectID: message.ProjectID, ConversationID: message.ConversationID}
			if err := transaction.AppendQueueEvent(ctx, queueEvent(message.ProjectID, message.ConversationID, message.ID,
				turnID(message.ID), domainchat.StatusInterrupted, queueCounts[key], "chat-recovery", occurredAt,
				"chat turn interrupted during recovery")); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return RecoveryResult{}, mapQueueError(err)
	}
	return result, nil
}

type conversationKey struct {
	ProjectID      int64
	ConversationID int64
}
