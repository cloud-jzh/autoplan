'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const { LoopService } = require('../../src/loopService');
const {
  createEmptyFixture,
  generateFixtures,
  isInside,
  looksLikeUserData,
} = require('../migration-baseline/fixtures');
const {
  NORMALIZATION_VERSION,
  assertSanitizedContract,
  normalizeContracts,
  stableJson,
} = require('./normalize-contract');

const ROOT = path.resolve(__dirname, '../..');
const OUTPUT_ROOT = 'fixtures/migration/p03';
const RECIPE_PATH = 'fixtures/migration/p00/manifest.json';
const NORMALIZER_PATH = 'scripts/migration-p03/normalize-contract.js';
const GENERATOR_VERSION = 'p03-node-golden-v1';
const FIXED_TIMES = Object.freeze({
  created: '2026-01-02T03:04:05.000Z',
  alphaUpdated: '2026-01-02T03:04:07.000Z',
  latestUpdated: '2026-01-02T03:04:08.000Z',
});
const GOLDEN_FILES = Object.freeze([
  'projects.golden.json',
  'snapshot-empty.golden.json',
  'snapshot-project.golden.json',
]);
const EVIDENCE_STAGES = Object.freeze({
  p00: 'docs/migration/p00/evidence/runs',
  p01: 'docs/migration/p01/evidence/runs',
  p02: 'docs/migration/p02/evidence/runs',
});

class BlockedError extends Error {
  constructor(reason) {
    super(`blocked: ${reason}`);
    this.name = 'BlockedError';
    this.code = 'P03_BLOCKED';
    this.reason = reason;
  }
}

function sha256(value) {
  return crypto.createHash('sha256').update(value).digest('hex');
}

function sha256File(file) {
  return sha256(fs.readFileSync(file));
}

function verifyPrerequisites(rootDir = ROOT) {
  const stages = {};
  for (const [stage, relativeRoot] of Object.entries(EVIDENCE_STAGES)) {
    stages[stage] = verifyEvidenceStage(rootDir, stage, relativeRoot);
  }
  return stages;
}

function verifyEvidenceStage(rootDir, stage, relativeRoot) {
  const evidenceRoot = path.join(rootDir, relativeRoot);
  let names;
  try {
    names = fs.readdirSync(evidenceRoot, { withFileTypes: true })
      .filter((entry) => entry.isDirectory())
      .map((entry) => entry.name)
      .sort()
      .reverse();
  } catch {
    throw new BlockedError(`${stage}_evidence_missing`);
  }
  if (!names.length) throw new BlockedError(`${stage}_evidence_missing`);
  const runDir = path.join(evidenceRoot, names[0]);
  const summary = readJsonOrBlock(path.join(runDir, 'summary.json'), `${stage}_evidence_invalid`);
  const manifest = readJsonOrBlock(path.join(runDir, 'evidence-manifest.json'), `${stage}_evidence_invalid`);
  if (summary.schemaVersion !== 1 || manifest.schemaVersion !== 1 || manifest.immutableRunDirectory !== true) {
    throw new BlockedError(`${stage}_evidence_invalid`);
  }
  verifyEvidenceManifest(runDir, manifest, stage);
  if (!stageCompleted(stage, summary)) throw new BlockedError(`${stage}_gate_failed`);
  if (!(summary.commandResults || []).every((result) => result?.evaluation?.accepted === true)) {
    throw new BlockedError(`${stage}_command_failed`);
  }
  if (stage === 'p01') requireAccepted(summary, ['p00-gate', 'inventory', 'renderer-boundary'], stage);
  if (stage === 'p02') requireAccepted(summary, ['p00-gate', 'p01-gate'], stage);
  const frozenSourcesSha256 = verifyFrozenSources(rootDir, summary.sourceHashesEnd, stage);
  return { status: 'completed', frozenSourcesSha256 };
}

