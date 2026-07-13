package terminal

import (
	domainterminal "github.com/lyming99/autoplan/backend/internal/domain/terminal"
)

const (
	defaultReplayEntries = 4096
	defaultReplayBytes   = 1 << 20
)

// replayBuffer is deliberately memory-only. It retains complete output chunks
// in ascending sequence order and drops the oldest complete chunk on either
// entry or byte pressure; it never slices a UTF-8 chunk to fit a budget.
type replayBuffer struct {
	entries  []domainterminal.Output
	bytes    int
	maximums replayLimits
}

type replayLimits struct {
	entries int
	bytes   int
}

func newReplayBuffer(limits replayLimits) replayBuffer {
	if limits.entries <= 0 {
		limits.entries = defaultReplayEntries
	}
	if limits.bytes <= 0 {
		limits.bytes = defaultReplayBytes
	}
	return replayBuffer{maximums: limits, entries: make([]domainterminal.Output, 0, limits.entries)}
}

func (buffer *replayBuffer) append(output domainterminal.Output) {
	if buffer == nil || output.Seq == 0 || output.Data == "" {
		return
	}
	buffer.entries = append(buffer.entries, output)
	buffer.bytes += len(output.Data)
	for len(buffer.entries) > 0 && (len(buffer.entries) > buffer.maximums.entries || buffer.bytes > buffer.maximums.bytes) {
		buffer.bytes -= len(buffer.entries[0].Data)
		buffer.entries[0].Data = ""
		buffer.entries = buffer.entries[1:]
	}
	if len(buffer.entries) == 0 {
		buffer.bytes = 0
	}
}

func (buffer *replayBuffer) after(lastSeq, watermark uint64) ([]domainterminal.Output, uint64, error) {
	if lastSeq > watermark {
		return nil, 0, domainterminal.ErrCursorTooOld
	}
	if len(buffer.entries) == 0 {
		if lastSeq != watermark {
			return nil, 0, domainterminal.ErrReplayGap
		}
		return []domainterminal.Output{}, 0, nil
	}
	first := buffer.entries[0].Seq
	if first == 0 || (lastSeq < first-1) {
		return nil, first, domainterminal.ErrReplayGap
	}
	result := make([]domainterminal.Output, 0, len(buffer.entries))
	for _, entry := range buffer.entries {
		if entry.Seq > lastSeq && entry.Seq <= watermark {
			result = append(result, entry)
		}
	}
	return result, first, nil
}

func (buffer *replayBuffer) clear() {
	if buffer == nil {
		return
	}
	for index := range buffer.entries {
		buffer.entries[index].Data = ""
	}
	buffer.entries = buffer.entries[:0]
	buffer.bytes = 0
}
