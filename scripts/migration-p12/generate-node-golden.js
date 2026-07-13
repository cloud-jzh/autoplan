'use strict';

// P12 golden data is a compatibility description, not a process test. The
// generator reads only the checked-in contract and writes only an explicitly
// selected artifact below fixtures/migration/p12.
const fs = require('node:fs');
const path = require('node:path');
const {
  ROOT,
  assertSanitizedContract,
  readContract,
} = require('./inventory-runtime-contract');

const FIXTURE_DIRECTORY = 'fixtures/migration/p12';
const GOLDEN_PATH = `${FIXTURE_DIRECTORY}/node-runtime.golden.json`;

function isWithin(candidate, root) {
  const relative = path.relative(path.resolve(root), path.resolve(candidate));
  return relative === '' || (!relative.startsWith('..') && !path.isAbsolute(relative));
}

function operationAccepted(type) {
  return {
    operation_id: `<operation:${type}>`,
    status: 'queued',
    request_id: `<request:${type}>`,
    accepted_at: '<utc>',
  };
}

function sseLifecycle(type, terminalStatus) {
  return [
    { sequence: 0, type: 'operation.queued', operation_id: `<operation:${type}>`, project_id: 7 },
    { sequence: 1, type: 'operation.running', operation_id: `<operation:${type}>`, project_id: 7 },
    { sequence: 2, type: `operation.${terminalStatus}`, operation_id: `<operation:${type}>`, project_id: 7 },
    { sequence: 3, type: 'project.snapshot', operation_id: `<operation:${type}>`, project_id: 7 },
  ];
}

