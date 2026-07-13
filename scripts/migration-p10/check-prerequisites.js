'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { inspectEvidenceFile, inspectOwnerSafety, isWithin, sha256, validateFixtureRoot } = require('./check-safety');

const REQUIRED_EVIDENCE_STAGES = ['p09'];

function checkPrerequisites(options = {}) {
  const rootDir = path.resolve(options.rootDir || path.join(__dirname, '..', '..'));
  const fixture = validateFixtureRoot(options.fixtureRoot);
  const baseline = inspectP00Baseline(rootDir);
  const stages = REQUIRED_EVIDENCE_STAGES.map((stage) => inspectStageEvidence(rootDir, stage));
  const owner = fixture.ok ? inspectOwnerSafety(options.fixtureRoot) : { ok: false, code: 'owner_not_checked' };
  const p09Owner = inspectP09OwnerEvidence(stages.find((stage) => stage.stage === 'p09'));
  const frozenTests = inspectFrozenTestSurface(rootDir);
  const failures = [fixture, baseline, ...stages, owner, p09Owner, frozenTests].filter((item) => !item.ok).map((item) => item.code);
  return {
    schema_version: 1, status: failures.length ? 'blocked' : 'ready', ok: failures.length === 0,
    code: failures[0] || 'prerequisites_ready', failures, fixture, baseline, stages, owner, p09_owner: p09Owner, frozen_tests: frozenTests,
  };
}

function inspectP00Baseline(rootDir) {
  const result = inspectLatestRun(path.join(rootDir, 'docs', 'migration', 'p00', 'evidence', 'runs'));
  if (!result.ok) return { ...result, code: `p00_${result.code}` };
  const summary = result.summary;
  const commands = Array.isArray(summary.commandResults) ? summary.commandResults : [];
  const check = commands.find((item) => item.id === 'check');
  const test = commands.find((item) => item.id === 'test');
  const expectedRed = check?.expectedOutcome === 'exact-known-failure' && check?.exitCode !== 0 &&
    Array.isArray(check?.failureSignatures) && check.failureSignatures.includes('file-length|scripts/smoke-test.js|limit=3800');
  if (!summary.sourceHashesStable || !summary.expectationsHashStable || !summary.ok || !expectedRed ||
      check?.evaluation?.accepted !== true || test?.evaluation?.accepted !== true) return { ok: false, code: 'p00_baseline_signature_invalid' };
  return { ok: true, code: 'p00_baseline_accepted', run_id: result.run_id, summary_sha256: result.summary_sha256 };
}

function inspectStageEvidence(rootDir, stage) {
  const result = inspectLatestRun(path.join(rootDir, 'docs', 'migration', stage, 'evidence', 'runs'));
  if (!result.ok) return { ...result, stage, code: `${stage}_${result.code}` };
  const summary = result.summary;
  if (summary.status !== 'completed' || summary.ok !== true ||
      (summary.sourceHashesStable !== true && summary.source_hashes_stable !== true)) {
    return { ok: false, stage, code: `${stage}_summary_not_completed` };
  }
  return { ok: true, stage, code: `${stage}_evidence_accepted`, run_id: result.run_id, summary_sha256: result.summary_sha256 };
}

function inspectP09OwnerEvidence(stage) {
  if (!stage?.ok || !Array.isArray(stage.summary?.owner_timeline)) return { ok: false, code: 'p09_owner_evidence_invalid' };
  const goOwner = stage.summary.owner_timeline.some((item) => item?.owner === 'go' && item?.writer_count === 1);
  const nodeWriter = stage.summary.owner_timeline.some((item) => item?.owner === 'node' || item?.node_writer === true || item?.writer_count > 1);
  return goOwner && !nodeWriter ? { ok: true, code: 'p09_go_owner_accepted' } : { ok: false, code: 'p09_owner_evidence_invalid' };
}

function inspectLatestRun(evidenceRoot) {
  const rootInfo = fs.lstatSync(evidenceRoot, { throwIfNoEntry: false });
  if (!rootInfo?.isDirectory() || rootInfo.isSymbolicLink()) return { ok: false, code: 'evidence_runs_missing' };
  const names = fs.readdirSync(evidenceRoot, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && /^[A-Za-z0-9._-]{1,160}$/.test(entry.name))
    .map((entry) => entry.name).sort().reverse();
  for (const runID of names) {
    const runDir = path.join(evidenceRoot, runID);
    const summaryPath = path.join(runDir, 'summary.json');
    const manifestPath = path.join(runDir, 'evidence-manifest.json');
    const summary = readSafeJSON(summaryPath, runDir);
    const manifest = readSafeJSON(manifestPath, runDir);
    if (summary && manifest && manifest.immutable_run_directory === true && verifyManifest(runDir, manifest)) {
      return { ok: true, code: 'evidence_run_valid', run_id: runID, summary, summary_sha256: sha256(fs.readFileSync(summaryPath)) };
    }
  }
  return { ok: false, code: 'evidence_integrity_invalid' };
}

