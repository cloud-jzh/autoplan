'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { scanSensitiveText } = require('./check-safety');

const ROOT = path.resolve(__dirname, '..', '..');
const EVIDENCE_ROOT = path.join(ROOT, 'docs', 'migration', 'p15', 'evidence', 'runs');
const MAX_INPUT_BYTES = 1024 * 1024;
const PLATFORMS = new Set(['windows', 'macos', 'linux', 'cross-platform']);
const STATUSES = new Set(['passed', 'failed', 'blocked']);

function sha256(value) { return crypto.createHash('sha256').update(value).digest('hex'); }
function blocked(code, extra = {}) { return { ok: false, status: 'blocked', code, ...extra }; }

function safeRelative(value) {
  return typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9._/-]{0,239}$/.test(value) &&
    !value.split('/').includes('..') && !value.includes('\\');
}

function safeText(value, maximum = 512) {
  return typeof value === 'string' && value.length > 0 && value.length <= maximum &&
    !scanSensitiveText(value).length && !/(?:[A-Za-z]:[\\/]|\/(?:Users|home|private|var|mnt)\/)/i.test(value);
}

function validTimestamp(value) {
  return typeof value === 'string' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{3})?Z$/.test(value) &&
    Number.isFinite(Date.parse(value));
}

function readInput(file) {
  const resolved = path.resolve(file || '');
  const info = fs.lstatSync(resolved, { throwIfNoEntry: false });
  if (!path.isAbsolute(file || '') || !info?.isFile() || info.isSymbolicLink() || info.size <= 0 || info.size > MAX_INPUT_BYTES) return null;
  let value;
  try { value = JSON.parse(fs.readFileSync(resolved, 'utf8')); } catch { return null; }
  return value;
}

function validateArtifact(value) {
  return Boolean(value && typeof value === 'object' && safeRelative(value.path) &&
    Number.isSafeInteger(value.bytes) && value.bytes >= 0 && /^[a-f0-9]{64}$/.test(value.sha256 || ''));
}

function validateCommand(value) {
  if (!value || typeof value !== 'object' || !/^[a-z][a-z0-9_-]{1,95}$/.test(value.id || '') ||
      !PLATFORMS.has(value.platform) || !STATUSES.has(value.status) || !Number.isInteger(value.exit_code) ||
      !validTimestamp(value.started_at) || !validTimestamp(value.ended_at) || value.ended_at < value.started_at ||
      !safeText(value.command, 512) || !Array.isArray(value.artifacts) || value.artifacts.length > 32 ||
      value.artifacts.some((artifact) => !validateArtifact(artifact))) return false;
  return value.status !== 'passed' || value.exit_code === 0;
}

function validateCollection(value) {
  if (!value || value.schema_version !== 1 || value.kind !== 'p15-evidence-collection-input' ||
      !/^[A-Za-z0-9._-]{1,160}$/.test(value.run_id || '') ||
      !/^(?:[a-f0-9]{40}|v[0-9A-Za-z._-]{1,120})$/.test(value.source_revision || '') ||
      !value.fixture || value.fixture.authorized_copy !== true || value.fixture.real_userdata !== false ||
      !/^[A-Za-z0-9._-]{1,120}$/.test(value.fixture.id || '') || !Array.isArray(value.commands) ||
      value.commands.length === 0 || value.commands.length > 128 || value.commands.some((command) => !validateCommand(command))) {
    return blocked('evidence_collection_input_invalid');
  }
  const ids = new Set();
  for (const command of value.commands) {
    if (ids.has(command.id)) return blocked('evidence_collection_command_duplicate');
    ids.add(command.id);
  }
  const hasFailure = value.commands.some((command) => command.status !== 'passed');
  return {
    ok: true,
    status: hasFailure ? 'blocked' : 'completed',
    code: hasFailure ? 'evidence_collection_contains_nonpassing_command' : 'evidence_collection_valid',
  };
}

function outputDirectory(runId) {
  const target = path.resolve(EVIDENCE_ROOT, runId);
  const relative = path.relative(EVIDENCE_ROOT, target);
  if (!relative || relative.startsWith('..') || path.isAbsolute(relative)) throw new Error('output_directory_invalid');
  return target;
}

function writeJSON(file, value) {
  fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`, { encoding: 'utf8', mode: 0o600, flag: 'wx' });
}

function collectEvidence(options = {}) {
  const input = readInput(options.input);
  const validation = validateCollection(input);
  if (!validation.ok) return validation;
  const runDir = outputDirectory(input.run_id);
  if (fs.existsSync(runDir)) return blocked('evidence_collection_run_exists');
  fs.mkdirSync(runDir, { recursive: true, mode: 0o700 });
  const commands = input.commands.map((command) => ({
    id: command.id, platform: command.platform, status: command.status, exit_code: command.exit_code,
    command: command.command, started_at: command.started_at, ended_at: command.ended_at, artifacts: command.artifacts,
  }));
  const summary = {
    schema_version: 1,
    kind: 'p15-evidence-run-summary',
    run_id: input.run_id,
    status: validation.status,
    source_revision: input.source_revision,
    fixture: { id: input.fixture.id, authorized_copy: true, real_userdata: false },
    command_results: commands,
    publish_policy: 'never',
  };
  writeJSON(path.join(runDir, 'summary.json'), summary);
  const artifacts = [{
    path: 'summary.json', bytes: Buffer.byteLength(`${JSON.stringify(summary, null, 2)}\n`),
    sha256: sha256(`${JSON.stringify(summary, null, 2)}\n`),
  }];
  writeJSON(path.join(runDir, 'evidence-manifest.json'), {
    schema_version: 1, kind: 'p15-evidence-run-manifest', run_id: input.run_id,
    immutable_run_directory: true, artifacts,
  });
  return { ok: validation.status === 'completed', status: validation.status, code: validation.code, run_id: input.run_id };
}

function parseArgs(argv) {
  if (argv.length !== 3 || argv[0] !== 'collect' || argv[1] !== '--input' || !argv[2]) throw new Error('arguments_invalid');
  return { input: argv[2] };
}

if (require.main === module) {
  try {
    const result = collectEvidence(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify(result)}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch {
    process.stdout.write('{"status":"blocked","code":"evidence_collection_arguments_or_write_invalid"}\n');
    process.exitCode = 2;
  }
}

module.exports = { EVIDENCE_ROOT, collectEvidence, parseArgs, safeRelative, validateCollection, validateCommand, validTimestamp };
