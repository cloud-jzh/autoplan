'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { GoDataClient, GoDataClientError, RUNTIME_COMMANDS } = require('../../src/data/goDataClient');

const REQUIRED_COMMANDS = Object.freeze([
  RUNTIME_COMMANDS.LOOP_START, RUNTIME_COMMANDS.LOOP_RUN_ONCE,
  RUNTIME_COMMANDS.PLAN_GENERATE, RUNTIME_COMMANDS.PLAN_PARSE, RUNTIME_COMMANDS.PLAN_RUN,
  RUNTIME_COMMANDS.PLAN_STOP, RUNTIME_COMMANDS.PLAN_RESUME, RUNTIME_COMMANDS.PLAN_REEXECUTE,
  RUNTIME_COMMANDS.PLAN_RECREATE, RUNTIME_COMMANDS.PLAN_VALIDATE,
  RUNTIME_COMMANDS.TASK_RUN, RUNTIME_COMMANDS.TASK_RUN_BATCHES, RUNTIME_COMMANDS.TASK_STOP,
  RUNTIME_COMMANDS.CHAT_SEND, RUNTIME_COMMANDS.CHAT_PUMP, RUNTIME_COMMANDS.CHAT_GENERATE_TITLE, RUNTIME_COMMANDS.CHAT_STOP, RUNTIME_COMMANDS.CHAT_CLEAR,
  RUNTIME_COMMANDS.SCRIPT_RUN, RUNTIME_COMMANDS.SCRIPT_STOP,
  RUNTIME_COMMANDS.EXECUTOR_RUN, RUNTIME_COMMANDS.EXECUTOR_ACTION, RUNTIME_COMMANDS.EXECUTOR_STOP,
  RUNTIME_COMMANDS.LOOP_STOP,
]);

function loadScaleCopy(file) {
  if (!path.isAbsolute(file) || containsForbiddenPath(file)) throw stableError('runtime_copy_rejected');
  const info = fs.lstatSync(file, { throwIfNoEntry: false });
  if (!info?.isFile() || info.isSymbolicLink() || info.size > 128 * 1024 * 1024) throw stableError('runtime_copy_rejected');
  const copy = JSON.parse(fs.readFileSync(file, 'utf8'));
  validateScaleCopy(copy);
  return copy;
}

function validateScaleCopy(copy) {
  if (!copy || copy.schema_version !== 1 || !copy.seed || !copy.rows || !Array.isArray(copy.rows.projects) ||
      !Array.isArray(copy.rows.plans) || !Array.isArray(copy.rows.plan_tasks) || !Array.isArray(copy.rows.events) ||
      !Array.isArray(copy.rows.chat_messages) || !Array.isArray(copy.rows.scripts) || !Array.isArray(copy.rows.executors)) {
    throw stableError('runtime_copy_invalid');
  }
  const encoded = JSON.stringify(copy);
  if (/(?:api[_-]?key|secret|token|password|authorization|appdata[\\/]+roaming[\\/]+autoplan)/i.test(encoded)) throw stableError('runtime_copy_sensitive');
}