function buildGolden(options = {}) {
  const root = path.resolve(options.rootDir || ROOT);
  const contract = options.contract || readContract(root);
  assertSanitizedContract(contract);
  const actions = Array.isArray(contract.runtime_actions) ? contract.runtime_actions : [];
  return {
    schema_version: 1,
    kind: 'p12-node-script-executor-runtime-golden',
    generator: 'scripts/migration-p12/generate-node-golden.js',
    source: 'sanitized-static-contract-only',
    safety: {
      electron_started: false,
      database_opened: false,
      process_started: false,
      external_workspace_read: false,
      environment_values_recorded: false,
      fixture_values: 'identifiers, timestamps, paths, logs and secrets are placeholders only',
    },
    deterministic_replay: {
      rule: 'A normalized request with the same persisted project/resource identity and idempotency key resolves to the same operation; generated operation ids, timestamps, pid and executor run ids are normalized to placeholders.',
      forbidden_fixture_values: ['real path', 'environment value', 'command body', 'argument value', 'pid', 'credential', 'secret'],
    },
    runtime_actions: actions.map((action) => ({
      id: action.id,
      legacy_ipc: action.legacy_ipc,
      legacy_return: action.legacy_return,
      go_command: action.go_command,
      feature_flag: action.feature_flag,
      resource: action.resource,
    })),
    dto_compatibility: {
      identifiers: {
        request_input: ['projectId', 'scriptId|executorId', 'action only for executor.action'],
        persisted_storage: ['project_id', 'script_id|executor_id'],
        snapshot_aliases: ['executor.projectId/project_id', 'executor.lastStatus/last_status', 'executor.lastExitCode/last_exit_code', 'executor.lastDurationMs/last_duration_ms', 'executor.lastLog/last_log', 'executor.lastRunAt/last_run_at'],
      },
      operation_accepted: operationAccepted('script.run'),
      operation_aliases: {
        wire: ['operation_id', 'request_id', 'accepted_at'],
        renderer_compatibility: ['operationId', 'requestId', 'acceptedAt'],
        rule: 'HTTP, MCP and SSE use snake_case wire fields; renderer adapters may expose camelCase aliases without changing the wire value or ownership.',
      },
      script_run_result: {
        required: ['snapshot', 'status', 'exitCode', 'durationMs', 'log'],
        nullable: ['exitCode', 'durationMs', 'log', 'error'],
      },
      executor_run_result: {
        required: ['snapshot', 'executorId', 'label', 'status', 'exitCode', 'durationMs', 'log'],
        optional: ['logFile', 'timedOut', 'error', 'dependencyResults', 'pid'],
      },
      stop_result: {
        node_ipc: 'AppSnapshot after the stop request settles',
        go_transition: 'OperationAccepted followed by the authoritative Operation and project SSE/snapshot state',
      },
    },
    scenarios: [
      {
        id: 'script.manual.run.succeeded',
        input: { projectId: 7, scriptId: 11 },
        node_return: { snapshot: '<snapshot>', status: 'ok', exitCode: 0, durationMs: 42, log: '<log-tail>', timedOut: false, error: null },
        persisted_last: { last_status: 'ok', last_exit_code: 0, last_duration_ms: 42, last_log: '<log-tail>', last_run_at: '<utc>' },
        node_events: [{ order: 0, type: 'script.run.succeeded', exitCode: 0, durationMs: 42, timedOut: false }],
        go_accept: operationAccepted('script.run'),
        sse: sseLifecycle('script.run', 'succeeded'),
      },
      {
        id: 'script.schedule.timeout',
        input: { projectId: 7, scriptId: 12 },
        node_return: { status: 'bad', exitCode: -1, durationMs: 60000, log: '<log-tail>', timedOut: true, error: '<stable-summary>' },
        persisted_last: { last_status: 'bad', last_exit_code: -1, last_duration_ms: 60000, last_log: '<log-tail>', last_run_at: '<utc>' },
        node_events: [{ order: 0, type: 'script.run.failed', exitCode: -1, durationMs: 60000, timedOut: true }],
        go_accept: operationAccepted('script.run'),
        sse: sseLifecycle('script.run', 'failed'),
        failure_code: 'RUNTIME_TIMEOUT',
      },
      {
        id: 'script.stop.active',
        input: { projectId: 7, scriptId: 11 },
        node_return: { snapshot: '<snapshot>' },
        node_events: [{ order: 0, type: 'script.run.failed', exitCode: -1, durationMs: 42, timedOut: false }],
        go_accept: operationAccepted('script.stop'),
        sse: sseLifecycle('script.stop', 'cancelled'),
        failure_code: 'OPERATION_CANCELLED',
      },
      {
        id: 'executor.shell.run.succeeded',
        input: { projectId: 7, executorId: 21 },
        node_return: { snapshot: '<snapshot>', executorId: 21, label: 'fixture-shell', status: 'ok', exitCode: 0, durationMs: 42, log: '<log-tail>', timedOut: false, error: null, dependencyResults: [] },
        persisted_last: { lastStatus: 'ok', last_status: 'ok', lastExitCode: 0, last_exit_code: 0, lastDurationMs: 42, last_duration_ms: 42, lastLog: '<log-tail>', last_log: '<log-tail>', lastRunAt: '<utc>', last_run_at: '<utc>' },
        node_events: [{ order: 0, type: 'executor.run.started' }, { order: 1, type: 'executor.run.succeeded', exitCode: 0, durationMs: 42, timedOut: false }],
        go_accept: operationAccepted('executor.run'),
        sse: sseLifecycle('executor.run', 'succeeded'),
      },
      {
        id: 'executor.dependency.failure',
        input: { projectId: 7, executorId: 22 },
        node_return: { snapshot: '<snapshot>', executorId: 22, label: 'fixture-root', status: 'bad', exitCode: -1, durationMs: 0, log: '<stable-summary>', timedOut: false, error: '<stable-summary>', dependencyResults: [{ executorId: 23, label: 'fixture-dependency', status: 'bad', exitCode: -1, durationMs: 0, errorMessage: '<stable-summary>' }] },
        node_events: [{ order: 0, type: 'executor.run.failed', exitCode: -1, durationMs: 0, timedOut: false }],
        go_accept: operationAccepted('executor.run'),
        sse: sseLifecycle('executor.run', 'failed'),
        failure_code: 'EXECUTOR_DEPENDENCY_FAILED',
      },
      {
        id: 'executor.plugin.start.reload.stop',
        input: { projectId: 7, executorId: 24, action: 'start' },
        node_return: { snapshot: '<snapshot>', executorId: 24, label: 'fixture-plugin', status: 'running', exitCode: null, durationMs: 0, log: '', timedOut: false, error: null, pid: '<pid>', dependencyResults: [] },
        node_events: [{ order: 0, type: 'executor.plugin.start' }, { order: 1, type: 'executor.plugin.reload' }, { order: 2, type: 'executor.plugin.stopped', exitCode: -1 }],
        persisted_plugin_state: { running: false, pid: null, lastAction: 'stop', lastActionAt: '<utc>', startedAt: '<utc>', exitCode: -1, error: null },
        go_accept: operationAccepted('executor.action'),
        sse: sseLifecycle('executor.action', 'cancelled'),
      },
    ],
    output_policy: {
      node_last_log_max_chars: 24000,
      node_truncation: 'retain the trailing 24000 characters in last_log/logBuffer; the fixture records only <log-tail>.',
      migration_metadata: ['stdout_bytes', 'stderr_bytes', 'stdout_lines', 'stderr_lines', 'stdout_truncated', 'stderr_truncated'],
      migration_rule: 'Truncation is explicit metadata; raw command, env, absolute path and unredacted output never enter Operation, SSE, snapshot, error or fixture.',
    },
    stable_failure_codes: [
      'PROJECT_NOT_FOUND', 'SCRIPT_NOT_FOUND', 'EXECUTOR_NOT_FOUND', 'RESOURCE_DISABLED',
      'ACTION_INVALID', 'OPERATION_NOT_FOUND', 'OPERATION_NOT_OWNED', 'OPERATION_ALREADY_TERMINAL',
      'RUNTIME_OWNER_MISMATCH', 'OPERATION_IN_PROGRESS', 'EXECUTOR_DEPENDENCY_MISSING',
      'EXECUTOR_DEPENDENCY_CYCLE', 'EXECUTOR_DEPENDENCY_FAILED', 'WORKSPACE_INVALID',
      'CWD_OUTSIDE_WORKSPACE', 'PROCESS_START_FAILED', 'RUNTIME_TIMEOUT', 'OPERATION_CANCELLED',
      'OUTPUT_TRUNCATED', 'RUNTIME_UNAVAILABLE',
    ],
  };
}

