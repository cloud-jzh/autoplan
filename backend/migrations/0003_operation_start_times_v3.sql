-- Repair synchronous idempotency operations written before their running
-- transition persisted started_at. The created timestamp is the exact start
-- boundary for those single-transaction operations.

UPDATE operations
SET started_at = created_at
WHERE started_at IS NULL
  AND status IN ('running', 'succeeded', 'failed', 'cancelled', 'interrupted');
