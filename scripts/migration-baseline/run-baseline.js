'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn, spawnSync } = require('node:child_process');

const ROOT = path.resolve(__dirname, '../..');
const EXPECTATIONS_PATH = 'docs/migration/p00/baseline-expectations.json';
const EVIDENCE_MANIFEST_PATH = 'docs/migration/p00/evidence/manifest.json';
const EVIDENCE_ROOT = 'docs/migration/p00/evidence/runs';
const SECRET_ENV_PATTERN = /(?:api[_-]?key|token|secret|credential|password|auth)/i;

function toPosix(value) {
  return String(value).replace(/\\/g, '/');
}

function sha256Buffer(value) {
  return crypto.createHash('sha256').update(value).digest('hex');
}

function sha256File(filePath) {
  return sha256Buffer(fs.readFileSync(filePath));
}

function hashSources(rootDir, files) {
  return files.map((file) => {
    const target = path.join(rootDir, file);
    return { path: toPosix(file), sha256: sha256File(target), bytes: fs.statSync(target).size };
  });
}

function stableRunId(now = new Date(), pid = process.pid) {
  return `${now.toISOString().replace(/[:.]/g, '-')}-pid-${pid}`;
}

function listFiles(root) {
  if (!fs.existsSync(root)) return [];
  const result = [];
  for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
    const target = path.join(root, entry.name);
    if (entry.isDirectory()) result.push(...listFiles(target));
    else if (entry.isFile()) result.push(target);
  }
  return result.sort((left, right) => left.localeCompare(right));
}

function snapshotDirectory(rootDir, target) {
  const absolute = path.join(rootDir, target);
  return listFiles(absolute).map((filePath) => ({
    path: toPosix(path.relative(rootDir, filePath)),
    bytes: fs.statSync(filePath).size,
    sha256: sha256File(filePath),
  }));
}

function directoryDelta(before, after) {
  const beforeByPath = new Map(before.map((item) => [item.path, item]));
  const afterByPath = new Map(after.map((item) => [item.path, item]));
  const paths = [...new Set([...beforeByPath.keys(), ...afterByPath.keys()])].sort();
  return paths.flatMap((file) => {
    const left = beforeByPath.get(file);
    const right = afterByPath.get(file);
    if (!left) return [{ path: file, change: 'added', after: right }];
    if (!right) return [{ path: file, change: 'deleted', before: left }];
    if (left.sha256 !== right.sha256) return [{ path: file, change: 'modified', before: left, after: right }];
    return [];
  });
}

function gitStatus(rootDir) {
  const result = spawnSync('git', ['status', '--porcelain=v1', '--untracked-files=all'], {
    cwd: rootDir,
    encoding: 'utf8',
    windowsHide: true,
  });
  const entries = String(result.stdout || '').split(/\r?\n/).filter(Boolean);
  const contentHashes = {};
  for (const entry of entries) {
    const rawPath = entry.slice(3).split(' -> ').at(-1);
    if (!rawPath || rawPath.startsWith('"')) continue;
    const target = path.join(rootDir, rawPath);
    if (fs.existsSync(target) && fs.statSync(target).isFile()) contentHashes[toPosix(rawPath)] = sha256File(target);
  }
  return {
    command: 'git status --porcelain=v1 --untracked-files=all',
    exitCode: typeof result.status === 'number' ? result.status : null,
    signal: result.signal || null,
    error: result.error ? result.error.message : null,
    entries,
    contentHashes,
    stderr: String(result.stderr || ''),
  };
}

function changedStatusEntries(before, after) {
  const left = new Set(before.entries);
  const right = new Set(after.entries);
  return {
    appeared: [...right].filter((item) => !left.has(item)).sort(),
    disappeared: [...left].filter((item) => !right.has(item)).sort(),
    contentChanged: [...new Set([...Object.keys(before.contentHashes || {}), ...Object.keys(after.contentHashes || {})])]
      .filter((file) => before.contentHashes?.[file] !== after.contentHashes?.[file])
      .sort(),
  };
}

function affectedFiles(workingTreeDelta, distDelta) {
  const statusPaths = [...workingTreeDelta.appeared, ...workingTreeDelta.disappeared]
    .map((entry) => entry.slice(3).split(' -> ').at(-1))
    .filter((file) => file && !file.startsWith('"'))
    .map(toPosix);
  return [...new Set([...statusPaths, ...workingTreeDelta.contentChanged, ...distDelta.map((item) => item.path)])].sort();
}

