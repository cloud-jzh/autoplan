'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const FIXTURE_MARKER = '.autoplan-p15-topology-fixture';
const FIXTURE_MANIFEST = 'topology-fixture-manifest.json';
const MAXIMUM_EVIDENCE_BYTES = 2 * 1024 * 1024;
const USER_DATA_PATH = /(?:appdata[\\/]roaming[\\/]autoplan|library[\\/]application support[\\/]autoplan|[\\/]\.config[\\/]autoplan|[\\/]userdata(?:[\\/]|$))/i;
const SENSITIVE_TEXT = /(?:\b(?:api[_-]?key|secret|token|password|authorization|cookie|session(?:[_-]?credential)?)\b\s*[=:]\s*[^\s,;]+|\b(?:sk|rk|pk|ghp|xox[baprs])[-_][A-Za-z0-9_-]{8,}|(?:file|autoplan-file):\/\/)/i;
const ACTIVE_DATABASE_MARKERS = [
  'autoplan.sqlite', 'autoplan.sqlite.autoplan-owner.lock', '.autoplan-owner.lock',
  'autoplan.sqlite-wal', 'autoplan.sqlite-shm', 'autoplan.sqlite-journal',
];

function sha256(value) { return crypto.createHash('sha256').update(value).digest('hex'); }

function isWithin(target, root) {
  const relative = path.relative(path.resolve(root), path.resolve(target));
  return relative !== '' && relative !== '..' && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative);
}

function safeRelative(target, root) {
  if (!isWithin(target, root)) return null;
  return path.relative(path.resolve(root), path.resolve(target)).replaceAll('\\', '/');
}

function blocked(code, extra = {}) { return { ok: false, code, ...extra }; }

