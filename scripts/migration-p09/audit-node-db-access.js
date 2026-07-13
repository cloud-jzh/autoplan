'use strict';

/*
 * P09 deliberately audits source instead of opening a SQLite file.  It is a
 * migration guard, not a data tool: reports contain only repository-relative
 * locations, operation categories, and reviewed contract identifiers.
 */

const fs = require('node:fs');
const path = require('node:path');

const DATABASE_METHODS = Object.freeze([
  'get',
  'all',
  'run',
  'insert',
  'runBatch',
  'getSetting',
  'getSettings',
  'setSetting',
]);

const READ_METHODS = new Set(['get', 'all']);
const WRITE_METHODS = new Set(['run', 'insert', 'runBatch']);
const SETTINGS_READ_METHODS = new Set(['getSetting', 'getSettings']);
const SETTINGS_WRITE_METHODS = new Set(['setSetting']);
const SOURCE_DIRECTORY = 'src';
const SOURCE_EXCLUSIONS = new Set(['renderer']);
const FORBIDDEN_ARTIFACT_KEYS = /(?:api[_-]?key|auth[_-]?token|secret|password|env[_-]?vars|workspace(?:[_-]?path)?|user[_-]?data|message(?:[_-]?(?:body|content))?|tool[_-]?data)/i;
const ABSOLUTE_PATH = /(?:^[A-Za-z]:[\\/]|^\\\\|^\/Users\/|^\/home\/|^\/var\/|^\/tmp\/)/;

function auditRepository(rootDir, manifest) {
  const errors = validateManifest(manifest);
  if (errors.length > 0) return emptyReport(errors);

  const files = listProductionJavaScript(path.join(rootDir, SOURCE_DIRECTORY));
  const sourceRules = manifest.source_rules || {};
  const seenRules = new Set();
  const calls = [];

  for (const absolutePath of files) {
    const relativePath = toRepositoryPath(rootDir, absolutePath);
    const source = fs.readFileSync(absolutePath, 'utf8');
    const accessCalls = collectSourceAccess(source, relativePath);
    const lifecycleCalls = collectLifecycleAccess(source, relativePath);
    if (accessCalls.length === 0 && lifecycleCalls.length === 0) continue;

    const rule = sourceRules[relativePath];
    if (!rule) {
      errors.push(problem('unmapped_database_source', relativePath));
      continue;
    }
    seenRules.add(relativePath);
    validateSourceRule(relativePath, rule, accessCalls, lifecycleCalls, manifest.contracts, errors);
    calls.push(...decorateCalls(accessCalls, lifecycleCalls, rule));
  }

  for (const relativePath of Object.keys(sourceRules)) {
    if (!seenRules.has(relativePath)) {
      errors.push(problem('stale_database_source_rule', relativePath));
    }
  }

  const orderedCalls = calls.sort(compareCall);
  const summary = summarizeCalls(orderedCalls);
  return {
    ok: errors.length === 0,
    schema_version: 1,
    source_root: SOURCE_DIRECTORY,
    summary,
    call_sites: orderedCalls,
    errors: errors.sort(compareProblem),
  };
}

function validateManifest(manifest) {
  const errors = [];
  if (!manifest || typeof manifest !== 'object' || Array.isArray(manifest)) {
    return [problem('invalid_manifest')];
  }
  if (manifest.schema_version !== 1) errors.push(problem('unsupported_manifest_schema'));
  if (!manifest.contracts || typeof manifest.contracts !== 'object' || Array.isArray(manifest.contracts)) {
    errors.push(problem('missing_contract_registry'));
  }
  if (!manifest.source_rules || typeof manifest.source_rules !== 'object' || Array.isArray(manifest.source_rules)) {
    errors.push(problem('missing_source_rules'));
  }
  scanArtifactValue(manifest, [], errors);
  return errors;
}

function scanArtifactValue(value, pathParts, errors) {
  if (Array.isArray(value)) {
    value.forEach((item, index) => scanArtifactValue(item, pathParts.concat(String(index)), errors));
    return;
  }
  if (!value || typeof value !== 'object') {
    if (typeof value === 'string' && ABSOLUTE_PATH.test(value)) {
      errors.push(problem('unsafe_artifact_value', pathParts.join('.')));
    }
    return;
  }
  for (const [key, nested] of Object.entries(value)) {
    const nextPath = pathParts.concat(key);
    if (FORBIDDEN_ARTIFACT_KEYS.test(key)) {
      errors.push(problem('unsafe_artifact_field', nextPath.join('.')));
    }
    scanArtifactValue(nested, nextPath, errors);
  }
}

