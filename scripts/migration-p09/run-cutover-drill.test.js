'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { describe, it } = require('node:test');
const { parseArgs, readFaultMatrix, runCutoverDrill, validateDrillPaths } = require('./run-cutover-drill');

const repositoryRoot = path.resolve(__dirname, '..', '..');

describe('P09 cutover drill contract', () => {
  it('pins the complete redacted fault matrix', () => {
    const matrix = readFaultMatrix(repositoryRoot);
    assert.equal(matrix.failures.length, 14);
    assert.equal(matrix.failures.some((fault) => fault.id === 'owner_lock_conflict'), true);
    assert.equal(matrix.required_invariants.includes('no_node_fallback'), true);
  });

  it('rejects real userData-like and non-fixture roots', () => {
    assert.throws(() => validateDrillPaths({ fixtureRoot: path.join(process.cwd(), 'ordinary'), evidenceDir: path.join(process.cwd(), 'ordinary', 'evidence') }), /drill_fixture_rejected/);
    assert.throws(() => parseArgs(['--approve-real', 'true']), /drill_arguments_invalid/);
  });

  it('writes redacted evidence only after every command succeeds', () => {
    const fixtureRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-drill-fixture-'));
    const evidenceDir = path.join(fixtureRoot, 'evidence');
    try {
      const result = runCutoverDrill({ fixtureRoot, evidenceDir, repositoryRoot }, {
        runCommand: () => ({ status: 0, duration_ms: 1, stdout: 'token=must-not-appear', stderr: '/absolute/path/must-not-appear' }),
      });
      assert.equal(result.exitCode, 0);
      const report = fs.readFileSync(path.join(evidenceDir, 'cutover-drill-report.json'), 'utf8');
      assert.equal(report.includes('must-not-appear'), false);
      assert.equal(JSON.parse(report).commands.every((command) => /^[a-f0-9]{64}$/.test(command.output_sha256)), true);
    } finally {
      fs.rmSync(fixtureRoot, { recursive: true, force: true });
    }
  });
});
