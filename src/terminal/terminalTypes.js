const TERMINAL_CHANNELS = Object.freeze({
  CREATE: 'terminal:create',
  LIST: 'terminal:list',
  WRITE: 'terminal:write',
  RESIZE: 'terminal:resize',
  KILL: 'terminal:kill',
  CLOSE: 'terminal:close',
  RENAME: 'terminal:rename',
  REPLAY: 'terminal:replay',
  CLEAR: 'terminal:clear',
  DATA: 'terminal:data',
  EXIT: 'terminal:exit',
  STATUS: 'terminal:status',
  CLOSED: 'terminal:closed',
});

const TERMINAL_SESSION_ID_PREFIX = 'term';
const TERMINAL_SESSION_ID_PATTERN_SOURCE = '^term_[a-z0-9][a-z0-9_-]{5,}$';

// P14 keeps the runtime that created a terminal as immutable session metadata.
// It is deliberately not a routing switch: Node remains the only creator in
// this module until the separately gated Go transport is implemented.
const TERMINAL_RUNTIME = Object.freeze({
  NODE: 'node',
  GO: 'go',
});

// P008 treats legacy Node PTY withdrawal as a release-time, per-platform
// decision. It is deliberately separate from go_terminal_api: turning the
// Go transport off must never re-home an existing session or silently enable
// a removed Node creation path.
const TERMINAL_LEGACY_ADMISSION_STATE = Object.freeze({
  RETAINED: 'retained',
  BLOCKED: 'blocked',
  WITHDRAWN: 'withdrawn',
});

// The checked-in policy mirrors docs/migration/p14/legacy-removal-manifest.json.
// It lives in the packaged main-process contract so an environment variable or
// renderer request cannot fabricate release evidence. P008 starts with every
// supported platform blocked, which retains Node PTY creation safely.
const TERMINAL_LEGACY_PLATFORM_EVIDENCE = Object.freeze({
  win32: Object.freeze({
    state: TERMINAL_LEGACY_ADMISSION_STATE.BLOCKED,
    reason: 'packaged_smoke_evidence_missing',
    evidenceId: '',
    manifestHash: '',
    successfulPackagedSmokes: 0,
    owner: '',
    riskExpiryUTC: '',
  }),
  darwin: Object.freeze({
    state: TERMINAL_LEGACY_ADMISSION_STATE.BLOCKED,
    reason: 'packaged_smoke_evidence_missing',
    evidenceId: '',
    manifestHash: '',
    successfulPackagedSmokes: 0,
    owner: '',
    riskExpiryUTC: '',
  }),
  linux: Object.freeze({
    state: TERMINAL_LEGACY_ADMISSION_STATE.BLOCKED,
    reason: 'packaged_smoke_evidence_missing',
    evidenceId: '',
    manifestHash: '',
    successfulPackagedSmokes: 0,
    owner: '',
    riskExpiryUTC: '',
  }),
});

function terminalLegacyAdmissionForPlatform(platform = process.platform, evidence = TERMINAL_LEGACY_PLATFORM_EVIDENCE) {
  const name = String(platform || '').trim() || process.platform;
  const record = evidence && typeof evidence === 'object' ? evidence[name] : null;
  const withdrawn = acceptedLegacyRemovalEvidence(record);
  return Object.freeze({
    platform: name,
    state: withdrawn ? TERMINAL_LEGACY_ADMISSION_STATE.WITHDRAWN : TERMINAL_LEGACY_ADMISSION_STATE.BLOCKED,
    // Existing Node-owned sessions are unaffected. This is consulted only
    // before a new session can load node-pty or spawn a child process.
    allowNewNodeSessions: !withdrawn,
    reason: withdrawn ? 'packaged_smoke_evidence_accepted' : String(record?.reason || 'platform_evidence_missing'),
    evidenceId: withdrawn ? record.evidenceId : '',
  });
}

function acceptedLegacyRemovalEvidence(record) {
  return Boolean(record) &&
    record.state === TERMINAL_LEGACY_ADMISSION_STATE.WITHDRAWN &&
    /^p14-[a-z0-9][a-z0-9._-]*$/i.test(String(record.evidenceId || '')) &&
    /^[a-f0-9]{64}$/i.test(String(record.manifestHash || '')) &&
    Number.isInteger(record.successfulPackagedSmokes) && record.successfulPackagedSmokes >= 3 &&
    String(record.owner || '').trim().length > 0 &&
    /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/.test(String(record.riskExpiryUTC || ''));
}

const TERMINAL_STATUS = Object.freeze({
  STARTING: 'starting',
  RUNNING: 'running',
  EXITED: 'exited',
  KILLED: 'killed',
  ERROR: 'error',
});

const TERMINAL_PROFILE_KIND = Object.freeze({
  DEFAULT: 'default',
  CUSTOM: 'custom',
});

const TERMINAL_SESSION_FIELDS = Object.freeze([
  'id',
  'projectId',
  'title',
  'cwd',
  'shell',
  'status',
  'createdAt',
  'endedAt',
  'exitCode',
  'cols',
  'rows',
  'profile',
  'closed',
  'runtime',
]);

const TERMINAL_PROFILE_FIELDS = Object.freeze(['id', 'name', 'kind', 'shellPath', 'args', 'env']);

