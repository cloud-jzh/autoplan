'use strict';

const fs = require('node:fs');
const path = require('node:path');

const INVENTORY_PATH = 'docs/migration/p00/database-and-dependencies.json';
const DATABASE_PATH = 'src/database.js';
const NON_TABLE_SQL_WORDS = new Set(['directory', 'download', 'stdout']);

function readText(rootDir, relativePath) {
  return fs.readFileSync(path.join(rootDir, relativePath), 'utf8');
}

function sortedUnique(values) {
  return [...new Set(values.filter(Boolean))].sort((left, right) => left.localeCompare(right));
}

function normalizeSql(value) {
  return String(value || '').replace(/\s+/g, ' ').trim();
}

function balancedEnd(source, openIndex, openChar = '(', closeChar = ')') {
  let depth = 0;
  let quote = null;
  let escaped = false;
  for (let index = openIndex; index < source.length; index += 1) {
    const char = source[index];
    if (quote) {
      if (escaped) escaped = false;
      else if (char === '\\') escaped = true;
      else if (char === quote) quote = null;
      continue;
    }
    if (char === '"' || char === "'" || char === '`') {
      quote = char;
      continue;
    }
    if (char === openChar) depth += 1;
    if (char === closeChar) depth -= 1;
    if (depth === 0) return index;
  }
  return -1;
}

function splitSqlList(source) {
  const parts = [];
  let start = 0;
  let paren = 0;
  let quote = null;
  for (let index = 0; index < source.length; index += 1) {
    const char = source[index];
    if (quote) {
      if (char === quote && source[index - 1] !== '\\') quote = null;
      continue;
    }
    if (char === '"' || char === "'") quote = char;
    else if (char === '(') paren += 1;
    else if (char === ')') paren -= 1;
    else if (char === ',' && paren === 0) {
      parts.push(source.slice(start, index));
      start = index + 1;
    }
  }
  if (source.slice(start).trim()) parts.push(source.slice(start));
  return parts;
}

function extractCreateTables(source) {
  const tables = new Map();
  const pattern = /CREATE\s+TABLE(?:\s+IF\s+NOT\s+EXISTS)?\s+([a-z_][a-z0-9_]*)\s*\(/gi;
  for (const match of source.matchAll(pattern)) {
    const name = match[1].toLowerCase();
    const open = source.indexOf('(', match.index);
    const close = balancedEnd(source, open);
    if (close < 0) throw new Error(`CREATE TABLE 未闭合：${name}`);
    const columns = tables.get(name) || new Map();
    for (const raw of splitSqlList(source.slice(open + 1, close))) {
      const definition = normalizeSql(raw);
      if (!definition || /^(?:PRIMARY|UNIQUE|CHECK|FOREIGN|CONSTRAINT)\b/i.test(definition)) continue;
      const column = /^(?:\[([^\]]+)\]|"([^"]+)"|`([^`]+)`|([a-z_][a-z0-9_]*))\s+(.+)$/i.exec(definition);
      if (!column) continue;
      const columnName = (column[1] || column[2] || column[3] || column[4]).toLowerCase();
      if (!columns.has(columnName)) columns.set(columnName, normalizeSql(column[5]));
    }
    tables.set(name, columns);
  }
  return tables;
}

function extractEnsureColumns(source) {
  const result = new Map();
  const pattern = /this\.ensureColumn\(\s*'([^']+)'\s*,\s*'([^']+)'\s*,\s*(?:'([^']*)'|"([^"]*)")\s*\)/g;
  for (const match of source.matchAll(pattern)) {
    const table = match[1];
    const definition = normalizeSql(match[3] ?? match[4]);
    const entries = result.get(table) || [];
    entries.push(`${match[2]} ${definition}`.trim());
    result.set(table, entries);
  }
  return result;
}