function verifyEvidenceManifest(runDir, manifest, stage) {
  const artifacts = manifest.artifacts;
  if (!Array.isArray(artifacts) || !artifacts.length) throw new BlockedError(`${stage}_evidence_invalid`);
  let summaryFound = false;
  for (const artifact of artifacts) {
    const relative = normalizeSlash(artifact.path);
    const target = path.resolve(runDir, relative);
    if (!relative || !isInside(runDir, target) || !/^[a-f0-9]{64}$/.test(artifact.sha256 || '')) {
      throw new BlockedError(`${stage}_evidence_invalid`);
    }
    if (!fs.existsSync(target) || !fs.statSync(target).isFile() || sha256File(target) !== artifact.sha256) {
      throw new BlockedError(`${stage}_evidence_drift`);
    }
    if (relative === 'summary.json') summaryFound = true;
  }
  if (!summaryFound) throw new BlockedError(`${stage}_evidence_invalid`);
}

function stageCompleted(stage, summary) {
  if (summary.ok !== true || summary.sourceHashesStable !== true) return false;
  if (stage === 'p00') {
    return summary.expectationsHashStable === true && summary.evidenceCompleteness?.complete === true;
  }
  return summary.status === 'completed';
}

function requireAccepted(summary, ids, stage) {
  for (const id of ids) {
    const result = (summary.commandResults || []).find((item) => item.id === id);
    if (!result?.evaluation?.accepted) throw new BlockedError(`${stage}_boundary_unchecked`);
  }
}

function verifyFrozenSources(rootDir, records, stage) {
  if (!Array.isArray(records) || records.length === 0) throw new BlockedError(`${stage}_evidence_invalid`);
  const signature = [];
  for (const record of records) {
    const relative = normalizeSlash(record.path);
    if (!relative || record.missing || !/^[a-f0-9]{64}$/.test(record.sha256 || '')) {
      throw new BlockedError(`${stage}_evidence_invalid`);
    }
    const file = path.resolve(rootDir, relative);
    if (!isInside(rootDir, file) || !fs.existsSync(file) || sha256File(file) !== record.sha256) {
      throw new BlockedError(`${stage}_source_drift`);
    }
    signature.push(`${relative}\0${record.sha256}`);
  }
  return sha256(signature.sort().join('\n'));
}

function readJsonOrBlock(file, reason) {
  try {
    return JSON.parse(fs.readFileSync(file, 'utf8'));
  } catch {
    throw new BlockedError(reason);
  }
}

async function createGeneratedDatabase(rootDir, temporaryRoot, fixtureGenerator = generateFixtures) {
  const p00Output = path.join(temporaryRoot, 'p00');
  let basePath;
  let provenance = 'p00-empty+p03-project-seed-v1';
  try {
    const generated = await fixtureGenerator({
      rootDir,
      allowRoot: temporaryRoot,
      outputDir: p00Output,
    });
    basePath = path.join(generated.outputDir, 'empty.sqlite');
  } catch (error) {
    const knownP00IndexBoundaryDrift = String(error?.message || '').includes('unrecognized token: "`);"');
    if (fixtureGenerator !== generateFixtures || !knownP00IndexBoundaryDrift) throw error;
    fs.mkdirSync(p00Output, { recursive: false, mode: 0o700 });
    basePath = path.join(p00Output, 'empty.sqlite');
    const SQL = await loadSqlJs(rootDir);
    const databaseSource = fs.readFileSync(path.join(rootDir, 'src/database.js'), 'utf8');
    const compatibleSource = normalizeP00IndexTerminators(databaseSource);
    fs.writeFileSync(basePath, createEmptyFixture(SQL, compatibleSource), { flag: 'wx', mode: 0o600 });
    provenance = 'p00-createEmptyFixture+p03-project-seed-v1';
  }
  const databasePath = path.join(temporaryRoot, 'p03-node.sqlite');
  const seeded = await seedP03Database(rootDir, fs.readFileSync(basePath));
  fs.writeFileSync(databasePath, seeded, { flag: 'wx', mode: 0o600 });
  return {
    databasePath,
    baseDatabaseSha256: sha256File(basePath),
    databaseSha256: sha256(seeded),
    provenance,
  };
}