const TERMINAL_DEFAULTS = Object.freeze({
  cols: 80,
  rows: 24,
  scrollbackLimit: 10000,
  title: 'Terminal',
  retainOnExit: true,
});

const TERMINAL_LIMITS = Object.freeze({
  minCols: 2,
  maxCols: 500,
  minRows: 1,
  maxRows: 200,
  maxInputBytes: 65536,
  maxTitleLength: 80,
  maxCwdLength: 2048,
  maxProfileNameLength: 80,
  maxShellPathLength: 2048,
  maxProfileArgs: 32,
  maxProfileArgLength: 512,
  minScrollbackLimit: 100,
  maxScrollbackLimit: 50000,
});

const TERMINAL_PAYLOAD_KEYS = Object.freeze({
  create: Object.freeze(['projectId', 'cwd', 'profileId', 'profile', 'title', 'cols', 'rows', 'env']),
  list: Object.freeze(['projectId']),
  write: Object.freeze(['sessionId', 'data']),
  resize: Object.freeze(['sessionId', 'cols', 'rows']),
  kill: Object.freeze(['sessionId']),
  close: Object.freeze(['sessionId']),
  rename: Object.freeze(['sessionId', 'title']),
  replay: Object.freeze(['sessionId']),
  clear: Object.freeze(['sessionId']),
});

const TERMINAL_EVENT_TYPES = Object.freeze({
  DATA: 'data',
  EXIT: 'exit',
  STATUS: 'status',
  CLOSED: 'closed',
});

// The P14 wire protocol is independent from project SSE.  These names are
// shared contract identifiers only; the Go REST/WebSocket handlers are added
// in later P14 tasks.
const TERMINAL_PROTOCOL = Object.freeze({
  FEATURE: 'go_terminal_api',
  CREATE_PATH: '/api/v1/projects/{project_id}/terminals',
  SESSION_PATH: '/api/v1/terminals/{id}',
  WEBSOCKET_PATH: '/api/v1/terminals/{id}/ws',
  INPUT: 'input',
  RESIZE: 'resize',
  OUTPUT: 'output',
  EXIT: 'exit',
  STATUS: 'status',
  CLOSED: 'closed',
  PING: 'ping',
  PONG: 'pong',
});

const TERMINAL_ERROR_CODES = Object.freeze({
  PTY_UNAVAILABLE: 'PTY_UNAVAILABLE',
  LEGACY_ADMISSION_DISABLED: 'LEGACY_ADMISSION_DISABLED',
  INVALID_PROJECT: 'INVALID_PROJECT',
  INVALID_SESSION: 'INVALID_SESSION',
  INVALID_PAYLOAD: 'INVALID_PAYLOAD',
  CWD_OUTSIDE_WORKSPACE: 'CWD_OUTSIDE_WORKSPACE',
  SESSION_NOT_FOUND: 'SESSION_NOT_FOUND',
  WRITE_FAILED: 'WRITE_FAILED',
  RESIZE_FAILED: 'RESIZE_FAILED',
  KILL_FAILED: 'KILL_FAILED',
});

const TERMINAL_PTY_UNAVAILABLE_MESSAGE = '终端能力不可用';
const TERMINAL_LEGACY_ADMISSION_DISABLED_MESSAGE = '当前平台已停止创建旧终端会话';

function terminalError(code, message, details) {
  const error = {
    ok: false,
    code: String(code || TERMINAL_ERROR_CODES.INVALID_PAYLOAD),
    message: String(message || '终端请求无效'),
  };
  if (details !== undefined && details !== null && details !== '') {
    error.details = String(details);
  }
  return error;
}

function terminalPtyUnavailableError(details) {
  return terminalError(
    TERMINAL_ERROR_CODES.PTY_UNAVAILABLE,
    TERMINAL_PTY_UNAVAILABLE_MESSAGE,
    details,
  );
}

function terminalLegacyAdmissionDisabledError(details) {
  return terminalError(
    TERMINAL_ERROR_CODES.LEGACY_ADMISSION_DISABLED,
    TERMINAL_LEGACY_ADMISSION_DISABLED_MESSAGE,
    details,
  );
}

module.exports = {
  TERMINAL_CHANNELS,
  TERMINAL_SESSION_ID_PREFIX,
  TERMINAL_SESSION_ID_PATTERN_SOURCE,
  TERMINAL_RUNTIME,
  TERMINAL_LEGACY_ADMISSION_STATE,
  TERMINAL_LEGACY_PLATFORM_EVIDENCE,
  TERMINAL_STATUS,
  TERMINAL_PROFILE_KIND,
  TERMINAL_SESSION_FIELDS,
  TERMINAL_PROFILE_FIELDS,
  TERMINAL_DEFAULTS,
  TERMINAL_LIMITS,
  TERMINAL_PAYLOAD_KEYS,
  TERMINAL_EVENT_TYPES,
  TERMINAL_PROTOCOL,
  TERMINAL_ERROR_CODES,
  TERMINAL_PTY_UNAVAILABLE_MESSAGE,
  TERMINAL_LEGACY_ADMISSION_DISABLED_MESSAGE,
  terminalError,
  terminalLegacyAdmissionForPlatform,
  terminalPtyUnavailableError,
  terminalLegacyAdmissionDisabledError,
};