function validateSourceRule(relativePath, rule, accessCalls, lifecycleCalls, contracts, errors) {
  if (!rule || typeof rule !== 'object' || Array.isArray(rule)) {
    errors.push(problem('invalid_source_rule', relativePath));
    return;
  }
  const contractIds = Array.isArray(rule.contract_ids) ? rule.contract_ids : [];
  if (contractIds.length === 0 || contractIds.some((id) => !contracts?.[id])) {
    errors.push(problem('unmapped_go_contract', relativePath));
  }

  const expectedMethods = rule.methods && typeof rule.methods === 'object' ? rule.methods : {};
  const actualMethods = countBy(accessCalls, (call) => call.method);
  const methodNames = new Set([...Object.keys(expectedMethods), ...Object.keys(actualMethods)]);
  for (const method of methodNames) {
    const expected = Number(expectedMethods[method] || 0);
    const actual = Number(actualMethods[method] || 0);
    if (!DATABASE_METHODS.includes(method) || !Number.isSafeInteger(expected) || expected < 0 || actual !== expected) {
      errors.push(problem('database_access_count_mismatch', relativePath, method));
    }
  }

  const dynamicCalls = accessCalls.filter((call) => call.dynamic_sql);
  if (dynamicCalls.length > 0 && rule.dynamic_sql !== 'reviewed') {
    errors.push(problem('unmapped_dynamic_sql', relativePath));
  }
  const directSettingsCalls = accessCalls.filter((call) => call.direct_settings_sql);
  if (directSettingsCalls.length > 0 && rule.direct_settings_sql !== 'reviewed') {
    errors.push(problem('unmapped_direct_settings_sql', relativePath));
  }
  const settingCalls = accessCalls.filter((call) => call.category === 'settings_read' || call.category === 'settings_write');
  if (settingCalls.length > 0 && rule.settings_access !== 'reviewed') {
    errors.push(problem('unmapped_settings_access', relativePath));
  }

  const expectedLifecycle = rule.lifecycle && typeof rule.lifecycle === 'object' ? rule.lifecycle : {};
  const actualLifecycle = countBy(lifecycleCalls, (call) => call.category);
  const lifecycleNames = new Set([...Object.keys(expectedLifecycle), ...Object.keys(actualLifecycle)]);
  for (const category of lifecycleNames) {
    if (Number(expectedLifecycle[category] || 0) !== Number(actualLifecycle[category] || 0)) {
      errors.push(problem('database_lifecycle_count_mismatch', relativePath, category));
    }
  }
}

function decorateCalls(accessCalls, lifecycleCalls, rule) {
  const contractId = rule.contract_ids?.[0] || 'unmapped';
  return accessCalls.concat(lifecycleCalls).map((call) => ({
    source_path: call.source_path,
    line: call.line,
    category: call.category,
    contract_id: contractId,
  }));
}

function collectSourceAccess(source, relativePath) {
  const calls = [];
  const receiver = String.raw`(?:\b(?:this|[A-Za-z_$][\w$]*)\.)?db\??\.`;
  const matcher = new RegExp(`${receiver}(${DATABASE_METHODS.join('|')})\\s*\\(`, 'g');
  let match;
  while ((match = matcher.exec(source))) {
    const openParen = source.indexOf('(', match.index + match[0].length - 1);
    const expression = readBalancedExpression(source, openParen);
    const method = match[1];
    const sqlText = firstArgument(expression);
    calls.push({
      source_path: relativePath,
      line: lineNumberAt(source, match.index),
      method,
      category: categoryForMethod(method),
      dynamic_sql: isDynamicSqlSource(method, sqlText),
      direct_settings_sql: /\bsettings\b/i.test(sqlText),
    });
    matcher.lastIndex = Math.max(matcher.lastIndex, openParen + 1);
  }
  return calls;
}