function normalizeP00IndexTerminators(databaseSource) {
  return databaseSource.replace(
    /(CREATE\s+(?:UNIQUE\s+)?INDEX\s+IF\s+NOT\s+EXISTS[^`]*?\))(?=\r?\n\s*`)/gi,
    '$1;',
  );
}

async function seedP03Database(rootDir, baseBytes) {
  const SQL = await loadSqlJs(rootDir);
  const db = new SQL.Database(baseBytes);
  try {
    db.run("INSERT OR REPLACE INTO settings (key, value) VALUES ('mcp.authToken', ''), ('mcp.enabled', 'false')");
    const projects = [
      ['Synthetic Alpha', '/__autoplan_fixture__/p03/alpha', '', FIXED_TIMES.alphaUpdated],
      ['Synthetic Beta', '/__autoplan_fixture__/p03/beta', 'Synthetic nullable/default coverage project.', FIXED_TIMES.latestUpdated],
      ['Synthetic Gamma', '/__autoplan_fixture__/p03/gamma', 'Synthetic sort tie-break project.', FIXED_TIMES.latestUpdated],
    ];
    for (const [name, workspace, description, updatedAt] of projects) {
      db.run(
        'INSERT INTO projects (name, workspace_path, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?)',
        [name, workspace, description, FIXED_TIMES.created, updatedAt],
      );
    }
    db.run(
      `INSERT INTO project_states
        (project_id, running, phase, interval_seconds, validation_command, project_prompt,
         agent_cli_provider, agent_cli_command, codex_reasoning_effort,
         plan_generation_strategy, plan_generation_provider, plan_generation_command,
         plan_generation_model, plan_generation_codex_reasoning_effort,
         plan_generation_claude_base_url, plan_generation_claude_auth_token,
         plan_generation_claude_model, plan_generation_claude_config_id,
         plan_execution_strategy, plan_execution_provider, plan_execution_command,
         plan_execution_model, plan_execution_codex_reasoning_effort,
         plan_execution_claude_base_url, plan_execution_claude_auth_token,
         plan_execution_claude_model, plan_execution_claude_config_id, env_vars, updated_at)
       VALUES (1, 0, 'idle', 9, '', 'Synthetic project prompt.', 'claude', '', NULL,
               'external-cli-structured', 'claude', '', '', NULL,
               'https://example.invalid/claude', 'fixture-sensitive-mask', 'synthetic-model', 0,
               'external-cli', 'codex', '', '', 'medium', '', '', '', 0,
               '[{"name":"FIXTURE_FLAG","value":"fixture-private-value"}]', ?)`,
      [FIXED_TIMES.alphaUpdated],
    );
    db.run(
      `INSERT INTO project_states
        (project_id, running, phase, interval_seconds, validation_command, project_prompt,
         agent_cli_provider, agent_cli_command, codex_reasoning_effort, env_vars, updated_at)
       VALUES (2, 0, 'stopped', 5, 'npm test', '', 'codex', '', NULL, '', ?)`,
      [FIXED_TIMES.latestUpdated],
    );
    return Buffer.from(db.export());
  } finally {
    db.close();
  }
}

async function loadSqlJs(rootDir) {
  const initSqlJs = require('sql.js');
  const wasmPath = require.resolve('sql.js/dist/sql-wasm.wasm', { paths: [rootDir] });
  return initSqlJs({ locateFile: () => wasmPath });
}

async function readNodeContracts(rootDir, databasePath, options = {}) {
  const before = stableDatabaseRead(databasePath);
  const SQL = await loadSqlJs(rootDir);
  const raw = new SQL.Database(before.bytes);
  const db = new ReadOnlySqlJsFacade(raw);
  try {
    const result = withStableSnapshotEnvironment(() => {
      const service = new LoopService(db);
      const projects = service.projects();
      if (!projects.length) throw new BlockedError('fixture_has_no_projects');
      assertProjectOrder(projects);
      const requestedProjectId = Number(options.projectId || Math.min(...projects.map((project) => Number(project.id))));
      const emptySnapshot = service.snapshot();
      const projectSnapshot = service.snapshot(requestedProjectId);
      const missingSnapshot = service.snapshot(Number.MAX_SAFE_INTEGER);
      if (!projectSnapshot.activeProject || Number(projectSnapshot.activeProjectId) !== requestedProjectId) {
        throw new BlockedError('fixture_project_missing');
      }
      if (stableJson(missingSnapshot) !== stableJson(emptySnapshot)) {
        throw new BlockedError('missing_project_semantics_drift');
      }
      return { projects, emptySnapshot, projectSnapshot, missingSnapshotEquivalent: true, requestedProjectId };
    });
    const after = stableDatabaseRead(databasePath);
    if (before.sha256 !== after.sha256) throw new BlockedError('database_changed_during_node_read');
    return result;
  } finally {
    raw.close();
  }
}

class ReadOnlySqlJsFacade {
  constructor(db) {
    this.db = db;
  }

  all(sql, params = []) {
    const statement = this.db.prepare(sql);
    try {
      statement.bind(params);
      const rows = [];
      while (statement.step()) rows.push(statement.getAsObject());
      return rows;
    } finally {
      statement.free();
    }
  }

  get(sql, params = []) {
    return this.all(sql, params)[0] || null;
  }

  getSettings(prefix = '') {
    const rows = this.all('SELECT key, value FROM settings WHERE key LIKE ? ORDER BY key', [`${prefix}%`]);
    return Object.fromEntries(rows.map((row) => [row.key, row.value]));
  }

  run(sql, params = []) {
    if (!/^\s*UPDATE\s+(?:project_states|loop_state|plan_tasks)\b/i.test(sql)) {
      throw new BlockedError('node_read_path_attempted_write');
    }
    this.db.run(sql, params);
    if (this.db.getRowsModified() !== 0) throw new BlockedError('fixture_runtime_state_not_quiescent');
  }

  runBatch(statements = []) {
    for (const statement of statements) this.run(statement.sql, statement.params || []);
  }
}

function withStableSnapshotEnvironment(callback) {
  const names = [
    'AUTOPLAN_MCP_ENABLED', 'AUTOPLAN_MCP_TRANSPORT', 'AUTOPLAN_MCP_HOST',
    'AUTOPLAN_MCP_PORT', 'AUTOPLAN_MCP_PATH', 'AUTOPLAN_MCP_AUTH_TOKEN',
  ];
  const saved = new Map(names.map((name) => [name, process.env[name]]));
  try {
    names.forEach((name) => delete process.env[name]);
    return callback();
  } finally {
    for (const [name, value] of saved) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
  }
}

function assertProjectOrder(projects) {
  for (let index = 1; index < projects.length; index += 1) {
    const previous = projects[index - 1];
    const current = projects[index];
    const comparison = String(previous.updated_at).localeCompare(String(current.updated_at));
    if (comparison < 0 || (comparison === 0 && Number(previous.id) < Number(current.id))) {
      throw new BlockedError('projects_sort_drift');
    }
  }
}

function stableDatabaseRead(databasePath) {
  const firstStat = fs.statSync(databasePath);
  const bytes = fs.readFileSync(databasePath);
  const secondStat = fs.statSync(databasePath);
  if (!firstStat.isFile() || firstStat.size !== secondStat.size || firstStat.mtimeMs !== secondStat.mtimeMs) {
    throw new BlockedError('database_appears_in_use');
  }
  return { bytes, sha256: sha256(bytes) };
}

function validateExplicitDatabase(databasePath, allowRoot, sanitizedCopy) {
  if (!sanitizedCopy) throw new BlockedError('database_copy_not_marked_sanitized');
  if (!databasePath || !path.isAbsolute(databasePath) || !allowRoot || !path.isAbsolute(allowRoot)) {
    throw new BlockedError('database_path_not_authorized');
  }
  const target = path.resolve(databasePath);
  const root = path.resolve(allowRoot);
  if (!fs.existsSync(root) || !fs.statSync(root).isDirectory() || !isInside(root, target)) {
    throw new BlockedError('database_path_not_authorized');
  }
  if (looksLikeUserData(target) || path.basename(target).toLowerCase() === 'autoplan.sqlite') {
    throw new BlockedError('database_path_forbidden');
  }
  assertNoSymlinkPath(root, target);
  if (!fs.existsSync(target) || !fs.statSync(target).isFile() || path.extname(target).toLowerCase() !== '.sqlite') {
    throw new BlockedError('database_copy_invalid');
  }
  for (const suffix of ['-wal', '-shm', '-journal', '.lock']) {
    if (fs.existsSync(`${target}${suffix}`)) throw new BlockedError('database_appears_in_use');
  }
  return target;
}

function assertNoSymlinkPath(root, target) {
  let current = target;
  while (isInside(root, current)) {
    if (fs.existsSync(current) && fs.lstatSync(current).isSymbolicLink()) throw new BlockedError('database_symlink_forbidden');
    if (path.resolve(current) === path.resolve(root)) break;
    current = path.dirname(current);
  }
}

async function buildGoldenBundle(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  const gates = options.gateCheck ? await options.gateCheck(rootDir) : verifyPrerequisites(rootDir);
  let temporaryRoot;
  try {
    temporaryRoot = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'autoplan-p03-node-'));
    let source;
    if (options.databasePath) {
      const databasePath = validateExplicitDatabase(options.databasePath, options.allowRoot, options.sanitizedCopy);
      source = {
        databasePath,
        baseDatabaseSha256: null,
        databaseSha256: sha256File(databasePath),
        provenance: 'caller-declared-sanitized-copy',
      };
    } else {
      source = await createGeneratedDatabase(rootDir, temporaryRoot, options.fixtureGenerator);
    }
    const raw = await readNodeContracts(rootDir, source.databasePath, options);
    const normalized = normalizeContracts({
      projects: raw.projects,
      emptySnapshot: raw.emptySnapshot,
      projectSnapshot: raw.projectSnapshot,
    }, {
      fixtureRoot: options.databasePath ? options.allowRoot : temporaryRoot,
      homeDir: os.homedir(),
      appData: process.env.APPDATA,
      localAppData: process.env.LOCALAPPDATA,
    });
    const artifacts = {
      'projects.golden.json': stableJson(normalized.value.projects),
      'snapshot-empty.golden.json': stableJson(normalized.value.emptySnapshot),
      'snapshot-project.golden.json': stableJson(normalized.value.projectSnapshot),
    };
    for (const content of Object.values(artifacts)) assertSanitizedContract(JSON.parse(content));
    const manifest = {
      schemaVersion: 1,
      version: GENERATOR_VERSION,
      generator: 'scripts/migration-p03/generate-node-golden.js',
      source: {
        recipe: RECIPE_PATH,
        recipeSha256: sha256File(path.join(rootDir, RECIPE_PATH)),
        fixture: source.provenance,
        baseDatabaseSha256: source.baseDatabaseSha256,
        databaseSha256: source.databaseSha256,
        syntheticOnly: true,
      },
      prerequisites: {
        policy: 'latest immutable evidence and frozen sources must validate before generation',
        stages: Object.keys(gates).sort(),
      },
      normalization: {
        ...normalized.metadata,
        source: NORMALIZER_PATH,
        sourceSha256: sha256File(path.join(rootDir, NORMALIZER_PATH)),
      },
      scenarios: {
        projects: 'LoopService.projects()',
        snapshotEmpty: 'LoopService.snapshot()',
        snapshotProject: `LoopService.snapshot(${normalized.metadata.idMaps.projects[String(raw.requestedProjectId)]})`,
        snapshotMissing: 'structurally identical to snapshot-empty.golden.json',
      },
      artifacts: GOLDEN_FILES.map((name) => ({ name, sha256: sha256(artifacts[name]) })),
    };
    artifacts['manifest.json'] = stableJson(manifest);
    assertSanitizedContract(manifest);
    return { artifacts, manifest };
  } finally {
    if (temporaryRoot) safeRemoveTemporaryRoot(temporaryRoot);
  }
}

