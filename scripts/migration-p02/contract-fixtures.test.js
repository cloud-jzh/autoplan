'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');

const baseline = require('../migration-baseline/contract-baseline');

const ROOT = path.resolve(__dirname, '../..');
const MANIFEST_PATH = path.join(ROOT, 'fixtures/contracts/p02/manifest.json');
const SCHEMA_ROOT = path.join(ROOT, 'backend/openapi/schemas');
const TYPES_PATH = path.join(ROOT, 'src/renderer/types.ts');

function readJson(file) {
  return JSON.parse(fs.readFileSync(file, 'utf8'));
}

function sorted(values) {
  return [...values].sort((left, right) => left.localeCompare(right));
}

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

function validTimestamp(value) {
  return typeof value === 'string'
    && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/.test(value)
    && Number.isFinite(Date.parse(value));
}

function validID(value, maximum = 128) {
  return typeof value === 'string'
    && value.length <= maximum
    && /^[A-Za-z0-9][A-Za-z0-9._:-]*$/.test(value);
}

function validType(value) {
  return typeof value === 'string' && /^[a-z][a-z0-9]*(?:[._:-][a-z0-9]+)*$/.test(value);
}

function exactKeys(value, required, allowed = required) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const keys = Object.keys(value);
  return required.every((key) => Object.hasOwn(value, key))
    && keys.every((key) => allowed.includes(key));
}

function safePresence(name) {
  return /(?:^|_)has_|Has[A-Z]/.test(name);
}

function safeMasked(name) {
  return /(?:_masked|Masked)$/.test(name);
}

function sensitiveName(name) {
  const normalized = name.toLowerCase().replace(/[^a-z0-9]+/g, '');
  return normalized.includes('workspacepath') || normalized === 'env' || normalized.includes('envvars')
    || normalized.includes('apikey') || normalized.includes('authtoken')
    || normalized.includes('accesstoken') || normalized.includes('refreshtoken')
    || normalized.includes('token') || normalized.includes('secret')
    || normalized.includes('credential') || normalized.includes('privatekey')
    || ['authorization', 'cookie', 'password', 'session', 'sessiontoken', 'sessionsecret'].includes(normalized);
}

function pathName(name) {
  const normalized = name.toLowerCase().replace(/[^a-z0-9]+/g, '');
  return ['path', 'cwd', 'workdir'].includes(normalized)
    || normalized.endsWith('path') || normalized.endsWith('logfile');
}

function relativePath(value) {
  return value === null || (typeof value === 'string'
    && !value.startsWith('/') && !value.startsWith('\\') && !/^[A-Za-z]:[\\/]/.test(value));
}

function safeValue(value, depth = 0) {
  if (depth > 32) return false;
  if (Array.isArray(value)) return value.every((item) => safeValue(item, depth + 1));
  if (value === null || typeof value !== 'object') return true;
  return Object.entries(value).every(([name, item]) => {
    if (sensitiveName(name)) {
      if (safePresence(name)) return typeof item === 'boolean';
      if (safeMasked(name)) return typeof item === 'string';
      return false;
    }
    if (/^(?:auth_header|authHeader)$/.test(name)) {
      return typeof item === 'string' && (item === '' || item.includes('<token>'));
    }
    if (pathName(name) && !relativePath(item)) return false;
    return safeValue(item, depth + 1);
  });
}

