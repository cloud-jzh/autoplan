'use strict';

const path = require('node:path');

const NORMALIZATION_VERSION = 'p03-node-contract-v1';
const FIXTURE_ROOT = '<fixture-root>';
const FIXTURE_WORKSPACE = '<fixture-workspace>';
const REDACTED = '<redacted>';
const REDACTED_ENV_VARS = '<redacted-env-vars>';

const UTC_FIELD = /(?:^|_)(?:created|updated|started|finished|scanned|modified)_at$/i;
const CAMEL_UTC_FIELD = /(?:created|updated|started|finished|scanned|modified)At$/;
const WINDOWS_ABSOLUTE = /^[A-Za-z]:[\\/]/;
const CREDENTIAL_PATTERNS = [
  /\bsk-[A-Za-z0-9_-]{12,}\b/,
  /\b(?:ghp|github_pat)_[A-Za-z0-9_]{12,}\b/i,
  /-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----/,
  /\bBearer\s+[A-Za-z0-9._~+\/-]{8,}=*\b/i,
];

function createNormalizationContext(value, options = {}) {
  const projectIds = new Set();
  collectProjectIds(value, projectIds);
  const projectIdMap = new Map(
    [...projectIds].sort((left, right) => left - right).map((id, index) => [id, index + 1]),
  );
  return {
    fixtureRoot: options.fixtureRoot ? path.resolve(options.fixtureRoot) : null,
    projectIdMap,
    runtimeSessionIds: new Map(),
  };
}

function collectProjectIds(value, ids, trail = []) {
  if (Array.isArray(value)) {
    value.forEach((item) => collectProjectIds(item, ids, trail));
    return;
  }
  if (!value || typeof value !== 'object') return;
  for (const [key, child] of Object.entries(value)) {
    const nextTrail = [...trail, key];
    if (isProjectIdField(key, trail) && Number.isSafeInteger(Number(child)) && Number(child) > 0) {
      ids.add(Number(child));
    }
    collectProjectIds(child, ids, nextTrail);
  }
}

function normalizeContracts(contracts, options = {}) {
  const context = options.context || createNormalizationContext(contracts, options);
  const normalized = normalizeValue(contracts, context, []);
  assertSanitizedContract(normalized, options);
  return {
    value: normalized,
    metadata: {
      version: NORMALIZATION_VERSION,
      placeholders: {
        fixtureRoot: FIXTURE_ROOT,
        fixtureWorkspace: FIXTURE_WORKSPACE,
        redacted: REDACTED,
        redactedEnvVars: REDACTED_ENV_VARS,
      },
      idMaps: {
        projects: Object.fromEntries([...context.projectIdMap.entries()].map(([source, target]) => [String(source), target])),
      },
    },
  };
}

function normalizeContract(value, options = {}) {
  const context = options.context || createNormalizationContext(value, options);
  const normalized = normalizeValue(value, context, []);
  assertSanitizedContract(normalized, options);
  return normalized;
}

function normalizeValue(value, context, trail) {
  if (value === null || value === undefined || typeof value === 'boolean') return value;
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) throw new Error(`不可规范化的非有限数字：${pointer(trail)}`);
    return value;
  }
  if (typeof value === 'string') return normalizeString(value, context, trail);
  if (Array.isArray(value)) return value.map((item, index) => normalizeValue(item, context, [...trail, index]));
  if (typeof value !== 'object') throw new Error(`不可规范化的值类型：${pointer(trail)}`);

  const output = {};
  for (const key of Object.keys(value).sort()) {
    const child = value[key];
    const nextTrail = [...trail, key];
    if (isProjectIdField(key, trail) && child !== null) {
      output[key] = normalizeProjectId(child, context, nextTrail);
      continue;
    }
    if (key === 'env_vars') {
      output[key] = child ? REDACTED_ENV_VARS : child;
      continue;
    }
    if (isRawCredentialField(key)) {
      output[key] = typeof child === 'string' && child.startsWith('····') ? child : (child ? REDACTED : child);
      continue;
    }
    if (isRuntimeSessionField(key) && child) {
      output[key] = normalizeRuntimeSession(child, context);
      continue;
    }
    output[key] = normalizeValue(child, context, nextTrail);
  }
  return output;
}

function normalizeString(value, context, trail) {
  const key = String(trail.at(-1) ?? '');
  if ((UTC_FIELD.test(key) || CAMEL_UTC_FIELD.test(key)) && value) {
    const timestamp = new Date(value);
    if (Number.isNaN(timestamp.valueOf())) throw new Error(`无效 UTC 时间：${pointer(trail)}`);
    return timestamp.toISOString();
  }
  if (looksAbsolute(value) && !isLocalProtocolRoute(trail)) return normalizeAbsolutePath(value, context, trail);
  return value;
}

