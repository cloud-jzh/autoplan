package audit

import (
	"context"
	"fmt"
	"strings"
)

type relationSpec struct {
	name        string
	table       string
	code        string
	countQuery  string
	sampleQuery string
}

var relationSpecs = []relationSpec{
	projectRelation("project_states.project", "project_states", "project_id", "project_id"),
	projectRelation("requirements.project", "requirements", "id", "project_id"),
	projectRelation("feedback.project", "feedback", "id", "project_id"),
	projectRelation("attachments.project", "attachments", "id", "project_id"),
	projectRelation("plans.project", "plans", "id", "project_id"),
	projectRelation("events.project", "events", "id", "project_id"),
	projectRelation("scripts.project", "scripts", "id", "project_id"),
	projectRelation("executors.project", "executors", "id", "project_id"),
	optionalProjectRelation("ai_configs.project", "ai_configs", "id", "project_id"),
	optionalProjectRelation("claude_cli_configs.project", "claude_cli_configs", "id", "project_id"),
	projectRelation("conversations.project", "conversations", "id", "project_id"),
	projectRelation("chat_messages.project", "chat_messages", "id", "project_id"),
	projectRelation("scan_files.project", "scan_files", "project_id || ':' || scan_type", "project_id"),
	optionalProjectRelation("operations.project", "operations", "operation_id", "project_id"),
	optionalProjectRelation("event_outbox.project", "event_outbox", "event_id", "project_id"),
	{
		name: "feedback.requirement", table: "feedback", code: "orphan_or_cross_project_requirement",
		countQuery: `SELECT COUNT(*) FROM feedback f LEFT JOIN requirements r ON r.id = f.requirement_id
WHERE f.requirement_id IS NOT NULL AND (r.id IS NULL OR r.project_id IS NOT f.project_id)`,
		sampleQuery: `SELECT CAST(f.id AS TEXT) FROM feedback f LEFT JOIN requirements r ON r.id = f.requirement_id
WHERE f.requirement_id IS NOT NULL AND (r.id IS NULL OR r.project_id IS NOT f.project_id) ORDER BY f.id LIMIT ?`,
	},
	legacyPlanRelation("requirements.linked_plan", "requirements"),
	legacyPlanRelation("feedback.linked_plan", "feedback"),
	{
		name: "plan_tasks.plan", table: "plan_tasks", code: "orphan_plan_task",
		countQuery: `SELECT COUNT(*) FROM plan_tasks t LEFT JOIN plans p ON p.id = t.plan_id WHERE p.id IS NULL`,
		sampleQuery: `SELECT CAST(t.id AS TEXT) FROM plan_tasks t LEFT JOIN plans p ON p.id = t.plan_id
WHERE p.id IS NULL ORDER BY t.id LIMIT ?`,
	},
	{
		name: "conversations.ai_config", table: "conversations", code: "orphan_ai_config",
		countQuery: `SELECT COUNT(*) FROM conversations c LEFT JOIN ai_configs a ON a.id = c.ai_config_id
WHERE c.ai_config_id IS NOT NULL AND a.id IS NULL`,
		sampleQuery: `SELECT CAST(c.id AS TEXT) FROM conversations c LEFT JOIN ai_configs a ON a.id = c.ai_config_id
WHERE c.ai_config_id IS NOT NULL AND a.id IS NULL ORDER BY c.id LIMIT ?`,
	},
	{
		name: "chat_messages.conversation", table: "chat_messages", code: "orphan_or_cross_project_conversation",
		countQuery: `SELECT COUNT(*) FROM chat_messages m LEFT JOIN conversations c ON c.id = m.conversation_id
WHERE m.conversation_id IS NULL OR c.id IS NULL OR c.project_id IS NOT m.project_id`,
		sampleQuery: `SELECT CAST(m.id AS TEXT) FROM chat_messages m LEFT JOIN conversations c ON c.id = m.conversation_id
WHERE m.conversation_id IS NULL OR c.id IS NULL OR c.project_id IS NOT m.project_id ORDER BY m.id LIMIT ?`,
	},
	{
		name: "attachments.owner", table: "attachments", code: "invalid_or_cross_project_attachment_owner",
		countQuery: `SELECT COUNT(*) FROM attachments a
LEFT JOIN requirements r ON a.owner_type = 'requirement' AND r.id = a.owner_id
LEFT JOIN feedback f ON a.owner_type = 'feedback' AND f.id = a.owner_id
WHERE a.owner_type NOT IN ('requirement','feedback')
   OR (a.owner_type = 'requirement' AND (r.id IS NULL OR r.project_id IS NOT a.project_id))
   OR (a.owner_type = 'feedback' AND (f.id IS NULL OR f.project_id IS NOT a.project_id))`,
		sampleQuery: `SELECT CAST(a.id AS TEXT) FROM attachments a
LEFT JOIN requirements r ON a.owner_type = 'requirement' AND r.id = a.owner_id
LEFT JOIN feedback f ON a.owner_type = 'feedback' AND f.id = a.owner_id
WHERE a.owner_type NOT IN ('requirement','feedback')
   OR (a.owner_type = 'requirement' AND (r.id IS NULL OR r.project_id IS NOT a.project_id))
   OR (a.owner_type = 'feedback' AND (f.id IS NULL OR f.project_id IS NOT a.project_id))
ORDER BY a.id LIMIT ?`,
	},
	{
		name: "intake_plan_links.project_plan", table: "intake_plan_links", code: "orphan_or_cross_project_intake_plan",
		countQuery: `SELECT COUNT(*) FROM intake_plan_links l
LEFT JOIN projects j ON j.id = l.project_id LEFT JOIN plans p ON p.id = l.plan_id
WHERE j.id IS NULL OR p.id IS NULL OR p.project_id IS NOT l.project_id`,
		sampleQuery: `SELECT CAST(l.id AS TEXT) FROM intake_plan_links l
LEFT JOIN projects j ON j.id = l.project_id LEFT JOIN plans p ON p.id = l.plan_id
WHERE j.id IS NULL OR p.id IS NULL OR p.project_id IS NOT l.project_id ORDER BY l.id LIMIT ?`,
	},
	{
		name: "intake_plan_links.intake", table: "intake_plan_links", code: "invalid_or_cross_project_intake",
		countQuery: `SELECT COUNT(*) FROM intake_plan_links l
LEFT JOIN requirements r ON l.intake_type = 'requirement' AND r.id = l.intake_id
LEFT JOIN feedback f ON l.intake_type = 'feedback' AND f.id = l.intake_id
WHERE l.intake_type NOT IN ('requirement','feedback')
   OR (l.intake_type = 'requirement' AND (r.id IS NULL OR r.project_id IS NOT l.project_id))
   OR (l.intake_type = 'feedback' AND (f.id IS NULL OR f.project_id IS NOT l.project_id))`,
		sampleQuery: `SELECT CAST(l.id AS TEXT) FROM intake_plan_links l
LEFT JOIN requirements r ON l.intake_type = 'requirement' AND r.id = l.intake_id
LEFT JOIN feedback f ON l.intake_type = 'feedback' AND f.id = l.intake_id
WHERE l.intake_type NOT IN ('requirement','feedback')
   OR (l.intake_type = 'requirement' AND (r.id IS NULL OR r.project_id IS NOT l.project_id))
   OR (l.intake_type = 'feedback' AND (f.id IS NULL OR f.project_id IS NOT l.project_id))
ORDER BY l.id LIMIT ?`,
	},
	{
		name: "event_outbox.operation", table: "event_outbox", code: "orphan_or_cross_project_outbox_operation",
		countQuery: `SELECT COUNT(*) FROM event_outbox e LEFT JOIN operations o ON o.operation_id = e.operation_id
WHERE e.operation_id IS NOT NULL AND (o.operation_id IS NULL OR o.project_id IS NOT e.project_id)`,
		sampleQuery: `SELECT CAST(e.id AS TEXT) FROM event_outbox e LEFT JOIN operations o ON o.operation_id = e.operation_id
WHERE e.operation_id IS NOT NULL AND (o.operation_id IS NULL OR o.project_id IS NOT e.project_id) ORDER BY e.id LIMIT ?`,
	},
}

