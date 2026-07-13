'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const { AppDatabase, nowIso } = require('../../src/database');
const { DuplicateIntakeError, createIntakeService, titleFromBody } = require('../../src/intakeService');
const { LoopService, nextIntakeAgentCliConfig, nextIntakePlanGenerationConfig } = require('../../src/loopService');
const intakePlanLinks = require('../../src/loop/intakePlanLinks');
const { CONTRACT_VERSION, sha256, stableJson } = require('./inventory-intake-contract');

const ROOT = path.resolve(__dirname, '../..');
const OUTPUT_DIR = 'fixtures/migration/p06';
const GOLDEN_NAME = 'node-intake.golden.json';
const MANIFEST_NAME = 'manifest.json';
const GENERATOR_VERSION = 'p06-node-intake-golden-v1';
const NORMALIZATION_VERSION = 'p06-node-intake-normalization-v1';
const TEMP_PREFIX = 'autoplan-p06-node-';
const FIXED_EPOCH_MS = Date.parse('2026-07-11T00:00:00.000Z');
const FIXTURE_ROOT = '<fixture-root>';
const UTC = '<utc>';
const REDACTED = '<redacted>';
const ATTACHMENT_BYTES = Buffer.from('synthetic-p06-attachment\n', 'utf8');

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
    const value = Buffer.alloc(size);
    for (let index = 0; index < size; index += 1) value[index] = (randomCounter + index + 31) % 256;
    randomCounter += size;
    return value;
  };
  return () => {
    global.Date = NativeDate;
    crypto.randomBytes = originalRandomBytes;
  };
}

function assertApprovedOutput(rootDir, outputDir, options = {}) {
  const expected = path.resolve(rootDir, OUTPUT_DIR);
  const actual = path.resolve(outputDir);
  if (actual !== expected && !options.allowExternalOutput) throw new BlockedError('golden_output_not_approved');
}

function assertP05Prerequisites(rootDir) {
  const required = [
    'docs/migration/p05/write-contract.json',
    'fixtures/migration/p05/manifest.json',
    'backend/migrations/0001_schema_v1.sql',
  ];
  const artifacts = [];
  for (const relativePath of required) {
    const absolutePath = path.join(rootDir, relativePath);
    if (!fs.existsSync(absolutePath) || !fs.statSync(absolutePath).isFile()) {
      throw new BlockedError(`p05_prerequisite_missing:${relativePath}`);
    }
    artifacts.push({ path: relativePath, sha256: sha256(fs.readFileSync(absolutePath)) });
  }
  const manifest = JSON.parse(fs.readFileSync(path.join(rootDir, 'fixtures/migration/p05/manifest.json'), 'utf8'));
  if (manifest.source?.syntheticOnly !== true || manifest.writerHandoff?.sameCopyConcurrentWritersAllowed !== false) {
    throw new BlockedError('p05_fixture_or_writer_handoff_not_approved');
  }
  return artifacts;
}

function assertContractFrozen(rootDir) {
  const relativePath = 'docs/migration/p06/intake-contract.json';
  const absolutePath = path.join(rootDir, relativePath);
  if (!fs.existsSync(absolutePath)) throw new BlockedError('intake_contract_missing');
  const bytes = fs.readFileSync(absolutePath);
  const contract = JSON.parse(bytes.toString('utf8'));
  if (contract.version !== CONTRACT_VERSION || contract.status !== 'frozen-with-recorded-legacy-gaps') {
    throw new BlockedError('intake_contract_not_frozen');
  }
  return { path: relativePath, sha256: sha256(bytes) };
}

function insertClosedRequirement(db, projectId) {
  const timestamp = nowIso();
  return db.insert(
    `INSERT INTO requirements (project_id, title, body, status, created_at, updated_at)
     VALUES (?, ?, ?, ?, ?, ?)`,
    [projectId, 'Synthetic requirement', 'Synthetic requirement\nBody with spaces', 'closed', timestamp, timestamp],
  );
}

