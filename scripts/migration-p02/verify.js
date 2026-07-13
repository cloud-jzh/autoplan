'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn, spawnSync } = require('node:child_process');

const {
  evaluateCommand,
  failureSignatures,
  sanitizeForSignature,
  stableRunId,
} = require('../migration-baseline/run-baseline');

const ROOT = path.resolve(__dirname, '../..');
const EXPECTATIONS = 'docs/migration/p00/baseline-expectations.json';
const EVIDENCE_ROOT = 'docs/migration/p02/evidence/runs';
const SECRET_NAME = /(?:api[_-]?key|token|secret|credential|password|auth)/i;
const SOURCE_FILES = [
  'package.json',
  'fixtures/contracts/p02/manifest.json',
  'backend/openapi/openapi.yaml',
  'backend/openapi/schemas/project.schema.json',
  'backend/openapi/schemas/snapshot.schema.json',
  'backend/openapi/schemas/error.schema.json',
  'backend/openapi/schemas/operation.schema.json',
  'backend/openapi/schemas/sse-envelope-v1.schema.json',
  'backend/openapi/schemas/ws-envelope-v1.schema.json',
  'backend/internal/domain/contracts/types.go',
  'backend/internal/domain/contracts/decode.go',
  'backend/internal/domain/contracts/validation.go',
  'backend/internal/domain/contracts/fixtures_test.go',
  'backend/internal/httpapi/contract_test.go',
  'backend/internal/httpapi/security_test.go',
  'backend/internal/bootstrap/lifecycle_test.go',
  'scripts/migration-p02/contract-fixtures.test.js',
  'scripts/migration-p02/verify.js',
  'docs/migration/p02/architecture.md',
  'docs/migration/p02/contracts.md',
  'docs/migration/p02/README.md',
  'docs/migration/p02/evidence/README.md',
];

function toPosix(value) {
  return String(value).replace(/\\/g, '/');
}

function sha256(value) {
  return crypto.createHash('sha256').update(value).digest('hex');
}

function fileRecord(rootDir, relativePath) {
  const target = path.join(rootDir, relativePath);
  if (!fs.existsSync(target)) return { path: relativePath, missing: true };
  const content = fs.readFileSync(target);
  return { path: relativePath, bytes: content.length, sha256: sha256(content) };
}

