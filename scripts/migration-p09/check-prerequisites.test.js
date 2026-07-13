'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const { checkPrerequisites, parseArgs } = require('./check-prerequisites');

function digest(value) { return crypto.createHash('sha256').update(value).digest('hex'); }

function writeRun(root, stage, summary) {
  const run = path.join(root, 'docs', 'migration', stage, 'evidence', 'runs', '2026-07-12T09-00-00');
  fs.mkdirSync(run, { recursive: true });
  const summaryBytes = Buffer.from(`${JSON.stringify(summary, null, 2)}\n`);
  fs.writeFileSync(path.join(run, 'summary.json'), summaryBytes);
  fs.writeFileSync(path.join(run, 'evidence-manifest.json'), `${JSON.stringify({ immutableRunDirectory: true, artifacts: [{ path: 'summary.json', bytes: summaryBytes.length, sha256: digest(summaryBytes) }] })}\n`);
}

function preparedRoot(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p09-prerequisite-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.mkdirSync(path.join(root, 'src'), { recursive: true });
  fs.mkdirSync(path.join(root, 'scripts', 'migration-p09'), { recursive: true });
  fs.writeFileSync(path.join(root, 'package.json'), JSON.stringify({ scripts: { test: 'node --test "src/**/*.test.js"', check: 'tsc --noEmit' } }));
  const fixture = path.join(root, 'sanitized-fixture');
  fs.mkdirSync(fixture);
  fs.writeFileSync(path.join(fixture, '.autoplan-p09-scale-copy'), 'p09-scale-v1\n');
  fs.writeFileSync(path.join(fixture, 'scale-copy.json'), JSON.stringify({ rows: { projects: [] } }));
  fs.writeFileSync(path.join(fixture, 'scale-manifest.json'), JSON.stringify({ kind: 'p09-generated-scale-copy' }));
  writeRun(root, 'p00', {
    sourceHashesStable: true, expectationsHashStable: true, ok: true,
    commandResults: [
      { id: 'check', expectedOutcome: 'exact-known-failure', exitCode: 1, failureSignatures: ['file-length|scripts/smoke-test.js|limit=3800'], evaluation: { accepted: true } },
      { id: 'test', evaluation: { accepted: true } },
    ],
  });
  for (const stage of ['p04', 'p05', 'p06', 'p07', 'p08']) writeRun(root, stage, { status: 'completed', ok: true, sourceHashesStable: true });
  return { root, fixture };
}

test('P00 frozen red signature and P04-P08 immutable evidence are required', (t) => {
  const { root, fixture } = preparedRoot(t);
  const result = checkPrerequisites({ rootDir: root, fixtureRoot: fixture });
  assert.equal(result.ok, true);
  assert.equal(result.status, 'ready');
  assert.deepEqual(parseArgs(['--fixture-root', fixture]), { fixtureRoot: fixture });
  assert.throws(() => parseArgs([]), /usage:/);
});

test('changed P00 red signature blocks later cutover commands', (t) => {
  const { root, fixture } = preparedRoot(t);
  const p00 = path.join(root, 'docs', 'migration', 'p00', 'evidence', 'runs', '2026-07-12T09-00-00', 'summary.json');
  const summary = JSON.parse(fs.readFileSync(p00, 'utf8'));
  summary.commandResults[0].failureSignatures = ['different-signature'];
  const bytes = Buffer.from(`${JSON.stringify(summary)}\n`);
  fs.writeFileSync(p00, bytes);
  const manifest = path.join(path.dirname(p00), 'evidence-manifest.json');
  fs.writeFileSync(manifest, `${JSON.stringify({ immutableRunDirectory: true, artifacts: [{ path: 'summary.json', bytes: bytes.length, sha256: digest(bytes) }] })}\n`);
  const result = checkPrerequisites({ rootDir: root, fixtureRoot: fixture });
  assert.equal(result.ok, false);
  assert.ok(result.failures.includes('p00_baseline_signature_invalid'));
});

test('missing P04-P08 evidence blocks even with an authorized sanitized fixture', (t) => {
  const { root, fixture } = preparedRoot(t);
  fs.rmSync(path.join(root, 'docs', 'migration', 'p06'), { recursive: true, force: true });
  const result = checkPrerequisites({ rootDir: root, fixtureRoot: fixture });
  assert.equal(result.ok, false);
  assert.ok(result.failures.includes('p06_evidence_runs_missing'));
});