function sanitizeForSignature(value, rootDir = ROOT) {
  return toPosix(String(value)
    .replace(/\u001b\[[0-9;]*m/g, '')
    .trim())
    .replaceAll(toPosix(rootDir), '<repo>')
    .replaceAll(toPosix(os.tmpdir()), '<tmp>')
    .replace(/\b\d+(?:\.\d+)?\s*m?s\b/gi, '<duration>')
    .trim();
}

function failureSignatures(stdout, stderr, rootDir = ROOT) {
  const lines = `${stdout || ''}\n${stderr || ''}`.split(/\r?\n/).map((line) => sanitizeForSignature(line, rootDir));
  const signatures = [];
  for (const line of lines) {
    let recognized = false;
    let match = /^-\s+(.+?):\s+\d+ lines \(limit (\d+)\)$/.exec(line);
    if (match) {
      signatures.push(`file-length|${toPosix(match[1])}|limit=${match[2]}`);
      recognized = true;
    }
    match = /^(.+?)\(\d+,\d+\): error (TS\d+):\s*(.+)$/.exec(line);
    if (match) {
      signatures.push(`typescript|${toPosix(match[1]).replace(/^<repo>\/?/, '')}|${match[2]}|${match[3]}`);
      recognized = true;
    }
    match = /^not ok \d+ - (.+)$/.exec(line);
    if (match) {
      signatures.push(`node-test|${match[1].replace(/\s+# time=.*$/, '')}`);
      recognized = true;
    }
    match = /Could not find ['"](.+?\*\*.+?)['"]/.exec(line);
    if (match) {
      signatures.push(`node-test-discovery|not-found|${toPosix(match[1]).replace(/^<repo>\/?/, '')}`);
      recognized = true;
    }
    match = /(?:Cannot find module|ERR_MODULE_NOT_FOUND).*?['"]([^'"]+)['"]/.exec(line);
    if (match) {
      signatures.push(`dependency|module-not-found|${toPosix(match[1])}`);
      recognized = true;
    }
    const wrapper = /^File length check failed:$|^npm (?:error|ERR!)/i.test(line);
    if (!recognized && !wrapper && /^(?:Error|AssertionError|TypeError|ReferenceError|fatal):/i.test(line)) {
      signatures.push(`diagnostic|${line}`);
    }
  }
  return [...new Set(signatures)].sort();
}

function evaluateCommand(command, actual) {
  if (actual.blocked) return { accepted: false, reason: `blocked: ${actual.blocked.reason}` };
  if (actual.exitCode === null) return { accepted: false, reason: actual.error || 'command did not return an exit code' };
  const expectedSignatures = [...(command.allowedFailureSignatures || [])].sort();
  const actualSignatures = [...(actual.failureSignatures || [])].sort();
  if (command.outcome === 'success') {
    return actual.exitCode === 0
      ? { accepted: true, reason: 'exit code 0' }
      : { accepted: false, reason: `expected exit code 0, received ${actual.exitCode}` };
  }
  if (command.outcome !== 'exact-known-failure') return { accepted: false, reason: `unknown expectation outcome ${command.outcome}` };
  if (actual.exitCode === 0) return { accepted: false, reason: 'known failure disappeared without an expectations update' };
  if (!(command.allowedExitCodes || []).includes(actual.exitCode)) {
    return { accepted: false, reason: `exit code ${actual.exitCode} is not frozen` };
  }
  if (JSON.stringify(expectedSignatures) !== JSON.stringify(actualSignatures)) {
    return { accepted: false, reason: 'failure signature set drifted', expectedSignatures, actualSignatures };
  }
  return { accepted: true, reason: 'exact frozen failure set matched', knownFailure: true };
}

function smokeSafety(rootDir) {
  const smokePath = path.join(rootDir, 'scripts/smoke-test.js');
  const helperPath = path.join(rootDir, 'scripts/smoke-helpers.js');
  if (!fs.existsSync(smokePath) || !fs.existsSync(helperPath)) {
    return { safe: false, reasons: ['smoke test or smoke helper is missing'] };
  }
  const source = fs.readFileSync(smokePath, 'utf8');
  const helperSource = fs.readFileSync(helperPath, 'utf8');
  const checks = [
    ['unique system temporary root', /fs\.mkdtempSync\(path\.join\(os\.tmpdir\(\), ['"]autoplan-smoke-/],
    ['temporary database', /const dbPath = path\.join\(tempRoot, ['"]data['"], ['"]autoplan\.sqlite['"]\)/],
    ['temporary root cleanup', /finally\s*\{\s*fs\.rmSync\(tempRoot, \{ recursive: true, force: true \}\)/],
    ['stubbed agent child', /loadPatchedLoopService\(\{[\s\S]*?spawnOverride:/],
    ['synthetic API key only', /sk-smoke-ai-123456/],
  ];
  const reasons = checks.filter(([, pattern]) => !pattern.test(source)).map(([label]) => `missing invariant: ${label}`);
  if (/app\.getPath\(['"]userData['"]\)/.test(source)) reasons.push('smoke source reads Electron userData');
  if (/require\(['"]node:child_process['"]\)|require\(['"]child_process['"]\)/.test(source)) {
    reasons.push('smoke source imports child_process directly');
  }
  if (!/request === ['"]node:child_process['"]\) return fakeChildProcess/.test(helperSource)
      || !/spawn:\s*\(command, args, options\) => spawnOverride\(command, args, options\)/.test(helperSource)) {
    reasons.push('smoke helper does not replace child_process with the spawn override');
  }
  return { safe: reasons.length === 0, reasons, checks: checks.map(([label]) => label) };
}

function testControlViolations(rootDir) {
  const roots = ['src', 'scripts/migration-baseline'];
  const violations = [];
  for (const relativeRoot of roots) {
    for (const filePath of listFiles(path.join(rootDir, relativeRoot)).filter((file) => file.endsWith('.test.js'))) {
      const lines = fs.readFileSync(filePath, 'utf8').split(/\r?\n/);
      lines.forEach((line, index) => {
        if (/\b(?:test|it|describe)\.(?:skip|only)\s*\(/.test(line) || /\b(?:skip|only)\s*:\s*true\b/.test(line)) {
          violations.push(`${toPosix(path.relative(rootDir, filePath))}:${index + 1}`);
        }
      });
    }
  }
  return violations;
}

function safeSmokeEnvironment(baseEnvironment = process.env) {
  const environment = {};
  const removedSecretVariableNames = [];
  for (const [name, value] of Object.entries(baseEnvironment)) {
    if (SECRET_ENV_PATTERN.test(name)) removedSecretVariableNames.push(name);
    else environment[name] = value;
  }
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p00-smoke-env-'));
  environment.TEMP = temporaryRoot;
  environment.TMP = temporaryRoot;
  environment.TMPDIR = temporaryRoot;
  environment.AUTOPLAN_P00_SAFE_SMOKE = '1';
  return { environment, temporaryRoot, removedSecretVariableNames: removedSecretVariableNames.sort() };
}

function cleanupSmokeEnvironment(temporaryRoot) {
  const resolved = path.resolve(temporaryRoot);
  const parent = path.dirname(resolved);
  if (parent !== path.resolve(os.tmpdir()) || !path.basename(resolved).startsWith('autoplan-p00-smoke-env-')) {
    return { cleaned: false, error: 'refused to remove a non-owned temporary root' };
  }
  try {
    fs.rmSync(resolved, { recursive: true, force: true });
    return { cleaned: true, error: null };
  } catch (error) {
    return { cleaned: false, error: error.message };
  }
}

function displayCommand(executable, args) {
  return [executable, ...args].map((item) => /\s/.test(item) ? JSON.stringify(item) : item).join(' ');
}

function execute(spec, options = {}) {
  return new Promise((resolve) => {
    const startedAt = new Date();
    let stdout = '';
    let stderr = '';
    let settled = false;
    const child = spawn(spec.executable, spec.args, {
      cwd: options.rootDir || ROOT,
      env: spec.env || process.env,
      shell: false,
      windowsHide: true,
      windowsVerbatimArguments: Boolean(spec.windowsVerbatimArguments),
    });
    child.stdout.on('data', (chunk) => { stdout += chunk.toString(); });
    child.stderr.on('data', (chunk) => { stderr += chunk.toString(); });
    child.on('error', (error) => {
      if (settled) return;
      settled = true;
      resolve({ exitCode: null, signal: null, error: error.message, stdout, stderr, startedAt: startedAt.toISOString(), endedAt: new Date().toISOString() });
    });
    child.on('close', (exitCode, signal) => {
      if (settled) return;
      settled = true;
      resolve({ exitCode, signal, error: null, stdout, stderr, startedAt: startedAt.toISOString(), endedAt: new Date().toISOString() });
    });
  });
}

function commandSpecs(rootDir, expectations, smokeEnvironment) {
  const node = process.execPath;
  const npm = process.platform === 'win32' ? 'npm.cmd' : 'npm';
  const tests = listFiles(path.join(rootDir, 'scripts/migration-baseline'))
    .filter((file) => file.endsWith('.test.js'))
    .map((file) => path.relative(rootDir, file));
  const npmCommand = (args, env) => process.platform === 'win32' ? {
    executable: process.env.ComSpec || 'cmd.exe',
    args: ['/d', '/s', '/c', `call npm.cmd ${args.join(' ')}`],
    windowsVerbatimArguments: true,
    displayExecutable: npm,
    displayArgs: args,
    env,
  } : { executable: npm, args, displayExecutable: npm, displayArgs: args, env };
  const runtime = {
    specialized: { executable: node, args: ['--test', ...tests] },
    check: npmCommand(['run', 'check']),
    test: npmCommand(['test']),
    build: npmCommand(['run', 'build']),
    smoke: npmCommand(['run', 'smoke'], smokeEnvironment),
  };
  return expectations.commandOrder.map((id) => ({ id, ...expectations.commands[id], ...runtime[id] }));
}

function writeJson(filePath, value) {
  fs.writeFileSync(filePath, `${JSON.stringify(value, null, 2)}\n`, 'utf8');
}

function environmentSummary(smokeEnvironment) {
  return {
    platform: process.platform,
    arch: process.arch,
    node: process.version,
    npmExecutable: process.platform === 'win32' ? 'npm.cmd' : 'npm',
    cwd: '<repository-root>',
    locale: Intl.DateTimeFormat().resolvedOptions().locale,
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    smokeTemporaryRoot: `<system-temp>/${path.basename(smokeEnvironment.temporaryRoot)}`,
    removedSecretVariableNames: smokeEnvironment.removedSecretVariableNames,
    environmentValuesCaptured: false,
  };
}

async function runBaseline(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  const expectations = JSON.parse(fs.readFileSync(path.join(rootDir, EXPECTATIONS_PATH), 'utf8'));
  const evidenceDefinition = JSON.parse(fs.readFileSync(path.join(rootDir, EVIDENCE_MANIFEST_PATH), 'utf8'));
  const requestedEvidenceRoot = options.evidenceRoot || EVIDENCE_ROOT;
  const evidenceRoot = path.isAbsolute(requestedEvidenceRoot)
    ? requestedEvidenceRoot
    : path.join(rootDir, requestedEvidenceRoot);
  const runDir = path.join(evidenceRoot, options.runId || stableRunId());
  if (fs.existsSync(runDir)) throw new Error(`refusing to overwrite evidence directory: ${runDir}`);
  fs.mkdirSync(runDir, { recursive: true });
  const initialGit = gitStatus(rootDir);
  const initialDist = snapshotDirectory(rootDir, 'dist');
  writeJson(path.join(runDir, 'git-status-start.json'), initialGit);
  writeJson(path.join(runDir, 'dist-start.json'), initialDist);

  const safety = smokeSafety(rootDir);
  const smoke = safeSmokeEnvironment(options.environment || process.env);
  const summary = {
    schemaVersion: 1,
    runId: path.basename(runDir),
    startedAt: new Date().toISOString(),
    expectationsSha256: sha256File(path.join(rootDir, EXPECTATIONS_PATH)),
    sourceHashesStart: hashSources(rootDir, expectations.sourceFiles),
    environment: environmentSummary(smoke),
    smokeSafety: safety,
    testControlViolations: testControlViolations(rootDir),
    commandResults: [],
  };
  const executor = options.execute || execute;
  for (const command of commandSpecs(rootDir, expectations, smoke.environment)) {
    const beforeGit = gitStatus(rootDir);
    const beforeDist = snapshotDirectory(rootDir, 'dist');
    let actual;
    if (command.id === 'smoke' && !safety.safe) {
      const timestamp = new Date().toISOString();
      actual = { exitCode: null, signal: null, error: null, stdout: '', stderr: '', startedAt: timestamp, endedAt: timestamp, blocked: { reason: safety.reasons.join('; ') } };
    } else {
      actual = await executor(command, { rootDir });
    }
    const afterGit = gitStatus(rootDir);
    const afterDist = snapshotDirectory(rootDir, 'dist');
    const logBase = `${String(summary.commandResults.length + 1).padStart(2, '0')}-${command.id}`;
    const stdoutLog = `${logBase}.stdout.log`;
    const stderrLog = `${logBase}.stderr.log`;
    const stdoutPath = path.join(runDir, stdoutLog);
    const stderrPath = path.join(runDir, stderrLog);
    fs.writeFileSync(stdoutPath, actual.stdout || '', 'utf8');
    fs.writeFileSync(stderrPath, actual.stderr || '', 'utf8');
    actual.failureSignatures = actual.exitCode && actual.exitCode !== 0
      ? failureSignatures(actual.stdout, actual.stderr, rootDir)
      : [];
    if (actual.exitCode && actual.exitCode !== 0 && actual.failureSignatures.length === 0) {
      const normalized = sanitizeForSignature(`${actual.stdout}\n${actual.stderr}`, rootDir);
      actual.failureSignatures = [`unclassified|sha256=${sha256Buffer(normalized)}`];
    }
    const evaluation = evaluateCommand(command, actual);
    const workingTreeDelta = changedStatusEntries(beforeGit, afterGit);
    const distDelta = directoryDelta(beforeDist, afterDist);
    const { stdout: _stdout, stderr: _stderr, ...actualWithoutLogs } = actual;
    summary.commandResults.push({
      id: command.id,
      command: displayCommand(command.displayExecutable || command.executable, command.displayArgs || command.args),
      expectedOutcome: command.outcome,
      environment: { profile: command.id === 'smoke' ? 'sanitized-isolated-temporary-root' : 'inherited-process', valuesCaptured: false },
      ...actualWithoutLogs,
      evaluation,
      affectedFiles: affectedFiles(workingTreeDelta, distDelta),
      workingTreeDelta,
      distDelta,
      stdoutLog,
      stdoutBytes: fs.statSync(stdoutPath).size,
      stdoutSha256: sha256File(stdoutPath),
      stderrLog,
      stderrBytes: fs.statSync(stderrPath).size,
      stderrSha256: sha256File(stderrPath),
    });
  }

  summary.smokeTemporaryCleanup = cleanupSmokeEnvironment(smoke.temporaryRoot);

  const finalGit = gitStatus(rootDir);
  const finalDist = snapshotDirectory(rootDir, 'dist');
  writeJson(path.join(runDir, 'git-status-end.json'), finalGit);
  writeJson(path.join(runDir, 'dist-end.json'), finalDist);
  summary.endedAt = new Date().toISOString();
  summary.expectationsSha256End = sha256File(path.join(rootDir, EXPECTATIONS_PATH));
  summary.expectationsHashStable = summary.expectationsSha256 === summary.expectationsSha256End;
  summary.sourceHashesEnd = hashSources(rootDir, expectations.sourceFiles);
  summary.sourceHashesStable = JSON.stringify(summary.sourceHashesStart) === JSON.stringify(summary.sourceHashesEnd);
  summary.workingTreeDelta = changedStatusEntries(initialGit, finalGit);
  summary.distDelta = directoryDelta(initialDist, finalDist);
  const pendingArtifacts = new Set(['summary.json', 'evidence-manifest.json']);
  const missingArtifacts = evidenceDefinition.requiredArtifacts.map((item) => item.path)
    .filter((file) => !pendingArtifacts.has(file) && !fs.existsSync(path.join(runDir, file)));
  summary.evidenceCompleteness = { complete: missingArtifacts.length === 0, missingArtifacts };
  summary.ok = initialGit.exitCode === 0 && finalGit.exitCode === 0 && summary.sourceHashesStable
    && summary.expectationsHashStable
    && summary.evidenceCompleteness.complete
    && summary.testControlViolations.length === 0
    && summary.smokeTemporaryCleanup.cleaned
    && summary.commandResults.every((item) => item.evaluation.accepted);
  writeJson(path.join(runDir, 'summary.json'), summary);
  const artifacts = listFiles(runDir).map((filePath) => ({
    path: toPosix(path.relative(runDir, filePath)),
    bytes: fs.statSync(filePath).size,
    sha256: sha256File(filePath),
  }));
  writeJson(path.join(runDir, 'evidence-manifest.json'), {
    schemaVersion: 1,
    runId: summary.runId,
    generatedAt: summary.endedAt,
    immutableRunDirectory: true,
    artifacts,
  });
  return { runDir, summary };
}

function parseArgs(argv) {
  if (argv.length !== 1 || argv[0] !== 'verify') throw new Error('usage: node scripts/migration-baseline/run-baseline.js verify');
  return { mode: 'verify' };
}

if (require.main === module) {
  try {
    parseArgs(process.argv.slice(2));
    runBaseline().then(({ runDir, summary }) => {
      for (const item of summary.commandResults) {
        process.stdout.write(`${item.id}: exit=${item.exitCode ?? 'blocked'} accepted=${item.evaluation.accepted}\n`);
      }
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
  changedStatusEntries,
  cleanupSmokeEnvironment,
  commandSpecs,
  directoryDelta,
  evaluateCommand,
  failureSignatures,
  hashSources,
  parseArgs,
  runBaseline,
  safeSmokeEnvironment,
  sanitizeForSignature,
  smokeSafety,
  stableRunId,
  testControlViolations,
};