function insertPlan(db, projectId, filePath, status, sortOrder) {
  const timestamp = nowIso();
  return db.insert(
    `INSERT INTO plans
     (project_id, issue_hash, file_path, hash, status, sort_order, total_tasks, completed_tasks, validation_passed, created_at, updated_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
    [projectId, `issue-${sortOrder}`, filePath, `plan-hash-${sortOrder}`, status, sortOrder, 2, status === 'completed' ? 2 : 0, status === 'completed' ? 1 : 0, timestamp, timestamp],
  );
}

function updateRequirement(db, loop, input = {}) {
  const projectId = requireProject(loop, input.projectId);
  const id = requireRecordId(input.id);
  const current = db.get('SELECT * FROM requirements WHERE id = ? AND project_id = ?', [id, projectId]);
  if (!current) throw new Error('需求不存在');
  const body = input.body ?? current.body ?? '';
  const agentCliConfig = nextIntakeAgentCliConfig(current, input);
  const planGenerationConfig = nextIntakePlanGenerationConfig(current, input);
  db.run(
    `UPDATE requirements
        SET title = ?, body = ?, status = ?,
            agent_cli_provider = ?, agent_cli_command = ?, codex_reasoning_effort = ?,
            plan_generation_strategy = ?, plan_generation_provider = ?, plan_generation_command = ?,
            plan_generation_model = ?, plan_generation_codex_reasoning_effort = ?, updated_at = ?
      WHERE id = ? AND project_id = ?`,
    [
      input.title ?? titleFromBody(body, '未命名需求'), body, input.status ?? current.status ?? 'open',
      agentCliConfig.provider, agentCliConfig.command, agentCliConfig.codexReasoningEffort,
      planGenerationConfig.strategy, planGenerationConfig.provider, planGenerationConfig.command,
      planGenerationConfig.model, planGenerationConfig.codexReasoningEffort, nowIso(), id, projectId,
    ],
  );
  return loop.snapshot(projectId);
}

function updateFeedback(db, loop, input = {}) {
  const projectId = requireProject(loop, input.projectId);
  const id = requireRecordId(input.id);
  const current = db.get('SELECT * FROM feedback WHERE id = ? AND project_id = ?', [id, projectId]);
  if (!current) throw new Error('反馈不存在');
  const body = input.body ?? current.body ?? '';
  const agentCliConfig = nextIntakeAgentCliConfig(current, input);
  const planGenerationConfig = nextIntakePlanGenerationConfig(current, input);
  db.run(
    `UPDATE feedback
        SET requirement_id = ?, title = ?, body = ?, status = ?,
            agent_cli_provider = ?, agent_cli_command = ?, codex_reasoning_effort = ?,
            plan_generation_strategy = ?, plan_generation_provider = ?, plan_generation_command = ?,
            plan_generation_model = ?, plan_generation_codex_reasoning_effort = ?, updated_at = ?
      WHERE id = ? AND project_id = ?`,
    [
      input.requirementId === undefined ? current.requirement_id || null : input.requirementId || null,
      input.title ?? titleFromBody(body, '未命名反馈'), body, input.status ?? current.status ?? 'open',
      agentCliConfig.provider, agentCliConfig.command, agentCliConfig.codexReasoningEffort,
      planGenerationConfig.strategy, planGenerationConfig.provider, planGenerationConfig.command,
      planGenerationConfig.model, planGenerationConfig.codexReasoningEffort, nowIso(), id, projectId,
    ],
  );
  return loop.snapshot(projectId);
}

function requireProject(loop, value) {
  const projectId = Number(value || 0);
  if (!projectId || !loop.project(projectId)) throw new Error('项目不存在');
  return projectId;
}

function requireRecordId(value) {
  const id = Number(value || 0);
  if (!id) throw new Error('记录不存在');
  return id;
}

function captureFailure(action) {
  try {
    action();
  } catch (error) {
    return {
      ok: false,
      error: normalizeError(error),
    };
  }
  throw new Error('expected operation failure');
}

function normalizeError(error) {
  const output = {
    name: error?.name || 'Error',
    code: error?.code || 'LEGACY_NODE_ERROR',
    message: String(error?.message || ''),
  };
  if (error instanceof DuplicateIntakeError || error?.code === 'DUPLICATE_INTAKE') {
    output.intakeType = error.intakeType;
    output.existingId = Number(error.existingId || error.existing?.id || 0) || null;
    output.existing = selectIntake(error.existing);
  }
  return output;
}

async function executeScenarios(temporaryRoot) {
  const databasePath = path.join(temporaryRoot, 'sanitized-p06-node.sqlite');
  const attachmentsRoot = path.join(temporaryRoot, 'attachments');
  const workspaceAlpha = path.join(temporaryRoot, 'workspace-alpha');
  const workspaceBeta = path.join(temporaryRoot, 'workspace-beta');
  fs.mkdirSync(workspaceAlpha, { recursive: true });
  fs.mkdirSync(workspaceBeta, { recursive: true });

  const db = new AppDatabase(databasePath);
  let closed = false;
  try {
    await db.init();
    const databaseBeforeSha256 = sha256(fs.readFileSync(databasePath));
    const loop = new LoopService(db);
    const service = createIntakeService({ db, loop, attachmentsRoot });

    const alpha = service.createProject({ name: 'P06 Alpha', workspacePath: workspaceAlpha });
    const projectId = Number(alpha.activeProjectId);
    const beta = service.createProject({ name: 'P06 Beta', workspacePath: workspaceBeta });
    const otherProjectId = Number(beta.activeProjectId);
    const historicalId = insertClosedRequirement(db, projectId);

    let snapshot = service.createRequirement({
      projectId,
      body: '  Synthetic requirement  \r\nBody   with   spaces  ',
      attachments: [{
        source: 'clipboard-image',
        name: '../synthetic-diagram.png',
        type: 'image/png; charset=binary',
        bytes: [...ATTACHMENT_BYTES],
      }],
      planGenerationStrategy: 'external-cli-markdown',
      planGenerationProvider: 'codex',
      planGenerationCodexReasoningEffort: 'high',
    });
    const requirement = db.get('SELECT * FROM requirements WHERE project_id = ? AND status = ? ORDER BY id ASC LIMIT 1', [projectId, 'open']);
    const attachment = db.get('SELECT * FROM attachments WHERE project_id = ? ORDER BY id ASC LIMIT 1', [projectId]);
    const scenarios = [{
      id: 'default-title-and-attachment',
      response: successSnapshot(snapshot, 'requirement', requirement.id),
      observations: {
        closedHistoricalId: historicalId,
        defaultTitle: requirement.title,
        bodyPreservesOriginalWhitespace: requirement.body,
        attachment: attachmentObservation(attachment, attachmentsRoot),
      },
    }];

    scenarios.push({
      id: 'normalized-duplicate',
      response: captureFailure(() => service.createRequirement({
        projectId,
        title: ' Synthetic    requirement ',
        body: 'Synthetic requirement\n Body with spaces',
        autoRun: true,
      })),
      observation: 'the open row blocks; the earlier closed row did not block the first create',
    });

    snapshot = service.createRequirement({ projectId, title: 'Second requirement', body: 'Second body' });
    const secondRequirement = db.get('SELECT * FROM requirements WHERE project_id = ? AND title = ?', [projectId, 'Second requirement']);
    scenarios.push({
      id: 'closed-duplicate',
      response: successSnapshot(snapshot, 'requirement', requirement.id),
      observations: { historicalId, createdOpenId: requirement.id, secondRequirementId: secondRequirement.id },
    });

    service.createRequirement({ projectId: otherProjectId, title: 'Other project requirement', body: 'Other body' });
    const otherRequirement = db.get('SELECT * FROM requirements WHERE project_id = ? ORDER BY id ASC LIMIT 1', [otherProjectId]);

    snapshot = service.createFeedback({
      projectId,
      requirementId: requirement.id,
      body: 'Feedback first line\nFeedback body',
    });
    const feedback = db.get('SELECT * FROM feedback WHERE project_id = ? ORDER BY id ASC LIMIT 1', [projectId]);
    const duplicateFeedback = captureFailure(() => service.createFeedback({
      projectId,
      requirementId: requirement.id,
      title: 'Feedback first line',
      body: 'Feedback first line\r\nFeedback   body',
    }));
    service.createFeedback({
      projectId,
      requirementId: secondRequirement.id,
      title: 'Feedback first line',
      body: 'Feedback first line\nFeedback body',
    });
    const groupedFeedback = db.get('SELECT * FROM feedback WHERE project_id = ? AND requirement_id = ?', [projectId, secondRequirement.id]);
    scenarios.push({
      id: 'feedback-association',
      response: successSnapshot(snapshot, 'feedback', feedback.id),
      observations: { feedback: selectIntake(feedback), duplicateSameGroup: duplicateFeedback },
    });
    scenarios.push({
      id: 'feedback-duplicate-groups',
      response: { ok: true },
      observations: {
        sameContentDifferentRequirementAllowed: true,
        firstRequirementId: feedback.requirement_id,
        secondRequirementId: groupedFeedback.requirement_id,
      },
    });
    scenarios.push({
      id: 'cross-project-feedback',
      response: captureFailure(() => service.createFeedback({
        projectId,
        requirementId: otherRequirement.id,
        title: 'Cross project',
        body: 'Rejected',
      })),
    });

    snapshot = updateRequirement(db, loop, { projectId, id: requirement.id, title: '', body: '' });
    snapshot = updateFeedback(db, loop, {
      projectId,
      id: feedback.id,
      requirementId: 0,
      body: 'Re-derived title\nUpdated feedback body',
    });
    scenarios.push({
      id: 'update-null-and-empty',
      response: successSnapshot(snapshot, 'feedback', feedback.id),
      observations: {
        requirement: selectIntake(db.get('SELECT * FROM requirements WHERE id = ?', [requirement.id])),
        feedback: selectIntake(db.get('SELECT * FROM feedback WHERE id = ?', [feedback.id])),
      },
    });

    const acceptedOnce = loop.acceptIntakeItem(projectId, { intakeType: 'feedback', id: feedback.id });
    const acceptedTwice = loop.acceptIntakeItem(projectId, { intakeType: 'feedback', id: feedback.id });
    const unaccepted = loop.unacceptIntakeItem(projectId, { intakeType: 'feedback', id: feedback.id });
    snapshot = loop.snapshot(projectId);
    scenarios.push({
      id: 'accept-unaccept',
      response: successSnapshot(snapshot, 'feedback', feedback.id),
      observations: {
        acceptedOnce, acceptedTwice, unaccepted,
        statusUnchanged: db.get('SELECT status FROM feedback WHERE id = ?', [feedback.id])?.status === 'open',
        events: acceptanceEvents(db, projectId, feedback.id),
      },
    });

    const planOneId = insertPlan(db, projectId, 'docs/plan/synthetic_phase01.md', 'completed', 1);
    const planTwoId = insertPlan(db, projectId, 'docs/plan/synthetic_phase02.md', 'pending', 2);
    intakePlanLinks.writeIntakePlanLinks(service, projectId, 'requirement', requirement.id, [
      { planId: planTwoId, phaseIndex: 2, phaseTitle: 'Implement' },
      { planId: planOneId, phaseIndex: 1, phaseTitle: 'Discover' },
    ], { clearExisting: true });
    snapshot = loop.snapshot(projectId);
    scenarios.push({
      id: 'multi-phase-links',
      response: successSnapshot(snapshot, 'requirement', requirement.id),
      observations: {
        links: linkRows(db, projectId, 'requirement', requirement.id),
        legacyLinkedPlanId: db.get('SELECT linked_plan_id FROM requirements WHERE id = ?', [requirement.id])?.linked_plan_id,
      },
    });

    snapshot = loop.deletePlan(projectId, planOneId, { reason: 'synthetic golden plan removal' });
    scenarios.push({
      id: 'delete-plan-keep-intake',
      response: successSnapshot(snapshot, 'requirement', requirement.id),
      observations: {
        intakeStillExists: Boolean(db.get('SELECT id FROM requirements WHERE id = ? AND project_id = ?', [requirement.id, projectId])),
        remainingLinks: linkRows(db, projectId, 'requirement', requirement.id),
        legacyLinkedPlanId: db.get('SELECT linked_plan_id FROM requirements WHERE id = ?', [requirement.id])?.linked_plan_id,
      },
    });

    const cascadeFeedbackSnapshot = service.createFeedback({
      projectId,
      requirementId: requirement.id,
      title: 'Cascade feedback',
      body: 'Association survives as null',
    });
    const cascadeFeedback = cascadeFeedbackSnapshot.feedback.find((item) => item.title === 'Cascade feedback');
    snapshot = loop.deleteIntake(projectId, 'requirement', requirement.id, { attachmentsRoot });
    scenarios.push({
      id: 'delete-requirement-cascade',
      response: successSnapshot(snapshot),
      observations: {
        requirementExists: Boolean(db.get('SELECT id FROM requirements WHERE id = ?', [requirement.id])),
        planExists: Boolean(db.get('SELECT id FROM plans WHERE id = ?', [planTwoId])),
        linkCount: Number(db.get('SELECT COUNT(*) AS count FROM intake_plan_links WHERE intake_id = ?', [requirement.id])?.count || 0),
        attachmentMetadataExists: Boolean(db.get('SELECT id FROM attachments WHERE id = ?', [attachment.id])),
        attachmentBytesExist: fs.existsSync(attachment.stored_path),
        feedbackRequirementId: db.get('SELECT requirement_id FROM feedback WHERE id = ?', [cascadeFeedback.id])?.requirement_id ?? null,
        deleteEvent: latestEvent(db, projectId, 'intake.deleted'),
      },
    });

    const databaseAfterSha256 = sha256(fs.readFileSync(databasePath));
    const logicalBeforeSha256 = sha256(stableJson({ tables: [], schema: 'initialized-schema-v1' }));
    const logicalAfter = logicalDatabaseState(db);
    const logicalAfterSha256 = sha256(stableJson(logicalAfter));
    db.close();
    closed = true;
    return {
      databasePath,
      databaseBeforeSha256,
      databaseAfterSha256,
      logicalBeforeSha256,
      logicalAfterSha256,
      attachmentBytesSha256: sha256(ATTACHMENT_BYTES),
      raw: {
        schemaVersion: 1,
        version: GENERATOR_VERSION,
        scenarios,
        finalLogicalState: logicalAfter,
        handoff: { sqlJsClosed: true, databaseOwnerReleased: true },
      },
    };
  } finally {
    if (!closed) db.close();
  }
}

function successSnapshot(snapshot, intakeType = null, intakeId = null) {
  const collection = intakeType === 'feedback' ? snapshot.feedback : snapshot.requirements;
  const row = intakeType && intakeId ? collection.find((item) => Number(item.id) === Number(intakeId)) : null;
  return {
    ok: true,
    snapshot: snapshotProjection(snapshot),
    intake: row ? snapshotIntakeProjection(row) : null,
  };
}

function snapshotProjection(snapshot = {}) {
  return {
    topLevelFields: Object.keys(snapshot).sort(),
    activeProjectId: snapshot.activeProjectId ?? null,
    requirementIds: (snapshot.requirements || []).map((row) => row.id),
    feedbackIds: (snapshot.feedback || []).map((row) => row.id),
    attachmentIds: (snapshot.attachments || []).map((row) => row.id),
    planIds: (snapshot.plans || []).map((row) => row.id),
    eventTypes: (snapshot.events || []).map((row) => row.type),
  };
}

function snapshotIntakeProjection(row = {}) {
  const redactedFields = Object.keys(row).filter(isSensitiveField).sort();
  return {
    fields: Object.keys(row).sort(),
    redactedFields,
    values: normalizeFixtureValue(Object.fromEntries(
      Object.entries(row).filter(([key]) => !isSensitiveField(key)),
    )),
  };
}

function isSensitiveField(key) {
  const normalized = String(key).toLowerCase();
  return normalized.includes('auth_token') || normalized === 'source_path' || normalized === 'source_hash'
    || normalized.includes('log_file') || normalized === 'stored_path' || normalized === 'hash';
}

function selectIntake(row) {
  if (!row) return null;
  return normalizeFixtureValue({
    id: row.id,
    project_id: row.project_id,
    requirement_id: row.requirement_id ?? null,
    title: row.title,
    body: row.body,
    status: row.status,
    linked_plan_id: row.linked_plan_id ?? null,
    accepted_at: row.accepted_at ?? null,
    created_at: row.created_at,
    updated_at: row.updated_at,
  });
}

function attachmentObservation(row, attachmentsRoot) {
  if (!row) return null;
  const storedPath = path.resolve(row.stored_path);
  const rootPath = path.resolve(attachmentsRoot);
  const relative = path.relative(rootPath, storedPath).replace(/\\/g, '/');
  return normalizeFixtureValue({
    legacyPublicMetadataFields: Object.keys(row).filter((key) => !isSensitiveField(key)).sort(),
    privateStoragePathFieldPresent: typeof row.stored_path === 'string' && row.stored_path.length > 0,
    privateIntegrityFieldPresent: typeof row.hash === 'string' && row.hash.length > 0,
    id: row.id,
    project_id: row.project_id,
    owner_type: row.owner_type,
    owner_id: row.owner_id,
    original_name: row.original_name,
    mime_type: row.mime_type,
    size: row.size,
    created_at: row.created_at,
    privateStoragePathRisk: { absolute: path.isAbsolute(row.stored_path), within_fixture_attachment_root: !relative.startsWith('..') && !path.isAbsolute(relative) },
    hash_recorded: /^[a-f0-9]{64}$/.test(row.hash),
    bytes_sha256: sha256(fs.readFileSync(row.stored_path)),
    proposed_public_dto: {
      id: row.id,
      display_name: row.original_name,
      size: row.size,
      mime_type: row.mime_type,
      download_url: `/api/v1/attachments/${row.id}/content`,
    },
  });
}

function acceptanceEvents(db, projectId, intakeId) {
  return db.all(
    `SELECT type, meta, created_at FROM events
      WHERE project_id = ? AND type IN ('feedback.accepted', 'feedback.unaccepted')
      ORDER BY id ASC`,
    [projectId],
  ).filter((event) => Number(JSON.parse(event.meta || '{}').feedbackId || 0) === Number(intakeId))
    .map((event) => ({ type: event.type, meta: normalizeFixtureValue(JSON.parse(event.meta)), created_at: UTC }));
}

function linkRows(db, projectId, intakeType, intakeId) {
  return normalizeFixtureValue(db.all(
    `SELECT id, project_id, intake_type, intake_id, plan_id, phase_index, phase_title, created_at, updated_at
       FROM intake_plan_links
      WHERE project_id = ? AND intake_type = ? AND intake_id = ?
      ORDER BY phase_index ASC, plan_id ASC`,
    [projectId, intakeType, intakeId],
  ));
}

function latestEvent(db, projectId, type) {
  const row = db.get('SELECT type, message, meta, created_at FROM events WHERE project_id = ? AND type = ? ORDER BY id DESC LIMIT 1', [projectId, type]);
  if (!row) return null;
  const meta = JSON.parse(row.meta || '{}');
  return normalizeFixtureValue({
    type: row.type,
    created_at: row.created_at,
    meta: {
      intakeType: meta.intakeType || null,
      intakeId: meta.intakeId ?? null,
      planId: meta.planId ?? null,
      planIds: Array.isArray(meta.planIds) ? meta.planIds : [],
      attachments: meta.attachments || null,
    },
  });
}

function logicalDatabaseState(db) {
  const tableQueries = {
    projects: 'SELECT id, name, description, created_at, updated_at FROM projects ORDER BY id ASC',
    requirements: 'SELECT id, project_id, title, body, status, linked_plan_id, accepted_at, created_at, updated_at FROM requirements ORDER BY id ASC',
    feedback: 'SELECT id, project_id, requirement_id, title, body, status, linked_plan_id, accepted_at, created_at, updated_at FROM feedback ORDER BY id ASC',
    attachments: 'SELECT id, project_id, owner_type, owner_id, original_name, mime_type, size, created_at FROM attachments ORDER BY id ASC',
    plans: 'SELECT id, project_id, file_path, status, sort_order, total_tasks, completed_tasks, validation_passed, created_at, updated_at FROM plans ORDER BY id ASC',
    intake_plan_links: 'SELECT id, project_id, intake_type, intake_id, plan_id, phase_index, phase_title, created_at, updated_at FROM intake_plan_links ORDER BY id ASC',
    events: 'SELECT id, project_id, type, created_at FROM events ORDER BY id ASC',
  };
  return normalizeFixtureValue(Object.fromEntries(
    Object.entries(tableQueries).map(([table, query]) => [table, db.all(query)]),
  ));
}

function normalizeFixtureValue(value, trail = []) {
  if (value === null || value === undefined || typeof value === 'boolean' || typeof value === 'number') return value;
  if (typeof value === 'string') {
    const key = String(trail.at(-1) || '');
    if (/(?:^|_)(?:created|updated|accepted)_at$/i.test(key) && value) return UTC;
    const normalized = value.replace(/\\/g, '/');
    const home = os.homedir().replace(/\\/g, '/');
    if (home && normalized.toLowerCase().includes(home.toLowerCase())) return REDACTED;
    return normalized;
  }
  if (Array.isArray(value)) return value.map((item, index) => normalizeFixtureValue(item, [...trail, index]));
  const output = {};
  for (const [key, child] of Object.entries(value)) {
    if (isSensitiveField(key)) output[key] = child ? REDACTED : child;
    else output[key] = normalizeFixtureValue(child, [...trail, key]);
  }
  return output;
}

function assertSanitized(value, temporaryRoot) {
  const encoded = JSON.stringify(value);
  const normalized = encoded.replace(/\\/g, '/').toLowerCase();
  const forbidden = [temporaryRoot, os.homedir(), process.env.APPDATA, process.env.LOCALAPPDATA]
    .filter(Boolean)
    .map((item) => path.resolve(item).replace(/\\/g, '/').toLowerCase());
  if (forbidden.some((item) => normalized.includes(item))) throw new BlockedError('golden_contains_real_absolute_path');
  if (/\b(?:sk-|ghp_|github_pat_)[A-Za-z0-9_-]{8,}\b/.test(encoded)) throw new BlockedError('golden_contains_credential_shape');
}

function safeRemoveTemporaryRoot(temporaryRoot) {
  const tempRoot = fs.realpathSync(os.tmpdir());
  const resolved = path.resolve(temporaryRoot);
  const relative = path.relative(tempRoot, resolved);
  if (!relative || relative.startsWith('..') || path.isAbsolute(relative) || !path.basename(resolved).startsWith(TEMP_PREFIX)) {
    throw new BlockedError('temporary_cleanup_boundary_failed');
  }
  fs.rmSync(resolved, { recursive: true, force: true });
}

function writeArtifacts(outputDir, artifacts) {
  fs.mkdirSync(outputDir, { recursive: true });
  const staging = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p06-output-'));
  try {
    for (const [name, content] of Object.entries(artifacts)) fs.writeFileSync(path.join(staging, name), content, 'utf8');
    for (const name of Object.keys(artifacts)) fs.copyFileSync(path.join(staging, name), path.join(outputDir, name));
  } finally {
    fs.rmSync(staging, { recursive: true, force: true });
  }
}

async function buildGoldenBundle(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  const p05Artifacts = assertP05Prerequisites(rootDir);
  const contract = assertContractFrozen(rootDir);
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), TEMP_PREFIX));
  const restoreRuntime = installDeterministicRuntime();
  try {
    const execution = await executeScenarios(temporaryRoot);
    const golden = {
      ...normalizeFixtureValue(execution.raw),
      normalization: {
        version: NORMALIZATION_VERSION,
        placeholders: { fixtureRoot: FIXTURE_ROOT, utc: UTC, redacted: REDACTED },
      },
    };
    assertSanitized(golden, temporaryRoot);
    const goldenContent = stableJson(golden);
    const generatorPath = path.join(rootDir, 'scripts/migration-p06/generate-node-golden.js');
    const manifest = {
      schemaVersion: 1,
      version: GENERATOR_VERSION,
      generator: 'scripts/migration-p06/generate-node-golden.js',
      generatorSha256: sha256(fs.readFileSync(generatorPath)),
      source: {
        syntheticOnly: true,
        databaseCopy: 'generator-owned OS temporary database; never Electron userData or production autoplan.sqlite',
        execution: 'Node scenarios are serial and close sql.js before artifacts are committed',
        databaseBeforeSha256: execution.databaseBeforeSha256,
        databaseAfterSha256: execution.databaseAfterSha256,
        databaseLogicalBeforeSha256: execution.logicalBeforeSha256,
        databaseLogicalAfterSha256: execution.logicalAfterSha256,
        attachmentBytesSha256: execution.attachmentBytesSha256,
      },
      prerequisites: { intakeContract: contract, p05: p05Artifacts },
      normalization: golden.normalization,
      scenarios: golden.scenarios.map((scenario) => scenario.id),
      writerHandoff: {
        sequence: ['Node opens generator-owned database', 'Node executes serial scenarios', 'Node closes sql.js and releases ownership', 'Go may later open a separately reset copy'],
        sameCopyConcurrentWritersAllowed: false,
        nodeClosedBeforeArtifactCommit: true,
      },
      publicAttachmentFields: ['id', 'display_name', 'size', 'mime_type', 'download_url'],
      forbiddenPublicAttachmentClasses: ['private storage path', 'private integrity digest', 'local file URL', 'server absolute path'],
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
  assertApprovedOutput(rootDir, outputDir, options);
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
  executeScenarios,
  generateNodeGolden,
  normalizeFixtureValue,
  parseArgs,
  safeRemoveTemporaryRoot,
  snapshotProjection,
  updateFeedback,
  updateRequirement,
};
