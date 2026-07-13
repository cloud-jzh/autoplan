package process

import (
	"strings"
	"unicode/utf8"
)

// BoundPersistentOutput applies the stricter archive boundary to a runner result.
// The runner's in-memory tail stays available for the active operation, while
// durable records can retain a smaller tail without losing stream, sequence or
// truncation metadata. Persistence code must use this helper rather than
// slicing raw output itself.
func BoundPersistentOutput(value Output, maxBytes, maxLines int) Output {
	if maxBytes <= 0 || maxLines <= 0 {
		value.Tail = ""
		value.Truncated = true
		return value
	}
	tail := trimTailLines(value.Tail, maxLines)
	tail = appendTail("", tail, maxBytes)
	if tail != value.Tail {
		value.Truncated = true
	}
	value.Tail = tail
	return value
}

// PersistentOutput applies this Runner's configured durable-tail limits. It
// is intentionally separate from Run: active callers may need the larger
// bounded in-memory tail, while persistence must never silently inherit it.
func (runner *Runner) PersistentOutput(value Output) Output {
	if runner == nil {
		return BoundPersistentOutput(value, 0, 0)
	}
	return BoundPersistentOutput(value, runner.config.MaxPersistentTailBytes, runner.config.MaxPersistentTailLines)
}

func trimTailLines(value string, maximum int) string {
	if maximum <= 0 || value == "" {
		return ""
	}
	if strings.Count(value, "\n")+1 <= maximum {
		return value
	}
	index := len(value)
	for retained := 0; index > 0 && retained < maximum; {
		previous := strings.LastIndex(value[:index], "\n")
		if previous < 0 {
			return value
		}
		index = previous
		retained++
	}
	if index >= len(value) {
		return ""
	}
	start := index + 1
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}