func projectRelation(name, table, recordExpression, projectColumn string) relationSpec {
	return newProjectRelation(name, table, recordExpression, projectColumn, true)
}

func optionalProjectRelation(name, table, recordExpression, projectColumn string) relationSpec {
	return newProjectRelation(name, table, recordExpression, projectColumn, false)
}

func newProjectRelation(name, table, recordExpression, projectColumn string, required bool) relationSpec {
	condition := fmt.Sprintf("c.%s IS NOT NULL AND p.id IS NULL", projectColumn)
	if required {
		condition = fmt.Sprintf("c.%s IS NULL OR p.id IS NULL", projectColumn)
	}
	return relationSpec{
		name: name, table: table, code: "orphan_project",
		countQuery: fmt.Sprintf(`SELECT COUNT(*) FROM %s c LEFT JOIN projects p ON p.id = c.%s
WHERE %s`, table, projectColumn, condition),
		sampleQuery: fmt.Sprintf(`SELECT CAST(%s AS TEXT) FROM %s c LEFT JOIN projects p ON p.id = c.%s
WHERE %s ORDER BY %s LIMIT ?`, recordExpression, table, projectColumn, condition, recordExpression),
	}
}

func legacyPlanRelation(name, table string) relationSpec {
	return relationSpec{
		name: name, table: table, code: "orphan_or_cross_project_legacy_plan",
		countQuery: fmt.Sprintf(`SELECT COUNT(*) FROM %s i LEFT JOIN plans p ON p.id = i.linked_plan_id
WHERE i.linked_plan_id IS NOT NULL AND (p.id IS NULL OR p.project_id IS NOT i.project_id)`, table),
		sampleQuery: fmt.Sprintf(`SELECT CAST(i.id AS TEXT) FROM %s i LEFT JOIN plans p ON p.id = i.linked_plan_id
WHERE i.linked_plan_id IS NOT NULL AND (p.id IS NULL OR p.project_id IS NOT i.project_id) ORDER BY i.id LIMIT ?`, table),
	}
}

