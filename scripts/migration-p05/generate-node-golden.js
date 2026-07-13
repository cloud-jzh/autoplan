'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const { AppDatabase, nowIso } = require('../../src/database');
const { createIntakeService } = require('../../src/intakeService');
const { LoopService } = require('../../src/loopService');
const { saveMcpSettings } = require('../../src/mcpConfig');
const { buildContract, sha256, stableJson } = require('./inventory-write-contract');

const ROOT = path.resolve(__dirname, '../..');
const OUTPUT_DIR = 'fixtures/migration/p05';
const GOLDEN_NAME = 'node-mutations.golden.json';
const MANIFEST_NAME = 'manifest.json';
const GENERATOR_VERSION = 'p05-node-mutation-golden-v1';
const NORMALIZATION_VERSION = 'p05-node-mutation-normalization-v1';
const FIXED_EPOCH_MS = Date.parse('2026-07-11T00:00:00.000Z');
const TEMP_PREFIX = 'autoplan-p05-node-';
const REDACTED = '<redacted>';
const REDACTED_COMMAND = '<redacted-command>';
const REDACTED_ENV = '<redacted-env-vars>';
const FIXTURE_ROOT = '<fixture-root>';

class BlockedError extends Error {
  constructor(reason) {
    super(`blocked: ${reason}`);
    this.name = 'BlockedError';
    this.reason = reason;
  }
}

function installDeterministicRuntime() {
  const NativeDate = global.Date;
  const originalRandomBytes = crypto.randomBytes;
  let tick = 0;
  let randomCounter = 0;
  class DeterministicDate extends NativeDate {
    constructor(...args) {
      if (args.length) super(...args);
      else super(FIXED_EPOCH_MS + tick++ * 1000);
    }

    static now() {
      return FIXED_EPOCH_MS + tick++ * 1000;
    }
  }
  DeterministicDate.parse = NativeDate.parse;
  DeterministicDate.UTC = NativeDate.UTC;
  global.Date = DeterministicDate;
  crypto.randomBytes = (size) => {
    const buffer = Buffer.alloc(size);
    for (let index = 0; index < size; index += 1) buffer[index] = (randomCounter + index + 17) % 256;
    randomCounter += size;
    return buffer;
  };
  return () => {
    global.Date = NativeDate;
    crypto.randomBytes = originalRandomBytes;
  };
}

function assertApprovedRoot(rootDir, outputDir, options = {}) {
  const expected = path.resolve(rootDir, OUTPUT_DIR);
  const actual = path.resolve(outputDir);
  if (actual !== expected && !options.allowExternalOutput) throw new BlockedError('golden_output_not_approved');
}

function assertP04Prerequisites(rootDir) {
  const required = [
    'docs/migration/p04/schema-inventory.json',
    'fixtures/migration/p04/manifest.json',
    'backend/migrations/0001_schema_v1.sql',
  ];
  const hashes = {};
  for (const relative of required) {
    const absolute = path.join(rootDir, relative);
    if (!fs.existsSync(absolute) || !fs.statSync(absolute).isFile()) throw new BlockedError(`p04_prerequisite_missing:${relative}`);
    hashes[relative] = sha256(fs.readFileSync(absolute));
  }
  const p04Manifest = JSON.parse(fs.readFileSync(path.join(rootDir, 'fixtures/migration/p04/manifest.json'), 'utf8'));
  const approvedTemporaryFixture = p04Manifest.fixture_set === 'autoplan-p04-copy-migration'
    && p04Manifest.artifact_policy?.location === 'caller-provided system temporary directory'
    && p04Manifest.artifact_policy?.overwrite === false;
  if (!approvedTemporaryFixture) {
    throw new BlockedError('p04_fixture_not_synthetic');
  }
  return hashes;
}

