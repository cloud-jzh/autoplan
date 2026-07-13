'use strict';

const fs = require('node:fs');
const path = require('node:path');
const {
  inspectEvidenceFile, isWithin, readSafeJSON, scanSensitiveText, validateFixtureRoot,
} = require('./check-safety');

const ROOT = path.resolve(__dirname, '..', '..');
const SCENARIOS_FILE = 'trace-scenarios.json';
const TRACE_KINDS = new Set([
  'renderer_to_go_transport', 'mcp_to_go_application', 'go_application', 'go_repository',
  'database_open', 'database_write', 'process_lifecycle', 'sse', 'terminal_websocket', 'ipc',
]);
const OWNERS = new Set(['go', 'node', 'electron']);
const CHANNEL_PATTERN = /^(?:snapshot|[a-z][a-z0-9-]{0,63}:[a-z][A-Za-z0-9-]{0,63})$/;

function loadScenarios(fixtureRoot) {
  const target = path.join(fixtureRoot, SCENARIOS_FILE);
  const parsed = readSafeJSON(target, fixtureRoot);
  if (!parsed.ok || parsed.value?.schema_version !== 1 || parsed.value?.kind !== 'p15-runtime-trace-scenarios' || !Array.isArray(parsed.value?.scenarios)) {
    return { ok: false, code: parsed.ok ? 'trace_scenarios_invalid' : parsed.code };
  }
  const ids = new Set();
  for (const scenario of parsed.value.scenarios) {
    if (!scenario || !/^[a-z][a-z0-9_]{1,63}$/.test(scenario.id || '') || ids.has(scenario.id) ||
        !Array.isArray(scenario.domains) || scenario.domains.length === 0 ||
        !Array.isArray(scenario.must_observe) || scenario.must_observe.length === 0 ||
        scenario.domains.some((domain) => !/^[a-z][a-z0-9_]{1,63}$/.test(domain)) ||
        scenario.must_observe.some((kind) => !TRACE_KINDS.has(kind))) {
      return { ok: false, code: 'trace_scenarios_invalid' };
    }
    ids.add(scenario.id);
  }
  return { ok: true, code: 'trace_scenarios_valid', scenarios: parsed.value.scenarios };
}

function validateEvent(event, scenarios) {
  const allowedKeys = new Set(['sequence', 'scenario', 'domain', 'kind', 'owner', 'channel', 'bytes']);
  if (!event || typeof event !== 'object' || Array.isArray(event) || Object.keys(event).some((key) => !allowedKeys.has(key)) ||
      !Number.isSafeInteger(event.sequence) || event.sequence < 1 || !scenarios.has(event.scenario) ||
      typeof event.domain !== 'string' || !TRACE_KINDS.has(event.kind) || !OWNERS.has(event.owner)) {
    return { ok: false, code: 'trace_event_invalid' };
  }
  const domains = scenarios.get(event.scenario);
  if (!domains?.has(event.domain)) return { ok: false, code: 'trace_domain_invalid' };
  if (event.kind === 'ipc') {
    if (!CHANNEL_PATTERN.test(event.channel || '')) return { ok: false, code: 'trace_ipc_channel_invalid' };
  } else if (Object.hasOwn(event, 'channel')) return { ok: false, code: 'trace_event_invalid' };
  if (Object.hasOwn(event, 'bytes') && (!Number.isSafeInteger(event.bytes) || event.bytes < 0 || event.bytes > 1024 * 1024)) {
    return { ok: false, code: 'trace_event_invalid' };
  }
  if (event.owner === 'node' && ['database_open', 'database_write', 'go_application', 'go_repository'].includes(event.kind)) {
    return { ok: false, code: 'node_business_owner_observed' };
  }
  if (event.kind === 'database_write' && event.owner !== 'go') return { ok: false, code: 'database_writer_not_go' };
  if (['go_application', 'go_repository', 'database_open', 'database_write'].includes(event.kind) && event.owner !== 'go') {
    return { ok: false, code: 'go_owner_boundary_invalid' };
  }
  if (event.kind === 'terminal_websocket' && event.owner !== 'go') return { ok: false, code: 'terminal_websocket_owner_invalid' };
  return { ok: true, code: 'trace_event_valid' };
}

function validateTrace(trace, scenarios) {
  if (!trace || trace.schema_version !== 1 || trace.kind !== 'p15-runtime-trace' ||
      !/^[A-Za-z0-9._-]{1,160}$/.test(trace.run_id || '') || !Array.isArray(trace.events) || trace.events.length === 0) {
    return { ok: false, code: 'trace_shape_invalid' };
  }
  if (Object.keys(trace).some((key) => !['schema_version', 'kind', 'run_id', 'source_revision', 'events'].includes(key)) ||
      (trace.source_revision !== undefined && !/^[A-Za-z0-9._/-]{1,160}$/.test(trace.source_revision))) {
    return { ok: false, code: 'trace_shape_invalid' };
  }
  const names = new Map(scenarios.map((scenario) => [scenario.id, new Set(scenario.domains)]));
  let sequence = 0;
  const observed = new Map(scenarios.map((scenario) => [scenario.id, new Set()]));
  const channels = new Set();
  for (const event of trace.events) {
    const result = validateEvent(event, names);
    if (!result.ok) return result;
    if (event.sequence <= sequence) return { ok: false, code: 'trace_sequence_invalid' };
    sequence = event.sequence;
    observed.get(event.scenario).add(event.kind);
    if (event.kind === 'ipc') channels.add(event.channel);
  }
  const missing = scenarios
    .filter((scenario) => scenario.must_observe.some((kind) => !observed.get(scenario.id).has(kind)))
    .map((scenario) => ({ scenario: scenario.id, missing: scenario.must_observe.filter((kind) => !observed.get(scenario.id).has(kind)) }));
  return {
    ok: missing.length === 0,
    code: missing.length ? 'trace_coverage_incomplete' : 'trace_owner_evidence_complete',
    missing,
    event_count: trace.events.length,
    ipc_channels: [...channels].sort(),
  };
}