func auditRelations(ctx context.Context, database Queryer, available map[string]struct{}, maximum int, report *Report) error {
	for _, spec := range relationSpecs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !queryTablesAvailable(spec.countQuery, available) {
			report.Relations = append(report.Relations, RelationMetric{
				Relation: spec.name, RecordSetSHA256: newRecordSetHasher().Sum(), Evaluated: false,
			})
			continue
		}
		var count int64
		if err := database.QueryRowContext(ctx, spec.countQuery).Scan(&count); err != nil {
			return err
		}
		identifiers := make([]string, 0)
		fingerprint := newRecordSetHasher()
		if count > 0 {
			query, ok := strings.CutSuffix(spec.sampleQuery, " LIMIT ?")
			if !ok {
				return ErrAuditInvalid
			}
			rows, err := database.QueryContext(ctx, query)
			if err != nil {
				return err
			}
			for rows.Next() {
				var key string
				if err := rows.Scan(&key); err != nil {
					_ = rows.Close()
					return err
				}
				identifier := recordIdentifier(spec.name, key)
				fingerprint.Add(identifier)
				if len(identifiers) < maximum {
					identifiers = append(identifiers, identifier)
				}
			}
			if err := rows.Close(); err != nil || rows.Err() != nil {
				return ErrAuditIncomplete
			}
		}
		report.Relations = append(report.Relations, RelationMetric{
			Relation: spec.name, Count: count, RecordIDs: identifiers, RecordSetSHA256: fingerprint.Sum(),
			Truncated: count > int64(len(identifiers)), Evaluated: true,
		})
		if count > 0 {
			report.Findings = append(report.Findings, blocking("relation", spec.code, spec.table, "", count, identifiers))
		}
	}
	return nil
}
