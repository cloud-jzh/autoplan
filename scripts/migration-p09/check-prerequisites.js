'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { inspectEvidenceFile, inspectOwnerSafety, isWithin, sha256, validateFixtureRoot } = require('./check-safety');

const REQUIRED_STAGES = ['p04', 'p05', 'p06', 'p07', 'p08'];

function checkPrerequisites(options = {}) {
  const rootDir = path.resolve(options.rootDir || path.join(__dirname, '..', '..'));
  const fixture = validateFixtureRoot(options.fixtureRoot);
  const baseline = inspectP00Baseline(rootDir);
  const stages = REQUIRED_STAGES.map((stage) => inspectStageEvidence(rootDir, stage));
  const owner = fixture.ok ? inspectOwnerSafety(options.fixtureRoot) : { ok: false, code: 'owner_not_checked' };
  const frozenTests = inspectFrozenTestSurface(rootDir);
  const failures = [fixture, baseline, ...stages, owner, frozenTests].filter((item) => !item.ok).map((item) => item.code);
  return {
    schema_version: 1, status: failures.length ? 'blocked' : 'ready', ok: failures.length === 0,
    code: failures[0] || 'prerequisites_ready', failures, fixture, baseline, stages, owner, frozen_tests: frozenTests,
  };
}

function inspectP00Baseline(rootDir) {
  const evidenceRoot = path.join(rootDir, 'docs', 'migration', 'p00', 'evidence', 'runs');
  const result = inspectLatestRun(evidenceRoot);
  if (!result.ok) return { ...result, code: `p00_${result.code}` };
  const summary = result.summary;
  const commands = Array.isArray(summary.commandResults) ? summary.commandResults : [];
  const check = commands.find((item) => item.id === 'check');
  const test = commands.find((item) => item.id === 'test');
  const expectedRed = check?.expectedOutcome === 'exact-known-failure' && check?.exitCode !== 0 &&
    Array.isArray(check?.failureSignatures) && check.failureSignatures.includes('file-length|scripts/smoke-test.js|limit=3800');
  if (!summary.sourceHashesStable || !summary.expectationsHashStable || !summary.ok || !expectedRed ||
      check?.evaluation?.accepted !== true || test?.evaluation?.accepted !== true) {
    return { ok: false, code: 'p00_baseline_signature_invalid' };
  }
  return { ok: true, code: 'p00_baseline_accepted', run_id: result.run_id, summary_sha256: result.summary_sha256 };
}

function inspectStageEvidence(rootDir, stage) {
  const evidenceRoot = path.join(rootDir, 'docs', 'migration', stage, 'evidence', 'runs');
  const result = inspectLatestRun(evidenceRoot);
  if (!result.ok) return { ...result, stage, code: `${stage}_${result.code}` };
  const summary = result.summary;
  if (summary.status !== 'completed' || summary.ok !== true || summary.sourceHashesStable !== true) {
    return { ok: false, stage, code: `${stage}_summary_not_completed` };
  }
  return { ok: true, stage, code: `${stage}_evidence_accepted`, run_id: result.run_id, summary_sha256: result.summary_sha256 };
}

function inspectLatestRun(evidenceRoot) {
  const rootInfo = fs.lstatSync(evidenceRoot, { throwIfNoEntry: false });
  if (!rootInfo?.isDirectory() || rootInfo.isSymbolicLink()) return { ok: false, code: 'evidence_runs_missing' };
  const names = fs.readdirSync(evidenceRoot, { withFileTypes: true }).filter((entry) => entry.isDirectory() && /^[A-Za-z0-9._-]{1,160}$/.test(entry.name)).map((entry) => entry.name).sort().reverse();
  if (!names.length) return { ok: false, code: 'evidence_run_missing' };
  for (const name of names) {
    const runDir = path.join(evidenceRoot, name);
    const summaryPath = path.join(runDir, 'summary.json');
    const manifestPath = path.join(runDir, 'evidence-manifest.json');
    const summary = readSafeJSON(summaryPath, runDir);
    const manifest = readSafeJSON(manifestPath, runDir);
    if (!summary || !manifest || manifest.immutableRunDirectory !== true || !verifyManifest(runDir, manifest)) continue;
    return { ok: true, code: 'evidence_run_valid', run_id: name, summary, summary_sha256: sha256(fs.readFileSync(summaryPath)) };
  }
  return { ok: false, code: 'evidence_integrity_invalid' };
}