function writeJson(file, value) {
  fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`, 'utf8');
}

function sanitizeLog(value, rootDir, temporaryRoot) {
  return toPosix(String(value || ''))
    .replaceAll(toPosix(rootDir), '<repo>')
    .replaceAll(toPosix(os.homedir()), '<home>')
    .replaceAll(toPosix(temporaryRoot), '<p02-temp>')
    .replaceAll(toPosix(os.tmpdir()), '<tmp>')
    .replace(/\b(Bearer\s+)[A-Za-z0-9._~+\/-]+/gi, '$1<redacted>')
    .replace(/\b(sk-[A-Za-z0-9_-]{8,})\b/g, '<redacted-key>')
    .replace(/((?:api[_-]?key|token|secret|credential|password|auth)\s*[=:]\s*)[^\s,;]+/gi, '$1<redacted>');
}

function secretFindings(stdout, stderr) {
  const content = `${stdout || ''}\n${stderr || ''}`;
  const checks = [
    ['usable-api-key', /\bsk-[A-Za-z0-9_-]{12,}\b/],
    ['usable-bearer', /Bearer\s+(?!<token>)[A-Za-z0-9._~+\/-]{12,}/i],
    ['private-key', /BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY/],
    ['credential-assignment', /(?:api[_-]?key|token|secret|credential|password|auth)\s*[=:]\s*(?!<redacted>|false\b|true\b|null\b)[^\s,;]{8,}/i],
  ];
  return checks.filter(([, pattern]) => pattern.test(content)).map(([label]) => label);
}

function safeEnvironment(temporaryRoot, source = process.env) {
  const environment = {};
  const removedSecretVariableNames = [];
  for (const [name, value] of Object.entries(source)) {
    if (SECRET_NAME.test(name)) removedSecretVariableNames.push(name);
    else environment[name] = value;
  }
  environment.TEMP = temporaryRoot;
  environment.TMP = temporaryRoot;
  environment.TMPDIR = temporaryRoot;
  environment.GOTMPDIR = temporaryRoot;
  environment.AUTOPLAN_P02_VERIFY = '1';
  return { environment, removedSecretVariableNames: removedSecretVariableNames.sort() };
}

function execute(spec, rootDir, environment) {
  return new Promise((resolve) => {
    const startedAt = new Date().toISOString();
    let stdout = '';
    let stderr = '';
    let settled = false;
    const child = spawn(spec.executable, spec.args, {
      cwd: path.join(rootDir, spec.cwd || '.'),
      env: environment,
      shell: false,
      windowsHide: true,
      windowsVerbatimArguments: Boolean(spec.windowsVerbatimArguments),
    });
    const timeout = setTimeout(() => {
      if (!settled) child.kill();
    }, spec.timeoutMS || 15 * 60 * 1000);
    const finish = (actual) => {
      if (settled) return;
      settled = true;
      clearTimeout(timeout);
      resolve({ ...actual, stdout, stderr, startedAt, endedAt: new Date().toISOString() });
    };
    child.stdout.on('data', (chunk) => { stdout += chunk.toString(); });
    child.stderr.on('data', (chunk) => { stderr += chunk.toString(); });
    child.on('error', (error) => finish({ exitCode: null, signal: null, error: error.message }));
    child.on('close', (exitCode, signal) => finish({ exitCode, signal: signal || null, error: null }));
  });
}

function successSpec(id, executable, args, description, cwd = '.') {
  return {
    id, executable, args, description, cwd, command: [executable, ...args].join(' '),
    outcome: 'success', allowedFailureSignatures: [],
  };
}

function npmSpec(id, args, expectation) {
  if (process.platform === 'win32') {
    return {
      id, ...expectation, executable: process.env.ComSpec || 'cmd.exe',
      args: ['/d', '/s', '/c', `call npm.cmd ${args.join(' ')}`],
      windowsVerbatimArguments: true, command: `npm.cmd ${args.join(' ')}`, cwd: '.',
    };
  }
  return { id, ...expectation, executable: 'npm', args, command: `npm ${args.join(' ')}`, cwd: '.' };
}

function gitStatus(rootDir) {
  const result = spawnSync('git', ['status', '--porcelain=v1', '--untracked-files=all'], {
    cwd: rootDir, encoding: 'utf8', windowsHide: true,
  });
  return {
    exitCode: typeof result.status === 'number' ? result.status : null,
    entries: String(result.stdout || '').split(/\r?\n/).filter(Boolean),
    stderr: String(result.stderr || ''),
  };
}

function statusPaths(status) {
  return status.entries.map((entry) => entry.slice(3).split(' -> ').at(-1)).filter(Boolean).map(toPosix);
}

function testControlViolations(rootDir) {
  const roots = ['src', 'scripts/migration-baseline', 'scripts/migration-p01', 'scripts/migration-p02', 'backend'];
  const violations = [];
  const visit = (directory) => {
    if (!fs.existsSync(directory)) return;
    for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
      const target = path.join(directory, entry.name);
      if (entry.isDirectory()) visit(target);
      else if (entry.isFile() && /(?:_test\.go|\.test\.(?:js|ts|tsx))$/.test(entry.name)) {
        fs.readFileSync(target, 'utf8').split(/\r?\n/).forEach((line, index) => {
          if (/\b(?:test|it|describe)\.(?:skip|only)\s*\(/.test(line)
              || /\b(?:skip|only)\s*:\s*true\b/.test(line)
              || /\bt\.Skip(?:f|Now)?\s*\(/.test(line)) {
            violations.push(`${toPosix(path.relative(rootDir, target))}:${index + 1}`);
          }
        });
      }
    }
  };
  roots.forEach((root) => visit(path.join(rootDir, root)));
  return [...new Set(violations)].sort();
}

async function runCommand(spec, state) {
  const actual = await execute(spec, state.rootDir, state.environment);
  actual.failureSignatures = actual.exitCode && actual.exitCode !== 0
    ? failureSignatures(actual.stdout, actual.stderr, state.rootDir)
    : [];
  if (actual.exitCode && actual.exitCode !== 0 && actual.failureSignatures.length === 0) {
    const normalized = sanitizeForSignature(`${actual.stdout}\n${actual.stderr}`, state.rootDir);
    actual.failureSignatures = [`unclassified|sha256=${sha256(normalized)}`];
  }
  const findings = secretFindings(actual.stdout, actual.stderr);
  const evaluation = evaluateCommand(spec, actual);
  if (findings.length) {
    evaluation.accepted = false;
    evaluation.reason = `credential-shaped output detected: ${findings.join(', ')}`;
  }
  const base = `${String(state.results.length + 1).padStart(2, '0')}-${spec.id}`;
  const stdout = sanitizeLog(actual.stdout, state.rootDir, state.temporaryRoot);
  const stderr = sanitizeLog(actual.stderr, state.rootDir, state.temporaryRoot);
  const stdoutLog = `${base}.stdout.log`;
  const stderrLog = `${base}.stderr.log`;
  fs.writeFileSync(path.join(state.runDir, stdoutLog), stdout, 'utf8');
  fs.writeFileSync(path.join(state.runDir, stderrLog), stderr, 'utf8');
  state.results.push({
    id: spec.id, description: spec.description, command: spec.command,
    expectedOutcome: spec.outcome, exitCode: actual.exitCode, signal: actual.signal,
    error: actual.error ? sanitizeLog(actual.error, state.rootDir, state.temporaryRoot) : null,
    startedAt: actual.startedAt, endedAt: actual.endedAt,
    failureSignatures: actual.failureSignatures, secretFindings: findings, evaluation,
    stdoutLog, stdoutBytes: Buffer.byteLength(stdout), stdoutSha256: sha256(stdout),
    stderrLog, stderrBytes: Buffer.byteLength(stderr), stderrSha256: sha256(stderr),
  });
  return evaluation.accepted;
}

function cleanupTemporaryRoot(temporaryRoot) {
  const resolved = path.resolve(temporaryRoot);
  if (path.dirname(resolved) !== path.resolve(os.tmpdir()) || !path.basename(resolved).startsWith('autoplan-p02-')) {
    return { cleaned: false, error: 'refused to remove non-owned temporary root' };
  }
  try {
    fs.rmSync(resolved, { recursive: true, force: true });
    return { cleaned: true, error: null };
  } catch (error) {
    return { cleaned: false, error: error.message };
  }
}

async function runVerification(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  const expectations = JSON.parse(fs.readFileSync(path.join(rootDir, EXPECTATIONS), 'utf8'));
  const runDir = path.join(rootDir, EVIDENCE_ROOT, options.runId || stableRunId());
  if (fs.existsSync(runDir)) throw new Error(`refusing to overwrite evidence directory: ${runDir}`);
  fs.mkdirSync(runDir, { recursive: true });
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p02-'));
  const safe = safeEnvironment(temporaryRoot, options.environment || process.env);
  const startStatus = gitStatus(rootDir);
  const summary = {
    schemaVersion: 1,
    runId: path.basename(runDir),
    startedAt: new Date().toISOString(),
    status: 'running',
    environment: {
      platform: process.platform, arch: process.arch, node: process.version,
      cwd: '<repository-root>', temporaryRoot: `<system-temp>/${path.basename(temporaryRoot)}`,
      removedSecretVariableNames: safe.removedSecretVariableNames,
      environmentValuesCaptured: false, electronUserDataAccessed: false,
    },
    sourceHashesStart: SOURCE_FILES.map((file) => fileRecord(rootDir, file)),
    testControlViolations: testControlViolations(rootDir),
    commandResults: [],
  };
  const state = { rootDir, runDir, temporaryRoot, environment: safe.environment, results: summary.commandResults };

  const gates = [
    npmSpec('p00-gate', ['run', 'migration:p00:verify'], {
      description: 'P00 baseline and safety hard gate.', outcome: 'success', allowedFailureSignatures: [],
    }),
    npmSpec('p01-gate', ['run', 'migration:p01:verify'], {
      description: 'P01 renderer boundary hard gate.', outcome: 'success', allowedFailureSignatures: [],
    }),
  ];
  for (const gate of gates) {
    if (!await runCommand(gate, state)) {
      summary.status = 'blocked';
      summary.blocked = `${gate.id} failed; no later P02 command was started.`;
      break;
    }
  }

  if (summary.status !== 'blocked') {
    const commands = [
      successSpec('node-contracts', process.execPath, ['--test', 'scripts/migration-p02/contract-fixtures.test.js'], 'Node shared-fixture and drift guard.'),
      successSpec('go-contracts', 'go', ['test', './internal/domain/contracts', '-run', '^TestShared', '-count=1'], 'Go shared-fixture strict decoding and schema guard.', 'backend'),
      successSpec('go-http-security', 'go', ['test', './internal/httpapi', '-count=1'], 'Stable HTTP and unified REST/SSE/WebSocket security tests.', 'backend'),
      successSpec('go-lifecycle', 'go', ['test', './internal/bootstrap', '-run', '^(TestLifecycle|TestRunServerLifecycle)', '-count=1'], 'Random-port startup, readiness, and graceful shutdown smoke.', 'backend'),
      successSpec('go-all', 'go', ['test', './...'], 'Complete backend Go test and compile gate.', 'backend'),
      npmSpec('check', ['run', 'check'], expectations.commands.check),
      npmSpec('test', ['test'], expectations.commands.test),
    ];
    for (const command of commands) await runCommand(command, state);
    summary.status = 'completed';
  }

  summary.temporaryCleanup = cleanupTemporaryRoot(temporaryRoot);
  const endStatus = gitStatus(rootDir);
  summary.endedAt = new Date().toISOString();
  summary.sourceHashesEnd = SOURCE_FILES.map((file) => fileRecord(rootDir, file));
  summary.sourceHashesStable = JSON.stringify(summary.sourceHashesStart) === JSON.stringify(summary.sourceHashesEnd);
  summary.gitStatusStart = { ...startStatus, stderr: sanitizeLog(startStatus.stderr, rootDir, temporaryRoot) };
  summary.gitStatusEnd = { ...endStatus, stderr: sanitizeLog(endStatus.stderr, rootDir, temporaryRoot) };
  summary.affectedFiles = [...new Set([...statusPaths(startStatus), ...statusPaths(endStatus)])].sort();
  summary.remainingRisks = summary.commandResults.filter((item) => !item.evaluation.accepted)
    .map((item) => `${item.id}: ${item.evaluation.reason}`);
  if (summary.testControlViolations.length) summary.remainingRisks.push(`forbidden skip/only controls: ${summary.testControlViolations.join(', ')}`);
  if (!summary.sourceHashesStable) summary.remainingRisks.push('P02 guarded sources changed during verification.');
  if (!summary.temporaryCleanup.cleaned) summary.remainingRisks.push('P02 system-temporary root cleanup failed.');
  summary.ok = summary.status === 'completed' && startStatus.exitCode === 0 && endStatus.exitCode === 0
    && summary.sourceHashesStable && summary.testControlViolations.length === 0
    && summary.temporaryCleanup.cleaned && summary.commandResults.every((item) => item.evaluation.accepted);
  writeJson(path.join(runDir, 'summary.json'), summary);
  const artifacts = fs.readdirSync(runDir).sort().map((name) => fileRecord(runDir, name));
  writeJson(path.join(runDir, 'evidence-manifest.json'), {
    schemaVersion: 1, runId: summary.runId, generatedAt: summary.endedAt,
    immutableRunDirectory: true, artifacts,
  });
  return { runDir, summary };
}

function parseArgs(argv) {
  if (argv.length !== 1 || argv[0] !== 'verify') {
    throw new Error('usage: node scripts/migration-p02/verify.js verify');
  }
  return { mode: 'verify' };
}

if (require.main === module) {
  try {
    parseArgs(process.argv.slice(2));
    runVerification().then(({ runDir, summary }) => {
      for (const item of summary.commandResults) {
        process.stdout.write(`${item.id}: exit=${item.exitCode ?? 'missing'} accepted=${item.evaluation.accepted}\n`);
      }
      if (summary.status === 'blocked') process.stderr.write(`blocked: ${summary.blocked}\n`);
      process.stdout.write(`evidence: ${toPosix(path.relative(ROOT, runDir))}\n`);
      process.exitCode = summary.ok ? 0 : 1;
    }).catch((error) => {
      process.stderr.write(`${error.stack || error.message}\n`);
      process.exitCode = 1;
    });
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}

module.exports = {
  parseArgs,
  runVerification,
  safeEnvironment,
  sanitizeLog,
  secretFindings,
  testControlViolations,
};
