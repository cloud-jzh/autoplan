package mcp

import (
	"context"
	"strings"
	"time"
)

// AuditEvent records transport facts only. Arguments, results, credentials,
// paths, request IDs, and implementation errors are intentionally absent.
type AuditEvent struct {
	OccurredAt time.Time
	Transport  string
	Action     string
	Tool       string
	Outcome    string
}

type AuditSink interface {
	Record(context.Context, AuditEvent)
}

type nopAudit struct{}

func (nopAudit) Record(context.Context, AuditEvent) {}

func recordAudit(ctx context.Context, sink AuditSink, event AuditEvent) {
	if sink == nil {
		return
	}
	event.Transport = boundedAuditText(event.Transport, 16)
	event.Action = boundedAuditText(event.Action, 32)
	event.Tool = boundedAuditText(event.Tool, 128)
	event.Outcome = boundedAuditText(event.Outcome, 64)
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	} else {
		event.OccurredAt = event.OccurredAt.UTC()
	}
	defer func() { _ = recover() }()
	sink.Record(ctx, event)
}

func boundedAuditText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