function assertContractFrozen(rootDir) {
  const contractPath = path.join(rootDir, 'docs/migration/p05/write-contract.json');
  if (!fs.existsSync(contractPath)) throw new BlockedError('write_contract_missing');
  const bytes = fs.readFileSync(contractPath);
  const committed = JSON.parse(bytes.toString('utf8'));
  const current = buildContract(rootDir);
  if (stableJson(committed) !== stableJson(current)) throw new BlockedError('write_contract_drift');
  return { contract: committed, sha256: sha256(bytes) };
}

function projectIdFrom(input, loop) {
  const projectId = Number(input?.projectId || input?.id || 0);
  if (!projectId || !loop.project(projectId)) throw compatibilityError('PROJECT_NOT_FOUND', '项目不存在');
  return projectId;
}

function compatibilityError(code, message) {
  const error = new Error(message);
  error.code = code;
  return error;
}

function updateProject(db, loop, input = {}) {
  const projectId = projectIdFrom(input, loop);
  const current = loop.project(projectId);
  if (loop.hasRuntimeConfigInput(input)) loop.configure(projectId, input);
  db.run(
    `UPDATE projects
     SET name = ?, workspace_path = ?, description = ?, updated_at = ?
     WHERE id = ?`,
    [
      String(input.name || current.name).trim(),
      input.workspacePath ?? current.workspace_path,
      input.description ?? current.description,
      nowIso(),
      projectId,
    ],
  );
  return loop.snapshot(projectId);
}

function configureProject(db, loop, input = {}) {
  const projectId = projectIdFrom(input, loop);
  saveMcpSettings(db, input);
  loop.configure(projectId, input);
  return loop.snapshot(projectId);
}

function deleteProject(db, loop, input = {}) {
  const projectId = projectIdFrom(input, loop);
  const state = loop.status(projectId);
  if (state && state.running) throw compatibilityError('PROJECT_RUNNING', '请先停止该项目循环再删除');
  loop.stop(projectId);
  const planIds = db.all('SELECT id FROM plans WHERE project_id = ?', [projectId]).map((row) => row.id);
  for (const table of ['requirements', 'feedback', 'attachments', 'events', 'scan_files']) {
    db.run(`DELETE FROM ${table} WHERE project_id = ?`, [projectId]);
  }
  if (planIds.length) {
    const placeholders = planIds.map(() => '?').join(',');
    db.run(`DELETE FROM plan_tasks WHERE plan_id IN (${placeholders})`, planIds);
  }
  db.run('DELETE FROM plans WHERE project_id = ?', [projectId]);
  db.run('DELETE FROM project_states WHERE project_id = ?', [projectId]);
  db.run('DELETE FROM projects WHERE id = ?', [projectId]);
  return loop.snapshot(null);
}

function captureFailure(action) {
  try {
    action();
  } catch (error) {
    return {
      ok: false,
      error: {
        code: error.code || 'LEGACY_NODE_ERROR',
        message: String(error.message || ''),
      },
    };
  }
  throw new Error('expected mutation failure');
}

function captureSuccess(snapshot) {
  return { ok: true, snapshot };
}

function mutationInputs(fixtureRoot) {
  const workspaceAlpha = path.join(fixtureRoot, 'workspace-alpha');
  const workspaceBeta = path.join(fixtureRoot, 'workspace-beta');
  return {
    create: {
      name: '  Alpha Project  ',
      workspacePath: workspaceAlpha,
      description: 'Synthetic project',
      intervalSeconds: 7,
      validationCommand: 'synthetic validation command',
      projectPrompt: 'Synthetic prompt',
      agentCliProvider: 'CODEX',
      codexReasoningEffort: 'HIGH',
    },
    update: {
      name: '  Updated Alpha  ',
      workspacePath: workspaceBeta,
      description: null,
    },
    configure: {
      intervalSeconds: 0,
      validation_command: 'synthetic replacement command',
      project_prompt: '',
      agent_cli_provider: 'claude',
      agent_cli_command: 'synthetic-agent',
      planGenerationStrategy: 'external-cli-markdown',
      planGenerationProvider: 'codex',
      planGenerationCommand: 'synthetic-generator',
      planExecutionProvider: 'claude',
      planExecutionCommand: 'synthetic-executor',
      planExecutionClaudeAuthToken: 'non-working-fixture-token-0000',
      envVars: [{ name: ' SYNTHETIC_NAME ', value: 'non-secret-fixture-value' }, { name: ' ', value: 'discarded' }],
      mcpEnabled: false,
      mcpPort: 43999,
      mcpAuthToken: 'non-working-mcp-fixture-0000',
    },
  };
}

