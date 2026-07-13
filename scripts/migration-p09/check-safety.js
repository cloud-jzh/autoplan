'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const SENSITIVE_TEXT = /(?:\b(?:api[_-]?key|secret|token|password|authorization|cookie|session)\b\s*[=:]\s*[^\s,;]+|\b(?:sk|rk|pk|ghp|xox[baprs])[-_][A-Za-z0-9_-]{8,}\b|appdata[\\/]+roaming[\\/]+autoplan|library[\\/]+application support[\\/]+autoplan|\.config[\\/]+autoplan|(?:file|autoplan-file):\/\/)/i;
const USER_DATA_PATH = /(?:appdata[\\/]+roaming[\\/]+autoplan|library[\\/]+application support[\\/]+autoplan|\.config[\\/]+autoplan)/i;
const FIXTURE_MARKERS = ['.autoplan-p09-scale-copy', 'scale-copy.json', 'scale-manifest.json'];

function sha256(value) { return crypto.createHash('sha256').update(value).digest('hex'); }

function scanSensitiveText(value) {
  const findings = [];
  const text = String(value || '');
  if (SENSITIVE_TEXT.test(text)) findings.push('sensitive_pattern');
  if (/(?:^|[\s"'])((?:[A-Za-z]:[\\/]|\/(?:Users|home|private|var|mnt)\/)[^\s"']+)/m.test(text)) findings.push('absolute_path');
  return findings;
}

function sanitizeText(value, options = {}) {
  let text = String(value || '').replaceAll('\\', '/');
  for (const [source, replacement] of [[options.rootDir, '<repo>'], [options.fixtureRoot, '<fixture>'], [options.temporaryRoot, '<temp>'], [os.homedir(), '<home>'], [os.tmpdir(), '<tmp>']]) {
    if (source) text = text.replaceAll(String(source).replaceAll('\\', '/'), replacement);
  }
  return text
    .replace(/\b(?:Bearer\s+)?(?:sk|rk|pk|ghp|xox[baprs])[-_][A-Za-z0-9._-]{8,}\b/gi, '<redacted>')
    .replace(/((?:api[_-]?key|secret|token|password|authorization|cookie|session)\s*[=:]\s*)[^\s,;]+/gi, '$1<redacted>')
    .replace(/\b[A-Za-z]:\/[^\s"'<>|]+/g, '<absolute-path>')
    .replace(/(^|[\s"'(])\/(?:Users|home|private|var|mnt)\/[^\s"']*/g, '$1<absolute-path>')
    .replace(/(?:file|autoplan-file):\/\/[^\s"']+/gi, '<controlled-url>');
}

function validateFixtureRoot(value) {
  if (!value || !path.isAbsolute(value)) return blocked('fixture_path_invalid');
  const root = path.resolve(value);
  if (USER_DATA_PATH.test(root)) return blocked('fixture_real_userdata_rejected');
  const info = fs.lstatSync(root, { throwIfNoEntry: false });
  if (!info?.isDirectory() || info.isSymbolicLink()) return blocked('fixture_root_invalid');
  if (!isFixtureOrTemporary(root)) return blocked('fixture_authorization_missing');
  const markerStatus = FIXTURE_MARKERS.map((name) => fs.lstatSync(path.join(root, name), { throwIfNoEntry: false }));
  if (markerStatus.some((info) => info?.isSymbolicLink())) return blocked('fixture_marker_symlink');
  if (!markerStatus[0]?.isFile() || !markerStatus[1]?.isFile() || !markerStatus[2]?.isFile()) return blocked('fixture_marker_missing');
  if (fs.existsSync(path.join(root, 'autoplan.sqlite')) || fs.existsSync(path.join(root, 'autoplan.sqlite.autoplan-owner.lock'))) return blocked('fixture_active_database_rejected');
  return { ok: true, code: 'fixture_authorized', fixture_id: sha256(path.basename(root)).slice(0, 16), marker_sha256: markerStatus.map((info, index) => sha256(fs.readFileSync(path.join(root, FIXTURE_MARKERS[index])))).sort() };
}

function inspectOwnerSafety(fixtureRoot) {
  const root = path.resolve(fixtureRoot || '');
  if (!root || !fs.existsSync(root)) return blocked('owner_fixture_unavailable');
  const blockedNames = ['autoplan.sqlite.autoplan-owner.lock', '.autoplan-owner.lock', 'autoplan.sqlite-wal', 'autoplan.sqlite-shm', 'autoplan.sqlite-journal'];
  for (const name of blockedNames) {
    if (fs.existsSync(path.join(root, name))) return blocked('owner_or_sidecar_active');
  }
  return {
    ok: true, code: 'owner_idle', writer_count: 0, node_writer: false, go_writer: false,
    process_handle_scan: 'database_lock_and_sidecar_absence', process_handles_observed: false,
  };
}

function inspectEvidenceFile(file, allowedRoot) {
  if (!path.isAbsolute(file) || !isWithin(file, allowedRoot)) return blocked('evidence_path_invalid');
  const info = fs.lstatSync(file, { throwIfNoEntry: false });
  if (!info?.isFile() || info.isSymbolicLink() || info.size > 8 * 1024 * 1024) return blocked('evidence_file_invalid');
  const bytes = fs.readFileSync(file);
  const findings = scanSensitiveText(bytes.toString('utf8'));
  if (findings.length) return { ok: false, code: 'evidence_sensitive', findings };
  return { ok: true, code: 'evidence_safe', bytes: bytes.length, sha256: sha256(bytes) };
}

function createSafeEnvironment(temporaryRoot, source = process.env) {
  const environment = {};
  let removedCount = 0;
  for (const [name, value] of Object.entries(source)) {
    if (/(?:api[_-]?key|secret|token|password|authorization|cookie|session|userdata|database|db[_-]?path|^AUTOPLAN_)/i.test(name)) removedCount += 1;
    else environment[name] = value;
  }
  const home = path.join(temporaryRoot, 'home');
  Object.assign(environment, {
    TEMP: temporaryRoot, TMP: temporaryRoot, TMPDIR: temporaryRoot, HOME: home, USERPROFILE: home,
    APPDATA: path.join(temporaryRoot, 'appdata'), LOCALAPPDATA: path.join(temporaryRoot, 'localappdata'),
    XDG_CONFIG_HOME: path.join(temporaryRoot, 'xdg-config'), XDG_CACHE_HOME: path.join(temporaryRoot, 'xdg-cache'), XDG_DATA_HOME: path.join(temporaryRoot, 'xdg-data'),
    AUTOPLAN_P09_VERIFICATION: '1',
  });
  return { environment, removedCount };
}

function isFixtureOrTemporary(root) {
  const temporary = path.resolve(os.tmpdir());
  if (isWithin(root, temporary) || root === temporary) return true;
  return root.split(path.sep).some((part) => /(?:fixture|sanitized|temp|tmp|drill)/i.test(part));
}
function isWithin(target, root) { const relative = path.relative(path.resolve(root), path.resolve(target)); return relative !== '' && relative !== '..' && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative); }
function blocked(code) { return { ok: false, code }; }

module.exports = { createSafeEnvironment, inspectEvidenceFile, inspectOwnerSafety, isFixtureOrTemporary, isWithin, sanitizeText, scanSensitiveText, sha256, validateFixtureRoot };
