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
const EVIDENCE_ROOT = 'docs/migration/p01/evidence/runs';
const TYPESCRIPT_TESTS = [
  'src/renderer/hooks/useSnapshot.test.ts',
  'src/renderer/hooks/useTerminalSessions.test.ts',
  'src/renderer/pages/WorkspacePage.test.tsx',
  'src/renderer/components/workspace/WorkspaceExecutorsView.test.tsx',
];
const SOURCE_FILES = [
  'package.json',
  'scripts/migration-p01/check-renderer-boundary.js',
  'scripts/migration-p01/check-renderer-boundary.test.js',
  'scripts/migration-p01/verify.js',
  'scripts/migration-baseline/inventory-ipc.js',
  'scripts/migration-baseline/inventory-ipc.test.js',
  'src/renderer/lib/api/client.ts',
  'src/renderer/lib/api/events.ts',
  'src/renderer/lib/api/ipcClient.ts',
  'src/renderer/lib/api/provider.tsx',
  'src/renderer/lib/api/transport.ts',
  'src/renderer/lib/desktop/bridge.ts',
  'src/renderer/lib/desktop/ipcBridge.ts',
  'src/renderer/main.tsx',
  'src/renderer/types.ts',
  'src/renderer/hooks/useSnapshot.ts',
  'src/renderer/hooks/useSnapshot.test.ts',
  'src/renderer/hooks/useChat.ts',
  'src/renderer/hooks/useChatQueue.ts',
  'src/renderer/hooks/useTerminalSessions.ts',
  'src/renderer/hooks/useTerminalSessions.test.ts',
  'src/renderer/hooks/useUpdateStatus.ts',
  'src/renderer/lib/api/ipcClient.test.js',
  'src/renderer/lib/desktop/ipcBridge.test.js',
  'src/renderer/lib/api/subscriptions.test.js',
  'src/renderer/pages/ProjectsPage.tsx',
  'src/renderer/pages/WorkspacePage.tsx',
  'src/renderer/hooks/useWorkspaceController.ts',
  'src/renderer/components/IntakePanel.tsx',
  'src/renderer/components/workspace/AcceptanceView.tsx',
  'src/renderer/pages/WorkspacePage.projectId.test.js',
  'src/renderer/pages/WorkspacePage.test.tsx',
  'src/renderer/types.syntax.test.js',
  'src/renderer/components/workspace/ScriptEditorModal.tsx',
  'src/renderer/components/workspace/ExecutorEditorModal.tsx',
  'src/renderer/components/workspace/WorkspaceExecutorsView.tsx',
  'src/renderer/components/workspace/WorkspaceExecutorsView.test.tsx',
  'src/renderer/components/workspace/WorkspaceSettingsView.tsx',
  'src/renderer/components/workspace/WorkspaceSidebar.tsx',
  'src/renderer/components/workspace/McpControlPanel.tsx',
  'src/renderer/components/UpdateNotice.tsx',
  'src/renderer/components/shared.tsx',
  'docs/migration/p01/README.md',
  'docs/migration/p01/client-boundary.md',
  'docs/migration/p01/evidence/README.md',
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

function writeJson(filePath, value) {
  fs.writeFileSync(filePath, `${JSON.stringify(value, null, 2)}\n`, 'utf8');
}

function sanitizeLog(value, rootDir) {
  return toPosix(String(value || ''))
    .replaceAll(toPosix(rootDir), '<repo>')
    .replaceAll(toPosix(os.homedir()), '<home>')
    .replaceAll(toPosix(os.tmpdir()), '<tmp>')
    .replace(/\b(Bearer\s+)[A-Za-z0-9._~+\/-]+/gi, '$1<redacted>')
    .replace(/\b(sk-[A-Za-z0-9_-]{8,})\b/g, '<redacted-key>')
    .replace(/((?:api[_-]?key|token|secret|credential|password|auth)\s*[=:]\s*)[^\s,;]+/gi, '$1<redacted>');
}

function execute(spec, rootDir) {
  return new Promise((resolve) => {
    const startedAt = new Date().toISOString();
    let stdout = '';
    let stderr = '';
    let settled = false;
    const child = spawn(spec.executable, spec.args, {
      cwd: rootDir,
      env: process.env,
      shell: false,
      windowsHide: true,
      windowsVerbatimArguments: Boolean(spec.windowsVerbatimArguments),
    });
    child.stdout.on('data', (chunk) => { stdout += chunk.toString(); });
    child.stderr.on('data', (chunk) => { stderr += chunk.toString(); });
    child.on('error', (error) => {
      if (settled) return;
      settled = true;
      resolve({ exitCode: null, signal: null, error: error.message, stdout, stderr, startedAt, endedAt: new Date().toISOString() });
    });
    child.on('close', (exitCode, signal) => {
      if (settled) return;
      settled = true;
      resolve({ exitCode, signal: signal || null, error: null, stdout, stderr, startedAt, endedAt: new Date().toISOString() });
    });
  });
}

function npmSpec(id, args, expectation) {
  if (process.platform === 'win32') {
    return {
      id,
      ...expectation,
      executable: process.env.ComSpec || 'cmd.exe',
      args: ['/d', '/s', '/c', `call npm.cmd ${args.join(' ')}`],
      windowsVerbatimArguments: true,
      command: `npm.cmd ${args.join(' ')}`,
    };
  }
  return { id, ...expectation, executable: 'npm', args, command: `npm ${args.join(' ')}` };
}

function nodeSpec(id, args, description) {
  return {
    id,
    description,
    outcome: 'success',
    allowedFailureSignatures: [],
    executable: process.execPath,
    args,
    command: `node ${args.join(' ')}`,
  };
}

function addFailureSignatures(actual, rootDir) {
  actual.failureSignatures = actual.exitCode && actual.exitCode !== 0
    ? failureSignatures(actual.stdout, actual.stderr, rootDir)
    : [];
  if (actual.exitCode && actual.exitCode !== 0 && actual.failureSignatures.length === 0) {
    const normalized = sanitizeForSignature(`${actual.stdout}\n${actual.stderr}`, rootDir);
    actual.failureSignatures = [`unclassified|sha256=${sha256(normalized)}`];
  }
  return actual;
}

function testControlViolations(rootDir) {
  const violations = [];
  const visit = (directory) => {
    if (!fs.existsSync(directory)) return;
    for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
      const target = path.join(directory, entry.name);
      if (entry.isDirectory()) visit(target);
      else if (entry.isFile() && /\.test\.(?:js|ts|tsx)$/.test(entry.name)) {
        fs.readFileSync(target, 'utf8').split(/\r?\n/).forEach((line, index) => {
          if (/\b(?:test|it|describe)\.(?:skip|only)\s*\(/.test(line) || /\b(?:skip|only)\s*:\s*true\b/.test(line)) {
            violations.push(`${toPosix(path.relative(rootDir, target))}:${index + 1}`);
          }
        });
      }
    }
  };
  visit(path.join(rootDir, 'src'));
  visit(path.join(rootDir, 'scripts/migration-baseline'));
  visit(path.join(rootDir, 'scripts/migration-p01'));
  return violations.sort();
}

function gitStatus(rootDir) {
  const result = spawnSync('git', ['status', '--porcelain=v1', '--untracked-files=all'], {
    cwd: rootDir,
    encoding: 'utf8',
    windowsHide: true,
  });
  return {
    exitCode: typeof result.status === 'number' ? result.status : null,
    entries: String(result.stdout || '').split(/\r?\n/).filter(Boolean),
    stderr: sanitizeLog(result.stderr || '', rootDir),
  };
}

function runTypeScriptTests(rootDir = ROOT) {
  const ts = require('typescript');
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p01-ts-tests-'));
  try {
    const compiledTests = TYPESCRIPT_TESTS.map((relativePath, index) => {
      const source = fs.readFileSync(path.join(rootDir, relativePath), 'utf8');
      const result = ts.transpileModule(source, {
        fileName: relativePath,
        reportDiagnostics: true,
        compilerOptions: {
          module: ts.ModuleKind.CommonJS,
          target: ts.ScriptTarget.ES2022,
          jsx: ts.JsxEmit.ReactJSX,
        },
      });
      const diagnostics = result.diagnostics || [];
      if (diagnostics.length) {
        const details = diagnostics.map((diagnostic) => ts.flattenDiagnosticMessageText(diagnostic.messageText, '\n')).join('\n');
        throw new Error(`failed to transpile ${relativePath}\n${details}`);
      }
      const output = path.join(temporaryRoot, `${String(index + 1).padStart(2, '0')}-${path.basename(relativePath).replace(/\.[^.]+$/, '.js')}`);
      fs.writeFileSync(output, result.outputText, 'utf8');
      return output;
    });
    const result = spawnSync(process.execPath, ['--test', ...compiledTests], {
      cwd: rootDir,
      encoding: 'utf8',
      windowsHide: true,
    });
    return {
      exitCode: typeof result.status === 'number' ? result.status : null,
      signal: result.signal || null,
      error: result.error ? result.error.message : null,
      stdout: String(result.stdout || ''),
      stderr: String(result.stderr || ''),
    };
  } finally {
    fs.rmSync(temporaryRoot, { recursive: true, force: true });
  }
}

function statusPaths(status) {
  return status.entries.map((entry) => entry.slice(3).split(' -> ').at(-1)).filter(Boolean).map(toPosix).sort();
}

async function runCommand(spec, rootDir, runDir, sequence) {
  const actual = addFailureSignatures(await execute(spec, rootDir), rootDir);
  const evaluation = evaluateCommand(spec, actual);
  const base = `${String(sequence).padStart(2, '0')}-${spec.id}`;
  const stdout = sanitizeLog(actual.stdout, rootDir);
  const stderr = sanitizeLog(actual.stderr, rootDir);
  const stdoutLog = `${base}.stdout.log`;
  const stderrLog = `${base}.stderr.log`;
  fs.writeFileSync(path.join(runDir, stdoutLog), stdout, 'utf8');
  fs.writeFileSync(path.join(runDir, stderrLog), stderr, 'utf8');
  return {
    id: spec.id,
    description: spec.description,
    command: spec.command,
    expectedOutcome: spec.outcome,
    exitCode: actual.exitCode,
    signal: actual.signal,
    error: actual.error ? sanitizeLog(actual.error, rootDir) : null,
    startedAt: actual.startedAt,
    endedAt: actual.endedAt,
    failureSignatures: actual.failureSignatures,
    evaluation,
    stdoutLog,
    stdoutBytes: Buffer.byteLength(stdout),
    stdoutSha256: sha256(stdout),
    stderrLog,
    stderrBytes: Buffer.byteLength(stderr),
    stderrSha256: sha256(stderr),
  };
}

async function runVerification(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  const expectations = JSON.parse(fs.readFileSync(path.join(rootDir, EXPECTATIONS), 'utf8'));
  const runDir = path.join(rootDir, EVIDENCE_ROOT, options.runId || stableRunId());
  if (fs.existsSync(runDir)) throw new Error(`refusing to overwrite evidence directory: ${runDir}`);
  fs.mkdirSync(runDir, { recursive: true });

  const startStatus = gitStatus(rootDir);
  const summary = {
    schemaVersion: 1,
    runId: path.basename(runDir),
    startedAt: new Date().toISOString(),
    status: 'running',
    environment: {
      platform: process.platform,
      arch: process.arch,
      node: process.version,
      cwd: '<repository-root>',
      environmentValuesCaptured: false,
      electronUserDataAccessed: false,
    },
    sourceHashesStart: SOURCE_FILES.map((file) => fileRecord(rootDir, file)),
    testControlViolations: testControlViolations(rootDir),
    commandResults: [],
  };

  const p00 = npmSpec('p00-gate', ['run', 'migration:p00:verify'], {
    description: 'P00 hard gate; a non-zero exit blocks every P01 command.',
    outcome: 'success',
    allowedFailureSignatures: [],
  });
  summary.commandResults.push(await runCommand(p00, rootDir, runDir, 1));
  if (!summary.commandResults[0].evaluation.accepted) {
    summary.status = 'blocked';
    summary.blocked = 'P00 gate failed; no P01 implementation verification command was started.';
  } else {
    const specializedTests = [
      'scripts/migration-p01/check-renderer-boundary.test.js',
      'scripts/migration-baseline/inventory-ipc.test.js',
      'src/renderer/lib/api/ipcClient.test.js',
      'src/renderer/lib/desktop/ipcBridge.test.js',
      'src/renderer/lib/api/subscriptions.test.js',
      'src/renderer/types.syntax.test.js',
      'src/renderer/pages/WorkspacePage.projectId.test.js',
    ];
    const commands = [
      nodeSpec('inventory', ['scripts/migration-baseline/inventory-ipc.js'], 'P00 capability and IPC inventory drift guard.'),
      nodeSpec('renderer-boundary', ['scripts/migration-p01/check-renderer-boundary.js'], 'Renderer direct-access allowlist guard.'),
      nodeSpec('p01-specialized', ['--test', ...specializedTests], 'P01 client, desktop bridge, subscription, contract and renderer tests.'),
      nodeSpec('renderer-ts-tests', ['scripts/migration-p01/verify.js', 'run-ts-tests'], 'Transpiled renderer TypeScript source tests in an isolated system temporary directory.'),
      npmSpec('check', ['run', 'check'], expectations.commands.check),
      npmSpec('test', ['test'], expectations.commands.test),
      npmSpec('build', ['run', 'build'], expectations.commands.build),
    ];
    for (const command of commands) {
      summary.commandResults.push(await runCommand(command, rootDir, runDir, summary.commandResults.length + 1));
    }
    summary.status = 'completed';
  }

  const endStatus = gitStatus(rootDir);
  summary.endedAt = new Date().toISOString();
  summary.sourceHashesEnd = SOURCE_FILES.map((file) => fileRecord(rootDir, file));
  summary.sourceHashesStable = JSON.stringify(summary.sourceHashesStart) === JSON.stringify(summary.sourceHashesEnd);
  summary.gitStatusStart = startStatus;
  summary.gitStatusEnd = endStatus;
  summary.affectedFiles = [...new Set([...statusPaths(startStatus), ...statusPaths(endStatus)])].sort();
  summary.remainingRisks = summary.commandResults
    .filter((item) => !item.evaluation.accepted)
    .map((item) => `${item.id}: ${item.evaluation.reason}`);
  if (summary.testControlViolations.length) {
    summary.remainingRisks.push(`forbidden skip/only controls: ${summary.testControlViolations.join(', ')}`);
  }
  if (!summary.sourceHashesStable) summary.remainingRisks.push('P01 verification sources changed while commands were running.');
  if (startStatus.exitCode !== 0 || endStatus.exitCode !== 0) summary.remainingRisks.push('git status exit code was unavailable or non-zero.');
  summary.ok = summary.status === 'completed'
    && startStatus.exitCode === 0
    && endStatus.exitCode === 0
    && summary.sourceHashesStable
    && summary.testControlViolations.length === 0
    && summary.commandResults.every((item) => item.evaluation.accepted);
  writeJson(path.join(runDir, 'summary.json'), summary);
  const artifacts = fs.readdirSync(runDir).sort().map((name) => fileRecord(runDir, name));
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
  if (argv.length !== 1 || !['verify', 'run-ts-tests'].includes(argv[0])) {
    throw new Error('usage: node scripts/migration-p01/verify.js <verify|run-ts-tests>');
  }
  return { mode: argv[0] };
}

if (require.main === module) {
  try {
    const { mode } = parseArgs(process.argv.slice(2));
    if (mode === 'run-ts-tests') {
      const result = runTypeScriptTests();
      process.stdout.write(result.stdout);
      process.stderr.write(result.stderr || result.error || '');
      process.exitCode = result.exitCode === 0 ? 0 : 1;
    } else runVerification().then(({ runDir, summary }) => {
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
  addFailureSignatures,
  parseArgs,
  runVerification,
  runTypeScriptTests,
  sanitizeLog,
  testControlViolations,
};