function relationCounts(db, projectId) {
  const directTables = ['requirements', 'feedback', 'attachments', 'plans', 'events', 'scan_files', 'scripts', 'executors', 'conversations', 'chat_messages', 'intake_plan_links', 'project_states'];
  return Object.fromEntries(directTables.map((table) => {
    const count = Number(db.get(`SELECT COUNT(*) AS count FROM ${table} WHERE project_id = ?`, [projectId])?.count || 0);
    return [table, count];
  }));
}

async function executeScenarios(rootDir, temporaryRoot) {
  const databasePath = path.join(temporaryRoot, 'sanitized-p05-node.sqlite');
  const inputs = mutationInputs(temporaryRoot);
  fs.mkdirSync(path.join(temporaryRoot, 'workspace-alpha'), { recursive: true });
  fs.mkdirSync(path.join(temporaryRoot, 'workspace-beta'), { recursive: true });
  const db = new AppDatabase(databasePath);
  let closed = false;
  try {
    await db.init();
    db.setSetting('mcp.authToken', 'non-working-initial-fixture-0000');
    const loop = new LoopService(db);
    const intake = createIntakeService({ db, loop, attachmentsRoot: path.join(temporaryRoot, 'attachments') });
    const databaseBeforeSha256 = sha256(fs.readFileSync(databasePath));
    const scenarios = [];

    const created = intake.createProject(inputs.create);
    const projectId = Number(created.activeProjectId);
    scenarios.push({ id: 'create', request: inputs.create, response: captureSuccess(created) });

    const duplicateCreated = intake.createProject(inputs.create);
    const duplicateId = Number(duplicateCreated.activeProjectId);
    scenarios.push({ id: 'duplicate-create', request: inputs.create, response: captureSuccess(duplicateCreated), observation: 'legacy Node creates a distinct project' });
    scenarios.push({ id: 'delete-duplicate-create', request: { projectId: duplicateId }, response: captureSuccess(deleteProject(db, loop, { projectId: duplicateId })) });

    const updateRequest = { projectId, ...inputs.update };
    scenarios.push({ id: 'update', request: updateRequest, response: captureSuccess(updateProject(db, loop, updateRequest)) });

    const configureRequest = { projectId, ...inputs.configure };
    scenarios.push({ id: 'configure', request: configureRequest, response: captureSuccess(configureProject(db, loop, configureRequest)) });
    scenarios.push({ id: 'duplicate-configure', request: configureRequest, response: captureSuccess(configureProject(db, loop, configureRequest)), observation: 'legacy Node writes updated_at again' });

    scenarios.push({ id: 'missing-update', request: { projectId: 999999, name: 'Missing' }, response: captureFailure(() => updateProject(db, loop, { projectId: 999999, name: 'Missing' })) });
    scenarios.push({ id: 'missing-delete', request: { projectId: 999999 }, response: captureFailure(() => deleteProject(db, loop, { projectId: 999999 })) });

    loop.runtime(projectId).running = true;
    scenarios.push({ id: 'running-delete', request: { projectId }, response: captureFailure(() => deleteProject(db, loop, { projectId })) });
    loop.runtime(projectId).running = false;

    const beforeDeleteRelations = relationCounts(db, projectId);
    scenarios.push({ id: 'delete', request: { projectId }, preconditionRelationCounts: beforeDeleteRelations, response: captureSuccess(deleteProject(db, loop, { projectId })) });
    scenarios.push({ id: 'duplicate-delete', request: { projectId }, response: captureFailure(() => deleteProject(db, loop, { projectId })), observation: 'without transport idempotency replay, legacy Node returns missing' });

    const databaseAfterSha256 = sha256(fs.readFileSync(databasePath));
    db.close();
    closed = true;
    return {
      databaseBeforeSha256,
      databaseAfterSha256,
      fixtureRecipe: inputs,
      raw: {
        schemaVersion: 1,
        version: GENERATOR_VERSION,
        scenarios,
        handoff: { sqlJsClosed: true, databaseOwnerReleased: true },
      },
    };
  } finally {
    if (!closed) db.close();
  }
}