function effectiveSchema(source) {
  const tables = extractCreateTables(source);
  const ensure = extractEnsureColumns(source);
  for (const [table, entries] of ensure.entries()) {
    if (!tables.has(table)) tables.set(table, new Map());
    const columns = tables.get(table);
    for (const entry of entries) {
      const split = entry.indexOf(' ');
      const name = (split < 0 ? entry : entry.slice(0, split)).toLowerCase();
      const definition = split < 0 ? '' : entry.slice(split + 1);
      if (!columns.has(name)) columns.set(name, definition);
    }
  }
  return tables;
}

function extractIndexes(source) {
  return sortedUnique([...source.matchAll(/CREATE\s+(?:UNIQUE\s+)?INDEX\s+IF\s+NOT\s+EXISTS\s+([a-z_][a-z0-9_]*)/gi)]
    .map((match) => match[1].toLowerCase()));
}

function walkFiles(dir) {
  const files = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const target = path.join(dir, entry.name);
    if (entry.isDirectory()) files.push(...walkFiles(target));
    else files.push(target);
  }
  return files;
}

function extractSqlTables(rootDir) {
  const tables = [];
  const pattern = /\b(?:FROM|JOIN|INTO|UPDATE|DELETE\s+FROM)\s+([a-z_][a-z0-9_]*)/gi;
  for (const file of walkFiles(path.join(rootDir, 'src'))) {
    if (!file.endsWith('.js') || /\.test\.js$/.test(file)) continue;
    const source = fs.readFileSync(file, 'utf8');
    for (const match of source.matchAll(pattern)) {
      const table = match[1].toLowerCase();
      if (!NON_TABLE_SQL_WORDS.has(table)) tables.push(table);
    }
  }
  return sortedUnique(tables);
}

function isTrackedExternalModule(moduleName) {
  return moduleName.startsWith('node:')
    || ['electron', 'sql.js', 'node-pty', 'openai'].includes(moduleName)
    || moduleName.startsWith('@modelcontextprotocol/');
}

function extractExternalImports(rootDir) {
  const groups = new Map();
  for (const file of walkFiles(path.join(rootDir, 'src'))) {
    if (!file.endsWith('.js') || /\.test\.js$/.test(file)) continue;
    const source = fs.readFileSync(file, 'utf8');
    const modules = [
      ...[...source.matchAll(/require\(\s*['"]([^'"]+)['"]\s*\)/g)].map((match) => match[1]),
      ...[...source.matchAll(/import\(\s*['"]([^'"]+)['"]\s*\)/g)].map((match) => match[1]),
    ];
    const relative = path.relative(rootDir, file).replace(/\\/g, '/');
    for (const moduleName of sortedUnique(modules.filter(isTrackedExternalModule))) {
      const files = groups.get(moduleName) || [];
      files.push(relative);
      groups.set(moduleName, files);
    }
  }
  return Object.fromEntries([...groups.entries()]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([moduleName, files]) => [moduleName, sortedUnique(files)]));
}

function schemaForJson(schema) {
  return Object.fromEntries([...schema.entries()]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([table, columns]) => [table, sortedUnique([...columns.keys()])]));
}

function ensureForJson(ensure) {
  return Object.fromEntries([...ensure.entries()]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([table, columns]) => [table, columns]));
}

function compareArray(label, actual, expected, errors) {
  if (actual.length !== expected.length || actual.some((item, index) => item !== expected[index])) {
    errors.push(`${label} 漂移：源码=${JSON.stringify(actual)} 清单=${JSON.stringify(expected)}`);
  }
}

function compareSet(label, actual, expected, errors) {
  compareArray(label, sortedUnique(actual), sortedUnique(expected), errors);
}

function compareObjectArrays(label, actual, expected, errors, ordered = false) {
  const actualKeys = sortedUnique(Object.keys(actual));
  const expectedKeys = sortedUnique(Object.keys(expected));
  compareArray(`${label} key`, actualKeys, expectedKeys, errors);
  for (const key of expectedKeys) {
    if (ordered) compareArray(`${label}.${key}`, actual[key] || [], expected[key] || [], errors);
    else compareSet(`${label}.${key}`, actual[key] || [], expected[key] || [], errors);
  }
}

