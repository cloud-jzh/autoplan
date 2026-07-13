'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const { parseArgs, runVerification, verificationCommands } = require('./verify');

function rootFixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p09-verify-test-'));
  const fixture = path.join(root, 'sanitized-scale-copy');
  fs.mkdirSync(fixture);
  fs.writeFileSync(path.join(fixture, '.autoplan-p09-scale-copy'), 'p09-scale-v1\n');
  fs.writeFileSync(path.join(fixture, 'scale-copy.json'), JSON.stringify({ rows: { projects: [] }, label: 'sanitized' }));
  fs.writeFileSync(path.join(fixture, 'scale-manifest.json'), JSON.stringify({
    kind: 'p09-generated-scale-copy', schema_version_target: 1, row_counts: { projects: 0 }, table_sha256: { projects: 'a'.repeat(64) },
  }));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return { root, fixture };
}

function result(exitCode, stdout = '', stderr = '') {
  return { exitCode, signal: null, error: null, stdout, stderr, startedAt: '2026-07-12T09:00:00.000Z', endedAt: '2026-07-12T09:00:00.010Z' };
}

function readyPrerequisiteOutput() {
  return JSON.stringify({
    status: 'ready', code: 'prerequisites_ready', failures: [], p00: { run_id: 'p00-run', summary_sha256: 'c'.repeat(64) },
    stages: ['p04', 'p05', 'p06', 'p07', 'p08'].map((stage) => ({ stage, run_id: `${stage}-run`, summary_sha256: 'd'.repeat(64) })),
  });
}

test('arguments and command plan are strict and serial', () => {
  assert.deepEqual(parseArgs(['verify', '--fixture-root', 'D:\\fixture']), { fixtureRoot: 'D:\\fixture' });
  assert.throws(() => parseArgs(['verify']), /usage:/);
  const ids = verificationCommands(path.join(os.tmpdir(), 'autoplan-p09-verify-plan')).map((item) => item.id);
  assert.deepEqual(ids.slice(0, 5), ['p04-completion-evidence', 'p05-completion-evidence', 'p06-completion-evidence', 'p07-completion-evidence', 'p08-completion-evidence']);
  assert.ok(ids.indexOf('legacy-runtime-godata') < ids.indexOf('cutover-recovery-drill'));
});

test('failed prerequisites preserve the original exit code and prevent all later work', async (t) => {
  const { root, fixture } = rootFixture(t);
  const calls = [];
  const verification = await runVerification({ rootDir: root, fixtureRoot: fixture, evidenceRoot: 'evidence', runId: 'blocked-run', sourceFiles: [], environment: {},
    executeCommand: async (spec) => { calls.push(spec.id); return result(47, '', 'precondition failed'); },
  });
  assert.deepEqual(calls, ['prerequisites']);
  assert.equal(verification.summary.status, 'blocked');
  assert.equal(verification.summary.blocked_exit_code, 47);
  assert.equal(verification.summary.temporary_cleanup.cleaned, true);
});

test('a zero exit without complete immutable upstream evidence is still blocked before cutover', async (t) => {
  const { root, fixture } = rootFixture(t);
  const calls = [];
  const verification = await runVerification({ rootDir: root, fixtureRoot: fixture, evidenceRoot: 'evidence', runId: 'incomplete-prerequisite-run', sourceFiles: [], environment: {},
    executeCommand: async (spec) => { calls.push(spec.id); return result(0, '{"status":"ready","code":"prerequisites_ready","failures":[]}'); },
  });
  assert.deepEqual(calls, ['prerequisites']);
  assert.equal(verification.summary.status, 'blocked');
  assert.equal(verification.summary.blocked_exit_code, 0);
});

test('nonzero commands stop serial verification without swallowing the exit code', async (t) => {
  const { root, fixture } = rootFixture(t);
  const calls = [];
  const verification = await runVerification({ rootDir: root, fixtureRoot: fixture, evidenceRoot: 'evidence', runId: 'failed-run', sourceFiles: [], environment: {},
    executeCommand: async (spec) => { calls.push(spec.id); return result(spec.id === 'p05-completion-evidence' ? 19 : 0, spec.id === 'prerequisites' ? readyPrerequisiteOutput() : ''); },
  });
  assert.deepEqual(calls, ['prerequisites', 'p04-completion-evidence', 'p05-completion-evidence']);
  assert.equal(verification.summary.status, 'failed');
  assert.equal(verification.summary.command_results.at(-1).exit_code, 19);
});

test('completed verification captures only sanitized hashes and owned temporary artifacts', async (t) => {
  const { root, fixture } = rootFixture(t);
  const verification = await runVerification({ rootDir: root, fixtureRoot: fixture, evidenceRoot: 'evidence', runId: 'completed-run', sourceFiles: [], environment: {},
    executeCommand: async (spec) => {
      if (spec.id === 'scale-copy-generate') {
        const copyRoot = spec.args.at(-1);
        fs.mkdirSync(copyRoot, { recursive: true });
        fs.writeFileSync(path.join(copyRoot, '.autoplan-p09-scale-copy'), 'p09-scale-v1\n');
        fs.writeFileSync(path.join(copyRoot, 'scale-copy.json'), JSON.stringify({ rows: { projects: [] } }));
        fs.writeFileSync(path.join(copyRoot, 'scale-manifest.json'), JSON.stringify({ kind: 'p09-generated-scale-copy', schema_version_target: 1, row_counts: {}, table_sha256: {} }));
      }
      if (spec.id === 'cutover-recovery-drill') {
        const evidence = spec.args[spec.args.indexOf('--evidence-dir') + 1];
        fs.mkdirSync(evidence, { recursive: true });
        fs.writeFileSync(path.join(evidence, 'cutover-drill-report.json'), JSON.stringify({ status: 'completed', fault_count: 14 }));
      }
      if (spec.id === 'prerequisites') return result(0, readyPrerequisiteOutput());
      return result(0, spec.id === 'legacy-runtime-godata'
        ? `{\"status\":\"completed\",\"command_count\":24,\"snapshot_sha256\":\"${'b'.repeat(64)}\",\"owner\":{\"owner\":\"go\",\"node_sql_attempt\":\"node_sql_blocked\",\"second_go_attempt\":\"go_owner_locked\",\"writer_count\":1}}\n`
        : '');
    },
  });
  assert.equal(verification.summary.status, 'completed');
  assert.equal(verification.summary.ok, true);
  assert.ok(verification.summary.captured_artifacts.some((item) => item.path === 'cutover-drill-report.json'));
  assert.ok(fs.existsSync(path.join(verification.runDir, 'evidence-manifest.json')));
});

test('unsafe command output is rejected even when a command exits zero', async (t) => {
  const { root, fixture } = rootFixture(t);
  const verification = await runVerification({ rootDir: root, fixtureRoot: fixture, evidenceRoot: 'evidence', runId: 'unsafe-output-run', sourceFiles: [], environment: {},
    executeCommand: async (spec) => result(0, spec.id === 'prerequisites' ? readyPrerequisiteOutput() : spec.id === 'p04-completion-evidence' ? 'token=not-safe-to-persist' : ''),
  });
  assert.equal(verification.summary.status, 'failed');
  assert.equal(verification.summary.command_results.at(-1).reason, 'unsafe_output_rejected');
});
