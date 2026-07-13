'use strict';

// This generator is intentionally a pure compatibility fixture builder.  It
// invokes only argument/configuration helpers and records them through a fake
// launcher; it never calls child_process, Electron, userData, or a real CLI.
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const {
  agentCliSpawnSpec,
  codexNewSessionArgs,
  codexResumeSessionArgs,
} = require('../../src/agentCli');
const { readContract } = require('./inventory-runtime-contract');

const ROOT = path.resolve(__dirname, '..', '..');
const FIXTURE_DIRECTORY = 'fixtures/migration/p11';
const GOLDEN_PATH = `${FIXTURE_DIRECTORY}/node-runtime.golden.json`;
const TEMPORARY_PREFIX = 'autoplan-p11-node-golden-';
const FIXTURE_CODEX_SESSION = '11111111-2222-4333-8444-555555555555';
const FIXTURE_AGENT_SESSION = 'fixture-session-0001';
const FIXTURE_LAST_FILE = '<fixture-last-file>';
const FIXTURE_TITLE = 'AutoPlan project 7 plan 11';
const CLAUDE_ENVIRONMENT_OVERRIDE_NAMES = Object.freeze([
  'ANTHROPIC_BASE_URL',
  'ANTHROPIC_AUTH_TOKEN',
  'ANTHROPIC_MODEL',
]);

function isWithin(candidate, root) {
  const relative = path.relative(path.resolve(root), path.resolve(candidate));
  return relative === '' || (!relative.startsWith('..') && !path.isAbsolute(relative));
}

function normalizedArgs(args) {
  return (args || []).map((value) => {
    if (value === FIXTURE_LAST_FILE) return '<last-file>';
    if (value === FIXTURE_CODEX_SESSION || value === FIXTURE_AGENT_SESSION) return '<session-id>';
    return value;
  });
}

class FakeProcessLauncher {
  constructor(workspace) {
    this.workspace = workspace;
    this.launches = [];
  }

  launch(label, spec) {
    if (!spec || !Array.isArray(spec.args) || !spec.command || spec.command.includes(path.sep)) {
      throw new Error('fake_process_spec_invalid');
    }
    const record = {
      label,
      provider: spec.agentCliProvider,
      command: spec.command,
      args: normalizedArgs(spec.args),
      prompt_source: spec.promptSource,
      last_file_source: spec.lastFileSource,
      node_windows_shell_compatibility: Boolean(spec.useShell),
      environment_override_names: Object.keys(spec.env || {}).sort(),
      cwd: '<fixture-workspace>',
      fake_exit_code: 0,
    };
    this.launches.push(record);
    return record;
  }
}

function captureSpec(fake, label, provider, command, codexArgs, options) {
  return fake.launch(label, agentCliSpawnSpec(provider, command, FIXTURE_LAST_FILE, codexArgs, options));
}

function providerGolden(workspace) {
  const fake = new FakeProcessLauncher(workspace);
  const records = [
    captureSpec(fake, 'codex.new', 'codex', '', codexNewSessionArgs(FIXTURE_LAST_FILE, { reasoningEffort: 'high' }), {}),
    captureSpec(fake, 'codex.resume', 'codex', '', codexResumeSessionArgs(FIXTURE_CODEX_SESSION, FIXTURE_LAST_FILE, { reasoningEffort: 'high' }), {}),
    captureSpec(fake, 'claude.new', 'claude', '', [], {}),
    captureSpec(fake, 'claude.resume', 'claude', '', [], { sessionMode: 'resume', requestedSessionId: FIXTURE_AGENT_SESSION }),
    captureSpec(fake, 'opencode.new', 'opencode', '', [], { title: FIXTURE_TITLE, agent: 'autoplan-plan', structuredPlan: true }),
    captureSpec(fake, 'opencode.resume', 'opencode', '', [], { sessionId: FIXTURE_AGENT_SESSION, title: FIXTURE_TITLE }),
    captureSpec(fake, 'oh-my-pi.new', 'oh-my-pi', '', [], {}),
  ];
  const custom_commands = ['codex', 'claude', 'opencode', 'oh-my-pi'].map((provider) => {
    const spec = agentCliSpawnSpec(provider, 'fixture-agent', FIXTURE_LAST_FILE, [], {});
    return { provider, configured_command: 'fixture-agent', resolved_command: spec.command };
  });
  return { records, custom_commands };
}