function verifyManifest(runDir, manifest) {
  if (!Array.isArray(manifest.artifacts) || !manifest.artifacts.length) return false;
  const summaryRecord = manifest.artifacts.find((item) => item?.path === 'summary.json');
  if (!summaryRecord || summaryRecord.sha256 !== sha256(fs.readFileSync(path.join(runDir, 'summary.json')))) return false;
  return manifest.artifacts.every((item) => {
    if (!item || typeof item.path !== 'string' || !/^[A-Za-z0-9._/-]+$/.test(item.path) ||
        !/^[a-f0-9]{64}$/.test(item.sha256 || '') || !Number.isInteger(item.bytes) || item.bytes < 0) return false;
    const target = path.resolve(runDir, item.path);
    const safe = inspectEvidenceFile(target, runDir);
    const info = fs.lstatSync(target, { throwIfNoEntry: false });
    return isWithin(target, runDir) && Boolean(info?.isFile() && !info.isSymbolicLink() && info.size === item.bytes && safe.ok && safe.sha256 === item.sha256);
  });
}

function readSafeJSON(file, root) {
  if (!inspectEvidenceFile(file, root).ok) return null;
  try { return JSON.parse(fs.readFileSync(file, 'utf8')); } catch { return null; }
}

function inspectFrozenTestSurface(rootDir) {
  try {
    const pkg = JSON.parse(fs.readFileSync(path.join(rootDir, 'package.json'), 'utf8'));
    if (pkg?.scripts?.test !== 'node --test "src/**/*.test.js"' || typeof pkg?.scripts?.check !== 'string' ||
        pkg?.scripts?.['migration:p10:verify'] !== 'node scripts/migration-p10/verify.js verify') return { ok: false, code: 'p00_test_surface_changed' };
  } catch { return { ok: false, code: 'p00_test_surface_unreadable' }; }
  const violations = listTestFiles(path.join(rootDir, 'src')).concat(listTestFiles(path.join(rootDir, 'scripts', 'migration-p10')))
    .flatMap((file) => fs.readFileSync(file, 'utf8').split(/\r?\n/).map((line, index) => ({ file, line, index })))
    .filter((item) => /\b(?:test|it|describe)\.(?:skip|only)\s*\(/.test(item.line));
  return violations.length ? { ok: false, code: 'forbidden_test_control', count: violations.length } : { ok: true, code: 'p00_test_surface_frozen' };
}

function listTestFiles(root) {
  const info = fs.lstatSync(root, { throwIfNoEntry: false });
  if (!info?.isDirectory() || info.isSymbolicLink()) return [];
  return fs.readdirSync(root, { withFileTypes: true }).flatMap((entry) => {
    const target = path.join(root, entry.name);
    if (entry.isDirectory()) return listTestFiles(target);
    return entry.isFile() && /\.test\.js$/.test(entry.name) ? [target] : [];
  });
}

function parseArgs(argv) {
  if (argv.length !== 2 || argv[0] !== '--fixture-root' || !argv[1]) throw new Error('usage: node scripts/migration-p10/check-prerequisites.js --fixture-root <authorized-p10-fixture>');
  return { fixtureRoot: argv[1] };
}

if (require.main === module) {
  try {
    const result = checkPrerequisites(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify({ status: result.status, code: result.code, failures: result.failures,
      p00: { run_id: result.baseline?.run_id || null, summary_sha256: result.baseline?.summary_sha256 || null },
      stages: result.stages.map((stage) => ({ stage: stage.stage, run_id: stage.run_id || null, summary_sha256: stage.summary_sha256 || null })),
    })}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch {
    process.stdout.write('{"status":"blocked","code":"prerequisite_arguments_invalid"}\n');
    process.exitCode = 2;
  }
}

module.exports = { REQUIRED_EVIDENCE_STAGES, checkPrerequisites, inspectFrozenTestSurface, inspectLatestRun, inspectP00Baseline, inspectP09OwnerEvidence, inspectStageEvidence, parseArgs, verifyManifest };