function scanSensitiveText(value) {
  const text = String(value || '');
  const findings = [];
  if (SENSITIVE_TEXT.test(text)) findings.push('sensitive_pattern');
  if (/(?:^|[\s"'])((?:[A-Za-z]:[\\/]|\/(?:Users|home|private|var|mnt)\/)[^\s"']+)/m.test(text)) findings.push('absolute_path');
  return findings;
}

function sanitizeText(value, options = {}) {
  let text = String(value || '').replaceAll('\\', '/');
  for (const [source, replacement] of [
    [options.rootDir, '<repo>'], [options.fixtureRoot, '<fixture>'], [options.temporaryRoot, '<temp>'],
    [os.homedir(), '<home>'], [os.tmpdir(), '<tmp>'],
  ]) {
    if (source) text = text.replaceAll(String(source).replaceAll('\\', '/'), replacement);
  }
  return text
    .replace(/\b(?:Bearer\s+)?(?:sk|rk|pk|ghp|xox[baprs])[-_][A-Za-z0-9._-]{8,}\b/gi, '<redacted>')
    .replace(/((?:api[_-]?key|secret|token|password|authorization|cookie|session(?:[_-]?credential)?)\s*[=:]\s*)[^\s,;]+/gi, '$1<redacted>')
    .replace(/\b[A-Za-z]:\/[^^\s"'<>|]+/g, '<absolute-path>')
    .replace(/(^|[\s"'(])\/(?:Users|home|private|var|mnt)\/[^\s"']*/g, '$1<absolute-path>')
    .replace(/(?:file|autoplan-file):\/\/[^\s"']+/gi, '<controlled-url>');
}

function fixtureTreeIssue(root, relative = '', seen = { entries: 0 }) {
  const directory = path.join(root, relative);
  let entries;
  try { entries = fs.readdirSync(directory, { withFileTypes: true }); } catch { return 'fixture_tree_unreadable'; }
  for (const entry of entries) {
    seen.entries += 1;
    if (seen.entries > 256) return 'fixture_tree_too_large';
    if (entry.isSymbolicLink()) return 'fixture_symlink_rejected';
    if (entry.isDirectory()) {
      const nested = fixtureTreeIssue(root, path.join(relative, entry.name), seen);
      if (nested) return nested;
    } else if (/^(?:autoplan\.sqlite(?:[._-].*)?|.*\.sqlite(?:-wal|-shm|-journal)?)$/i.test(entry.name) || ACTIVE_DATABASE_MARKERS.includes(entry.name)) {
      return 'fixture_owner_or_database_present';
    }
  }
  return null;
}

function validateFixtureRoot(value) {
  if (!value || !path.isAbsolute(value)) return blocked('fixture_path_invalid');
  const root = path.resolve(value);
  if (USER_DATA_PATH.test(root)) return blocked('fixture_real_userdata_rejected');
  const info = fs.lstatSync(root, { throwIfNoEntry: false });
  if (!info?.isDirectory() || info.isSymbolicLink()) return blocked('fixture_root_invalid');
  let actual;
  try { actual = fs.realpathSync.native(root); } catch { return blocked('fixture_root_invalid'); }
  if (USER_DATA_PATH.test(actual)) return blocked('fixture_real_userdata_rejected');
  const expectedRoot = process.platform === 'win32' ? root.toLowerCase() : root;
  const actualRoot = process.platform === 'win32' ? path.resolve(actual).toLowerCase() : path.resolve(actual);
  if (expectedRoot !== actualRoot) return blocked('fixture_parent_symlink_rejected');

  const marker = path.join(root, FIXTURE_MARKER);
  const manifest = path.join(root, FIXTURE_MANIFEST);
  const markerInfo = fs.lstatSync(marker, { throwIfNoEntry: false });
  const manifestInfo = fs.lstatSync(manifest, { throwIfNoEntry: false });
  if (!markerInfo?.isFile() || markerInfo.isSymbolicLink() || markerInfo.size > 1024 ||
      !manifestInfo?.isFile() || manifestInfo.isSymbolicLink() || manifestInfo.size > 64 * 1024) {
    return blocked('fixture_authorization_missing');
  }
  let descriptor;
  try { descriptor = JSON.parse(fs.readFileSync(manifest, 'utf8')); } catch { return blocked('fixture_manifest_invalid'); }
  if (descriptor?.schema_version !== 1 || descriptor?.kind !== 'p15-authorized-topology-fixture' ||
      descriptor?.authorized_copy !== true || descriptor?.real_userdata === true) {
    return blocked('fixture_manifest_invalid');
  }
  const scenarios = path.join(root, 'trace-scenarios.json');
  const scenariosInfo = fs.lstatSync(scenarios, { throwIfNoEntry: false });
  if (!Array.isArray(descriptor.contents) || !descriptor.contents.includes('trace-scenarios.json') ||
      !scenariosInfo?.isFile() || scenariosInfo.isSymbolicLink() || scenariosInfo.size > 64 * 1024) {
    return blocked('fixture_trace_scenarios_missing');
  }
  const treeIssue = fixtureTreeIssue(root);
  if (treeIssue) return blocked(treeIssue);
  for (const name of ACTIVE_DATABASE_MARKERS) {
    if (fs.existsSync(path.join(root, name))) return blocked('fixture_owner_or_database_present');
  }
  return {
    ok: true,
    code: 'fixture_authorized',
    fixture_id: sha256(path.basename(root)).slice(0, 16),
    marker_sha256: sha256(fs.readFileSync(marker)),
    manifest_sha256: sha256(fs.readFileSync(manifest)),
  };
}

function inspectEvidenceFile(file, allowedRoot) {
  if (!path.isAbsolute(file) || !isWithin(file, allowedRoot)) return blocked('evidence_path_invalid');
  const info = fs.lstatSync(file, { throwIfNoEntry: false });
  if (!info?.isFile() || info.isSymbolicLink() || info.size > MAXIMUM_EVIDENCE_BYTES) return blocked('evidence_file_invalid');
  const bytes = fs.readFileSync(file);
  const findings = scanSensitiveText(bytes.toString('utf8'));
  return findings.length ? blocked('evidence_sensitive', { findings }) : { ok: true, code: 'evidence_safe', bytes: bytes.length, sha256: sha256(bytes) };
}

function readSafeJSON(file, allowedRoot) {
  const inspection = inspectEvidenceFile(file, allowedRoot);
  if (!inspection.ok) return { ...inspection, value: null };
  try { return { ...inspection, value: JSON.parse(fs.readFileSync(file, 'utf8')) }; }
  catch { return blocked('evidence_json_invalid'); }
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
    XDG_CONFIG_HOME: path.join(temporaryRoot, 'xdg-config'), XDG_CACHE_HOME: path.join(temporaryRoot, 'xdg-cache'),
    XDG_DATA_HOME: path.join(temporaryRoot, 'xdg-data'), AUTOPLAN_P15_VERIFICATION: '1',
  });
  return { environment, removedCount };
}

module.exports = {
  ACTIVE_DATABASE_MARKERS, FIXTURE_MANIFEST, FIXTURE_MARKER, MAXIMUM_EVIDENCE_BYTES, USER_DATA_PATH,
  createSafeEnvironment, fixtureTreeIssue, inspectEvidenceFile, isWithin, readSafeJSON, safeRelative, sanitizeText,
  scanSensitiveText, sha256, validateFixtureRoot,
};