function validateProject(value, shape) {
  if (!exactKeys(value, shape.project_required, shape.project_fields)) return false;
  if (!Number.isInteger(value.id) || value.id <= 0 || typeof value.name !== 'string' || !value.name.trim()) return false;
  if (typeof value.description !== 'string' || !validTimestamp(value.created_at) || !validTimestamp(value.updated_at)) return false;
  if (Date.parse(value.created_at) > Date.parse(value.updated_at)) return false;
  if (Object.hasOwn(value, 'running') && ![0, 1].includes(value.running)) return false;
  if (Object.hasOwn(value, 'interval_seconds') && (!Number.isInteger(value.interval_seconds) || value.interval_seconds <= 0)) return false;
  const nonnullable = ['running', 'phase', 'interval_seconds', 'validation_command', 'project_prompt', 'agent_cli_provider', 'agent_cli_command'];
  if (nonnullable.some((field) => Object.hasOwn(value, field) && value[field] === null)) return false;
  if (value.plan_generation_strategy != null
      && !['external-cli-markdown', 'external-cli-structured', 'builtin-llm-structured'].includes(value.plan_generation_strategy)) return false;
  if (value.plan_execution_strategy != null
      && !['external-cli', 'builtin-llm'].includes(value.plan_execution_strategy)) return false;
  for (const field of ['plan_generation_claude_config_id', 'plan_execution_claude_config_id']) {
    if (value[field] != null && (!Number.isInteger(value[field]) || value[field] < 0)) return false;
  }
  return true;
}

function validateSnapshot(value, shape) {
  if (!exactKeys(value, shape.snapshot_fields, shape.snapshot_fields)) return false;
  const arrays = ['projects', 'requirements', 'feedback', 'attachments', 'plans', 'tasks', 'events', 'scans', 'scripts', 'executors', 'terminals', 'activeOperations'];
  if (!arrays.every((field) => Array.isArray(value[field]))) return false;
  if (!value.projects.every((project) => validateProject(project, shape))) return false;
  if ((value.activeProjectId === null) !== (value.activeProject === null)) return false;
  if (value.activeProject !== null) {
    if (!validateProject(value.activeProject, shape) || value.activeProject.id !== value.activeProjectId) return false;
    if (!value.projects.some((project) => project.id === value.activeProjectId)) return false;
  }
  const objects = ['mcp', 'scanSummary', ...arrays.filter((field) => field !== 'projects')];
  if (!objects.every((field) => safeValue(value[field]))) return false;
  return ['state', 'activeOperation', 'lastOperation'].every((field) => value[field] === null || safeValue(value[field]));
}

function validateError(value) {
  const allowed = ['code', 'message', 'request_id', 'retryable', 'details'];
  if (!exactKeys(value, ['code', 'message', 'request_id', 'retryable'], allowed)) return false;
  if (!/^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$/.test(value.code)) return false;
  if (typeof value.message !== 'string' || !value.message.trim() || !validID(value.request_id, 64)) return false;
  return typeof value.retryable === 'boolean'
    && (!Object.hasOwn(value, 'details') || (value.details !== null && safeValue(value.details)));
}

function validateOperation(value, shape) {
  if (!exactKeys(value, shape.operation_required, shape.operation_required)) return false;
  if (!validID(value.operation_id) || !validType(value.type) || !shape.operation_statuses.includes(value.status)) return false;
  if (!validID(value.request_id, 64) || (value.idempotency_key !== null && !validID(value.idempotency_key))) return false;
  if (![value.created_at, value.updated_at].every(validTimestamp)) return false;
  if (value.started_at !== null && !validTimestamp(value.started_at)) return false;
  if (value.finished_at !== null && !validTimestamp(value.finished_at)) return false;
  const times = [value.created_at, value.started_at, value.finished_at, value.updated_at].filter(Boolean).map(Date.parse);
  if (times.some((item, index) => index > 0 && item < times[index - 1])) return false;
  if (value.result !== null && !safeValue(value.result)) return false;
  if (value.error !== null && (!validateError(value.error) || value.error.request_id !== value.request_id)) return false;
  const stateRules = {
    queued: value.started_at === null && value.finished_at === null && value.error === null,
    running: value.started_at !== null && value.finished_at === null && value.error === null,
    succeeded: value.started_at !== null && value.finished_at !== null && value.error === null,
    failed: value.started_at !== null && value.finished_at !== null && value.error !== null,
    cancelled: value.finished_at !== null,
    interrupted: value.finished_at !== null,
  };
  return stateRules[value.status];
}

