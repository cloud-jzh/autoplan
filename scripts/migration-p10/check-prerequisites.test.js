'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const { checkPrerequisites, parseArgs } = require('./check-prerequisites');
const { FIXTURE_MANIFEST, FIXTURE_MARKER } = require('./check-safety');

function digest(value) { return crypto.createHash('sha256').update(value).digest('hex'); }

function writeRun(root, stage, summary) {
  const run = path.join(root, 'docs', 'migration', stage, 'evidence', 'runs', '2026-07-12T10-00-00');
  fs.mkdirSync(run, { recursive: true });
  const bytes = Buffer.from(`${JSON.stringify(summary, null, 2)}\n`);
  fs.writeFileSync(path.join(run, 'summary.json'), bytes);
  fs.writeFileSync(path.join(run, 'evidence-manifest.json'), JSON.stringify({
    immutable_run_directory: true, artifacts: [{ path: 'summary.json', bytes: bytes.length, sha256: digest(bytes) }],
  }));
}

function preparedRoot(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p10-prerequisite-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.mkdirSync(path.join(root, 'src'), { recursive: true });
  fs.mkdirSync(path.join(root, 'scripts', 'migration-p10'), { recursive: true });
  fs.writeFileSync(path.join(root, 'package.json'), JSON.stringify({ scripts: {
    test: 'node --test "src/**/*.test.js"', check: 'tsc --noEmit', 'migration:p10:verify': 'node scripts/migration-p10/verify.js verify',
  } }));
  const fixture = path.join(root, 'authorized-p10-fixture');
  fs.mkdirSync(fixture);
  fs.writeFileSync(path.join(fixture, FIXTURE_MARKER), 'p10-authorized-v1\n');
  fs.writeFileSync(path.join(fixture, FIXTURE_MANIFEST), JSON.stringify({ kind: 'p10-authorized-fixture', schema_version: 1, authorized_copy: true }));
  writeRun(root, 'p00', { sourceHashesStable: true, expectationsHashStable: true, ok: true, commandResults: [
    { id: 'check', expectedOutcome: 'exact-known-failure', exitCode: 1, failureSignatures: ['file-length|scripts/smoke-test.js|limit=3800'], evaluation: { accepted: true } },
    { id: 'test', evaluation: { accepted: true } },
  ] });
  writeRun(root, 'p09', { status: 'completed', ok: true, sourceHashesStable: true, owner_timeline: [{ event: 'cutover_complete', owner: 'go', writer_count: 1 }] });
  return { root, fixture };
}

test('P00 baseline, P09 evidence, explicit fixture, and owner idle state are all required', (t) => {
  const { root, fixture } = preparedRoot(t);
  const result = checkPrerequisites({ rootDir: root, fixtureRoot: fixture });
  assert.equal(result.ok, true);
  assert.equal(result.status, 'ready');
  assert.deepEqual(parseArgs(['--fixture-root', fixture]), { fixtureRoot: fixture });
  assert.throws(() => parseArgs([]), /usage:/);
});

test('a modified P00 red signature blocks P10 before any test command', (t) => {
  const { root, fixture } = preparedRoot(t);
  const summaryFile = path.join(root, 'docs', 'migration', 'p00', 'evidence', 'runs', '2026-07-12T10-00-00', 'summary.json');
  const summary = JSON.parse(fs.readFileSync(summaryFile, 'utf8'));
  summary.commandResults[0].failureSignatures = ['drift'];
  const bytes = Buffer.from(`${JSON.stringify(summary)}\n`);
  fs.writeFileSync(summaryFile, bytes);
  fs.writeFileSync(path.join(path.dirname(summaryFile), 'evidence-manifest.json'), JSON.stringify({ immutable_run_directory: true, artifacts: [{ path: 'summary.json', bytes: bytes.length, sha256: digest(bytes) }] }));
  const result = checkPrerequisites({ rootDir: root, fixtureRoot: fixture });
  assert.equal(result.ok, false);
  assert.ok(result.failures.includes('p00_baseline_signature_invalid'));
});

test('missing immutable P09 evidence blocks P10 even for an authorized fixture', (t) => {
  const { root, fixture } = preparedRoot(t);
  fs.rmSync(path.join(root, 'docs', 'migration', 'p09'), { recursive: true, force: true });
  const result = checkPrerequisites({ rootDir: root, fixtureRoot: fixture });
  assert.equal(result.ok, false);
  assert.ok(result.failures.includes('p09_evidence_runs_missing'));
});
