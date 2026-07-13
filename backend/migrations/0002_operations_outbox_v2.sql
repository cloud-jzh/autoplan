-- P10 Operation and durable event-outbox contract.
-- This is append-only history: it upgrades an already-applied P09 v1 copy
-- without changing the v1 checksum or its compatibility tables.

ALTER TABLE operations ADD COLUMN cancel_requested_at TEXT;
ALTER TABLE operations ADD COLUMN output_json TEXT;

ALTER TABLE event_outbox ADD COLUMN event_class TEXT NOT NULL DEFAULT 'business'
  CHECK (event_class IN ('business', 'operation', 'control'));
ALTER TABLE event_outbox ADD COLUMN project_revision INTEGER;

CREATE TABLE IF NOT EXISTS project_revisions (
  project_id INTEGER PRIMARY KEY,
  revision INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT
);

-- The cursor is deliberately independent of legacy opaque event_id values.
-- P10 IDs begin after the highest physical outbox row and are stored as
-- decimal strings so transport clients do not lose integer precision.
CREATE TABLE IF NOT EXISTS event_cursors (
  name TEXT PRIMARY KEY CHECK (name = 'outbox'),
  next_event_id INTEGER NOT NULL DEFAULT 0 CHECK (next_event_id >= 0)
);

CREATE TABLE IF NOT EXISTS event_retention_watermarks (
  project_id INTEGER PRIMARY KEY,
  deleted_through_event_id TEXT NOT NULL DEFAULT '0'
    CHECK (deleted_through_event_id GLOB '[0-9]*'),
  updated_at TEXT NOT NULL,
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT
);

INSERT OR IGNORE INTO project_revisions (project_id, revision)
SELECT id, 0 FROM projects;

INSERT OR IGNORE INTO event_cursors (name, next_event_id)
SELECT 'outbox', COALESCE(MAX(id), 0) FROM event_outbox;

CREATE INDEX IF NOT EXISTS idx_operations_project_type_status
ON operations (project_id, type, status, created_at, operation_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_event_outbox_project_revision
ON event_outbox (project_id, project_revision)
WHERE project_id IS NOT NULL AND project_revision IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_event_outbox_operation_cursor
ON event_outbox (operation_id, CAST(event_id AS INTEGER))
WHERE operation_id IS NOT NULL AND project_revision IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_event_outbox_p10_cursor
ON event_outbox (CAST(event_id AS INTEGER))
WHERE project_revision IS NOT NULL;
