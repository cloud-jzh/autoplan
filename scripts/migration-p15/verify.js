'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { checkUniqueWriter } = require('./check-unique-writer');
const { runElectronGoE2E } = require('./run-electron-go-e2e');
const { runInstallUpgrade } = require('./run-install-upgrade');
const { smokePackage } = require('./smoke-package');

const ROOT = path.resolve(__dirname, '..', '..');
const PLATFORM_BY_HOST = Object.freeze({ win32: 'windows', darwin: 'macos', linux: 'linux' });
const REQUIRED_PLATFORMS = Object.freeze(['windows', 'macos', 'linux']);

function sha256(value) { return crypto.createHash('sha256').update(value).digest('hex'); }
function blocked(code, extra = {}) { return { ok: false, status: 'blocked', code, ...extra }; }

function sourceHash(relative) {
  const file = path.join(ROOT, relative);
  const info = fs.lstatSync(file, { throwIfNoEntry: false });
  if (!info?.isFile() || info.isSymbolicLink()) return { path: relative, missing: true };
  const contents = fs.readFileSync(file);
  return { path: relative, bytes: contents.length, sha256: sha256(contents) };
}

function parseArgs(argv) {
  if (argv[0] !== 'verify') throw new Error('arguments_invalid');
  const options = { fixtureRoot: null, platform: PLATFORM_BY_HOST[process.platform] || null, mode: 'unsigned-test' };
  for (let index = 1; index < argv.length; index += 2) {
    const key = argv[index]; const value = argv[index + 1];
    if (!['--fixture-root', '--e2e-driver', '--install-driver', '--package-driver', '--release-dir', '--platform', '--mode'].includes(key) || !value) throw new Error('arguments_invalid');
    options[{
      '--fixture-root': 'fixtureRoot', '--e2e-driver': 'e2eDriver', '--install-driver': 'installDriver', '--package-driver': 'packageDriver',
      '--release-dir': 'releaseDir', '--platform': 'platform', '--mode': 'mode',
    }[key]] = value;
  }
  if (!options.fixtureRoot || !REQUIRED_PLATFORMS.includes(options.platform) || !['unsigned-test', 'signed-notarized'].includes(options.mode)) throw new Error('arguments_invalid');
  return options;
}

function platformEvidence(platform, options) {
  const e2e = runElectronGoE2E({ fixtureRoot: options.fixtureRoot, driver: options.e2eDriver, environment: options.environment });
  const installUpgrade = runInstallUpgrade({ fixtureRoot: options.fixtureRoot, driver: options.installDriver, environment: options.environment });
  let packageSmoke = blocked('package_smoke_not_requested');
  if (options.releaseDir || options.packageDriver) {
    packageSmoke = smokePackage({
      platform, mode: options.mode, fixtureRoot: options.fixtureRoot, releaseDir: options.releaseDir,
      driver: options.packageDriver, environment: options.environment,
    });
  }
  const failures = [e2e, installUpgrade, packageSmoke].filter((item) => !item.ok).map((item) => item.code);
  return { ok: failures.length === 0, platform, e2e, install_upgrade: installUpgrade, package_smoke: packageSmoke, failures };
}

function verify(options = {}) {
  const platform = options.platform || PLATFORM_BY_HOST[process.platform];
  if (!REQUIRED_PLATFORMS.includes(platform)) return blocked('p15_platform_unsupported');
  const sourceFiles = [
    'package.json', 'scripts/migration-p15/check-unique-writer.js', 'scripts/migration-p15/run-electron-go-e2e.js',
    'scripts/migration-p15/run-install-upgrade.js', 'scripts/migration-p15/smoke-package.js', 'scripts/migration-p15/verify.js',
    'fixtures/migration/p15/electron-go/fixture-manifest.json', 'fixtures/migration/p15/electron-go/e2e-scenarios.json',
  ];
  const sourceHashes = sourceFiles.map(sourceHash);
  const uniqueWriter = checkUniqueWriter();
  const current = platformEvidence(platform, options);
  const platformEvidenceSummary = Object.fromEntries(REQUIRED_PLATFORMS.map((name) => [name, name === platform ? current : { ok: false, status: 'blocked', code: 'platform_evidence_missing' }]));
  const missingPlatforms = REQUIRED_PLATFORMS.filter((name) => !platformEvidenceSummary[name].ok);
  const failures = [
    ...(uniqueWriter.ok ? [] : [uniqueWriter.code]),
    ...current.failures,
    ...(missingPlatforms.length ? ['three_platform_evidence_missing'] : []),
  ];
  return {
    schema_version: 1,
    kind: 'p15-verification',
    status: failures.length ? 'blocked' : 'completed',
    ok: failures.length === 0,
    code: failures[0] || 'p15_verification_completed',
    failures,
    platform,
    publish_policy: 'never',
    source_hashes: sourceHashes,
    unique_writer: uniqueWriter,
    platform_evidence: platformEvidenceSummary,
  };
}

if (require.main === module) {
  try {
    const result = verify(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify({ status: result.status, ok: result.ok, code: result.code, failures: result.failures, platform: result.platform })}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch {
    process.stdout.write('{"status":"blocked","code":"p15_verification_arguments_invalid"}\n');
    process.exitCode = 2;
  }
}

module.exports = { PLATFORM_BY_HOST, REQUIRED_PLATFORMS, parseArgs, platformEvidence, sourceHash, verify };
