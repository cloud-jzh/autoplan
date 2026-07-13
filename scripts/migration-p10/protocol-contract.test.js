'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { describe, it } = require('node:test');

const root = path.resolve(__dirname, '..', '..');
const fixtureRoot = path.join(root, 'fixtures', 'migration', 'p10');
const terminalStates = new Set(['succeeded', 'failed', 'cancelled', 'interrupted']);
const operationTypes = new Set(['operation.queued', 'operation.running', 'operation.succeeded', 'operation.failed', 'operation.cancelled', 'operation.interrupted']);
const resyncReasons = new Set(['last_event_id_invalid', 'last_event_id_future', 'history_expired', 'revision_gap', 'project_mismatch', 'slow_consumer']);
const rendererResyncReasons = new Set([...resyncReasons, 'invalid_event', 'event_out_of_order']);
const forbidden = /workspace[_-]?path|user[_-]?data|(?:^|_)(?:env|token|password|secret|credential)(?:$|_)|api[_-]?key|authorization|cookie|session|command|stdout|stderr|(?:^|_)path(?:$|_)|cwd|workdir/i;

function readJSON(name) {
  return JSON.parse(fs.readFileSync(path.join(fixtureRoot, name), 'utf8'));
}

function compareEventID(left, right) {
  assert.match(left, /^[1-9][0-9]{0,18}$/);
  assert.match(right, /^[1-9][0-9]{0,18}$/);
  return left.length === right.length ? left.localeCompare(right) : left.length - right.length;
}

function assertSafe(value, trail = 'root') {
  if (value === null || ['string', 'boolean', 'number'].includes(typeof value)) return;
  if (Array.isArray(value)) {
    assert.ok(value.length <= 128, `${trail} has an unbounded array`);
    value.forEach((item, index) => assertSafe(item, `${trail}[${index}]`));
    return;
  }
  assert.equal(typeof value, 'object', `${trail} is not JSON-safe`);
  for (const [key, child] of Object.entries(value)) {
    assert.doesNotMatch(key, forbidden, `${trail}.${key} is sensitive`);
    assertSafe(child, `${trail}.${key}`);
  }
}

function assertEnvelope(entry, projectID, permitsUnknownOperationType) {
  const event = entry.data;
  assert.deepEqual(Object.keys(event).sort(), [
    'event_class', 'event_id', 'occurred_at', 'operation_id', 'payload', 'project_id', 'project_revision', 'request_id', 'schema_version', 'type',
  ]);
  assert.equal(event.schema_version, 1);
  assert.match(event.occurred_at, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/);
  assertSafe(event.payload, 'payload');
  if (event.event_class === 'control') {
    assert.equal(event.event_id, null);
    assert.equal(event.project_revision, null);
    assert.equal(entry.sse_id, null);
    if (event.type === 'heartbeat') assert.deepEqual(event.payload, {});
    else {
      assert.equal(event.type, 'resync_required');
      assert.ok(resyncReasons.has(event.payload.reason));
    }
    return;
  }
  assert.ok(['business', 'operation'].includes(event.event_class));
  assert.match(event.event_id, /^[1-9][0-9]{0,18}$/);
  assert.ok(Number.isSafeInteger(event.project_revision) && event.project_revision > 0);
  assert.equal(entry.sse_id, event.event_id);
  assert.equal(entry.event, event.type);
  if (event.event_class === 'operation') {
    if (permitsUnknownOperationType) assert.ok(!operationTypes.has(event.type));
    else assert.ok(operationTypes.has(event.type));
    assert.match(event.operation_id, /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/);
  }
  if (event.project_id !== projectID) assert.equal(projectID, 7, 'cross-project case must stay explicit');
}

describe('P10 protocol contract fixtures', () => {
  it('keeps operation matrix deterministic, idempotent, and single-terminal', () => {
    const fixture = readJSON('operation-cases.json');
    assert.equal(fixture.schema_version, 1);
    assert.equal(fixture.fixture_kind, 'p10-operation-fault-matrix');
    assert.ok(Array.isArray(fixture.cases) && fixture.cases.length >= 6);
    const ids = new Set();
    for (const item of fixture.cases) {
      assert.match(item.id, /^[a-z0-9_]+$/);
      assert.ok(!ids.has(item.id), `duplicate case ${item.id}`);
      ids.add(item.id);
      assert.ok(Number.isSafeInteger(item.project_id) && item.project_id > 0);
      assert.match(item.operation_type, /^[a-z][a-z0-9]*(?:[._:-][a-z0-9]+)*$/);
      assert.match(item.idempotency_key, /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/);
      assert.match(item.request_digest, /^[a-f0-9]{64}$/);
      assert.ok(Array.isArray(item.commands) && item.commands.length > 0);
      assertSafe(item.expected, `operation.${item.id}.expected`);
      const terminalCount = item.expected.terminal_events ?? item.expected.outbox_events?.filter((type) => terminalStates.has(String(type).slice('operation.'.length))).length ?? 0;
      assert.ok(terminalCount <= 1, `${item.id} has more than one terminal event`);
    }
  });

  it('keeps event streams P10-shaped and makes resync explicit', () => {
    const fixture = readJSON('event-streams.json');
    assert.equal(fixture.schema_version, 1);
    assert.equal(fixture.fixture_kind, 'p10-event-stream-fault-matrix');
    assert.ok(Array.isArray(fixture.streams) && fixture.streams.length >= 6);
    for (const stream of fixture.streams) {
      assert.match(stream.id, /^[a-z0-9_]+$/);
      assert.ok(Number.isSafeInteger(stream.project_id) && stream.project_id > 0);
      if (stream.last_event_id !== null) assert.match(stream.last_event_id, /^[1-9][0-9]{0,18}$/);
      let previous = stream.last_event_id;
      let revision = null;
      for (const entry of stream.entries) {
        assertEnvelope(entry, stream.project_id, stream.expected.reason === 'invalid_event');
        const event = entry.data;
        if (event.event_id === null) continue;
        if (previous !== null && compareEventID(event.event_id, previous) <= 0) continue;
        if (revision !== null && event.project_id === stream.project_id && stream.expected.action === 'apply') {
          assert.equal(event.project_revision, revision + 1, `${stream.id} lost a project revision`);
        }
        previous = event.event_id;
        revision = event.project_revision;
      }
      if (stream.expected.action === 'resync') assert.ok(rendererResyncReasons.has(stream.expected.reason));
      if (stream.expected.action === 'apply') assert.match(stream.expected.event_id, /^[1-9][0-9]{0,18}$/);
    }
  });

  it('documents fixture isolation and keeps P007 fault tests present', () => {
    const readme = fs.readFileSync(path.join(fixtureRoot, 'README.md'), 'utf8');
    assert.match(readme, /不会启动服务|不会.*访问/);
    for (const source of [
      'backend/internal/application/operations/integration_test.go',
      'backend/internal/runtime/eventbus/recovery_test.go',
      'backend/internal/httpapi/sse_integration_test.go',
      'src/renderer/lib/api/eventStream.contract.test.ts',
    ]) {
      assert.ok(fs.statSync(path.join(root, source)).isFile(), `missing P007 test ${source}`);
    }
  });
});