function validateShape(inventory, errors) {
  for (const key of ['schemaVersion', 'schema', 'columnPolicies', 'provenanceDifferences', 'tableSemantics', 'ensureColumns', 'indexes', 'migrationSteps', 'sqlTables', 'externalImports', 'domainDependencies', 'sensitiveData', 'sourceAssertions']) {
    if (!(key in inventory)) errors.push(`清单缺少顶层字段 ${key}`);
  }
  if (inventory.schemaVersion !== 1) errors.push('database-and-dependencies schemaVersion 必须为 1');
  if (!Array.isArray(inventory.indexes) || !inventory.indexes.length) errors.push('indexes 必须是非空数组');
  if (!Array.isArray(inventory.migrationSteps) || !inventory.migrationSteps.length) errors.push('migrationSteps 必须是非空数组');
  if (!Array.isArray(inventory.domainDependencies) || !inventory.domainDependencies.length) errors.push('domainDependencies 必须是非空数组');
  if (!Array.isArray(inventory.sensitiveData) || !inventory.sensitiveData.length) errors.push('sensitiveData 必须是非空数组');
  if (!Array.isArray(inventory.provenanceDifferences) || !inventory.provenanceDifferences.length) errors.push('provenanceDifferences 必须是非空数组');
  if (!Array.isArray(inventory.tableSemantics) || !inventory.tableSemantics.length) errors.push('tableSemantics 必须是非空数组');
  compareSet('columnPolicies table', Object.keys(inventory.columnPolicies || {}), Object.keys(inventory.schema || {}), errors);
  for (const [table, policy] of Object.entries(inventory.columnPolicies || {})) {
    if (!policy || typeof policy.defaults !== 'object' || !Array.isArray(policy.nullable)) errors.push(`columnPolicies.${table} 必须包含 defaults 与 nullable`);
    const known = new Set(inventory.schema?.[table] || []);
    for (const column of [...Object.keys(policy?.defaults || {}), ...(policy?.nullable || [])]) {
      if (!known.has(column)) errors.push(`columnPolicies.${table} 引用了未知列 ${column}`);
    }
  }
  compareSet('tableSemantics table', (inventory.tableSemantics || []).map((item) => item.table), Object.keys(inventory.schema || {}), errors);
  const ids = [...(inventory.provenanceDifferences || []), ...(inventory.migrationSteps || []), ...(inventory.domainDependencies || []), ...(inventory.sensitiveData || []), ...(inventory.sourceAssertions || [])]
    .map((item) => item.id).filter(Boolean);
  const duplicates = ids.filter((id, index) => ids.indexOf(id) !== index);
  if (duplicates.length) errors.push(`清单 id 重复：${sortedUnique(duplicates).join(', ')}`);
  for (const item of inventory.provenanceDifferences || []) {
    for (const field of ['table', 'columns', 'freshDatabase', 'legacyDatabase', 'risk']) {
      if (!(field in item) || item[field] === '') errors.push(`${item.id} 缺少来源差异字段 ${field}`);
    }
  }
  for (const item of inventory.tableSemantics || []) {
    for (const field of ['primaryKey', 'relations', 'orphanRisk', 'writers', 'notes']) {
      if (!(field in item) || item[field] === '') errors.push(`tableSemantics.${item.table} 缺少字段 ${field}`);
    }
  }
  for (const item of inventory.domainDependencies || []) {
    for (const field of ['domain', 'tables', 'database', 'filesystem', 'process', 'network', 'native', 'operations', 'transactionBoundary']) {
      if (!(field in item) || item[field] === '') errors.push(`${item.id} 缺少依赖字段 ${field}`);
    }
  }
  for (const item of inventory.sensitiveData || []) {
    for (const field of ['locations', 'classification', 'apiPolicy', 'logPolicy', 'eventPolicy', 'fixturePolicy']) {
      if (!(field in item) || item[field] === '') errors.push(`${item.id} 缺少敏感策略字段 ${field}`);
    }
  }
}