function createNormalizationContext(value, fixtureRoot) {
  const projectIds = new Set();
  collectProjectIds(value, projectIds, []);
  const projectIdMap = new Map([...projectIds].filter((id) => id !== 999999).sort((a, b) => a - b).map((id, index) => [id, index + 1]));
  return { fixtureRoot: path.resolve(fixtureRoot), projectIdMap, times: new Map() };
}

function collectProjectIds(value, ids, trail) {
  if (Array.isArray(value)) {
    value.forEach((item, index) => collectProjectIds(item, ids, [...trail, index]));
    return;
  }
  if (!value || typeof value !== 'object') return;
  for (const [key, child] of Object.entries(value)) {
    if (isProjectIdField(key, trail) && Number.isSafeInteger(Number(child)) && Number(child) > 0) ids.add(Number(child));
    collectProjectIds(child, ids, [...trail, key]);
  }
}

function isProjectIdField(key, trail) {
  if (['projectId', 'project_id', 'activeProjectId'].includes(key)) return true;
  if (key !== 'id') return false;
  const parent = String(trail.at(-1) ?? '');
  const grandparent = String(trail.at(-2) ?? '');
  return parent === 'activeProject' || parent === 'projects' || grandparent === 'projects';
}

function isTimeField(key) {
  return /(?:^|_)(?:created|updated|started|finished|accepted|scanned|modified|checked|run)_at$/i.test(key)
    || /(?:created|updated|started|finished|accepted|scanned|modified|checked|run)At$/.test(key);
}

function isCommandField(key) {
  return key === 'validation_command' || key === 'agent_cli_command' || key === 'command'
    || key.endsWith('_command') || key.endsWith('Command');
}

function isSecretField(key) {
  const normalized = key.toLowerCase();
  return normalized === 'env_vars' || normalized === 'envvars' || normalized === 'secret_refs'
    || normalized.includes('authtoken') || normalized.includes('auth_token')
    || normalized.includes('api_key') || normalized === 'apikey';
}

function normalizeGolden(value, options = {}) {
  const context = options.context || createNormalizationContext(value, options.fixtureRoot);
  const normalized = normalizeValue(value, context, []);
  assertSanitized(normalized, options);
  return {
    value: normalized,
    metadata: {
      version: NORMALIZATION_VERSION,
      placeholders: { fixtureRoot: FIXTURE_ROOT, timestamp: '<time-N>', redacted: REDACTED, command: REDACTED_COMMAND, envVars: REDACTED_ENV },
      idMaps: { projects: Object.fromEntries([...context.projectIdMap].map(([source, target]) => [String(source), target])) },
    },
  };
}