/**
 * Small, payload-free recorder for the later isolated Electron+Go runner.
 * It intentionally records topology facts only; callers must never pass a
 * command, path, database value, environment field, PID, output or secret.
 */
function createTraceRecorder(options = {}) {
  const fixtureRoot = path.resolve(options.fixtureRoot || '');
  const fixture = validateFixtureRoot(fixtureRoot);
  if (!fixture.ok) throw new Error(fixture.code);
  const loaded = loadScenarios(fixtureRoot);
  if (!loaded.ok) throw new Error(loaded.code);
  const runId = String(options.runId || '');
  if (!/^[A-Za-z0-9._-]{1,160}$/.test(runId)) throw new Error('trace_run_id_invalid');
  const sourceRevision = options.sourceRevision;
  if (sourceRevision !== undefined && !/^[A-Za-z0-9._/-]{1,160}$/.test(sourceRevision)) throw new Error('trace_source_revision_invalid');
  const scenarioDomains = new Map(loaded.scenarios.map((scenario) => [scenario.id, new Set(scenario.domains)]));
  const events = [];
  return Object.freeze({
    record(input = {}) {
      const event = { ...input, sequence: events.length + 1 };
      const result = validateEvent(event, scenarioDomains);
      if (!result.ok) throw new Error(result.code);
      events.push(Object.freeze(event));
      return event.sequence;
    },
    trace() {
      return Object.freeze({
        schema_version: 1,
        kind: 'p15-runtime-trace',
        run_id: runId,
        ...(sourceRevision === undefined ? {} : { source_revision: sourceRevision }),
        events: Object.freeze(events.map((event) => ({ ...event }))),
      });
    },
    validate() { return validateTrace(this.trace(), loaded.scenarios); },
  });
}

function traceRuntime(options = {}) {
  const fixtureRoot = path.resolve(options.fixtureRoot || '');
  const fixture = validateFixtureRoot(fixtureRoot);
  if (!fixture.ok) return { schema_version: 1, status: 'blocked', ok: false, code: fixture.code, fixture };
  const scenarios = loadScenarios(fixtureRoot);
  if (!scenarios.ok) return { schema_version: 1, status: 'blocked', ok: false, code: scenarios.code, fixture };
  if (!options.tracePath) return { schema_version: 1, status: 'blocked', ok: false, code: 'runtime_trace_missing', fixture, scenario_count: scenarios.scenarios.length };
  const tracePath = path.resolve(options.tracePath);
  if (!isWithin(tracePath, fixtureRoot)) return { schema_version: 1, status: 'blocked', ok: false, code: 'runtime_trace_outside_fixture_rejected', fixture };
  const inspection = inspectEvidenceFile(tracePath, fixtureRoot);
  if (!inspection.ok) return { schema_version: 1, status: 'blocked', ok: false, code: inspection.code, fixture };
  let trace;
  try { trace = JSON.parse(fs.readFileSync(tracePath, 'utf8')); } catch { return { schema_version: 1, status: 'blocked', ok: false, code: 'runtime_trace_json_invalid', fixture }; }
  if (scanSensitiveText(JSON.stringify(trace)).length) return { schema_version: 1, status: 'blocked', ok: false, code: 'runtime_trace_sensitive', fixture };
  const result = validateTrace(trace, scenarios.scenarios);
  return {
    schema_version: 1,
    kind: 'p15-runtime-trace-check',
    status: result.ok ? 'ready' : 'blocked',
    ok: result.ok,
    code: result.code,
    fixture,
    scenario_count: scenarios.scenarios.length,
    event_count: result.event_count || 0,
    missing: result.missing || [],
    ipc_channels: result.ipc_channels || [],
  };
}

function parseArgs(argv) {
  if (argv.length !== 4 || argv[0] !== '--fixture-root' || !argv[1] || argv[2] !== '--trace' || !argv[3]) throw new Error('arguments_invalid');
  return { fixtureRoot: argv[1], tracePath: argv[3] };
}

if (require.main === module) {
  try {
    const result = traceRuntime(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify({ status: result.status, ok: result.ok, code: result.code, scenario_count: result.scenario_count || 0, event_count: result.event_count || 0 })}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch {
    process.stdout.write('{"status":"blocked","code":"p15_runtime_trace_arguments_invalid"}\n');
    process.exitCode = 2;
  }
}

module.exports = { CHANNEL_PATTERN, ROOT, SCENARIOS_FILE, TRACE_KINDS, createTraceRecorder, loadScenarios, parseArgs, traceRuntime, validateEvent, validateTrace };