function stableJson(value) {
  return `${JSON.stringify(value, null, 2)}\n`;
}

function parseArgs(argv) {
  if (argv.length !== 2 || argv[0] !== '--output' || !argv[1]) {
    throw new Error('usage: node scripts/migration-p12/generate-node-golden.js --output fixtures/migration/p12/node-runtime.golden.json');
  }
  return { output: argv[1] };
}

function generate(options = {}) {
  const root = path.resolve(options.rootDir || ROOT);
  const requestedOutput = options.output || GOLDEN_PATH;
  const outputPath = path.resolve(root, requestedOutput);
  const fixtureRoot = path.resolve(root, FIXTURE_DIRECTORY);
  if (!isWithin(outputPath, fixtureRoot)) throw new Error('golden_output_must_be_under_authorized_fixture_root');
  const golden = buildGolden({ rootDir: root, contract: options.contract });
  if (options.check) {
    return { ok: stableJson(JSON.parse(fs.readFileSync(outputPath, 'utf8'))) === stableJson(golden), golden };
  }
  fs.mkdirSync(path.dirname(outputPath), { recursive: true, mode: 0o700 });
  fs.writeFileSync(outputPath, stableJson(golden), 'utf8');
  return { ok: true, golden, artifact: { path: outputPath } };
}

if (require.main === module) {
  try {
    const result = generate(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify({ ok: result.ok, artifact: result.artifact || null })}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch (error) {
    process.stdout.write(`${JSON.stringify({ ok: false, code: error?.message || 'node_golden_generation_failed' })}\n`);
    process.exitCode = 2;
  }
}

module.exports = {
  FIXTURE_DIRECTORY,
  GOLDEN_PATH,
  buildGolden,
  generate,
  parseArgs,
  stableJson,
};
