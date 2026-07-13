package process

import (
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

// Output is bounded, UTF-8-safe and redacted. Bytes and Lines count accepted
// stream data rather than exposing a raw process transcript.
type Output struct {
	Stream          OutputStream
	Sequence        uint64
	Tail            string
	Bytes           int64
	Lines           int64
	Truncated       bool
	RedactionFailed bool
}

type OutputStream string

const (
	OutputStdout OutputStream = "stdout"
	OutputStderr OutputStream = "stderr"
)

type outputSequencer struct {
	mu   sync.Mutex
	next uint64
}

func (sequencer *outputSequencer) Next() uint64 {
	if sequencer == nil {
		return 0
	}
	sequencer.mu.Lock()
	defer sequencer.mu.Unlock()
	value := sequencer.next
	sequencer.next++
	return value
}

type outputBudget struct {
	mu       sync.Mutex
	bytes    int
	lines    int
	maxBytes int
	maxLines int
}

func newOutputBudget(maxBytes, maxLines int) *outputBudget {
	return &outputBudget{maxBytes: maxBytes, maxLines: maxLines}
}

func (budget *outputBudget) accept(bytes, lines int) bool {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if bytes < 0 || lines < 0 || budget.bytes > budget.maxBytes-bytes || budget.lines > budget.maxLines-lines {
		return false
	}
	budget.bytes += bytes
	budget.lines += lines
	return true
}

type outputCollector struct {
	mu              sync.Mutex
	limit           int
	lineLimit       int
	tailLimit       int
	chunkSize       int
	budget          *outputBudget
	stream          OutputStream
	sequencer       *outputSequencer
	redactor        *streamingRedactor
	pending         []byte
	tail            string
	bytes           int
	lines           int
	sequence        uint64
	truncated       bool
	redactionFailed bool
}

// Write lets os/exec stream directly into the bounded collector. It always
// reports success after accepting the caller's bytes, so a verbose child can
// never be stalled by a downstream result consumer.
func (collector *outputCollector) Write(chunk []byte) (int, error) {
	count := len(chunk)
	for len(chunk) != 0 {
		size := collector.chunkSize
		if size <= 0 || size > len(chunk) {
			size = len(chunk)
		}
		collector.append(chunk[:size])
		chunk = chunk[size:]
	}
	return count, nil
}

func newOutputCollector(limit, lineLimit, tailLimit, chunkSize int, budget *outputBudget) *outputCollector {
	return &outputCollector{limit: limit, lineLimit: lineLimit, tailLimit: tailLimit, chunkSize: chunkSize, budget: budget}
}

func newRedactedOutputCollector(
	stream OutputStream,
	limit int,
	lineLimit int,
	tailLimit int,
	chunkSize int,
	budget *outputBudget,
	redactor Redactor,
	sequencer *outputSequencer,
) *outputCollector {
	collector := newOutputCollector(limit, lineLimit, tailLimit, chunkSize, budget)
	collector.stream = stream
	collector.sequencer = sequencer
	collector.redactor = newStreamingRedactor(redactor, tailLimit)
	return collector
}

func (collector *outputCollector) append(chunk []byte) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	data := append(collector.pending, chunk...)
	text, remainder, valid := decodeUTF8Prefix(data)
	collector.pending = remainder
	if !valid {
		collector.pending = nil
		collector.redactionFailed = true
		collector.truncated = true
		collector.tail = ""
		return
	}
	if text == "" {
		return
	}
	bytes := len(text)
	lines := strings.Count(text, "\n")
	if collector.bytes > collector.limit-bytes || collector.lines > collector.lineLimit-lines || !collector.budget.accept(bytes, lines) {
		collector.truncated = true
		return
	}
	collector.bytes += bytes
	collector.lines += lines
	if collector.sequencer != nil {
		collector.sequence = collector.sequencer.Next()
	}
	if collector.redactor != nil {
		redacted, safe := collector.redactor.RedactChunk(text)
		if !safe {
			collector.redactionFailed = true
			collector.truncated = true
			collector.tail = ""
			return
		}
		text = redacted
	}
	collector.tail = appendTail(collector.tail, text, collector.tailLimit)
}

func (collector *outputCollector) finalize(redactor Redactor) Output {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if len(collector.pending) != 0 {
		collector.pending = nil
		collector.truncated = true
		collector.redactionFailed = true
		collector.tail = ""
	}
	if collector.redactor != nil {
		flushed, safe := collector.redactor.Flush()
		if !safe {
			collector.redactionFailed = true
			collector.truncated = true
			collector.tail = ""
		} else {
			if flushed != "" && collector.sequencer != nil {
				collector.sequence = collector.sequencer.Next()
			}
			collector.tail = appendTail(collector.tail, flushed, collector.tailLimit)
		}
	}
	tail, safe := redactor.Redact(collector.tail)
	result := Output{
		Stream:          collector.stream,
		Sequence:        collector.sequence,
		Tail:            tail,
		Bytes:           int64(collector.bytes),
		Lines:           int64(collector.lines),
		Truncated:       collector.truncated,
		RedactionFailed: !safe || collector.redactionFailed,
	}
	if !safe || collector.redactionFailed {
		result.Tail = ""
		result.Truncated = true
	}
	return result
}

func readOutput(stream io.Reader, collector *outputCollector, chunkBytes int) error {
	buffer := make([]byte, chunkBytes)
	for {
		count, err := stream.Read(buffer)
		if count > 0 {
			collector.append(buffer[:count])
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func decodeUTF8Prefix(value []byte) (string, []byte, bool) {
	if len(value) == 0 {
		return "", nil, true
	}
	limit := 0
	for limit < len(value) {
		if !utf8.FullRune(value[limit:]) {
			break
		}
		character, size := utf8.DecodeRune(value[limit:])
		if character == utf8.RuneError && size == 1 {
			return "", nil, false
		}
		limit += size
	}
	return string(value[:limit]), append([]byte(nil), value[limit:]...), true
}

func appendTail(existing, addition string, limit int) string {
	if limit <= 0 {
		return ""
	}
	value := existing + addition
	if len(value) <= limit {
		return value
	}
	start := len(value) - limit
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}