function verifyManifest(runDir, manifest) {
  if (!Array.isArray(manifest.artifacts) || !manifest.artifacts.length) return false;
  const summaryRecord = manifest.artifacts.find((artifact) => artifact?.path === 'summary.json');
  if (!summaryRecord || summaryRecord.sha256 !== sha256(fs.readFileSync(path.join(runDir, 'summary.json')))) return false;
  return manifest.artifacts.every((artifact) => {
    if (!artifact || typeof artifact.path !== 'string' || !/^[A-Za-z0-9._/-]+$/.test(artifact.path) || !/^[a-f0-9]{64}$/.test(artifact.sha256 || '') || !Number.isInteger(artifact.bytes) || artifact.bytes < 0) return false;
    const target = path.resolve(runDir, artifact.path);
    if (!isWithin(target, runDir)) return false;
    const info = fs.lstatSync(target, { throwIfNoEntry: false });
    const safe = inspectEvidenceFile(target, runDir);
    return Boolean(info?.isFile() && !info.isSymbolicLink() && info.size === artifact.bytes && safe.ok && safe.sha256 === artifact.sha256);
  });
}

function readSafeJSON(file, root) {
  const evidence = inspectEvidenceFile(file, root);
  if (!evidence.ok) return null;
  try { return JSON.parse(fs.readFileSync(file, 'utf8')); } catch { return null; }
}

function inspectFrozenTestSurface(rootDir) {
  const packagePath = path.join(rootDir, 'package.json');
  try {
    const pkg = JSON.parse(fs.readFileSync(packagePath, 'utf8'));
    if (pkg?.scripts?.test !== 'node --test "src/**/*.test.js"' || !pkg?.scripts?.check) return { ok: false, code: 'p00_test_surface_changed' };
  } catch { return { ok: false, code: 'p00_test_surface_unreadable' }; }
  const testFiles = listTestFiles(path.join(rootDir, 'src')).concat(listTestFiles(path.join(rootDir, 'scripts', 'migration-p09')));
  const violations = [];
  for (const file of testFiles) {
    const lines = fs.readFileSync(file, 'utf8').split(/\r?\n/);
    lines.forEach((line, index) => { if (/\b(?:test|it|describe)\.(?:skip|only)\s*\(/.test(line)) violations.push(`${path.relative(rootDir, file).replaceAll('\\', '/')}:${index + 1}`); });
  }
  return violations.length ? { ok: false, code: 'forbidden_test_control', count: violations.length } : { ok: true, code: 'p00_test_surface_frozen', test_count: testFiles.length };
}

function listTestFiles(root) {
  const info = fs.lstatSync(root, { throwIfNoEntry: false });
  if (!info?.isDirectory() || info.isSymbolicLink()) return [];
  const files = [];
  for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
    const target = path.join(root, entry.name);
    if (entry.isDirectory()) files.push(...listTestFiles(target));
    else if (entry.isFile() && /\.test\.js$/.test(entry.name)) files.push(target);
  }
  return files;
}

function parseArgs(argv) {
  if (argv.length !== 2 || argv[0] !== '--fixture-root' || !argv[1]) throw new Error('usage: node scripts/migration-p09/check-prerequisites.js --fixture-root <sanitized-scale-copy>');
  return { fixtureRoot: argv[1] };
}

if (require.main === module) {
  try {
    const result = checkPrerequisites(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify({
      status: result.status, code: result.code, failures: result.failures,
      p00: { run_id: result.baseline?.run_id || null, summary_sha256: result.baseline?.summary_sha256 || null },
      stages: result.stages.map((stage) => ({ stage: stage.stage, run_id: stage.run_id || null, summary_sha256: stage.summary_sha256 || null })),
    })}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch (error) {
    process.stdout.write(`${JSON.stringify({ status: 'blocked', code: 'prerequisite_arguments_invalid' })}\n`);
    process.exitCode = 2;
  }
}

module.exports = { REQUIRED_STAGES, checkPrerequisites, inspectFrozenTestSurface, inspectLatestRun, inspectP00Baseline, inspectStageEvidence, parseArgs, verifyManifest };
