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
const { verifyPrerequisites } = require('./generate-node-golden');

const ROOT = path.resolve(__dirname, '../..');
const EXPECTATIONS = 'docs/migration/p00/baseline-expectations.json';
const EVIDENCE_ROOT = 'docs/migration/p03/evidence/runs';
const SENSITIVE_ENVIRONMENT = /(?:api[_-]?key|token|secret|credential|password|auth|session|cookie|userdata|database|db[_-]?path)/i;
const GOLDEN_FILES = [
  'fixtures/migration/p03/manifest.json',
  'fixtures/migration/p03/projects.golden.json',
  'fixtures/migration/p03/snapshot-empty.golden.json',
  'fixtures/migration/p03/snapshot-project.golden.json',
];
const SOURCE_FILES = [
  'package.json',
  EXPECTATIONS,
  'scripts/migration-p03/generate-node-golden.js',
  'scripts/migration-p03/normalize-contract.js',
  'scripts/migration-p03/generate-node-golden.test.js',
  'scripts/migration-p03/compare-golden.js',
  'scripts/migration-p03/compare-golden.test.js',
  'scripts/migration-p03/check-readonly.js',
  'scripts/migration-p03/check-readonly.test.js',
  'scripts/migration-p03/verify.js',
  ...GOLDEN_FILES,
  'fixtures/migration/p03/readme.md',
  'backend/internal/repository/repository.go',
  'backend/internal/repository/sqlite/readonly.go',
  'backend/internal/repository/sqlite/projects.go',
  'backend/internal/repository/sqlite/settings.go',
  'backend/internal/repository/sqlite/project_states.go',
  'backend/internal/repository/sqlite/readonly_test.go',
  'backend/internal/repository/sqlite/projects_test.go',
  'backend/internal/application/projects/service.go',
  'backend/internal/application/projects/golden_test.go',
  'backend/internal/application/projects/service_test.go',
  'backend/internal/domain/project/project.go',
  'backend/internal/domain/contracts/types.go',
  'backend/internal/domain/contracts/decode.go',
  'backend/internal/domain/contracts/validation.go',
  'backend/internal/domain/contracts/fixtures_test.go',
  'backend/internal/bootstrap/dependencies.go',
  'backend/internal/httpapi/router.go',
  'backend/internal/httpapi/projects.go',
  'backend/internal/httpapi/middleware.go',
  'backend/internal/httpapi/errors.go',
  'backend/internal/httpapi/projects_test.go',
  'backend/internal/httpapi/projects_contract_test.go',
  'backend/openapi/openapi.yaml',
  'backend/openapi/schemas/project.schema.json',
  'backend/openapi/schemas/snapshot.schema.json',
  'src/renderer/lib/api/client.ts',
  'src/renderer/lib/api/httpClient.ts',
  'src/renderer/lib/api/events.ts',
  'src/renderer/lib/api/provider.tsx',
  'src/renderer/lib/api/transport.ts',
  'src/renderer/lib/api/httpClient.test.js',
  'src/renderer/lib/api/transport.test.js',
  'src/renderer/lib/api/projectTransport.contract.test.js',
  'src/renderer/hooks/useSnapshot.transport.test.js',
  'src/renderer/pages/ProjectsPage.transport.test.js',
  'docs/migration/p03/README.md',
  'docs/migration/p03/evidence/README.md',
];
const P03_TEST_FILES = SOURCE_FILES.filter((file) => /(?:_test\.go|\.test\.(?:js|ts|tsx))$/.test(file));

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
    .replaceAll(toPosix(temporaryRoot), '<p03-temp>')
    .replaceAll(toPosix(os.tmpdir()), '<tmp>')
    .replace(/\b[A-Za-z]:\/[^\s"'<>|]+/g, '<absolute-path>')
    .replace(/(^|[\s"'(])\/(?:Users|home|tmp|var|private|mnt|opt)\/[^\s"'<>]*/g, '$1<absolute-path>')
    .replace(/\b(Bearer\s+)(?!<token>)[A-Za-z0-9._~+\/-]+/gi, '$1<redacted>')
    .replace(/\bsk-[A-Za-z0-9_-]{8,}\b/g, '<redacted-key>')
    .replace(/((?:api[_-]?key|token|secret|credential|password|auth|session|cookie)\s*[=:]\s*)[^\s,;]+/gi, '$1<redacted>')
    .replace(/env_vars/gi, '<redacted-field>');
}

function sanitizeEvidenceValue(value, rootDir, temporaryRoot) {
  if (Array.isArray(value)) return value.map((item) => sanitizeEvidenceValue(item, rootDir, temporaryRoot));
  if (value && typeof value === 'object') {
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, sanitizeEvidenceValue(item, rootDir, temporaryRoot)]));
  }
  return typeof value === 'string' ? sanitizeLog(value, rootDir, temporaryRoot) : value;
}

function secretFindings(stdout, stderr) {
  const content = `${stdout || ''}\n${stderr || ''}`;
  const checks = [
    ['usable-api-key', /\bsk-[A-Za-z0-9_-]{12,}\b/],
    ['usable-bearer', /Bearer\s+(?!<token>|<redacted>)[A-Za-z0-9._~+\/-]{12,}/i],
    ['private-key', /BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY/],
    ['credential-value', /(?:api[_-]?key|token|secret|credential|password|auth|session|cookie)[^\r\n]{0,12}[=:][^\r\n]{0,4}["']?[A-Za-z0-9._~+\/-]{12,}/i],
  ];
  return checks.filter(([, pattern]) => pattern.test(content)).map(([label]) => label);
}

function safeEnvironment(temporaryRoot, source = process.env) {
  const environment = {};
  let removedCount = 0;
  for (const [name, value] of Object.entries(source)) {
    if (SENSITIVE_ENVIRONMENT.test(name)) removedCount += 1;
    else environment[name] = value;
  }
  environment.TEMP = temporaryRoot;
  environment.TMP = temporaryRoot;
  environment.TMPDIR = temporaryRoot;
  environment.GOTMPDIR = temporaryRoot;
  environment.AUTOPLAN_P03_VERIFY = '1';
  return { environment, removedCount };
}

function execute(spec, rootDir, environment) {
  return new Promise((resolve) => {
    const startedAt = new Date().toISOString();
    let stdout = '';
    let stderr = '';
    let settled = false;
    const child = spawn(spec.executable, spec.args, {
      cwd: path.join(rootDir, spec.cwd || '.'), env: environment,
      shell: false, windowsHide: true,
      windowsVerbatimArguments: Boolean(spec.windowsVerbatimArguments),
    });
    const timer = setTimeout(() => { if (!settled) child.kill(); }, spec.timeoutMS || 15 * 60 * 1000);
    const finish = (actual) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve({ ...actual, stdout, stderr, startedAt, endedAt: new Date().toISOString() });
    };
    child.stdout.on('data', (chunk) => { stdout += chunk.toString(); });
    child.stderr.on('data', (chunk) => { stderr += chunk.toString(); });
    child.on('error', (error) => finish({ exitCode: null, signal: null, error: error.message }));
    child.on('close', (exitCode, signal) => finish({ exitCode, signal: signal || null, error: null }));
  });
}

function successSpec(id, executable, args, description, cwd = '.') {
  const displayExecutable = executable === process.execPath ? 'node' : executable;
  return {
    id, executable, args, description, cwd, command: [displayExecutable, ...args].join(' '),
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
  const violations = [];
  for (const relative of P03_TEST_FILES) {
    const target = path.join(rootDir, relative);
    if (!fs.existsSync(target)) continue;
    fs.readFileSync(target, 'utf8').split(/\r?\n/).forEach((line, index) => {
      if (/\b(?:test|it|describe)\.(?:skip|only)\s*\(/.test(line)
          || /\b(?:skip|only)\s*:\s*true\b/.test(line)
          || /\bt\.Skip(?:f|Now)?\s*\(/.test(line)) {
        violations.push(`${relative}:${index + 1}`);
      }
    });
  }
  return violations.sort();
}

function artifactSafety(rootDir) {
  const findings = [];
  const safeSensitive = /^(?:|<token>|<redacted>|<redacted-env-vars>|·{4}mask|Authorization: Bearer <token>)$/;
  const visit = (value, trail) => {
    if (Array.isArray(value)) return value.forEach((item, index) => visit(item, `${trail}/${index}`));
    if (value && typeof value === 'object') {
      return Object.entries(value).forEach(([key, item]) => {
        const pointerKey = /(?:api[_-]?key|token|secret|credential|password|auth|env_vars)/i.test(key)
          ? '<sensitive-field>' : key;
        if (/(?:api[_-]?key|token|secret|credential|password|auth|env_vars)/i.test(key)
            && typeof item === 'string' && !safeSensitive.test(item)) findings.push(`${trail}/${pointerKey}:sensitive-value`);
        visit(item, `${trail}/${pointerKey}`);
      });
    }
    if (typeof value !== 'string') return;
    if (/\bsk-[A-Za-z0-9_-]{12,}\b|BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY/.test(value)) findings.push(`${trail}:credential-shape`);
    if (/(?:[A-Za-z]:[\\/](?:Users|Documents and Settings)[\\/]|\/(?:Users|home)\/)/i.test(value)) findings.push(`${trail}:local-path`);
  };
  for (const relative of GOLDEN_FILES.slice(1)) {
    try { visit(JSON.parse(fs.readFileSync(path.join(rootDir, relative), 'utf8')), relative); }
    catch { findings.push(`${relative}:invalid-json`); }
  }
  return { ok: findings.length === 0, inspected: GOLDEN_FILES.slice(1), findings };
}

async function runCommand(spec, state) {
  const actual = await execute(spec, state.rootDir, state.environment);
  actual.failureSignatures = actual.exitCode && actual.exitCode !== 0
    ? failureSignatures(actual.stdout, actual.stderr, state.rootDir) : [];
  if (actual.exitCode && actual.exitCode !== 0 && actual.failureSignatures.length === 0) {
    actual.failureSignatures = [`unclassified|sha256=${sha256(sanitizeForSignature(`${actual.stdout}\n${actual.stderr}`, state.rootDir))}`];
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
  let machineResult = null;
  if (spec.captureJSON && actual.exitCode === 0) {
    try {
      const parsed = JSON.parse(actual.stdout.trim().split(/\r?\n/).at(-1));
      machineResult = {
        ok: parsed.ok === true,
        normalizationVersion: String(parsed.normalizationVersion || ''),
        databaseSha256: /^[a-f0-9]{64}$/.test(parsed.databaseSha256 || '') ? parsed.databaseSha256 : null,
        scenarios: Array.isArray(parsed.scenarios) ? parsed.scenarios.map(String) : [],
      };
    } catch {
      evaluation.accepted = false;
      evaluation.reason = 'machine-readable result was missing or invalid';
    }
  }
  const record = {
    id: spec.id, description: spec.description, command: spec.command,
    expectedOutcome: spec.outcome, exitCode: actual.exitCode, signal: actual.signal,
    error: actual.error ? sanitizeLog(actual.error, state.rootDir, state.temporaryRoot) : null,
    startedAt: actual.startedAt, endedAt: actual.endedAt,
    failureSignatures: sanitizeEvidenceValue(actual.failureSignatures, state.rootDir, state.temporaryRoot),
    secretFindings: findings,
    evaluation: sanitizeEvidenceValue(evaluation, state.rootDir, state.temporaryRoot),
    stdoutLog, stdoutBytes: Buffer.byteLength(stdout), stdoutSha256: sha256(stdout),
    stderrLog, stderrBytes: Buffer.byteLength(stderr), stderrSha256: sha256(stderr),
    ...(machineResult ? { machineResult } : {}),
  };
  state.results.push(record);
  return record;
}

function cleanupTemporaryRoot(temporaryRoot) {
  const resolved = path.resolve(temporaryRoot);
  if (path.dirname(resolved) !== path.resolve(os.tmpdir()) || !path.basename(resolved).startsWith('autoplan-p03-verify-')) {
    return { cleaned: false, error: 'refused to remove non-owned temporary root' };
  }
  try {
    fs.rmSync(resolved, { recursive: true, force: true });
    return { cleaned: true, error: null };
  } catch (error) {
    return { cleaned: false, error: 'owned temporary root cleanup failed' };
  }
}

function p03Commands(expectations) {
  const compare = successSpec('golden-compare', process.execPath, ['scripts/migration-p03/compare-golden.js'],
    'Generate a sanitized temporary database, serialize Node then Go, enforce byte stability, and deep-compare goldens.');
  compare.captureJSON = true;
  return [
    successSpec('readonly-source', process.execPath, ['scripts/migration-p03/check-readonly.js'], 'Static repository, route, OpenAPI, table, loopback, and mutation guard.'),
    successSpec('readonly-runtime', process.execPath, ['--test', 'scripts/migration-p03/check-readonly.test.js'], 'Runtime before/after byte and sidecar guard.'),
    successSpec('node-golden-contracts', process.execPath, ['--test', 'scripts/migration-p03/generate-node-golden.test.js', 'scripts/migration-p03/compare-golden.test.js'], 'Node deterministic generation, sanitization, and strict comparison contracts.'),
    successSpec('go-repository-application', 'go', ['test', './internal/repository/sqlite', './internal/application/projects', '-count=1'], 'Go immutable repository and shared application contracts.', 'backend'),
    successSpec('go-http-contracts', 'go', ['test', './internal/httpapi', '-count=1'], 'Authenticated paginated Projects and Snapshot HTTP contracts.', 'backend'),
    compare,
    successSpec('renderer-transports', process.execPath, ['--test',
      'src/renderer/lib/api/httpClient.test.js', 'src/renderer/lib/api/transport.test.js',
      'src/renderer/lib/api/projectTransport.contract.test.js', 'src/renderer/hooks/useSnapshot.transport.test.js',
      'src/renderer/pages/ProjectsPage.transport.test.js'], 'React IPC/HTTP transport parity, cancellation, fallback, and SSE placeholder contracts.'),
    successSpec('go-all', 'go', ['test', './...'], 'Complete backend Go compile and test gate.', 'backend'),
    npmSpec('check', ['run', 'check'], expectations.commands.check),
    npmSpec('test', ['test'], expectations.commands.test),
  ];
}

async function runVerification(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  const expectations = JSON.parse(fs.readFileSync(path.join(rootDir, EXPECTATIONS), 'utf8'));
  const runDir = path.join(rootDir, EVIDENCE_ROOT, options.runId || stableRunId());
  if (fs.existsSync(runDir)) throw new Error(`refusing to overwrite evidence directory: ${runDir}`);
  fs.mkdirSync(runDir, { recursive: true });
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p03-verify-'));
  const safe = safeEnvironment(temporaryRoot, options.environment || process.env);
  const startStatus = gitStatus(rootDir);
  const summary = {
    schemaVersion: 1, runId: path.basename(runDir), startedAt: new Date().toISOString(), status: 'running',
    environment: {
      platform: process.platform, arch: process.arch, node: process.version,
      cwd: '<repository-root>', temporaryRoot: `<system-temp>/${path.basename(temporaryRoot)}`,
      removedSensitiveEnvironmentVariableCount: safe.removedCount,
      environmentValuesCaptured: false, electronUserDataAccessed: false, reusableSessionCaptured: false,
    },
    sourceHashesStart: SOURCE_FILES.map((file) => fileRecord(rootDir, file)),
    fixtureAndGoldenHashes: GOLDEN_FILES.map((file) => fileRecord(rootDir, file)),
    artifactSafety: artifactSafety(rootDir),
    testControlViolations: testControlViolations(rootDir),
    interfaceCoverage: [
      'repository:projects/settings/project_states:read-only',
      'application:projects/list/get/snapshot',
      'http:GET+HEAD /api/v1/projects[/project_id][/snapshot]',
      'renderer:IPC-default+explicit-HTTP+SSE-placeholder',
    ],
    commandResults: [],
  };
  summary.sourceFilesComplete = summary.sourceHashesStart.every((record) => !record.missing);
  const state = { rootDir, runDir, temporaryRoot, environment: safe.environment, results: summary.commandResults };
  const gates = [
    npmSpec('p00-gate', ['run', 'migration:p00:verify'], { description: 'P00 baseline and safety hard gate.', outcome: 'success', allowedFailureSignatures: [] }),
    npmSpec('p01-gate', ['run', 'migration:p01:verify'], { description: 'P01 renderer boundary hard gate.', outcome: 'success', allowedFailureSignatures: [] }),
    npmSpec('p02-gate', ['run', 'migration:p02:verify'], { description: 'P02 sidecar contract and security hard gate.', outcome: 'success', allowedFailureSignatures: [] }),
  ];
  for (const gate of gates) {
    const result = await runCommand(gate, state);
    if (!result.evaluation.accepted) {
      summary.status = 'blocked';
      summary.blocked = `${gate.id} failed; no later P03 command was started.`;
      break;
    }
  }
  if (summary.status !== 'blocked') {
    try {
      summary.prerequisiteEvidence = verifyPrerequisites(rootDir);
    } catch (error) {
      summary.status = 'blocked';
      summary.blocked = sanitizeLog(
        `completed prerequisite evidence rejected: ${String(error.code || error.message || 'invalid')}`,
        rootDir,
        temporaryRoot,
      );
    }
  }
  if (summary.status !== 'blocked') {
    if (!summary.artifactSafety.ok) {
      summary.status = 'failed';
    } else {
      const commands = p03Commands(expectations);
      for (const command of commands) {
        const result = await runCommand(command, state);
        if (command.id === 'readonly-source' && !result.evaluation.accepted) {
          summary.status = 'failed';
          break;
        }
      }
      if (summary.status !== 'failed') summary.status = 'completed';
    }
  }
  const comparison = summary.commandResults.find((item) => item.id === 'golden-compare')?.machineResult;
  summary.databaseIntegrity = comparison?.databaseSha256 ? {
    beforeSha256: comparison.databaseSha256, afterSha256: comparison.databaseSha256,
    unchanged: comparison.ok === true, databaseBytesCaptured: false,
  } : { beforeSha256: null, afterSha256: null, unchanged: false, databaseBytesCaptured: false };
  summary.nodeGoDiff = comparison ? {
    equal: comparison.ok, normalizationVersion: comparison.normalizationVersion,
    scenarios: comparison.scenarios, differences: comparison.ok ? [] : ['comparison-failed'],
  } : { equal: false, scenarios: [], differences: ['comparison-not-completed'] };
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
  if (summary.testControlViolations.length) summary.remainingRisks.push(`forbidden test controls: ${summary.testControlViolations.join(', ')}`);
  if (!summary.artifactSafety.ok) summary.remainingRisks.push(`unsafe golden artifact: ${summary.artifactSafety.findings.join(', ')}`);
  if (!summary.sourceHashesStable) summary.remainingRisks.push('P03 guarded sources changed during verification.');
  if (!summary.sourceFilesComplete) summary.remainingRisks.push('One or more P03 guarded sources were missing.');
  if (!summary.temporaryCleanup.cleaned) summary.remainingRisks.push('P03 system-temporary root cleanup failed.');
  summary.remainingRisks.push('P15 retains the obligation to clear or explicitly disposition every frozen baseline failure.');
  summary.ok = summary.status === 'completed' && startStatus.exitCode === 0 && endStatus.exitCode === 0
    && summary.sourceHashesStable && summary.sourceFilesComplete
    && summary.testControlViolations.length === 0 && summary.artifactSafety.ok
    && summary.databaseIntegrity.unchanged && summary.nodeGoDiff.equal && summary.temporaryCleanup.cleaned
    && summary.commandResults.every((item) => item.evaluation.accepted);
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
    throw new Error('usage: node scripts/migration-p03/verify.js verify');
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
  artifactSafety,
  parseArgs,
  runVerification,
  safeEnvironment,
  sanitizeLog,
  secretFindings,
  sanitizeEvidenceValue,
  testControlViolations,
};