function collectLifecycleAccess(source, relativePath) {
  const probes = [
    ['app_database_constructor', /\bnew\s+AppDatabase\s*\(/g],
    ['sqljs_initialization', /\binitSqlJs\s*\(/g],
    ['database_export', /\b(?:this\.)?db\.export\s*\(/g],
    ['database_persist', /\bthis\.persist\s*\(/g],
    ['database_close', /\b(?:this\.)?db\.close\s*\(/g],
    ['mirror_overwrite_risk', /\.mirror\b/g],
    ['backup_overwrite_risk', /\.bak\b/g],
  ];
  const calls = [];
  for (const [category, pattern] of probes) {
    let match;
    while ((match = pattern.exec(source))) {
      calls.push({ source_path: relativePath, line: lineNumberAt(source, match.index), category });
    }
  }
  return calls;
}

function categoryForMethod(method) {
  if (READ_METHODS.has(method)) return 'read';
  if (WRITE_METHODS.has(method)) return 'write';
  if (SETTINGS_READ_METHODS.has(method)) return 'settings_read';
  if (SETTINGS_WRITE_METHODS.has(method)) return 'settings_write';
  return 'unknown';
}

function readBalancedExpression(source, openParen) {
  if (openParen < 0 || source[openParen] !== '(') return '';
  let depth = 0;
  let quote = '';
  let escaped = false;
  for (let index = openParen; index < source.length; index += 1) {
    const char = source[index];
    if (quote) {
      if (escaped) {
        escaped = false;
      } else if (char === '\\') {
        escaped = true;
      } else if (char === quote) {
        quote = '';
      }
      continue;
    }
    if (char === '\'' || char === '"' || char === '`') {
      quote = char;
      continue;
    }
    if (char === '(') depth += 1;
    if (char === ')') {
      depth -= 1;
      if (depth === 0) return source.slice(openParen + 1, index);
    }
  }
  return source.slice(openParen + 1);
}

function firstArgument(expression) {
  let quote = '';
  let escaped = false;
  let depth = 0;
  for (let index = 0; index < expression.length; index += 1) {
    const char = expression[index];
    if (quote) {
      if (escaped) escaped = false;
      else if (char === '\\') escaped = true;
      else if (char === quote) quote = '';
      continue;
    }
    if (char === '\'' || char === '"' || char === '`') {
      quote = char;
      continue;
    }
    if (char === '(' || char === '[' || char === '{') depth += 1;
    if (char === ')' || char === ']' || char === '}') depth -= 1;
    if (char === ',' && depth === 0) return expression.slice(0, index);
  }
  return expression;
}

function containsTemplateInterpolation(value) {
  return /`[\s\S]*?\$\{/.test(value);
}

function isDynamicSqlSource(method, value) {
  if (!READ_METHODS.has(method) && !WRITE_METHODS.has(method)) return false;
  const expression = String(value || '').trim();
  if (method === 'runBatch') return true;
  if (containsTemplateInterpolation(expression)) return true;
  return !/^(?:'[^]*'|"[^]*"|`[^]*`)$/u.test(expression);
}

function listProductionJavaScript(sourceRoot) {
  const result = [];
  const visit = (directory) => {
    for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
      if (entry.isDirectory()) {
        if (!SOURCE_EXCLUSIONS.has(entry.name)) visit(path.join(directory, entry.name));
        continue;
      }
      if (entry.isFile() && entry.name.endsWith('.js') && !entry.name.endsWith('.test.js')) {
        result.push(path.join(directory, entry.name));
      }
    }
  };
  visit(sourceRoot);
  return result.sort();
}

function lineNumberAt(source, index) {
  return source.slice(0, index).split('\n').length;
}

function toRepositoryPath(rootDir, absolutePath) {
  return path.relative(rootDir, absolutePath).split(path.sep).join('/');
}

function countBy(items, key) {
  return items.reduce((result, item) => {
    const name = key(item);
    result[name] = (result[name] || 0) + 1;
    return result;
  }, {});
}

function summarizeCalls(calls) {
  return {
    total: calls.length,
    by_category: countBy(calls, (call) => call.category),
    source_count: new Set(calls.map((call) => call.source_path)).size,
  };
}

function emptyReport(errors) {
  return { ok: false, schema_version: 1, source_root: SOURCE_DIRECTORY, summary: { total: 0, by_category: {}, source_count: 0 }, call_sites: [], errors };
}

function problem(code, sourcePath, detail) {
  const result = { code };
  if (sourcePath) result.source_path = sourcePath;
  if (detail) result.detail = detail;
  return result;
}

function compareCall(left, right) {
  return left.source_path.localeCompare(right.source_path) || left.line - right.line || left.category.localeCompare(right.category);
}

function compareProblem(left, right) {
  return `${left.code}:${left.source_path || ''}:${left.detail || ''}`.localeCompare(`${right.code}:${right.source_path || ''}:${right.detail || ''}`);
}

function readManifest(manifestPath) {
  return JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
}

function main() {
  const rootDir = path.resolve(__dirname, '..', '..');
  const manifestPath = path.join(rootDir, 'docs', 'migration', 'p09', 'node-db-access.json');
  const report = auditRepository(rootDir, readManifest(manifestPath));
  process.stdout.write(`${JSON.stringify(report)}\n`);
  process.exitCode = report.ok ? 0 : 1;
}

if (require.main === module) main();

module.exports = {
  auditRepository,
  collectLifecycleAccess,
  collectSourceAccess,
  containsTemplateInterpolation,
  isDynamicSqlSource,
  main,
  readBalancedExpression,
  validateManifest,
};