function validateSourceAssertions(rootDir, assertions, errors) {
  for (const item of assertions || []) {
    const source = readText(rootDir, item.source);
    for (const marker of item.contains || []) {
      if (!source.includes(marker)) errors.push(`${item.id} 缺少源码事实：${marker}`);
    }
    for (const sequence of item.ordered || []) {
      let cursor = -1;
      for (const marker of sequence) {
        const next = source.indexOf(marker, cursor + 1);
        if (next < 0) {
          errors.push(`${item.id} 顺序漂移或缺少 marker：${marker}`);
          break;
        }
        cursor = next;
      }
    }
  }
}

function scanInventoryForSecrets(inventory) {
  const text = JSON.stringify(inventory);
  const errors = [];
  const patterns = [
    { label: '本机用户路径', pattern: /[A-Za-z]:[\\/](?:Users|Documents and Settings|AppData)[\\/]/i },
    { label: 'OpenAI 风格密钥', pattern: /\bsk-[A-Za-z0-9_-]{12,}\b/ },
    { label: 'GitHub token', pattern: /\b(?:ghp|github_pat)_[A-Za-z0-9_]{12,}\b/i },
    { label: 'PEM 私钥', pattern: /BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY/ },
  ];
  for (const item of patterns) if (item.pattern.test(text)) errors.push(`依赖清单包含${item.label}`);
  return errors;
}

function validateInventory(rootDir, inventory) {
  const errors = [];
  validateShape(inventory, errors);
  const databaseSource = readText(rootDir, DATABASE_PATH);
  compareObjectArrays('schema', schemaForJson(effectiveSchema(databaseSource)), inventory.schema || {}, errors);
  compareObjectArrays('ensureColumns', ensureForJson(extractEnsureColumns(databaseSource)), inventory.ensureColumns || {}, errors, true);
  compareSet('indexes', extractIndexes(databaseSource), inventory.indexes || [], errors);
  compareSet('SQL table references', extractSqlTables(rootDir), inventory.sqlTables || [], errors);
  compareObjectArrays('externalImports', extractExternalImports(rootDir), inventory.externalImports || {}, errors);
  validateSourceAssertions(rootDir, [
    ...(inventory.migrationSteps || []),
    ...(inventory.sourceAssertions || []),
  ], errors);
  errors.push(...scanInventoryForSecrets(inventory));
  return errors;
}

function loadInventory(rootDir) {
  return JSON.parse(readText(rootDir, INVENTORY_PATH));
}

function run(rootDir = path.resolve(__dirname, '../..')) {
  const inventory = loadInventory(rootDir);
  const errors = validateInventory(rootDir, inventory);
  if (errors.length) {
    const error = new Error(`P00 数据库/依赖清单漂移（${errors.length} 项）\n- ${errors.join('\n- ')}`);
    error.errors = errors;
    throw error;
  }
  return {
    ok: true,
    tables: Object.keys(inventory.schema).length,
    indexes: inventory.indexes.length,
    migrationSteps: inventory.migrationSteps.length,
    domains: inventory.domainDependencies.length,
    sensitiveGroups: inventory.sensitiveData.length,
  };
}

if (require.main === module) {
  try {
    process.stdout.write(`${JSON.stringify(run(process.argv[2] ? path.resolve(process.argv[2]) : undefined))}\n`);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}

module.exports = {
  DATABASE_PATH,
  INVENTORY_PATH,
  effectiveSchema,
  extractCreateTables,
  extractEnsureColumns,
  extractExternalImports,
  extractIndexes,
  extractSqlTables,
  loadInventory,
  scanInventoryForSecrets,
  validateInventory,
  run,
};
