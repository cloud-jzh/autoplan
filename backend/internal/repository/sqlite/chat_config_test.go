package sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	domainchat "github.com/lyming99/autoplan/backend/internal/domain/chat"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

func TestChatConfigContractRejectsUnscopedConversationAndMessageReads(t *testing.T) {
	backend := &scriptBackend{}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()

	err := writer.TransactChat(context.Background(), func(transaction repository.ChatWriteTransaction) error {
		_, _, listErr := transaction.ListConversations(context.Background(), domainchat.ConversationListOptions{ProjectID: 0})
		return listErr
	})
	if !errors.Is(err, repository.ErrInvalidAutomation) {
		t.Fatalf("unscoped conversation list error=%v", err)
	}
	backend.assertFinished(t, 0, 1)

	backend = &scriptBackend{}
	writer, cleanup = newTestWriter(t, backend)
	defer cleanup()
	err = writer.TransactChat(context.Background(), func(transaction repository.ChatWriteTransaction) error {
		_, _, listErr := transaction.ListChatMessages(context.Background(), domainchat.MessageListOptions{ProjectID: 1, ConversationID: 0})
		return listErr
	})
	if !errors.Is(err, repository.ErrInvalidAutomation) {
		t.Fatalf("unscoped message list error=%v", err)
	}
	backend.assertFinished(t, 0, 1)
}

func TestChatConfigContractBoundsCursorsAndRollback(t *testing.T) {
	if boundedChatPage(0) != 100 || boundedChatPage(201) != 200 || boundedChatPage(3) != 3 {
		t.Fatal("chat page bounds drifted")
	}
	if _, err := domainchat.DecodeConversationCursor("forged"); err == nil {
		t.Fatal("forged conversation cursor must be rejected")
	}
	if _, err := domainchat.DecodeMessageCursor("forged"); err == nil {
		t.Fatal("forged message cursor must be rejected")
	}

	backend := &scriptBackend{}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()
	err := writer.TransactChat(context.Background(), func(repository.ChatWriteTransaction) error {
		return repository.ErrTransaction
	})
	if !errors.Is(err, repository.ErrTransaction) {
		t.Fatalf("chat transaction fault error=%v", err)
	}
	backend.assertFinished(t, 0, 1)
}

func TestChatConfigContractDeleteCascadesMessagesBeforeConversation(t *testing.T) {
	backend := &scriptBackend{steps: []scriptStep{
		queryStep("FROM conversations WHERE project_id = ? AND id = ?", conversationTestColumns(), conversationTestValues()),
		execStep("DELETE FROM chat_messages", 2, 0),
		execStep("DELETE FROM conversations", 1, 0),
	}}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()
	var deleted int64
	err := writer.TransactChat(context.Background(), func(transaction repository.ChatWriteTransaction) error {
		var deleteErr error
		deleted, deleteErr = transaction.DeleteConversation(context.Background(), 1, 9)
		return deleteErr
	})
	if err != nil || deleted != 2 {
		t.Fatalf("conversation cascade deleted=%d error=%v", deleted, err)
	}
	backend.assertFinished(t, 1, 0)
}

func TestChatConfigContractMessageWriteFaultRollsBackConversationTouch(t *testing.T) {
	backend := &scriptBackend{steps: []scriptStep{
		queryStep("FROM conversations WHERE project_id = ? AND id = ?", conversationTestColumns(), conversationTestValues()),
		execStep("INSERT INTO chat_messages", 1, 31),
	}}
	writer, cleanup := newTestWriter(t, backend)
	defer cleanup()
	writer.faults.afterWrite = func(label string) error {
		if label == "chat-messages:create" {
			return errors.New("injected message write fault")
		}
		return nil
	}
	err := writer.TransactChat(context.Background(), func(transaction repository.ChatWriteTransaction) error {
		_, appendErr := transaction.AppendChatMessage(context.Background(), domainchat.MessageInput{
			ProjectID: 1, ConversationID: 9, Role: "assistant", Content: "fixture content", CreatedAt: "2026-07-11T00:00:02.000Z",
		})
		return appendErr
	})
	if !errors.Is(err, repository.ErrTransaction) {
		t.Fatalf("message write fault error=%v", err)
	}
	backend.assertFinished(t, 0, 1)
}

func conversationTestColumns() []string {
	return []string{"id", "project_id", "title", "ai_config_id", "pinned_at", "created_at", "updated_at"}
}

func conversationTestValues() []driver.Value {
	return []driver.Value{int64(9), int64(1), "fixture conversation", nil, nil, "2026-07-11T00:00:00.000Z", "2026-07-11T00:00:01.000Z"}
}