async function verifyLegacyRuntime(copy, options = {}) {
  validateScaleCopy(copy);
  const bridge = createFixtureGoBridge(copy);
  const client = new GoDataClient({ baseUrl: 'http://127.0.0.1:43123', fetch: bridge.fetch, retryAttempts: 1, retryDelayMs: 0 });
  const project = copy.rows.projects[0];
  const plan = copy.rows.plans.find((item) => item.project_id === project.id);
  const tasks = copy.rows.plan_tasks.filter((item) => item.project_id === project.id && item.plan_id === plan.id);
  const script = copy.rows.scripts.find((item) => item.project_id === project.id);
  const executor = copy.rows.executors.find((item) => item.project_id === project.id);
  const conversationID = Number(copy.rows.chat_messages.find((item) => item.project_id === project.id)?.conversation_id || 1);
  const execute = (label, action) => action({ requestId: `p09-${label}`, idempotencyKey: `p09-intent-${label}` });

  await execute('loop-start', (metadata) => client.startLoop(project.id, metadata));
  await execute('loop-once', (metadata) => client.runLoopOnce(project.id, metadata));
  await execute('plan-generate', (metadata) => client.generatePlan(project.id, 1, metadata));
  await execute('plan-parse', (metadata) => client.parsePlan(project.id, plan.id, metadata));
  await execute('plan-run', (metadata) => client.runPlan(project.id, plan.id, metadata));
  await execute('plan-stop', (metadata) => client.stopPlan(project.id, plan.id, metadata));
  await execute('plan-resume', (metadata) => client.resumePlan(project.id, plan.id, metadata));
  await execute('plan-reexecute', (metadata) => client.reexecutePlan(project.id, plan.id, metadata));
  await execute('plan-recreate', (metadata) => client.recreatePlan(project.id, plan.id, metadata));
  await execute('plan-validate', (metadata) => client.validatePlan(project.id, plan.id, metadata));
  await execute('task-run', (metadata) => client.runTask(project.id, plan.id, tasks[0].id, metadata));
  await execute('task-batches', (metadata) => client.runTaskBatches(project.id, plan.id, [{ taskIds: tasks.slice(0, 3).map((item) => item.id) }], metadata));
  await execute('task-stop', (metadata) => client.stopTask(project.id, plan.id, tasks[0].id, metadata));
  await execute('chat-send', (metadata) => client.sendChat(project.id, conversationID, 'sanitized-runtime-message', metadata));
  await execute('chat-pump', (metadata) => client.pumpChat(project.id, conversationID, metadata));
  await execute('chat-title', (metadata) => client.generateChatTitle(project.id, conversationID, metadata));
  await execute('chat-stop', (metadata) => client.stopChat(project.id, conversationID, metadata));
  await execute('chat-clear', (metadata) => client.clearChat(project.id, conversationID, metadata));
  await execute('script-run', (metadata) => client.runScript(project.id, script.id, metadata));
  await execute('script-stop', (metadata) => client.stopScript(project.id, script.id, metadata));
  await execute('executor-run', (metadata) => client.runExecutor(project.id, executor.id, metadata));
  await execute('executor-action', (metadata) => client.runExecutorAction(project.id, executor.id, 'reload', metadata));
  await execute('executor-stop', (metadata) => client.stopExecutor(project.id, executor.id, metadata));
  await execute('loop-stop', (metadata) => client.stopLoop(project.id, metadata));

  const snapshot = client.snapshot(project.id);
  assertStableSnapshot(snapshot, project.id);
  assertNoNodeDatabaseSurface(client);
  if (bridge.attemptSecondGoOwner().code !== 'go_owner_locked' || bridge.attemptNodeSQLWrite().code !== 'node_sql_blocked') {
    throw stableError('runtime_owner_gate_failed');
  }
  const expected = new Set(REQUIRED_COMMANDS);
  if (bridge.commands.length !== expected.size || bridge.commands.some((command) => !expected.has(command.command))) throw stableError('runtime_command_coverage_failed');
  return {
    status: 'completed', command_count: bridge.commands.length, command_types: bridge.commands.map((command) => command.command),
    snapshot_sha256: sha256(JSON.stringify(snapshot)), owner: bridge.ownerEvidence(),
  };
}

function createFixtureGoBridge(copy) {
  const commands = [];
  const idempotency = new Set();
  let owner = 'go';
  const fetch = async (_url, init = {}) => {
    const payload = JSON.parse(String(init.body || '{}'));
    const requestID = String(init.headers?.['x-request-id'] || '');
    const key = String(init.headers?.['idempotency-key'] || '');
    if (owner !== 'go') return response(503, { error: { code: 'service_unavailable', request_id: requestID, retryable: false } });
    if (!RUNTIME_COMMANDS || !Object.values(RUNTIME_COMMANDS).includes(payload.command) || !requestID || !key) {
      return response(400, { error: { code: 'invalid_runtime_command', request_id: requestID, retryable: false } });
    }
    if (idempotency.has(key)) return response(409, { error: { code: 'idempotency_key_reused', request_id: requestID, retryable: false } });
    idempotency.add(key);
    commands.push(payload);
    return response(202, { data: { operation: { operation_id: `go-op-${commands.length}`, type: payload.command, status: 'accepted', request_id: requestID, accepted_at: '2026-07-11T04:45:56.000Z' }, snapshot: snapshotFor(copy, payload.project_id) } });
  };
  return {
    commands,
    fetch,
    attemptNodeSQLWrite() { return { ok: false, code: owner === 'go' ? 'node_sql_blocked' : 'service_unavailable' }; },
    attemptSecondGoOwner() { return { ok: false, code: owner === 'go' ? 'go_owner_locked' : 'service_unavailable' }; },
    ownerEvidence() { return { owner: 'go', node_sql_attempt: 'node_sql_blocked', second_go_attempt: 'go_owner_locked', writer_count: 1 }; },
    release() { owner = 'released'; },
  };
}