async function generateNodeGolden(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  const outputDir = path.resolve(options.outputDir || path.join(rootDir, OUTPUT_ROOT));
  if (!isInside(rootDir, outputDir) && !options.allowExternalOutput) {
    throw new BlockedError('golden_output_not_authorized');
  }
  const bundle = await buildGoldenBundle({ ...options, rootDir });
  commitArtifacts(outputDir, bundle.artifacts);
  return { artifacts: Object.keys(bundle.artifacts).sort(), manifest: bundle.manifest };
}

function commitArtifacts(outputDir, artifacts) {
  fs.mkdirSync(outputDir, { recursive: true });
  const staging = fs.mkdtempSync(path.join(outputDir, '.p03-golden-staging-'));
  const previous = new Map();
  try {
    for (const [name, content] of Object.entries(artifacts)) {
      if (!GOLDEN_FILES.includes(name) && name !== 'manifest.json') throw new Error(`非法黄金文件：${name}`);
      fs.writeFileSync(path.join(staging, name), content, { flag: 'wx' });
    }
    for (const name of Object.keys(artifacts)) {
      const target = path.join(outputDir, name);
      previous.set(name, fs.existsSync(target) ? fs.readFileSync(target) : null);
      fs.writeFileSync(target, fs.readFileSync(path.join(staging, name)));
    }
  } catch (error) {
    for (const [name, content] of previous) {
      const target = path.join(outputDir, name);
      if (content === null) {
        try { fs.unlinkSync(target); } catch {}
      } else {
        try { fs.writeFileSync(target, content); } catch {}
      }
    }
    throw error;
  } finally {
    fs.rmSync(staging, { recursive: true, force: true });
  }
}