function actionGolden(contract) {
  return contract.actions.map((action) => ({
    id: action.id,
    legacy_ipc: action.legacy_ipc,
    legacy_return: action.legacy_return,
    go_return: action.go_return,
    feature_flag: action.feature_flag,
  }));
}

function buildGolden(options = {}) {
  const root = path.resolve(options.rootDir || ROOT);
  const contract = options.contract || readContract(root);
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), TEMPORARY_PREFIX));
  const workspace = path.join(temporaryRoot, 'fixture-workspace');
  fs.mkdirSync(workspace, { recursive: true, mode: 0o700 });
  try {
    const providers = providerGolden(workspace);
    return {
      schema_version: 1,
      kind: 'p11-node-runtime-golden',
      generator: 'scripts/migration-p11/generate-node-golden.js',
      source: 'sanitized-fixture-and-fake-process-only',
      safety: {
        real_agent_cli_started: false,
        electron_userdata_read: false,
        real_configuration_read: false,
        process_launcher: 'fake',
        workspace: '<fixture-workspace>',
        secrets_or_environment_values_recorded: false,
      },
      runtime_actions: actionGolden(contract),
      agent_cli: {
        default_commands: { codex: 'codex', claude: 'claude', opencode: 'opencode', 'oh-my-pi': 'omp' },
        custom_commands: providers.custom_commands,
        claude_environment_override_names: CLAUDE_ENVIRONMENT_OVERRIDE_NAMES,
        fake_process_records: providers.records,
        session_semantics: {
          codex: 'new_or_resume_then_fallback_new_when_resume_error_matches',
          claude: 'new_continue_session_id_or_resume_then_fallback_new_when_session_missing',
          opencode: 'plan_session_title_lookup_and_resume_then_fallback_new_when_session_missing',
          'oh-my-pi': 'stateless_new_only',
        },
      },
      retry_and_recovery: {
        task_retry_backoff_seconds: [5, 10, 20, 30],
        task_timeout_opens_new_session: true,
        environment_blocked_does_not_retry: true,
        startup_recovery: 'active_loop_stopped_and_running_tasks_reset_pending_without_relaunching_child',
      },
    };
  } finally {
    fs.rmSync(temporaryRoot, { recursive: true, force: true });
  }
}

function stableJson(value) {
  return `${JSON.stringify(value, null, 2)}\n`;
}

function writeGolden(outputPath, golden) {
  fs.mkdirSync(path.dirname(outputPath), { recursive: true, mode: 0o700 });
  fs.writeFileSync(outputPath, stableJson(golden), { encoding: 'utf8' });
  return {
    path: outputPath,
    sha256: crypto.createHash('sha256').update(stableJson(golden), 'utf8').digest('hex'),
  };
}

function parseArgs(argv) {
  if (argv.length !== 2 || argv[0] !== '--output' || !argv[1]) {
    throw new Error('usage: node scripts/migration-p11/generate-node-golden.js --output fixtures/migration/p11/node-runtime.golden.json');
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
    const current = fs.readFileSync(outputPath, 'utf8');
    // Formatting is intentionally not part of the fixture contract.  Compare
    // parsed JSON so a reviewer can keep long argument vectors readable.
    return { ok: stableJson(JSON.parse(current)) === stableJson(golden), golden };
  }
  return { ok: true, golden, artifact: writeGolden(outputPath, golden) };
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
  CLAUDE_ENVIRONMENT_OVERRIDE_NAMES,
  FIXTURE_DIRECTORY,
  GOLDEN_PATH,
  FakeProcessLauncher,
  buildGolden,
  generate,
  parseArgs,
  providerGolden,
};
