'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { scanSensitiveText } = require('./check-safety');

const ROOT = path.resolve(__dirname, '..', '..');
const MANIFEST_PATH = path.join(ROOT, 'docs', 'migration', 'p15', 'evidence', 'manifest.json');
const LEGACY_MANIFEST_PATH = path.join(ROOT, 'docs', 'migration', 'p15', 'legacy-removal-manifest.json');
const BINARY_MANIFEST_PATH = path.join(ROOT, 'docs', 'migration', 'p15', 'binary-manifest.json');
const REQUIRED_PLATFORMS = new Set(['windows', 'macos', 'linux']);

function blocked(code, extra = {}) { return { ok: false, status: 'blocked', code, ...extra }; }

function readJSON(file, maximumBytes = 1024 * 1024) {
  const info = fs.lstatSync(file, { throwIfNoEntry: false });
  if (!info?.isFile() || info.isSymbolicLink() || info.size <= 0 || info.size > maximumBytes) return null;
  try { return JSON.parse(fs.readFileSync(file, 'utf8')); } catch { return null; }
}

function safeEvidenceEntry(entry) {
  return Boolean(entry && typeof entry === 'object' && /^[A-Za-z0-9._-]{1,160}$/.test(entry.run_id || '') &&
    /^(?:[a-f0-9]{40}|v[0-9A-Za-z._-]{1,120})$/.test(entry.source_revision || '') &&
    entry.status === 'passed' && Array.isArray(entry.platforms) && entry.platforms.every((platform) => REQUIRED_PLATFORMS.has(platform)) &&
    Array.isArray(entry.evidence_ids) && entry.evidence_ids.length > 0 && entry.evidence_ids.every((id) => /^[a-z][a-z0-9_-]{1,95}$/.test(id)) &&
    !scanSensitiveText(JSON.stringify(entry)).length);
}

function validateEvidence(options = {}) {
  const manifest = readJSON(options.manifestPath || MANIFEST_PATH);
  const legacy = readJSON(options.legacyManifestPath || LEGACY_MANIFEST_PATH);
  const binary = readJSON(options.binaryManifestPath || BINARY_MANIFEST_PATH);
  if (!manifest || manifest.schema_version !== 1 || manifest.kind !== 'p15-evidence-manifest' ||
      manifest.publish_policy !== 'never' || !Array.isArray(manifest.required_platforms) ||
      !Array.isArray(manifest.required_evidence) || !Array.isArray(manifest.entries) ||
      manifest.required_platforms.length !== REQUIRED_PLATFORMS.size ||
      manifest.required_platforms.some((platform) => !REQUIRED_PLATFORMS.has(platform))) return blocked('evidence_manifest_invalid');
  if (!legacy || legacy.deletion_authorized !== true) return blocked('p006_deletion_gate_blocked');
  if (!binary || binary.status !== 'ready' || !Array.isArray(binary.entries) || binary.entries.length === 0) return blocked('sidecar_release_evidence_blocked');
  const invalidEntries = manifest.entries.filter((entry) => !safeEvidenceEntry(entry));
  if (invalidEntries.length) return blocked('evidence_entry_invalid', { invalid_entries: invalidEntries.length });
  const evidenceIds = new Set(manifest.entries.flatMap((entry) => entry.evidence_ids));
  const missingEvidence = manifest.required_evidence.filter((id) => !evidenceIds.has(id));
  const platforms = new Set(manifest.entries.flatMap((entry) => entry.platforms));
  const missingPlatforms = [...REQUIRED_PLATFORMS].filter((platform) => !platforms.has(platform));
  if (manifest.status !== 'completed' || missingEvidence.length || missingPlatforms.length) {
    return blocked('evidence_incomplete', { missing_evidence: missingEvidence, missing_platforms: missingPlatforms });
  }
  return { ok: true, status: 'completed', code: 'p15_evidence_complete', entries: manifest.entries.length };
}

function parseArgs(argv) {
  if (argv.length === 0) return {};
  if (argv.length === 2 && argv[0] === '--manifest' && argv[1]) return { manifestPath: argv[1] };
  throw new Error('arguments_invalid');
}

if (require.main === module) {
  try {
    const result = validateEvidence(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify(result)}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch {
    process.stdout.write('{"status":"blocked","code":"evidence_validation_arguments_invalid"}\n');
    process.exitCode = 2;
  }
}

module.exports = { BINARY_MANIFEST_PATH, LEGACY_MANIFEST_PATH, MANIFEST_PATH, safeEvidenceEntry, validateEvidence };
