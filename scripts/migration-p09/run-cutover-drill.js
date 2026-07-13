'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const REQUIRED_CODES = new Set([
  'freeze_failed', 'drain_failed', 'node_release_failed', 'backup_failed', 'preflight_failed',
  'audit_before_failed', 'migration_failed', 'owner_lock_failed', 'smoke_failed', 'reopen_failed',
]);

function readFaultMatrix(repositoryRoot) {
  const matrixPath = path.join(repositoryRoot, 'fixtures', 'migration', 'p09', 'faults', 'matrix.json');
  const matrix = JSON.parse(fs.readFileSync(matrixPath, 'utf8'));
  if (!matrix || matrix.schema_version !== 1 || matrix.kind !== 'p09-cutover-fault-matrix' || !Array.isArray(matrix.failures)) {
    throw stableError('fault_matrix_invalid');
  }
  const ids = new Set();
  for (const fault of matrix.failures) {
    if (!safeLabel(fault?.id) || ids.has(fault.id) || !REQUIRED_CODES.has(fault.expected_code)) throw stableError('fault_matrix_invalid');
    ids.add(fault.id);
  }
  if (ids.size < 14 || !Array.isArray(matrix.required_invariants) || matrix.required_invariants.length < 5) {
    throw stableError('fault_matrix_incomplete');
  }
  return matrix;
}

function validateDrillPaths({ fixtureRoot, evidenceDir }) {
  if (!path.isAbsolute(fixtureRoot) || !path.isAbsolute(evidenceDir)) throw stableError('drill_path_invalid');
  const root = path.resolve(fixtureRoot);
  const evidence = path.resolve(evidenceDir);
  if (!isInside(root, evidence) || containsUserData(root) || !looksLikeFixtureRoot(root)) throw stableError('drill_fixture_rejected');
  const rootInfo = fs.lstatSync(root, { throwIfNoEntry: false });
  if (!rootInfo?.isDirectory() || rootInfo.isSymbolicLink()) throw stableError('drill_fixture_rejected');
  assertExistingParentsAreRegularDirectories(root, evidence);
  if (fs.existsSync(evidence)) {
    const evidenceInfo = fs.lstatSync(evidence);
    if (!evidenceInfo.isDirectory() || evidenceInfo.isSymbolicLink()) throw stableError('drill_evidence_rejected');
  }
  return { fixtureRoot: root, evidenceDir: evidence };
}

function assertExistingParentsAreRegularDirectories(root, target) {
  const relative = path.relative(root, target);
  let current = root;
  for (const segment of relative.split(path.sep)) {
    if (!segment) continue;
    current = path.join(current, segment);
    const info = fs.lstatSync(current, { throwIfNoEntry: false });
    if (!info) break;
    if (info.isSymbolicLink() || !info.isDirectory()) throw stableError('drill_evidence_rejected');
  }
}

function runCutoverDrill(options = {}, dependencies = {}) {
  const repositoryRoot = path.resolve(options.repositoryRoot || path.join(__dirname, '..', '..'));
  const paths = validateDrillPaths(options);
  const matrix = readFaultMatrix(repositoryRoot);
  const run = dependencies.runCommand || defaultRunCommand;
  const startedAt = new Date().toISOString();
  const commands = [
    { id: 'maintenance_fault_matrix', command: 'go', args: ['test', './internal/application/maintenance'], cwd: path.join(repositoryRoot, 'backend') },
    { id: 'restore_boundary', command: 'go', args: ['test', './internal/migration'], cwd: path.join(repositoryRoot, 'backend') },
    { id: 'drill_contract', command: process.execPath, args: ['--test', path.join(repositoryRoot, 'scripts', 'migration-p09', 'run-cutover-drill.test.js')], cwd: repositoryRoot },
  ];
  const results = commands.map((entry) => redactCommandResult(entry, run(entry)));
  const ok = results.every((result) => result.exit_code === 0);
  const report = {
    schema_version: 1,
    kind: 'p09-cutover-drill',
    status: ok ? 'completed' : 'blocked',
    started_at: startedAt,
    completed_at: new Date().toISOString(),
    fault_count: matrix.failures.length,
    expected_codes: Array.from(new Set(matrix.failures.map((fault) => fault.expected_code))).sort(),
    required_invariants: [...matrix.required_invariants].sort(),
    commands: results,
  };
  fs.mkdirSync(paths.evidenceDir, { recursive: true, mode: 0o700 });
  const reportPath = path.join(paths.evidenceDir, 'cutover-drill-report.json');
  if (fs.existsSync(reportPath)) throw stableError('drill_evidence_exists');
  fs.writeFileSync(reportPath, `${JSON.stringify(report, null, 2)}\n`, { encoding: 'utf8', mode: 0o600, flag: 'wx' });
  return { exitCode: ok ? 0 : 2, report };
}

function defaultRunCommand(entry) {
  const started = Date.now();
  const result = spawnSync(entry.command, entry.args, { cwd: entry.cwd, encoding: 'utf8', windowsHide: true, env: controlledEnvironment(process.env) });
  return { status: Number.isInteger(result.status) ? result.status : 2, duration_ms: Date.now() - started, stdout: result.stdout || '', stderr: result.stderr || '' };
}

function redactCommandResult(entry, result = {}) {
  const output = `${String(result.stdout || '')}\n${String(result.stderr || '')}`;
  return {
    id: entry.id,
    exit_code: Number.isInteger(result.status) ? result.status : 2,
    duration_ms: Math.max(0, Number(result.duration_ms) || 0),
    output_sha256: crypto.createHash('sha256').update(output).digest('hex'),
  };
}

function controlledEnvironment(environment = {}) {
  const result = Object.create(null);
  for (const key of process.platform === 'win32' ? ['SystemRoot', 'SYSTEMROOT', 'WINDIR', 'COMSPEC', 'Path', 'PATH'] : ['PATH']) {
    if (typeof environment[key] === 'string' && environment[key]) result[key] = environment[key];
  }
  return result;
}

function isInside(root, target) {
  const relative = path.relative(root, target);
  return relative !== '' && relative !== '..' && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative);
}

function looksLikeFixtureRoot(root) {
  return path.resolve(root).split(path.sep).some((segment) => /(?:fixture|sanitized|temp|tmp|drill)/i.test(segment));
}

function containsUserData(value) {
  const normalized = String(value || '').replaceAll('\\', '/').toLowerCase();
  return normalized.includes('/appdata/roaming/autoplan') || normalized.includes('/library/application support/autoplan') || normalized.includes('/.config/autoplan');
}

function safeLabel(value) { return typeof value === 'string' && /^[a-z0-9_-]{1,128}$/.test(value); }
function stableError(code) { const error = new Error(code); error.code = code; return error; }

function parseArgs(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 1) {
    const item = argv[index];
    if (item === '--fixture-root' || item === '--evidence-dir') {
      values[item.slice(2).replaceAll('-', '_')] = argv[index + 1] || '';
      index += 1;
    } else {
      throw stableError('drill_arguments_invalid');
    }
  }
  return { fixtureRoot: values.fixture_root || '', evidenceDir: values.evidence_dir || '' };
}

if (require.main === module) {
  try {
    const result = runCutoverDrill(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify({ status: result.report.status, fault_count: result.report.fault_count })}\n`);
    process.exitCode = result.exitCode;
  } catch (error) {
    process.stdout.write(`${JSON.stringify({ status: 'blocked', code: error?.code || 'drill_failed' })}\n`);
    process.exitCode = 2;
  }
}

module.exports = { controlledEnvironment, parseArgs, readFaultMatrix, runCutoverDrill, validateDrillPaths };
