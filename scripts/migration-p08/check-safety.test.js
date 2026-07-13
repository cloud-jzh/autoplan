'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const {
  GO_WRITER_COMMANDS,
  P00_GATE_COMMANDS,
  SafetyError,
  inspectEvidenceSummary,
  inspectP07Evidence,
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
  const ids = ['p07-gate', 'p08-safety-preflight', ...P00_GATE_COMMANDS, 'node-static-golden', ...GO_WRITER_COMMANDS, 'renderer-static-transport', 'p08-golden-and-safety-tests'];
  const epoch = Date.parse('2026-07-12T08:00:00.000Z');
  return ids.map((id, index) => accepted(id, epoch + index * 20, epoch + index * 20 + 10));
}

function safeSummary() {
  return {
    schemaVersion: 1,
    status: 'completed',
    environment: {
      electronUserDataAccessed: false, productionDatabaseOpened: false, databaseContentCaptured: false,
      userContentCaptured: false, externalProcessStarted: false, chatStreamStarted: false, mcpListenerStarted: false,
      temporaryRootsOnly: true,
    },
    databaseOwnership: {
      p07GateAccepted: true, p07EvidenceAccepted: true, p00BaselineAccepted: true, authorizedCopiesOnly: true,
      goWriteRequiresOwnerProof: true, secretStorageSeparateFromDatabase: true, ownerGuardSha256: 'a'.repeat(64),
    },
    sourceHashesStable: true,
    commandResults: safeTimeline(),
  };
}

function writeP07Evidence(root) {
  const run = path.join(root, 'docs/migration/p07/evidence/runs/20260712');
  fs.mkdirSync(run, { recursive: true });
  const summaryBytes = Buffer.from(JSON.stringify({ schemaVersion: 1, status: 'completed', ok: true, sourceHashesStable: true }) + '\n');
  fs.writeFileSync(path.join(run, 'summary.json'), summaryBytes);
  const digest = crypto.createHash('sha256').update(summaryBytes).digest('hex');
  fs.writeFileSync(path.join(run, 'evidence-manifest.json'), JSON.stringify({
    schemaVersion: 1, immutableRunDirectory: true, artifacts: [{ path: 'summary.json', sha256: digest }],
  }) + '\n');
  return digest;
}

test('argument parser accepts only explicit P08 safety modes', () => {
  assert.deepEqual(parseArgs(['preflight']), { mode: 'preflight' });
  assert.deepEqual(parseArgs(['evidence', 'summary.json']), { mode: 'evidence', summary: 'summary.json' });
  assert.throws(() => parseArgs([]), /usage:/);
});

test('P08 temporary ownership is restricted to the verifier prefix', () => {
  assert.equal(isOwnedTemporaryRoot(path.join(os.tmpdir(), 'autoplan-p08-verify-safe')), true);
  assert.equal(isOwnedTemporaryRoot(path.join(os.tmpdir(), 'autoplan-p07-verify-safe')), false);
  assert.equal(isOwnedTemporaryRoot(ROOT), false);
});

test('sensitive output scanner rejects credentials, locators and raw chat surfaces', () => {
  assert.deepEqual(scanSensitiveText('status=ok'), []);
  assert.ok(scanSensitiveText('Bearer abcdefghijklmnop').includes('usable-bearer'));
  assert.ok(scanSensitiveText('secret_ref=fixture').includes('secret-reference'));
  assert.ok(scanSensitiveText('{"content":"fixture"}').includes('raw-chat-data'));
});

test('writer timeline requires P08 Node golden before every Go writer', () => {
  const result = inspectWriterTimeline(safeTimeline());
  assert.equal(result.sequential, true);
  assert.equal(result.simultaneousNodeGoWriter, false);
  assert.deepEqual(new Set(result.p00GateCommands), P00_GATE_COMMANDS);
  assert.deepEqual(new Set(result.goWriterCommands), GO_WRITER_COMMANDS);
  const overlap = safeTimeline();
  overlap[4].startedAt = overlap[3].startedAt;
  assert.throws(() => inspectWriterTimeline(overlap), (error) => error instanceof SafetyError && error.code === 'command_timeline_overlap');
  const p00AfterNode = safeTimeline();
  const nodeIndex = p00AfterNode.findIndex((item) => item.id === 'node-static-golden');
  const testIndex = p00AfterNode.findIndex((item) => item.id === 'test');
  const epoch = Date.parse('2026-07-12T08:00:00.000Z');
  [p00AfterNode[nodeIndex], p00AfterNode[testIndex]] = [p00AfterNode[testIndex], p00AfterNode[nodeIndex]];
  p00AfterNode.forEach((item, index) => {
    const start = epoch + index * 20;
    item.startedAt = new Date(start).toISOString();
    item.endedAt = new Date(start + 10).toISOString();
  });
  assert.throws(() => inspectWriterTimeline(p00AfterNode), (error) => error instanceof SafetyError && error.code === 'p00_gate_order_invalid');
});

test('evidence requires accepted P07/P08 gates and isolated secret storage declarations', () => {
  assert.equal(inspectEvidenceSummary(safeSummary()).ok, true);
  const rejected = safeSummary();
  rejected.commandResults[3].evaluation.accepted = false;
  assert.throws(() => inspectEvidenceSummary(rejected), (error) => error instanceof SafetyError && error.code === 'command_not_accepted');
  const unsafe = safeSummary();
  unsafe.databaseOwnership.secretStorageSeparateFromDatabase = false;
  assert.throws(() => inspectEvidenceSummary(unsafe), (error) => error instanceof SafetyError && error.code === 'evidence_summary_invalid');
});

test('P07 evidence must be immutable, completed and hash-linked', (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p08-safety-test-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const digest = writeP07Evidence(root);
  assert.equal(inspectP07Evidence(root).summarySha256, digest);
  fs.writeFileSync(path.join(root, 'docs/migration/p07/evidence/runs/20260712/summary.json'), '{}');
  assert.throws(() => inspectP07Evidence(root), (error) => error instanceof SafetyError && error.code === 'p07_evidence_invalid');
});

test('checked P08 sources retain frozen goldens, owner guard and static closure boundaries', () => {
  const result = inspectSourceSafety(ROOT);
  assert.equal(result.ok, true);
  assert.equal(result.schemaVersion, 1);
  assert.ok(result.openapiRoutes.includes('/api/v1/projects/{project_id}/scripts'));
  assert.match(result.databaseOwnerGuardSha256, /^[a-f0-9]{64}$/);
});
