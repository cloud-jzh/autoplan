'use strict';

const crypto = require('node:crypto');

const SESSION_BYTES = 32;
const SESSION_PATTERN = /^[A-Za-z0-9_-]{43}$/;
const SESSION_PROTOCOL_VERSION = 1;
const SESSION_PROTOCOL_TYPE = 'autoplan_daemon_session';

class DaemonSessionError extends Error {
  constructor(code) {
    super(code);
    this.name = 'DaemonSessionError';
    this.code = code;
  }
}

// Session material stays within the privileged main-process/supervisor
// boundary. Its JSON representation is written exactly once to stdin; it is
// never an argv item, environment value, URL, preload value, or log field.
class DaemonSession {
  #value;
  #revoked = false;

  constructor(value) {
    if (!SESSION_PATTERN.test(value)) throw new DaemonSessionError('daemon_session_invalid');
    this.#value = value;
  }

  credential() {
    if (this.#revoked) throw new DaemonSessionError('daemon_session_revoked');
    return this.#value;
  }

  handoff() {
    return Buffer.from(`${JSON.stringify({
      version: SESSION_PROTOCOL_VERSION,
      type: SESSION_PROTOCOL_TYPE,
      session: this.credential(),
    })}\n`, 'utf8');
  }

  revoke() {
    this.#revoked = true;
    this.#value = '';
  }

  get revoked() { return this.#revoked; }
}

function createDaemonSession(randomBytes = crypto.randomBytes) {
  let raw;
  try { raw = randomBytes(SESSION_BYTES); } catch { throw new DaemonSessionError('daemon_session_generation_failed'); }
  if (!Buffer.isBuffer(raw) || raw.length !== SESSION_BYTES) throw new DaemonSessionError('daemon_session_generation_failed');
  try {
    return new DaemonSession(raw.toString('base64url'));
  } finally {
    raw.fill(0);
  }
}

function writeSessionHandoff(stream, session) {
  if (!stream || typeof stream.write !== 'function' || !(session instanceof DaemonSession)) {
    return Promise.reject(new DaemonSessionError('daemon_session_pipe_failed'));
  }
  let payload;
  try { payload = session.handoff(); } catch (error) { return Promise.reject(error); }
  return new Promise((resolve, reject) => {
    let settled = false;
    const done = (error) => {
      if (settled) return;
      settled = true;
      stream.removeListener?.('error', onError);
      payload.fill(0);
      if (error) reject(new DaemonSessionError('daemon_session_pipe_failed'));
      else resolve();
    };
    const onError = () => done(new Error('pipe_error'));
    stream.once?.('error', onError);
    try {
      const accepted = stream.write(payload, (error) => done(error));
      if (accepted === false) stream.once?.('drain', () => undefined);
    } catch (error) {
      done(error);
    }
  });
}

function closeSessionPipe(stream) {
  if (!stream || stream.destroyed || stream.writableEnded) return;
  try { stream.end(); } catch { try { stream.destroy(); } catch { /* process is already gone */ } }
}

module.exports = {
  DaemonSession,
  DaemonSessionError,
  SESSION_BYTES,
  SESSION_PATTERN,
  SESSION_PROTOCOL_TYPE,
  SESSION_PROTOCOL_VERSION,
  closeSessionPipe,
  createDaemonSession,
  writeSessionHandoff,
};
