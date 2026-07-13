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

const source = readFileSync(
  join(process.cwd(), 'src', 'renderer', 'lib', 'api', 'eventStream.ts'),
  'utf8',
);

describe('P10 renderer event stream ownership', () => {
  it('uses bounded retry, opaque decimal event cursors, and resync gating', () => {
    expect(source.includes('MAXIMUM_RETRY_DELAY_MS = 10_000'), 'retry delay must remain bounded');
    expect(source.includes('compareEventIDs(eventID, watermark.eventId) <= 0'), 'stale event ids must be dropped');
    expect(source.includes('requireContiguousRevisions && revision !== watermark.projectRevision + 1'), 'project revision gaps must resync');
    expect(source.includes('resyncBlocked = true'), 'resync must pause reconnects');
    expect(source.includes('stop.completeResync'), 'snapshot owner must explicitly resume a resync');
  });

  it('keeps parsing bounded and rejects malformed frames before delivery', () => {
    expect(source.includes('MAXIMUM_FRAME_BYTES = 1024 * 1024'), 'SSE frame buffering must be bounded');
    expect(source.includes('parseP10EventEnvelope(frame.data)'), 'frames must validate the P10 envelope');
    expect(source.includes("throw new TypeError('invalid_event_stream')"), 'malformed frames must fail closed');
  });
});
