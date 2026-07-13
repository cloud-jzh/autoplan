package chat

import (
	"testing"

	domainchat "github.com/lyming99/autoplan/backend/internal/domain/chat"
)

func TestProviderDigestBindsTurnAndPrompt(t *testing.T) {
	command := ProviderCommand{
		ProjectID: 1, ConversationID: 2, Prompt: "first", RequestID: "request-1",
		Profile: domainchat.ProviderProfile{Kind: domainchat.ProviderCodexCLI, Model: "model"},
	}
	claim := TurnClaim{TurnID: "chat-turn-9"}
	first := providerDigest(command, claim)
	command.Prompt = "second"
	if first == providerDigest(command, claim) || len(first) != 64 {
		t.Fatal("provider digest did not bind the durable turn intent")
	}
}

func TestChunkCollectorRejectsSplitSensitiveOutput(t *testing.T) {
	collector := &ChunkCollector{}
	if _, err := collector.Push(domainchat.ProviderChunk{Kind: domainchat.ChunkText, Text: "prefix to"}); err != nil {
		t.Fatal(err)
	}
	if _, err := collector.Push(domainchat.ProviderChunk{Kind: domainchat.ChunkText, Text: "ken=secret"}); err == nil {
		t.Fatal("split sensitive output was accepted")
	}
}

func TestParseOpenAIChunkPreservesTextOnly(t *testing.T) {
	chunks, err := parseOpenAIJSON([]byte(`{"choices":[{"delta":{"content":"safe text"}}]}`))
	if err != nil || len(chunks) != 1 || chunks[0].Kind != domainchat.ChunkText || chunks[0].Text != "safe text" {
		t.Fatalf("chunks=%#v error=%v", chunks, err)
	}
}
