package terminal

import (
	"errors"
	"testing"

	domainterminal "github.com/lyming99/autoplan/backend/internal/domain/terminal"
)

func TestReplayBufferDropsWholeChunksAndRejectsExpiredCursors(t *testing.T) {
	buffer := newReplayBuffer(replayLimits{entries: 2, bytes: 5})
	buffer.append(domainterminal.Output{Seq: 1, Data: "ab"})
	buffer.append(domainterminal.Output{Seq: 2, Data: "cd"})
	buffer.append(domainterminal.Output{Seq: 3, Data: "ef"})
	if len(buffer.entries) != 2 || buffer.entries[0].Seq != 2 || buffer.entries[0].Data != "cd" || buffer.bytes != 4 {
		t.Fatalf("bounded replay = %#v bytes=%d", buffer.entries, buffer.bytes)
	}
	if _, first, err := buffer.after(0, 3); !errors.Is(err, domainterminal.ErrReplayGap) || first != 2 {
		t.Fatalf("expired replay = first=%d err=%v", first, err)
	}
	entries, first, err := buffer.after(1, 3)
	if err != nil || first != 2 || len(entries) != 2 || entries[0].Seq != 2 || entries[1].Seq != 3 {
		t.Fatalf("replay after retained cursor = %#v first=%d err=%v", entries, first, err)
	}
	if _, _, err := buffer.after(4, 3); !errors.Is(err, domainterminal.ErrCursorTooOld) {
		t.Fatalf("future cursor error = %v", err)
	}
}

func TestReplayBufferClearDoesNotResetSequenceWatermark(t *testing.T) {
	buffer := newReplayBuffer(replayLimits{entries: 4, bytes: 32})
	buffer.append(domainterminal.Output{Seq: 7, Data: "retained"})
	buffer.clear()
	entries, first, err := buffer.after(7, 7)
	if err != nil || first != 0 || len(entries) != 0 {
		t.Fatalf("cleared replay at watermark = %#v first=%d err=%v", entries, first, err)
	}
	if _, _, err := buffer.after(6, 7); !errors.Is(err, domainterminal.ErrReplayGap) {
		t.Fatalf("cleared replay old cursor error = %v", err)
	}
}