function isLocalProtocolRoute(trail) {
  return trail.at(-1) === 'path' && trail.at(-2) === 'mcp';
}

function normalizeAbsolutePath(value, context, trail) {
  const posix = value.replace(/\\/g, '/');
  if (/^(?:\/?__autoplan_fixture__|Z:\/__autoplan_fixture__)(?:\/|$)/i.test(posix)) {
    const suffix = posix.replace(/^(?:\/?__autoplan_fixture__|Z:\/__autoplan_fixture__)/i, '');
    return `${FIXTURE_WORKSPACE}${suffix}`;
  }
  if (context.fixtureRoot) {
    const absolute = path.resolve(value);
    const relative = path.relative(context.fixtureRoot, absolute);
    if (relative === '' || (!relative.startsWith('..') && !path.isAbsolute(relative))) {
      const suffix = relative ? `/${relative.replace(/\\/g, '/')}` : '';
      return `${FIXTURE_ROOT}${suffix}`;
    }
  }
  throw new Error(`拒绝未授权绝对路径：${pointer(trail)}`);
}

function normalizeProjectId(value, context, trail) {
  const id = Number(value);
  if (!Number.isSafeInteger(id) || id <= 0) throw new Error(`无效 project id：${pointer(trail)}`);
  const mapped = context.projectIdMap.get(id);
  if (!mapped) throw new Error(`project id 缺少稳定映射：${pointer(trail)}`);
  return mapped;
}

function normalizeRuntimeSession(value, context) {
  const source = String(value);
  if (!context.runtimeSessionIds.has(source)) {
    context.runtimeSessionIds.set(source, `<runtime-session-${context.runtimeSessionIds.size + 1}>`);
  }
  return context.runtimeSessionIds.get(source);
}

function isProjectIdField(key, trail) {
  if (key === 'activeProjectId' || key === 'projectId' || key === 'project_id') return true;
  if (key !== 'id') return false;
  const parent = String(trail.at(-1) ?? '');
  const grandparent = String(trail.at(-2) ?? '');
  return parent === 'activeProject' || parent === 'projects' || grandparent === 'projects';
}

function isRawCredentialField(key) {
  const normalized = key.toLowerCase();
  if (normalized.startsWith('has') || normalized.includes('_has_') || normalized.endsWith('masked')) return false;
  if (normalized === 'authheader' || normalized === 'connectionexample') return false;
  return normalized === 'api_key' || normalized === 'apikey' || normalized === 'auth_token'
    || normalized === 'authtoken' || normalized.endsWith('_api_key') || normalized.endsWith('_auth_token')
    || normalized.endsWith('token') || normalized.endsWith('_secret') || normalized === 'password';
}

function isRuntimeSessionField(key) {
  return /(?:sessionId|session_id)$/.test(key) && !/^project_/.test(key);
}

function looksAbsolute(value) {
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(value)) return false;
  return path.isAbsolute(value) || WINDOWS_ABSOLUTE.test(value) || /^\/__autoplan_fixture__(?:\/|$)/.test(value);
}

function assertSanitizedContract(value, options = {}) {
  const encoded = JSON.stringify(value);
  const localPaths = [options.homeDir, options.appData, options.localAppData]
    .filter(Boolean)
    .map((item) => String(item).replace(/\\/g, '/').toLowerCase());
  const normalized = encoded.replace(/\\/g, '/').toLowerCase();
  if (localPaths.some((item) => normalized.includes(item))) throw new Error('规范化契约包含本机路径');
  if (CREDENTIAL_PATTERNS.some((pattern) => pattern.test(encoded))) throw new Error('规范化契约包含可用凭据形态');
  return true;
}

function stableJson(value) {
  return `${JSON.stringify(value, null, 2)}\n`;
}

function pointer(trail) {
  if (!trail.length) return '/';
  return `/${trail.map((part) => String(part).replace(/~/g, '~0').replace(/\//g, '~1')).join('/')}`;
}

module.exports = {
  FIXTURE_ROOT,
  FIXTURE_WORKSPACE,
  NORMALIZATION_VERSION,
  REDACTED,
  REDACTED_ENV_VARS,
  assertSanitizedContract,
  createNormalizationContext,
  normalizeContract,
  normalizeContracts,
  stableJson,
};