function normalizeValue(value, context, trail) {
  if (value === null || value === undefined || typeof value === 'boolean') return value;
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) throw new Error(`non-finite golden value at ${pointer(trail)}`);
    return value;
  }
  if (typeof value === 'string') return normalizeString(value, context, trail);
  if (Array.isArray(value)) return value.map((item, index) => normalizeValue(item, context, [...trail, index]));
  if (typeof value !== 'object') throw new Error(`unsupported golden value at ${pointer(trail)}`);
  const output = {};
  for (const key of Object.keys(value)) {
    const child = value[key];
    const childTrail = [...trail, key];
    if (isProjectIdField(key, trail) && child !== null && Number(child) !== 999999) {
      const mapped = context.projectIdMap.get(Number(child));
      if (!mapped) throw new Error(`unmapped project id at ${pointer(childTrail)}`);
      output[key] = mapped;
    } else if ((key === 'env_vars' || key === 'envVars') && child) {
      output[key] = REDACTED_ENV;
    } else if (isSecretField(key) && child) {
      output[key] = REDACTED;
    } else if (isCommandField(key) && child) {
      output[key] = REDACTED_COMMAND;
    } else if (key === 'authTokenMasked' && child) {
      output[key] = REDACTED;
    } else {
      output[key] = normalizeValue(child, context, childTrail);
    }
  }
  return output;
}

function normalizeString(value, context, trail) {
  const key = String(trail.at(-1) || '');
  if (isTimeField(key) && value) {
    const time = new Date(value);
    if (Number.isNaN(time.valueOf())) throw new Error(`invalid UTC timestamp at ${pointer(trail)}`);
    const canonical = time.toISOString();
    if (!context.times.has(canonical)) context.times.set(canonical, `<time-${context.times.size + 1}>`);
    return context.times.get(canonical);
  }
  const fixtureRoot = context.fixtureRoot.replace(/\\/g, '/');
  const normalized = value.replace(/\\/g, '/');
  if (normalized.toLowerCase() === fixtureRoot.toLowerCase()) return FIXTURE_ROOT;
  if (normalized.toLowerCase().startsWith(`${fixtureRoot.toLowerCase()}/`)) return `${FIXTURE_ROOT}${normalized.slice(fixtureRoot.length)}`;
  return value;
}

function assertSanitized(value, options = {}) {
  const encoded = JSON.stringify(value);
  const normalized = encoded.replace(/\\/g, '/').toLowerCase();
  const forbiddenPaths = [options.fixtureRoot, os.homedir(), process.env.APPDATA, process.env.LOCALAPPDATA]
    .filter(Boolean)
    .map((item) => path.resolve(item).replace(/\\/g, '/').toLowerCase());
  if (forbiddenPaths.some((item) => normalized.includes(item))) throw new BlockedError('golden_contains_absolute_path');
  if (/non-working-(?:fixture|mcp|initial)-token/i.test(encoded) || /non-secret-fixture-value/i.test(encoded)) {
    throw new BlockedError('golden_contains_sensitive_fixture_value');
  }
  if (/\b(?:sk-|ghp_|github_pat_)[A-Za-z0-9_-]{8,}\b/.test(encoded)) throw new BlockedError('golden_contains_credential_shape');
  return true;
}

function pointer(trail) {
  return trail.length ? `/${trail.map((part) => String(part).replace(/~/g, '~0').replace(/\//g, '~1')).join('/')}` : '/';
}

function safeRemoveTemporaryRoot(temporaryRoot) {
  const temp = fs.realpathSync(os.tmpdir());
  const resolved = path.resolve(temporaryRoot);
  const relative = path.relative(temp, resolved);
  if (!relative || relative.startsWith('..') || path.isAbsolute(relative) || !path.basename(resolved).startsWith(TEMP_PREFIX)) {
    throw new BlockedError('temporary_cleanup_boundary_failed');
  }
  fs.rmSync(resolved, { recursive: true, force: true });
}

function writeArtifacts(outputDir, artifacts) {
  fs.mkdirSync(outputDir, { recursive: true });
  const staging = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p05-output-'));
  try {
    for (const [name, content] of Object.entries(artifacts)) fs.writeFileSync(path.join(staging, name), content, 'utf8');
    for (const name of Object.keys(artifacts)) fs.copyFileSync(path.join(staging, name), path.join(outputDir, name));
  } finally {
    fs.rmSync(staging, { recursive: true, force: true });
  }
}

