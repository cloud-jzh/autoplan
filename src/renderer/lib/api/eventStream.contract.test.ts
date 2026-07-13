export {};

type TestRegistrar = (name: string, fn: () => void) => void;

declare function require(id: string): unknown;
declare const process: { cwd(): string };

const { describe, it } = require('node:test') as { describe: TestRegistrar; it: TestRegistrar };
const { readFileSync } = require('node:fs') as { readFileSync: (path: string, encoding: string) => string };
const { join } = require('node:path') as { join: (...parts: string[]) => string };

function expect(value: unknown, message: string) {
  if (!value) throw new Error(message);
}

function source(...parts: string[]) {
  return readFileSync(join(process.cwd(), ...parts), 'utf8');
}

describe('P10 renderer event stream contract matrix', () => {
  const stream = source('src', 'renderer', 'lib', 'api', 'eventStream.ts');
  const events = source('src', 'renderer', 'lib', 'api', 'events.ts');
  const hook = source('src', 'renderer', 'hooks', 'useSnapshot.ts');
  const fixtures = source('fixtures', 'migration', 'p10', 'event-streams.json');

  it('carries Last-Event-ID only after an accepted persistent envelope', () => {
    expect(stream.includes('compareEventIDs(eventID, watermark.eventId) <= 0'), 'event_id dedupe is required');
    expect(stream.includes('watermark = { eventId: eventID, projectRevision: revision };'), 'accepted event must advance both watermarks');
    expect(stream.includes('requireContiguousRevisions && revision !== watermark.projectRevision + 1'), 'project revision gaps must resync');
    expect(events.includes("event_out_of_order"), 'out-of-order events need a stable resync reason');
  });

  it('pauses reconnect until a single authoritative snapshot succeeds', () => {
    expect(stream.includes('resyncBlocked = true'), 'resync must pause the stream');
    expect(stream.includes('stop.completeResync'), 'snapshot owner must resume the stream explicitly');
    expect(hook.includes('let eventRefreshInFlight = false;'), 'snapshot refresh must be single-flight');
    expect(hook.includes('eventSubscription?.completeResync();'), 'resync completion must follow snapshot commit');
    expect(hook.includes('eventRefreshController?.abort();'), 'unmount must abort refreshes');
  });

  it('keeps duplicate, gap, retention and cross-project cases in the contract fixture', () => {
    for (const id of [
      'reconnect_drops_duplicate', 'revision_gap_requires_snapshot_resync',
      'retention_control_requires_snapshot_resync', 'cross_project_event_requires_snapshot_resync',
      'heartbeat_does_not_advance_cursor', 'invalid_cursor_control_requires_snapshot_resync',
      'future_cursor_control_requires_snapshot_resync', 'slow_consumer_control_requires_snapshot_resync',
      'replay_live_boundary_keeps_terminal_event', 'operation_filter_allows_project_revision_gap',
      'service_shutdown_retries_without_false_snapshot', 'unknown_event_requires_snapshot_resync',
    ]) {
      expect(fixtures.includes(`"id": "${id}"`), `missing stream fixture ${id}`);
    }
  });
});