function validateEnvelope(value, required, websocket = false) {
  const optional = websocket ? ['terminal_session_id'] : ['project_id', 'sequence'];
  if (!exactKeys(value, required, [...required, ...optional])) return false;
  if (value.schema_version !== 1 || !validID(value.event_id) || !validType(value.type)) return false;
  if (!validID(value.request_id, 64) || (value.operation_id !== null && !validID(value.operation_id))) return false;
  if (!validTimestamp(value.occurred_at) || !safeValue(value.data)) return false;
  if (websocket) {
    if (!['client_to_server', 'server_to_client'].includes(value.direction)) return false;
    return !Object.hasOwn(value, 'terminal_session_id') || validID(value.terminal_session_id);
  }
  if (Object.hasOwn(value, 'project_id') && (!Number.isInteger(value.project_id) || value.project_id <= 0)) return false;
  return !Object.hasOwn(value, 'sequence') || (Number.isInteger(value.sequence) && value.sequence >= 0);
}

function validateFixture(contract, value, shape, duplicate = false) {
  if (duplicate) return false;
  if (contract === 'project') return validateProject(value, shape);
  if (contract === 'snapshot') return validateSnapshot(value, shape);
  if (contract === 'error') return validateError(value);
  if (contract === 'operation') return validateOperation(value, shape);
  if (contract === 'operation_accepted') {
    return exactKeys(value, ['operation_id', 'status', 'request_id', 'accepted_at'])
      && validID(value.operation_id) && value.status === 'queued'
      && validID(value.request_id, 64) && validTimestamp(value.accepted_at);
  }
  if (contract === 'sse') return validateEnvelope(value, shape.sse_required);
  if (contract === 'ws') return validateEnvelope(value, shape.ws_required, true);
  return false;
}

function mutate(contract, mutation, value) {
  if (mutation === 'duplicate_key') return { value, duplicate: true };
  if (mutation === 'unknown_field') value.unexpected_field = true;
  else if (mutation === 'non_utc_time') value[contract === 'sse' || contract === 'ws' ? 'occurred_at' : 'updated_at'] = '2026-01-02T11:04:05+08:00';
  else if (mutation === 'null_nonnullable') value.phase = null;
  else if (mutation === 'missing_snapshot_field') delete value.scanSummary;
  else if (mutation === 'mismatched_active_project') value.activeProjectId = 2;
  else if (mutation === 'absolute_path') value.state = { source_path: '/synthetic/fixture.json' };
  else if (mutation === 'credential_field') {
    const target = { api_key: 'not-a-credential' };
    if (contract === 'snapshot') value.state = target;
    else value.data = target;
  } else if (mutation === 'missing_request_id') delete value.request_id;
  else if (mutation === 'unknown_status') value.status = 'unknown';
  else if (mutation === 'invalid_terminal') value.error = null;
  else if (mutation === 'invalid_idempotency') value.idempotency_key = 'contains whitespace';
  else if (mutation === 'unknown_version') value.schema_version = 2;
  else if (mutation === 'missing_data') delete value.data;
  else if (mutation === 'invalid_direction') value.direction = 'sideways';
  else throw new Error(`unknown fixture mutation ${mutation}`);
  return { value, duplicate: false };
}

function materialize(item, byID) {
  if (item.valid) return { value: clone(item.value), duplicate: false };
  const base = byID.get(item.base);
  assert.ok(base?.valid, `${item.id} must reference a valid base`);
  assert.equal(base.contract, item.contract, `${item.id} base contract`);
  return mutate(item.contract, item.mutation, clone(base.value));
}

function schemaProperties(name) {
  return Object.keys(readJson(path.join(SCHEMA_ROOT, name)).properties || {});
}

function schemaAllowsNull(definition) {
  if (!definition || typeof definition !== 'object') return false;
  if (definition.type === 'null' || (Array.isArray(definition.type) && definition.type.includes('null'))) return true;
  if (Array.isArray(definition.enum) && definition.enum.includes(null)) return true;
  return [...(definition.oneOf || []), ...(definition.anyOf || [])].some(schemaAllowsNull);
}

function goStructTags(source, name) {
  const match = new RegExp(`type\\s+${name}\\s+struct\\s*\\{([\\s\\S]*?)\\n\\}`).exec(source);
  assert.ok(match, `missing Go struct ${name}`);
  return [...match[1].matchAll(/json:"([^",]+)/g)].map((item) => item[1]);
}

