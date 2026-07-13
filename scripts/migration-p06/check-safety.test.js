'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const {
  GO_WRITER_COMMANDS,
  SafetyError,
  inspectEvidenceSummary,
  inspectP05Evidence,
  inspectSourceSafety,
  inspectWriterTimeline,
  isOwnedTemporaryRoot,
  parseArgs,
  scanSensitiveText,
} = require('./check-safety');

const ROOT = path.resolve(__dirname, '../..');

function accepted(id, start, end) {
  return {
    id, command: id, startedAt: new Date(start).toISOString(), endedAt: new Date(end).toISOString(),
    failureSignatures: [], structuredOutput: null, evaluation: { accepted: true, reason: 'exit code 0' },
  };
}

function safeTimeline() {
  const ids = ['p05-gate', 'p06-safety-preflight', 'node-golden-generator', ...GO_WRITER_COMMANDS, 'p06-golden-contract'];
  const epoch = Date.parse('2026-07-11T08:00:00.000Z');
  return ids.map((id, index) => accepted(id, epoch + index * 20, epoch + index * 20 + 10));
}

function safeSummary() {
  return {
    schemaVersion: 1,
    status: 'completed',
    environment: {
      electronUserDataAccessed: false, productionDatabaseOpened: false,
      databaseContentCaptured: false, attachmentContentCaptured: false, temporaryRootsOnly: true,
    },
    databaseOwnership: {
      p05GateAccepted: true, p05EvidenceAccepted: true, authorizedCopiesOnly: true,
      goWriteRequiresOwnerProof: true, ownerGuardSha256: 'a'.repeat(64),
    },
    sourceHashesStable: true,
    commandResults: safeTimeline(),
  };
}

test('argument parser accepts only explicit P06 safety modes', () => {
  assert.deepEqual(parseArgs(['preflight']), { mode: 'preflight' });
  assert.deepEqual(parseArgs(['evidence', 'summary.json']), { mode: 'evidence', summary: 'summary.json' });
  assert.throws(() => parseArgs([]), /usage:/);
  assert.throws(() => parseArgs(['evidence']), /usage:/);
});

test('P06 temporary ownership is constrained to its verifier prefix', () => {
  assert.equal(isOwnedTemporaryRoot(path.join(os.tmpdir(), 'autoplan-p06-verify-safe')), true);
  assert.equal(isOwnedTemporaryRoot(path.join(os.tmpdir(), 'autoplan-p05-verify-safe')), false);
  assert.equal(isOwnedTemporaryRoot(ROOT), false);
});

test('sensitive material scanner rejects reusable credentials and local data surfaces', () => {
  assert.deepEqual(scanSensitiveText('status=ok'), []);
  assert.ok(scanSensitiveText('Bearer abcdefghijklmnop').includes('usable-bearer'));
  assert.ok(scanSensitiveText('app.getPath("userData")').includes('electron-userdata'));
  assert.ok(scanSensitiveText('file:///private/fixture.txt').includes('local-file-url'));
});

test('writer timeline enforces P05 before one Node writer and all Go writers', () => {
  const result = inspectWriterTimeline(safeTimeline());
  assert.equal(result.sequential, true);
  assert.equal(result.simultaneousNodeGoWriter, false);
  assert.equal(result.nodeClosedBeforeGo, true);
  assert.deepEqual(new Set(result.goWriterCommands), GO_WRITER_COMMANDS);

  const overlap = safeTimeline();
  overlap[3].startedAt = overlap[2].startedAt;
  assert.throws(() => inspectWriterTimeline(overlap), (error) =>
    error instanceof SafetyError && error.code === 'command_timeline_overlap');
});

test('evidence requires accepted commands, P05 gate and temporary-only safety declarations', () => {
  assert.equal(inspectEvidenceSummary(safeSummary()).ok, true);

  const rejected = safeSummary();
  rejected.commandResults[3].evaluation.accepted = false;
  assert.throws(() => inspectEvidenceSummary(rejected), (error) =>
    error instanceof SafetyError && error.code === 'command_not_accepted');

  const unsafe = safeSummary();
  unsafe.environment.temporaryRootsOnly = false;
  assert.throws(() => inspectEvidenceSummary(unsafe), (error) =>
    error instanceof SafetyError && error.code === 'evidence_summary_invalid');
});

test('P05 evidence must be immutable, completed and hash-linked', (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p06-safety-test-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const run = path.join(root, 'docs/migration/p05/evidence/runs/20260711');
  fs.mkdirSync(run, { recursive: true });
  const summaryBytes = Buffer.from(JSON.stringify({
    schemaVersion: 1, status: 'completed', ok: true, sourceHashesStable: true,
  }) + '\n');
  fs.writeFileSync(path.join(run, 'summary.json'), summaryBytes);
  const digest = crypto.createHash('sha256').update(summaryBytes).digest('hex');
  fs.writeFileSync(path.join(run, 'evidence-manifest.json'), JSON.stringify({
    schemaVersion: 1, immutableRunDirectory: true, artifacts: [{ path: 'summary.json', sha256: digest }],
  }));
  assert.equal(inspectP05Evidence(root).summarySha256, digest);
  fs.writeFileSync(path.join(run, 'summary.json'), '{}');
  assert.throws(() => inspectP05Evidence(root), (error) =>
    error instanceof SafetyError && error.code === 'p05_evidence_invalid');
});

test('checked P06 sources retain frozen fixture hashes, routes and policy boundaries', () => {
  const result = inspectSourceSafety(ROOT);
  assert.equal(result.ok, true);
  assert.equal(result.schemaVersion, 1);
  assert.ok(result.openapiRoutes.includes('/api/v1/projects/{project_id}/requirements'));
  assert.match(result.goldenSha256, /^[a-f0-9]{64}$/);
});
