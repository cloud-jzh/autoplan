'use strict';

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(__dirname, '..', '..');
const MAX_SOURCE_BYTES = 2 * 1024 * 1024;
const MAX_RUNTIME_EVIDENCE_BYTES = 64 * 1024;
const RENDERER_DIRECT_DATABASE = /(?:require\(\s*['"][^'"]*(?:database|sql\.js|sqlite)[^'"]*['"]\s*\)|from\s+['"][^'"]*(?:database|sql\.js|sqlite)[^'"]*['"]|\bnew\s+(?:Database|SQL\.Database)\b|\bopenDatabase\s*\()/i;

function blocked(code, extra = {}) { return { ok: false, code, ...extra }; }

function readSource(relative) {
  const file = path.join(ROOT, relative);
  const info = fs.lstatSync(file, { throwIfNoEntry: false });
  if (!info?.isFile() || info.isSymbolicLink() || info.size > MAX_SOURCE_BYTES) return null;
  return fs.readFileSync(file, 'utf8');
}

function listSourceFiles(relative) {
  const root = path.join(ROOT, relative);
  const info = fs.lstatSync(root, { throwIfNoEntry: false });
  if (!info?.isDirectory() || info.isSymbolicLink()) return null;
  const files = [];
  const walk = (directory) => {
    for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
      const target = path.join(directory, entry.name);
      if (entry.isSymbolicLink()) throw new Error('writer_source_symlink');
      if (entry.isDirectory()) walk(target);
      else if (entry.isFile() && /\.(?:[cm]?js|tsx?)$/i.test(entry.name)) files.push(target);
    }
  };
  try { walk(root); } catch { return null; }
  return files;
}

function inspectRendererBoundary() {
  const files = ['src/renderer/lib/api', 'src/renderer/lib/desktop'].flatMap((directory) => listSourceFiles(directory) || []);
  if (files.length === 0) return blocked('writer_renderer_source_missing');
  const violations = [];
  for (const file of files) {
    const info = fs.lstatSync(file, { throwIfNoEntry: false });
    if (!info?.isFile() || info.size > MAX_SOURCE_BYTES) return blocked('writer_renderer_source_invalid');
    const relative = path.relative(ROOT, file).replaceAll('\\', '/');
    const text = fs.readFileSync(file, 'utf8');
    if (RENDERER_DIRECT_DATABASE.test(text)) violations.push(relative);
  }
  return violations.length ? blocked('writer_renderer_direct_database_reference', { violations }) : {
    ok: true, code: 'writer_renderer_transport_only', inspected_files: files.length,
  };
}

function inspectNodeOwnerGuard() {
  const main = readSource('src/main.js');
  const guard = readSource('src/data/databaseOwnerGuard.js');
  if (!main || !guard) return blocked('writer_node_guard_source_missing');
  const anchors = [
    'selectProcessDatabaseOwner({ env: process.env })',
    'databaseOwner.owner === DATABASE_OWNERS.GO',
    'db = createGoModeDatabaseBlocker()',
    'await daemonSupervisor.start()',
    'new AppDatabase(',
  ];
  const guardAnchors = ['NODE_SQL_FORBIDDEN', 'assertNodeMutationAllowed()', 'assertSqlJsAllowed(owner, databasePath'];
  const missing = anchors.filter((anchor) => !main.includes(anchor));
  const missingGuard = guardAnchors.filter((anchor) => !guard.includes(anchor));
  if (missing.length || missingGuard.length) return blocked('writer_node_guard_contract_invalid', {
    missing: [...missing, ...missingGuard],
  });
  const goBranch = main.slice(main.indexOf('if (databaseOwner.owner === DATABASE_OWNERS.GO)'), main.indexOf('} else {', main.indexOf('if (databaseOwner.owner === DATABASE_OWNERS.GO)')));
  if (!goBranch || /new\s+AppDatabase\s*\(/.test(goBranch) || /\.persist\s*\(/.test(goBranch)) {
    return blocked('writer_node_go_branch_not_inert');
  }
  return { ok: true, code: 'writer_node_go_branch_inert' };
}

function inspectGoOwner() {
  const lock = readSource('backend/internal/platform/instance/database_lock.go');
  const connection = readSource('backend/internal/repository/sqlite/connection.go');
  const startup = readSource('backend/internal/bootstrap/database.go');
  if (!lock || !connection || !startup) return blocked('writer_go_owner_source_missing');
  const required = [
    [lock, 'AcquireDatabaseLock'], [lock, 'ErrDatabaseOwnerLocked'], [lock, 'os.O_EXCL'],
    [connection, 'database.SetMaxOpenConns(1)'], [connection, 'database.SetMaxIdleConns(1)'],
    [startup, 'owner, err := callAcquireDatabaseOwner'], [startup, 'connection, err := callOpenStartupDatabase'],
    [startup, 'connection before the process-wide lock'],
  ];
  if (required.some(([source, token]) => !source.includes(token))) return blocked('writer_go_owner_contract_invalid');
  if (startup.indexOf('owner, err := callAcquireDatabaseOwner') > startup.indexOf('connection, err := callOpenStartupDatabase')) {
    return blocked('writer_go_connection_precedes_owner');
  }
  return { ok: true, code: 'writer_go_lock_before_connection' };
}

function inspectRuntimeEvidence(value) {
  if (!value) return { ok: true, code: 'writer_runtime_evidence_not_requested', observed: false };
  const file = path.resolve(value);
  const relative = path.relative(ROOT, file);
  const info = fs.lstatSync(file, { throwIfNoEntry: false });
  if (!relative || relative.startsWith('..') || path.isAbsolute(relative) || !info?.isFile() || info.isSymbolicLink() || info.size > MAX_RUNTIME_EVIDENCE_BYTES) {
    return blocked('writer_runtime_evidence_invalid');
  }
  let evidence;
  try { evidence = JSON.parse(fs.readFileSync(file, 'utf8')); } catch { return blocked('writer_runtime_evidence_invalid'); }
  if (evidence?.schema_version !== 1 || evidence?.kind !== 'p15-electron-go-runtime-result' ||
      evidence?.go_database_write_count < 1 || evidence?.node_database_open_count !== 0 ||
      evidence?.node_database_write_count !== 0 || evidence?.active_database_owner !== 'go') {
    return blocked('writer_runtime_evidence_failed');
  }
  return { ok: true, code: 'writer_runtime_evidence_accepted', observed: true };
}

function checkUniqueWriter(options = {}) {
  const renderer = inspectRendererBoundary();
  const node = inspectNodeOwnerGuard();
  const go = inspectGoOwner();
  const runtime = inspectRuntimeEvidence(options.runtimeEvidence);
  const failures = [renderer, node, go, runtime].filter((item) => !item.ok).map((item) => item.code);
  return {
    schema_version: 1,
    kind: 'p15-unique-writer-gate',
    status: failures.length ? 'blocked' : 'ready',
    ok: failures.length === 0,
    code: failures[0] || 'p15_unique_writer_ready',
    failures,
    renderer: { ok: renderer.ok, code: renderer.code },
    node: { ok: node.ok, code: node.code },
    go: { ok: go.ok, code: go.code },
    runtime: { ok: runtime.ok, code: runtime.code, observed: runtime.observed === true },
  };
}

function parseArgs(argv) {
  if (argv.length === 0) return {};
  if (argv.length === 2 && argv[0] === '--runtime-evidence' && argv[1]) return { runtimeEvidence: argv[1] };
  throw new Error('arguments_invalid');
}

if (require.main === module) {
  try {
    const result = checkUniqueWriter(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify(result)}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch {
    process.stdout.write('{"status":"blocked","code":"p15_unique_writer_arguments_invalid"}\n');
    process.exitCode = 2;
  }
}

module.exports = { ROOT, checkUniqueWriter, inspectGoOwner, inspectNodeOwnerGuard, inspectRendererBoundary, inspectRuntimeEvidence, parseArgs };