function safeRemoveTemporaryRoot(temporaryRoot) {
  const resolved = path.resolve(temporaryRoot);
  const temp = fs.realpathSync(os.tmpdir());
  if (!isInside(temp, resolved) || !path.basename(resolved).startsWith('autoplan-p03-node-')) {
    throw new BlockedError('temporary_cleanup_boundary_failed');
  }
  fs.rmSync(resolved, { recursive: true, force: true });
}

function normalizeSlash(value) {
  return String(value || '').replace(/\\/g, '/');
}

function parseArgs(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === '--database') options.databasePath = argv[++index];
    else if (arg === '--allow-root') options.allowRoot = argv[++index];
    else if (arg === '--sanitized-copy') options.sanitizedCopy = true;
    else throw new Error(`未知参数：${arg}`);
    if ((arg === '--database' || arg === '--allow-root') && argv[index] === undefined) {
      throw new Error(`参数缺少值：${arg}`);
    }
  }
  return options;
}

if (require.main === module) {
  generateNodeGolden(parseArgs(process.argv.slice(2)))
    .then((result) => process.stdout.write(`${JSON.stringify({ ok: true, artifacts: result.artifacts })}\n`))
    .catch((error) => {
      const message = error instanceof BlockedError ? error.message : 'golden generation failed safely';
      process.stderr.write(`${message}\n`);
      process.exitCode = 1;
    });
}

module.exports = {
  BlockedError,
  FIXED_TIMES,
  GENERATOR_VERSION,
  GOLDEN_FILES,
  ReadOnlySqlJsFacade,
  buildGoldenBundle,
  createGeneratedDatabase,
  generateNodeGolden,
  parseArgs,
  readNodeContracts,
  seedP03Database,
  sha256,
  normalizeP00IndexTerminators,
  validateExplicitDatabase,
  verifyEvidenceStage,
  verifyEvidenceManifest,
  verifyPrerequisites,
};
