'use strict';

const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const net = require('node:net');

const DATABASE_OWNER_ENV = 'AUTOPLAN_DATABASE_OWNER';
const GO_API_URL_ENV = 'AUTOPLAN_GO_API_URL';
const DATABASE_OWNERS = Object.freeze({
  NODE: 'node',
  GO: 'go',
});

const DATABASE_OWNER_ERROR_CODES = Object.freeze({
  CONFIG_INVALID: 'DATABASE_OWNER_CONFIGURATION_INVALID',
  OWNER_IMMUTABLE: 'DATABASE_OWNER_IMMUTABLE',
  NODE_SQL_FORBIDDEN: 'DATABASE_NODE_SQL_FORBIDDEN',
  GO_OWNER_MARKER: 'DATABASE_GO_OWNER_MARKER_PRESENT',
  GO_SCHEMA: 'DATABASE_SCHEMA_OWNED_BY_GO',
  UNSAFE_PATH: 'DATABASE_OWNER_PATH_UNSAFE',
  OWNER_LOCKED: 'DATABASE_OWNER_LOCKED',
  NODE_LEGACY_MCP_FORBIDDEN: 'DATABASE_NODE_LEGACY_MCP_FORBIDDEN',
});

class DatabaseOwnerGuardError extends Error {
  constructor(code) {
    super(code);
    this.name = 'DatabaseOwnerGuardError';
    this.code = code;
  }
}

let processOwner = null;

/**
 * Resolves the database writer before a runtime opens a database. The result
 * is intentionally immutable for the lifetime of the process: switching an
 * already selected Go owner back to sql.js would make an in-memory snapshot
 * capable of overwriting newer data.
 */
function selectProcessDatabaseOwner(options = {}) {
  const next = createDatabaseOwnerGuard(options);
  if (processOwner && processOwner.signature !== next.signature) {
    throw new DatabaseOwnerGuardError(DATABASE_OWNER_ERROR_CODES.OWNER_IMMUTABLE);
  }
  processOwner = next;
  return processOwner;
}

function selectedProcessDatabaseOwner() {
  return processOwner;
}

function createDatabaseOwnerGuard(options = {}) {
  const env = options.env || process.env;
  const owner = normalizeOwner(options.owner ?? env[DATABASE_OWNER_ENV]);
  const apiUrl = normalizeGoApiUrl(options.goApiUrl ?? env[GO_API_URL_ENV], owner);
  return Object.freeze({
    owner,
    goApiUrl: apiUrl,
    signature: `${owner}\u0000${apiUrl || ''}`,
    isGoOwner: () => owner === DATABASE_OWNERS.GO,
    assertSqlJsAllowed(databasePath, policy) {
      assertSqlJsAllowed(owner, databasePath, policy);
    },
    async assertSqlJsOpenAllowed(databasePath) {
      assertSqlJsAllowed(owner, databasePath);
      await assertNoActiveDatabaseOwner(databasePath);
    },
    assertSqlJsWriteAllowed(databasePath, policy) {
      assertSqlJsAllowed(owner, databasePath, policy);
    },
    assertNodeMutationAllowed() {
      if (owner === DATABASE_OWNERS.GO) {
        throw new DatabaseOwnerGuardError(DATABASE_OWNER_ERROR_CODES.NODE_SQL_FORBIDDEN);
      }
    },
    assertGoDataClientFallbackAllowed() {
      // The compatibility bridge is meaningful only while Go owns the data.
      // Rejecting it under a Node owner prevents a future mixed-mode caller
      // from treating the bridge as an alternate writer or a hidden fallback.
      if (owner !== DATABASE_OWNERS.GO) {
        throw new DatabaseOwnerGuardError(DATABASE_OWNER_ERROR_CODES.CONFIG_INVALID);
      }
    },
    assertLegacyNodeMcpAdapterAllowed(goMcpEnabled) {
      if (goMcpEnabled === true && owner === DATABASE_OWNERS.GO) {
        throw new DatabaseOwnerGuardError(DATABASE_OWNER_ERROR_CODES.NODE_LEGACY_MCP_FORBIDDEN);
      }
    },
  });
}

function guardFromExplicitEnvironment(env = process.env) {
  if (!Object.prototype.hasOwnProperty.call(env, DATABASE_OWNER_ENV)) return null;
  return createDatabaseOwnerGuard({ env });
}

function assertNodeMutationAllowed() {
  const guard = processOwner || guardFromExplicitEnvironment();
  guard?.assertNodeMutationAllowed?.();
}

function normalizeOwner(value) {
  const owner = String(value || '').trim().toLowerCase();
  if (owner === DATABASE_OWNERS.NODE || owner === DATABASE_OWNERS.GO) return owner;
  throw new DatabaseOwnerGuardError(DATABASE_OWNER_ERROR_CODES.CONFIG_INVALID);
}

function normalizeGoApiUrl(value, owner) {
  const configured = String(value || '').trim();
  if (owner === DATABASE_OWNERS.NODE) {
    if (configured) throw new DatabaseOwnerGuardError(DATABASE_OWNER_ERROR_CODES.CONFIG_INVALID);
    return null;
  }
  if (!configured || configured.length > 2048) {
    throw new DatabaseOwnerGuardError(DATABASE_OWNER_ERROR_CODES.CONFIG_INVALID);
  }
  let parsed;
  try {
    parsed = new URL(configured);
  } catch {
    throw new DatabaseOwnerGuardError(DATABASE_OWNER_ERROR_CODES.CONFIG_INVALID);
  }
  if (parsed.protocol !== 'http:' || parsed.username || parsed.password || parsed.search || parsed.hash ||
      !['127.0.0.1', '[::1]'].includes(parsed.hostname) || !parsed.port) {
    throw new DatabaseOwnerGuardError(DATABASE_OWNER_ERROR_CODES.CONFIG_INVALID);
  }
  return parsed.toString().replace(/\/$/, '');
}

