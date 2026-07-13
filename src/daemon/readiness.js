'use strict';

const crypto = require('node:crypto');
const { StringDecoder } = require('node:string_decoder');

const READINESS_VERSION = 1;
const READINESS_TYPE = 'autoplan_daemon_ready';
const MAX_READINESS_BYTES = 4096;

class DaemonReadinessError extends Error {
  constructor(code) {
    super(code);
    this.name = 'DaemonReadinessError';
    this.code = code;
  }
}

function createSessionProof(session, pid, port) {
  const secret = normalizeSession(session);
  const message = `autoplan-daemon-ready-v1\u0000${pid}\u0000${port}`;
  return crypto.createHmac('sha256', secret).update(message).digest('hex');
}

function parseReadinessLine(line) {
  if (typeof line !== 'string' || !line || Buffer.byteLength(line, 'utf8') > MAX_READINESS_BYTES) {
    throw new DaemonReadinessError('daemon_readiness_invalid');
  }
  let value;
  try {
    value = JSON.parse(line);
  } catch {
    throw new DaemonReadinessError('daemon_readiness_invalid');
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new DaemonReadinessError('daemon_readiness_invalid');
  }
  const allowed = new Set(['version', 'type', 'pid', 'host', 'port', 'ready', 'lock', 'session_proof']);
  if (Object.keys(value).some((key) => !allowed.has(key)) ||
      value.version !== READINESS_VERSION || value.type !== READINESS_TYPE ||
      !Number.isSafeInteger(value.pid) || value.pid <= 0 ||
      value.host !== '127.0.0.1' || !Number.isSafeInteger(value.port) || value.port < 1 || value.port > 65535 ||
      value.ready !== true || value.lock !== 'held' || !/^[a-f0-9]{64}$/.test(String(value.session_proof || ''))) {
    throw new DaemonReadinessError('daemon_readiness_invalid');
  }
  return Object.freeze({
    version: value.version, type: value.type, pid: value.pid, host: value.host,
    port: value.port, ready: value.ready, lock: value.lock, sessionProof: value.session_proof,
  });
}

function verifyReadiness(value, options = {}) {
  const readiness = value?.sessionProof ? value : parseReadinessLine(value);
  const expectedPid = Number(options.pid);
  if (!Number.isSafeInteger(expectedPid) || readiness.pid !== expectedPid) {
    throw new DaemonReadinessError('daemon_identity_mismatch');
  }
  const expected = Buffer.from(createSessionProof(options.session, readiness.pid, readiness.port), 'utf8');
  const actual = Buffer.from(readiness.sessionProof, 'utf8');
  if (expected.length !== actual.length || !crypto.timingSafeEqual(expected, actual)) {
    throw new DaemonReadinessError('daemon_session_mismatch');
  }
  return readiness;
}

class ReadinessCollector {
  constructor() {
    this.decoder = new StringDecoder('utf8');
    this.pending = '';
    this.message = null;
    this.failed = null;
  }

  push(chunk) {
    if (this.failed) throw this.failed;
    if (this.message) {
      this.failed = new DaemonReadinessError('daemon_stdout_noise');
      throw this.failed;
    }
    this.pending += this.decoder.write(Buffer.from(chunk));
    if (Buffer.byteLength(this.pending, 'utf8') > MAX_READINESS_BYTES) {
      this.failed = new DaemonReadinessError('daemon_readiness_invalid');
      throw this.failed;
    }
    const newline = this.pending.indexOf('\n');
    if (newline < 0) return null;
    const line = this.pending.slice(0, newline);
    const trailing = this.pending.slice(newline + 1);
    if (trailing.length !== 0 || line.endsWith('\r') || line.length === 0) {
      this.failed = new DaemonReadinessError('daemon_stdout_noise');
      throw this.failed;
    }
    this.message = parseReadinessLine(line);
    this.pending = '';
    return this.message;
  }

  end() {
    if (this.failed) throw this.failed;
    const tail = this.decoder.end();
    if (tail) this.pending += tail;
    if (!this.message || this.pending.length !== 0) {
      throw new DaemonReadinessError('daemon_readiness_missing');
    }
    return this.message;
  }
}

function normalizeSession(value) {
  const session = typeof value === 'string' ? value : '';
  if (!/^[A-Za-z0-9_-]{43}$/.test(session)) throw new DaemonReadinessError('daemon_session_invalid');
  return session;
}

module.exports = {
  DaemonReadinessError,
  MAX_READINESS_BYTES,
  READINESS_TYPE,
  READINESS_VERSION,
  ReadinessCollector,
  createSessionProof,
  normalizeSession,
  parseReadinessLine,
  verifyReadiness,
};