test('shared synthetic fixtures agree on validity in Node', () => {
  const manifest = readJson(MANIFEST_PATH);
  assert.equal(manifest.schema_version, 1);
  assert.equal(manifest.synthetic_only, true);
  const byID = new Map(manifest.cases.map((item) => [item.id, item]));
  assert.equal(byID.size, manifest.cases.length, 'fixture ids must be unique');
  for (const item of manifest.cases) {
    const materialized = materialize(item, byID);
    assert.equal(validateFixture(item.contract, materialized.value, manifest.shape, materialized.duplicate), item.valid, item.id);
    if (item.valid) assert.deepEqual(JSON.parse(JSON.stringify(materialized.value)), materialized.value, `${item.id} JSON round-trip`);
  }
  const errorCases = manifest.cases.filter((item) => item.valid && item.contract === 'error').map((item) => item.value.code);
  assert.deepEqual(sorted(errorCases), sorted(manifest.shape.error_catalog.map((item) => item.code)));
});

test('OpenAPI schemas, Go DTO tags, renderer types and P00 baseline cannot drift independently', () => {
  baseline.run(ROOT);
  const manifest = readJson(MANIFEST_PATH);
  const { shape } = manifest;
  for (const field of [...shape.project_fields, ...shape.operation_required, ...shape.sse_required, ...shape.ws_required]) {
    assert.match(field, /^[a-z][a-z0-9_]*$/, `new public field must be snake_case: ${field}`);
  }
  for (const forbidden of ['workspace_path', 'env_vars', 'api_key', 'auth_token', 'token']) {
    assert.ok(!shape.project_fields.includes(forbidden), `forbidden Project field ${forbidden}`);
    assert.ok(!shape.snapshot_fields.includes(forbidden), `forbidden Snapshot field ${forbidden}`);
  }
  assert.deepEqual(sorted(schemaProperties('project.schema.json')), sorted(shape.project_fields));
  assert.deepEqual(sorted(schemaProperties('snapshot.schema.json')), sorted(shape.snapshot_fields));

  const operationSchema = readJson(path.join(SCHEMA_ROOT, 'operation.schema.json')).$defs.Operation;
  assert.deepEqual(sorted(operationSchema.required), sorted(shape.operation_required));
  assert.deepEqual(sorted(operationSchema.properties.status.enum), sorted(shape.operation_statuses));
  assert.equal(operationSchema.additionalProperties, false);
  assert.equal(operationSchema.allOf.length, 5, 'Operation terminal constraints must remain explicit');
  const queuedOperation = manifest.cases.find((item) => item.id === 'operation_queued').value;
  for (const [field, value] of Object.entries(queuedOperation).filter(([, value]) => value === null)) {
    assert.ok(schemaAllowsNull(operationSchema.properties[field]), `Operation schema nullability drift for ${field}`);
  }
  const sseSchema = readJson(path.join(SCHEMA_ROOT, 'sse-envelope-v1.schema.json'));
  const wsSchema = readJson(path.join(SCHEMA_ROOT, 'ws-envelope-v1.schema.json'));
  assert.deepEqual(sorted(sseSchema.required), sorted(shape.sse_required));
  assert.deepEqual(sorted(wsSchema.required), sorted(shape.ws_required));
  assert.equal(sseSchema.properties.schema_version.const, 1);
  assert.equal(wsSchema.properties.schema_version.const, 1);

  const types = fs.readFileSync(TYPES_PATH, 'utf8');
  const projectDeclarations = [
    ...baseline.extractInterface(types, 'Project').fields,
    ...baseline.extractInterface(types, 'PlanGenerationSnapshotFields').fields,
    ...baseline.extractInterface(types, 'PlanExecutionSnapshotFields').fields,
  ];
  const projectOwn = baseline.extractInterface(types, 'Project').fields.map((field) => field.split(/[?:]/, 1)[0]);
  const generation = baseline.extractInterface(types, 'PlanGenerationSnapshotFields').fields.map((field) => field.split(/[?:]/, 1)[0]);
  const execution = baseline.extractInterface(types, 'PlanExecutionSnapshotFields').fields.map((field) => field.split(/[?:]/, 1)[0]);
  const publicRendererProject = [...projectOwn, ...generation, ...execution]
    .filter((field) => !['workspace_path', 'env_vars', 'plan_generation_claude_auth_token', 'plan_execution_claude_auth_token'].includes(field));
  assert.deepEqual(sorted(publicRendererProject), sorted(shape.project_fields));
  const snapshotFields = baseline.extractInterface(types, 'AppSnapshot').fields.map((field) => field.split(/[?:]/, 1)[0]);
  assert.deepEqual(snapshotFields, shape.snapshot_fields);

  const projectSchema = readJson(path.join(SCHEMA_ROOT, 'project.schema.json'));
  const fullProject = manifest.cases.find((item) => item.id === 'project_full').value;
  for (const [field, value] of Object.entries(fullProject).filter(([, value]) => value === null)) {
    const declaration = projectDeclarations.find((item) => item.startsWith(`${field}?`) || item.startsWith(`${field}:`));
    assert.ok(declaration?.includes('|null'), `renderer nullability drift for ${field}`);
    assert.ok(schemaAllowsNull(projectSchema.properties[field]), `Project schema nullability drift for ${field}`);
  }
  assert.ok(!schemaAllowsNull(projectSchema.properties.phase), 'phase must remain optional but non-nullable');

  const snapshotSchema = readJson(path.join(SCHEMA_ROOT, 'snapshot.schema.json'));
  const emptySnapshot = manifest.cases.find((item) => item.id === 'snapshot_empty').value;
  const snapshotDeclarations = baseline.extractInterface(types, 'AppSnapshot').fields;
  for (const [field, value] of Object.entries(emptySnapshot).filter(([, value]) => value === null)) {
    const declaration = snapshotDeclarations.find((item) => item.startsWith(`${field}?`) || item.startsWith(`${field}:`));
    assert.ok(declaration?.includes('|null'), `snapshot renderer nullability drift for ${field}`);
    assert.ok(schemaAllowsNull(snapshotSchema.properties[field]), `Snapshot schema nullability drift for ${field}`);
  }

  const goTypes = fs.readFileSync(path.join(ROOT, 'backend/internal/domain/contracts/types.go'), 'utf8');
  assert.deepEqual(sorted(goStructTags(goTypes, 'Project')), sorted(shape.project_fields));
  assert.deepEqual(goStructTags(goTypes, 'AppSnapshot'), shape.snapshot_fields);
  assert.deepEqual(goStructTags(goTypes, 'SSEEnvelopeV1').filter((field) => shape.sse_required.includes(field)), shape.sse_required);
  assert.deepEqual(goStructTags(goTypes, 'WSEnvelopeV1').filter((field) => shape.ws_required.includes(field)), shape.ws_required);

  const errorsSource = fs.readFileSync(path.join(ROOT, 'backend/internal/httpapi/errors.go'), 'utf8');
  for (const item of shape.error_catalog) {
    assert.ok(errorsSource.includes(`"${item.code}"`), `missing HTTP error code ${item.code}`);
    assert.ok(errorsSource.includes(`"${item.message}"`), `missing HTTP error message ${item.code}`);
  }
});

test('fixtures contain no machine identity, production path, or usable credential', () => {
  const source = fs.readFileSync(MANIFEST_PATH, 'utf8');
  assert.doesNotMatch(source, /[A-Za-z]:[\\/](?:Users|Documents and Settings|AppData)[\\/]/i);
  assert.doesNotMatch(source, /\/(?:Users|home)\/[^/]+\//);
  assert.doesNotMatch(source, /userData/i);
  assert.doesNotMatch(source, /\bsk-[A-Za-z0-9_-]{12,}\b/);
  assert.doesNotMatch(source, /Bearer\s+[A-Za-z0-9._~+/-]{12,}/i);
  assert.doesNotMatch(source, /BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY/);
  assert.ok(!source.includes('"workspace_path"'));
  assert.ok(!source.includes('"env_vars"'));
});