function assertSqlJsAllowed(owner, databasePath, policy = {}) {
  if (owner === DATABASE_OWNERS.GO) {
    throw new DatabaseOwnerGuardError(DATABASE_OWNER_ERROR_CODES.NODE_SQL_FORBIDDEN);
  }
  const target = normalizeDatabasePath(databasePath);
  for (const candidate of [target, `${target}.mirror`, `${target}.bak`]) {
    assertLegacySnapshot(candidate);
  }
  // The listener is the active lock, but a metadata record is deliberately
  // treated as a fail-closed owner marker. Node must never delete or replace a
  // Go-owned marker merely because it cannot prove it is stale.
  const markers = [`${target}.autoplan-owner.lock`, `${target}.autoplan-go-owner.lock`];
  for (const marker of markers) {
    if (policy.allowLegacyOwnerMarker === true && marker === markers[0]) continue;
    if (fs.existsSync(marker)) throw new DatabaseOwnerGuardError(DATABASE_OWNER_ERROR_CODES.GO_OWNER_MARKER);
  }
}

function normalizeDatabasePath(value) {
  const text = String(value || '').trim();
  if (!text || text !== value) throw new DatabaseOwnerGuardError(DATABASE_OWNER_ERROR_CODES.UNSAFE_PATH);
  return path.resolve(text);
}

function assertLegacySnapshot(candidate) {
  let info;
  try {
    info = fs.lstatSync(candidate);
  } catch (error) {
    if (error?.code === 'ENOENT') return;
    throw new DatabaseOwnerGuardError(DATABASE_OWNER_ERROR_CODES.UNSAFE_PATH);
  }
  if (!info.isFile() || info.isSymbolicLink()) {
    throw new DatabaseOwnerGuardError(DATABASE_OWNER_ERROR_CODES.UNSAFE_PATH);
  }
  let header;
  try {
    const descriptor = fs.openSync(candidate, 'r');
    try {
      header = Buffer.alloc(100);
      fs.readSync(descriptor, header, 0, header.length, 0);
    } finally {
      fs.closeSync(descriptor);
    }
  } catch {
    throw new DatabaseOwnerGuardError(DATABASE_OWNER_ERROR_CODES.UNSAFE_PATH);
  }
  if (header.subarray(0, 16).toString('ascii') !== 'SQLite format 3\u0000') return;
  // SQLite stores PRAGMA user_version as a big-endian integer at byte 60.
  // The Go migration ledger starts at version 1; reject it before sql.js is
  // initialized so a legacy compatibility migration cannot run in memory.
  if (header.readUInt32BE(60) >= 1) {
    throw new DatabaseOwnerGuardError(DATABASE_OWNER_ERROR_CODES.GO_SCHEMA);
  }
}

async function assertNoActiveDatabaseOwner(databasePath) {
  const target = normalizeDatabasePath(databasePath);
  let parent;
  try {
    parent = fs.realpathSync.native(path.dirname(target));
  } catch {
    // A missing parent cannot contain an active owner for this database. The
    // actual Node owner acquisition still races safely after directory setup.
    return;
  }
  const canonical = path.join(parent, path.basename(target));
  const normalized = normalizeIdentityPath(canonical);
  const digest = crypto.createHash('sha256').update(`autoplan-database-owner-v1\u0000${normalized}`).digest();
  const host = `127.${1 + digest[2] % 254}.${digest[3]}.${digest[4]}`;
  const port = 20000 + digest.readUInt16BE(0) % 20000;
  const server = net.createServer();
  try {
    await new Promise((resolve, reject) => {
      const onError = (error) => { server.removeListener('listening', onListening); reject(error); };
      const onListening = () => { server.removeListener('error', onError); resolve(); };
      server.once('error', onError);
      server.once('listening', onListening);
      server.listen({ host, port, exclusive: true });
    });
  } catch {
    throw new DatabaseOwnerGuardError(DATABASE_OWNER_ERROR_CODES.OWNER_LOCKED);
  } finally {
    if (server.listening) {
      await new Promise((resolve) => server.close(() => resolve()));
    }
  }
}

function normalizeIdentityPath(value) {
  let result = path.normalize(value);
  if (process.platform === 'win32') {
    if (result.startsWith('\\\\?\\UNC\\')) result = `\\\\${result.slice(8)}`;
    else if (result.startsWith('\\\\?\\')) result = result.slice(4);
  }
  return process.platform === 'win32' || process.platform === 'darwin' ? result.toLowerCase() : result;
}

module.exports = {
  DATABASE_OWNER_ENV,
  GO_API_URL_ENV,
  DATABASE_OWNERS,
  DATABASE_OWNER_ERROR_CODES,
  DatabaseOwnerGuardError,
  createDatabaseOwnerGuard,
  assertNodeMutationAllowed,
  guardFromExplicitEnvironment,
  selectProcessDatabaseOwner,
  selectedProcessDatabaseOwner,
};