function snapshotFor(copy, projectID) {
  const id = Number(projectID);
  const project = copy.rows.projects.find((item) => item.id === id) || null;
  const within = (rows) => rows.filter((item) => Number(item.project_id) === id);
  return {
    activeProjectId: id || null, activeProject: project,
    projects: [...copy.rows.projects].sort((left, right) => left.id - right.id),
    plans: within(copy.rows.plans).sort((left, right) => left.sort_order - right.sort_order || left.id - right.id),
    tasks: within(copy.rows.plan_tasks).sort((left, right) => left.plan_id - right.plan_id || left.sort_order - right.sort_order || left.id - right.id),
    events: within(copy.rows.events).sort((left, right) => left.created_at.localeCompare(right.created_at) || left.sequence - right.sequence || left.id - right.id),
    chat_messages: within(copy.rows.chat_messages).sort((left, right) => left.conversation_id - right.conversation_id || left.created_at.localeCompare(right.created_at) || left.id - right.id),
    scripts: within(copy.rows.scripts).sort((left, right) => left.sort_order - right.sort_order || left.id - right.id),
    executors: within(copy.rows.executors).sort((left, right) => left.sort_order - right.sort_order || left.id - right.id),
    state: project ? { phase: project.state, running: 0 } : null,
  };
}

function assertStableSnapshot(snapshot, projectID) {
  if (!snapshot || Number(snapshot.activeProjectId) !== Number(projectID) || !Array.isArray(snapshot.projects) || !Array.isArray(snapshot.tasks)) throw stableError('runtime_snapshot_invalid');
  const projectIDs = new Set(snapshot.projects.map((project) => Number(project.id)));
  if (!projectIDs.has(Number(projectID)) || snapshot.tasks.some((task) => Number(task.project_id) !== Number(projectID))) throw stableError('runtime_cross_project_data');
}

function assertNoNodeDatabaseSurface(client) {
  for (const method of ['run', 'query', 'get', 'all', 'insert', 'export', 'persist']) {
    if (typeof client[method] !== 'undefined') throw stableError('node_sql_surface_detected');
  }
}

function response(status, body) { return { ok: status >= 200 && status < 300, status, headers: { get: () => body?.request_id || null }, async json() { return body; } }; }
function sha256(value) { return crypto.createHash('sha256').update(value).digest('hex'); }
function containsForbiddenPath(value) { return /(?:appdata[\\/]+roaming[\\/]+autoplan|library[\\/]+application support[\\/]+autoplan|\.config[\\/]+autoplan)/i.test(String(value || '')); }
function stableError(code) { const error = new Error(code); error.code = code; return error; }

function parseArgs(argv) {
  if (argv.length !== 2 || argv[0] !== '--copy') throw stableError('runtime_arguments_invalid');
  return argv[1];
}

if (require.main === module) {
  verifyLegacyRuntime(loadScaleCopy(parseArgs(process.argv.slice(2))))
    .then((result) => { process.stdout.write(`${JSON.stringify({ status: result.status, command_count: result.command_count })}\n`); })
    .catch((error) => { process.stdout.write(`${JSON.stringify({ status: 'blocked', code: error?.code || 'runtime_verification_failed' })}\n`); process.exitCode = 2; });
}

module.exports = { REQUIRED_COMMANDS, assertNoNodeDatabaseSurface, createFixtureGoBridge, loadScaleCopy, snapshotFor, validateScaleCopy, verifyLegacyRuntime };
