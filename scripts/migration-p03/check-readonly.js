'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(__dirname, '../..');
const AUTHORIZED_TABLES = new Set(['projects', 'settings', 'project_states']);
const REPOSITORY_FILES = [
  'backend/internal/repository/repository.go',
  'backend/internal/repository/sqlite/readonly.go',
  'backend/internal/repository/sqlite/projects.go',
  'backend/internal/repository/sqlite/settings.go',
  'backend/internal/repository/sqlite/project_states.go',
];
const ROUTE_FILES = [
  'backend/internal/httpapi/router.go',
  'backend/internal/httpapi/projects.go',
];
const OPENAPI_FILE = 'backend/openapi/openapi.yaml';
const SIDECAR_SUFFIXES = ['-wal', '-shm', '-journal', '.lock', '.autoplan-server.lock'];

function sha256File(file) {
  return crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
}

function finding(file, rule, detail) {
  return { file: String(file).replace(/\\/g, '/'), rule, detail };
}

function scanRepositoryText(file, source) {
  const findings = [];
  const rules = [
    ['database-engine', /(?:"database\/sql"|modernc\.org\/sqlite|mattn\/go-sqlite3|zombiezen\.com\/go\/sqlite)/i,
      'database engines are outside the immutable reader boundary'],
    ['sql-execution', /\.(?:Exec|ExecContext|Prepare|PrepareContext|Begin|BeginTx)\s*\(/,
      'SQL execution or transactions are forbidden'],
    ['write-file-api', /\bos\.(?:Create|CreateTemp|WriteFile|OpenFile|Mkdir|MkdirAll|Remove|RemoveAll|Rename|Truncate|Chmod|Chown|Lchown|Chtimes|Link|Symlink)\s*\(|\.(?:Write|WriteAt|WriteString)\s*\(/,
      'repository source must not create or mutate filesystem entries'],
    ['mutation-method', /func\s*\([^)]*\*(?:Reader|Repository|Store)\s*\)\s*(?:Create|Insert|Update|Upsert|Save|Delete|Remove|Migrate|Apply|Write)\w*\s*\(/,
      'repository exposes a mutation method'],
    ['write-sql', /["'`]\s*(?:INSERT(?:\s+OR\s+\w+)?\s+INTO|UPDATE\s+\w+|DELETE\s+FROM|REPLACE\s+INTO|ALTER\s+TABLE|DROP\s+(?:TABLE|INDEX)|CREATE\s+(?:TABLE|INDEX)|VACUUM\b|ATTACH\b|DETACH\b|REINDEX\b|PRAGMA\s+[^;\n]*(?:=|\())/i,
      'write SQL or database configuration is forbidden'],
    ['automatic-migration', /\b(?:AutoMigrate|RunMigrations?|ApplyMigrations?|CreateDatabase|InitializeDatabase)\s*\(/i,
      'automatic migration or database creation is forbidden'],
    ['user-data-discovery', /\b(?:os\.UserHomeDir|user\.Current)\s*\(|\b(?:os\.LookupEnv|os\.Getenv)\s*\([^\n]*(?:APPDATA|LOCALAPPDATA|HOME|USERPROFILE)|app\.getPath\s*\(\s*["']userData["']|\belectron\b/i,
      'production user-data discovery is forbidden'],
  ];
  for (const [rule, pattern, detail] of rules) {
    if (pattern.test(source)) findings.push(finding(file, rule, detail));
  }
  const readPattern = /\.readRows\s*\(\s*[^,]+,\s*["'`]([a-z][a-z0-9_]*)["'`]/gi;
  const readCalls = [...source.matchAll(/\.readRows\s*\(/g)].length;
  let literalReadCalls = 0;
  for (const match of source.matchAll(readPattern)) {
    literalReadCalls += 1;
    if (!AUTHORIZED_TABLES.has(match[1].toLowerCase())) {
      findings.push(finding(file, 'unauthorized-table', `readRows references ${match[1]}`));
    }
  }
  if (readCalls !== literalReadCalls) {
    findings.push(finding(file, 'dynamic-table', 'readRows table names must be fixed string literals'));
  }
  return findings;
}

function scanRouteText(file, source) {
  const findings = [];
  if (/http\.Method(?:Post|Put|Patch|Delete)\b/.test(source)) {
    findings.push(finding(file, 'write-route', 'P03 project routes may register only GET and HEAD'));
  }
  if (/\.(?:Handle|HandlePattern)\s*\(\s*["'](?:POST|PUT|PATCH|DELETE)["']/i.test(source)) {
    findings.push(finding(file, 'write-route', 'literal write method registration is forbidden'));
  }
  if (/\b(?:Create|Update|Delete|Save|Upsert)Project\s*\(/.test(source)) {
    findings.push(finding(file, 'write-handler', 'project mutation handler is forbidden'));
  }
  return findings;
}

function scanOpenAPIText(file, source) {
  const findings = [];
  const serverURLs = [...source.matchAll(/^\s*-\s+url:\s*([^\s#]+)\s*$/gmi)].map((match) => match[1]);
  if (!serverURLs.length || serverURLs.some((url) => !/^http:\/\/127\.0\.0\.1:\{port\}\/?$/.test(url))) {
    findings.push(finding(file, 'non-loopback-server', 'OpenAPI servers must use only 127.0.0.1 with the runtime port'));
  }
  let currentPath = '';
  source.split(/\r?\n/).forEach((line) => {
    const pathMatch = /^  (\/[^:]+):\s*$/.exec(line);
    if (pathMatch) currentPath = pathMatch[1];
    const operation = /^    (post|put|patch|delete):\s*$/i.exec(line);
    if (operation && currentPath.startsWith('/api/v1/projects')) {
      findings.push(finding(file, 'write-operation', `${operation[1].toUpperCase()} ${currentPath}`));
    }
  });
  return findings;
}

function scanSourceText(kind, file, source) {
  if (kind === 'repository') return scanRepositoryText(file, source);
  if (kind === 'route') return scanRouteText(file, source);
  if (kind === 'openapi') return scanOpenAPIText(file, source);
  throw new TypeError(`unknown readonly scan kind: ${kind}`);
}

function scanReadonlySources(rootDir = ROOT) {
  const root = path.resolve(rootDir);
  const findings = [];
  for (const file of REPOSITORY_FILES) {
    findings.push(...scanSourceText('repository', file, fs.readFileSync(path.join(root, file), 'utf8')));
  }
  for (const file of ROUTE_FILES) {
    findings.push(...scanSourceText('route', file, fs.readFileSync(path.join(root, file), 'utf8')));
  }
  findings.push(...scanSourceText('openapi', OPENAPI_FILE, fs.readFileSync(path.join(root, OPENAPI_FILE), 'utf8')));
  return {
    ok: findings.length === 0,
    inspectedFiles: [...REPOSITORY_FILES, ...ROUTE_FILES, OPENAPI_FILE],
    authorizedTables: [...AUTHORIZED_TABLES].sort(),
    findings,
  };
}

function sidecars(file) {
  return SIDECAR_SUFFIXES.filter((suffix) => fs.existsSync(`${file}${suffix}`));
}

async function verifyFileUnchanged(file, operation) {
  const target = path.resolve(file);
  const beforeStat = fs.statSync(target);
  if (!beforeStat.isFile()) throw new Error('readonly target is not a regular file');
  const before = sha256File(target);
  const beforeSidecars = sidecars(target);
  if (beforeSidecars.length) throw new Error('readonly target has an active sidecar');
  const value = await operation({ path: target, sha256: before });
  const afterStat = fs.statSync(target);
  const after = sha256File(target);
  const afterSidecars = sidecars(target);
  if (before !== after || beforeStat.size !== afterStat.size || afterSidecars.length) {
    throw new Error('readonly target changed during operation');
  }
  return { value, beforeSha256: before, afterSha256: after, unchanged: true };
}

function assertReadonly(rootDir = ROOT) {
  const result = scanReadonlySources(rootDir);
  if (!result.ok) {
    const labels = result.findings.map((item) => `${item.file}:${item.rule}`).join(', ');
    throw new Error(`P03 readonly guard failed: ${labels}`);
  }
  return result;
}

if (require.main === module) {
  try {
    const result = assertReadonly();
    process.stdout.write(`${JSON.stringify({
      ok: true,
      inspectedFiles: result.inspectedFiles.length,
      authorizedTables: result.authorizedTables,
      loopbackOnly: true,
    })}\n`);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}

module.exports = {
  AUTHORIZED_TABLES,
  OPENAPI_FILE,
  REPOSITORY_FILES,
  ROUTE_FILES,
  SIDECAR_SUFFIXES,
  assertReadonly,
  scanOpenAPIText,
  scanReadonlySources,
  scanRepositoryText,
  scanRouteText,
  scanSourceText,
  sha256File,
  verifyFileUnchanged,
};
