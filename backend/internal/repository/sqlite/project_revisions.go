package sqlite

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/lyming99/autoplan/backend/internal/repository"
)

// allocateProjectRevision assigns the next visible P10 revision inside the
// caller's SQLite transaction. The Writer serializes top-level transactions;
// the compare-free increment is therefore contiguous for each project and is
// rolled back with the Operation and outbox write on every failure.
func (transaction *writeTransaction) allocateProjectRevision(ctx context.Context, projectID int64) (int64, error) {
	if projectID <= 0 {
		return 0, repository.ErrTransaction
	}
	if _, err := transaction.tx.ExecContext(ctx,
		"INSERT OR IGNORE INTO project_revisions (project_id, revision) VALUES (?, 0)", projectID); err != nil {
		return 0, safeSQLError(ctx, err)
	}
	if err := transaction.wrote("project-revisions:ensure"); err != nil {
		return 0, err
	}
	result, err := transaction.tx.ExecContext(ctx,
		"UPDATE project_revisions SET revision = revision + 1 WHERE project_id = ?", projectID)
	if err != nil {
		return 0, safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		return 0, err
	}
	if err := transaction.wrote("project-revisions:advance"); err != nil {
		return 0, err
	}
	var revision int64
	if err := transaction.tx.QueryRowContext(ctx,
		"SELECT revision FROM project_revisions WHERE project_id = ?", projectID).Scan(&revision); err != nil {
		return 0, safeSQLError(ctx, err)
	}
	if revision <= 0 {
		return 0, repository.ErrTransaction
	}
	return revision, nil
}

// allocateEventID advances the sole P10 cursor. The returned decimal string
// is opaque to callers but strictly increasing among committed outbox rows.
func (transaction *writeTransaction) allocateEventID(ctx context.Context) (string, error) {
	result, err := transaction.tx.ExecContext(ctx,
		"UPDATE event_cursors SET next_event_id = next_event_id + 1 WHERE name = 'outbox'")
	if err != nil {
		return "", safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		return "", err
	}
	if err := transaction.wrote("event-cursors:advance"); err != nil {
		return "", err
	}
	var cursor int64
	if err := transaction.tx.QueryRowContext(ctx,
		"SELECT next_event_id FROM event_cursors WHERE name = 'outbox'").Scan(&cursor); err != nil {
		if err == sql.ErrNoRows {
			return "", repository.ErrInvalidStore
		}
		return "", safeSQLError(ctx, err)
	}
	if cursor <= 0 {
		return "", repository.ErrTransaction
	}
	return strconv.FormatInt(cursor, 10), nil
}
