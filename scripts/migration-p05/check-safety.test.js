'use strict';

const assert = require('node:assert/strict');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const {
  SafetyError,
  inspectEvidenceSummary,
  inspectSourceSafety,
  inspectWriterTimeline,
  isOwnedTemporaryRoot,
  parseArgs,
  scanSensitiveText,
} = require('./check-safety');

const ROOT = path.resolve(__dirname, '../..');

function accepted(id, start, end) {
  return {
    id,
    command: id,
    startedAt: new Date(start).toISOString(),
    endedAt: new Date(end).toISOString(),
    failureSignatures: [],
    structuredOutput: null,
    evaluation: { accepted: true, reason: 'exit code 0' },
  };
}

function safeTimeline() {
  const ids = [
    'p04-gate', 'node-write-contracts', 'go-repository', 'go-application', 'go-http',
    'go-files', 'renderer-transport', 'mutation-golden-compare', 'p05-safety-preflight',
    'p05-orchestration-tests', 'check', 'test',
  ];
  const epoch = Date.parse('2026-07-11T05:00:00.000Z');
  return ids.map((id, index) => accepted(id, epoch + index * 20, epoch + index * 20 + 10));
}

function safeSummary() {
  return {
    schemaVersion: 1,
    environment: {
      electronUserDataAccessed: false,
      productionDatabaseOpened: false,
      databaseContentCaptured: false,
    },
    databaseOwnership: {
      authorizedCopiesOnly: true,
      p04OwnerGateAccepted: true,
      goWriteRequiresOwnerProof: true,
      ownerGuardSha256: 'a'.repeat(64),
    },
    sourceHashesStable: true,
    commandResults: safeTimeline(),
  };
}

test('argument parser only accepts explicit safety modes', () => {
  assert.deepEqual(parseArgs(['preflight']), { mode: 'preflight' });
  assert.deepEqual(parseArgs(['evidence', 'summary.json']), { mode: 'evidence', summary: 'summary.json' });
  assert.throws(() => parseArgs([]), /usage:/);
  assert.throws(() => parseArgs(['evidence']), /usage:/);
});

test('owned temporary roots are restricted to the P05 verifier prefix', () => {
  assert.equal(isOwnedTemporaryRoot(path.join(os.tmpdir(), 'autoplan-p05-verify-safe')), true);
  assert.equal(isOwnedTemporaryRoot(path.join(os.tmpdir(), 'autoplan-p04-verify-safe')), false);
  assert.equal(isOwnedTemporaryRoot(ROOT), false);
});

test('sensitive material scanner detects reusable credentials and userData access', () => {
  assert.deepEqual(scanSensitiveText('status=ok'), []);
  assert.ok(scanSensitiveText('Bearer abcdefghijklmnop').includes('usable-bearer'));
  assert.ok(scanSensitiveText('app.getPath("userData")').includes('electron-userdata'));
  assert.ok(scanSensitiveText('C:\\product\\autoplan.sqlite').includes('production-database'));
});

test('writer timeline proves the Node-to-Go handoff and rejects overlap', () => {
  const result = inspectWriterTimeline(safeTimeline());
  assert.equal(result.sequential, true);
  assert.equal(result.simultaneousNodeGoWriter, false);
  assert.equal(result.nodeClosedBeforeGo, true);

  const overlap = safeTimeline();
  overlap[2].startedAt = overlap[1].startedAt;
  assert.throws(() => inspectWriterTimeline(overlap), (error) =>
    error instanceof SafetyError && error.code === 'command_timeline_overlap');
});

test('evidence safety requires accepted commands, stable sources, and no sensitive output', () => {
  assert.equal(inspectEvidenceSummary(safeSummary()).ok, true);

  const rejected = safeSummary();
  rejected.commandResults[3].evaluation.accepted = false;
  assert.throws(() => inspectEvidenceSummary(rejected), (error) =>
    error instanceof SafetyError && error.code === 'command_not_accepted');

  const sensitive = safeSummary();
  sensitive.commandResults[3].command = 'Bearer abcdefghijklmnop';
  assert.throws(() => inspectEvidenceSummary(sensitive), (error) =>
    error instanceof SafetyError && error.code === 'evidence_sensitive');
});

test('checked P05 sources retain frozen hashes, routes, and transport boundaries', () => {
  const result = inspectSourceSafety(ROOT);
  assert.equal(result.ok, true);
  assert.equal(result.schemaVersion, 1);
  assert.ok(result.openapiRoutes.includes('/api/v1/projects'));
  assert.match(result.goldenSha256, /^[a-f0-9]{64}$/);
});