async function buildGoldenBundle(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  const p04Hashes = assertP04Prerequisites(rootDir);
  const writeContract = assertContractFrozen(rootDir);
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), TEMP_PREFIX));
  const restoreRuntime = installDeterministicRuntime();
  try {
    const execution = await executeScenarios(rootDir, temporaryRoot);
    const normalized = normalizeGolden(execution.raw, { fixtureRoot: temporaryRoot });
    const golden = {
      ...normalized.value,
      normalization: normalized.metadata,
    };
    const goldenContent = stableJson(golden);
    const generatorPath = path.join(rootDir, 'scripts/migration-p05/generate-node-golden.js');
    const fixtureRecipe = normalizeGolden(execution.fixtureRecipe, { fixtureRoot: temporaryRoot }).value;
    const manifest = {
      schemaVersion: 1,
      version: GENERATOR_VERSION,
      generator: 'scripts/migration-p05/generate-node-golden.js',
      generatorSha256: sha256(fs.readFileSync(generatorPath)),
      source: {
        syntheticOnly: true,
        databaseCopy: 'generator-owned OS temporary database; never Electron userData or autoplan.sqlite',
        fixtureRecipeSha256: sha256(stableJson(fixtureRecipe)),
        databaseBeforeSha256: execution.databaseBeforeSha256,
        databaseAfterSha256: execution.databaseAfterSha256,
      },
      prerequisites: {
        writeContract: { path: 'docs/migration/p05/write-contract.json', sha256: writeContract.sha256 },
        p04: Object.entries(p04Hashes).map(([artifactPath, hash]) => ({ path: artifactPath, sha256: hash })),
      },
      normalization: normalized.metadata,
      scenarios: golden.scenarios.map((scenario) => scenario.id),
      writerHandoff: {
        sequence: ['Node opens generator-owned copy', 'Node executes serial mutations', 'Node closes sql.js and owner lock', 'Go may open a separately reset copy later'],
        sameCopyConcurrentWritersAllowed: false,
        nodeClosedBeforeArtifactCommit: true,
      },
      artifacts: [{ name: GOLDEN_NAME, sha256: sha256(goldenContent) }],
    };
    return {
      artifacts: { [GOLDEN_NAME]: goldenContent, [MANIFEST_NAME]: stableJson(manifest) },
      golden,
      manifest,
    };
  } finally {
    restoreRuntime();
    safeRemoveTemporaryRoot(temporaryRoot);
  }
}

async function generateNodeGolden(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  const outputDir = path.resolve(options.outputDir || path.join(rootDir, OUTPUT_DIR));
  assertApprovedRoot(rootDir, outputDir, options);
  const bundle = await buildGoldenBundle({ rootDir });
  writeArtifacts(outputDir, bundle.artifacts);
  return bundle;
}

function parseArgs(argv) {
  if (argv.length) throw new Error(`unknown argument: ${argv[0]}`);
  return {};
}

if (require.main === module) {
  generateNodeGolden(parseArgs(process.argv.slice(2)))
    .then((bundle) => process.stdout.write(`${JSON.stringify({ ok: true, artifacts: Object.keys(bundle.artifacts) })}\n`))
    .catch((error) => {
      process.stderr.write(`${error instanceof BlockedError ? error.message : 'golden generation failed safely'}\n`);
      process.exitCode = 1;
    });
}

module.exports = {
  BlockedError,
  GENERATOR_VERSION,
  GOLDEN_NAME,
  NORMALIZATION_VERSION,
  buildGoldenBundle,
  captureFailure,
  deleteProject,
  generateNodeGolden,
  normalizeGolden,
  parseArgs,
  projectIdFrom,
  safeRemoveTemporaryRoot,
  updateProject,
};
