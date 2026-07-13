'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const ROOT = path.resolve(__dirname, '..');
const BACKEND_ROOT = path.join(ROOT, 'backend');
const OUTPUT_ROOT = path.join(ROOT, 'artifacts', 'sidecar');
const GO_BUILD_CACHE_ROOT = path.join(ROOT, '.autoplan-runtime', 'go-build-cache');
const TARGETS = Object.freeze({
  win32: Object.freeze({ goos: 'windows', binary: 'autoplan-server.exe', arches: Object.freeze({ x64: 'amd64' }) }),
  darwin: Object.freeze({ goos: 'darwin', binary: 'autoplan-server', arches: Object.freeze({ x64: 'amd64', arm64: 'arm64' }) }),
  linux: Object.freeze({ goos: 'linux', binary: 'autoplan-server', arches: Object.freeze({ x64: 'amd64' }) }),
});

class SidecarBuildError extends Error {
  constructor(code) {
    super(code);
    this.name = 'SidecarBuildError';
    this.code = code;
  }
}

function sha256(value) { return crypto.createHash('sha256').update(value).digest('hex'); }

function backendSourceHash() {
  const files = [];
  const collect = (directory, relative = '') => {
    for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
      const nextRelative = path.posix.join(relative, entry.name);
      const full = path.join(directory, entry.name);
      if (entry.isDirectory()) collect(full, nextRelative);
      else if (entry.isFile() && !entry.isSymbolicLink()) files.push({ full, relative: nextRelative });
      else throw new SidecarBuildError('sidecar_build_source_invalid');
    }
  };
  collect(BACKEND_ROOT);
  const digest = crypto.createHash('sha256');
  for (const file of files.sort((left, right) => left.relative < right.relative ? -1 : left.relative > right.relative ? 1 : 0)) {
    digest.update(file.relative).update('\0').update(fs.readFileSync(file.full)).update('\0');
  }
  return digest.digest('hex');
}

function assertBinaryFormat(content, platform) {
  if (platform === 'win32' && content.subarray(0, 2).equals(Buffer.from('MZ'))) return;
  if (platform === 'linux' && content.subarray(0, 4).equals(Buffer.from([0x7f, 0x45, 0x4c, 0x46]))) return;
  if (platform === 'darwin' && content.length >= 4) {
    const magic = content.readUInt32BE(0);
    if ([0xfeedface, 0xfeedfacf, 0xcefaedfe, 0xcffaedfe].includes(magic)) return;
  }
  throw new SidecarBuildError('sidecar_build_binary_platform_invalid');
}

function parseArgs(argv) {
  if (argv.length !== 4 || argv[0] !== '--platform' || argv[2] !== '--arch') throw new SidecarBuildError('sidecar_build_arguments_invalid');
  const platform = argv[1];
  const arch = argv[3];
  if (!TARGETS[platform] || !TARGETS[platform].arches[arch]) throw new SidecarBuildError('sidecar_build_target_unsupported');
  return { platform, arch };
}

function safeFile(file, maximumBytes = 32 * 1024 * 1024) {
  const info = fs.lstatSync(file, { throwIfNoEntry: false });
  if (!info?.isFile() || info.isSymbolicLink() || info.size <= 0 || info.size > maximumBytes) {
    throw new SidecarBuildError('sidecar_build_file_invalid');
  }
  return info;
}

function requireBackendModule() {
  for (const relative of ['go.mod', 'go.sum', 'cmd/autoplan-server/main.go']) {
    safeFile(path.join(BACKEND_ROOT, relative));
  }
  const module = fs.readFileSync(path.join(BACKEND_ROOT, 'go.mod'), 'utf8');
  if (!module.includes('module github.com/lyming99/autoplan/backend')) {
    throw new SidecarBuildError('sidecar_build_module_invalid');
  }
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd || ROOT,
    env: options.env || process.env,
    encoding: 'utf8',
    windowsHide: true,
    shell: false,
  });
  if (result.error || result.status !== 0) throw new SidecarBuildError(options.errorCode || 'sidecar_build_command_failed');
  return String(result.stdout || '').trim();
}

function gitCommit() {
  const commit = run('git', ['rev-parse', '--verify', 'HEAD'], { errorCode: 'sidecar_build_commit_unavailable' });
  if (!/^[a-f0-9]{40}$/i.test(commit)) throw new SidecarBuildError('sidecar_build_commit_invalid');
  return commit.toLowerCase();
}

function goVersion() {
  const version = run('go', ['version'], { cwd: BACKEND_ROOT, errorCode: 'sidecar_build_go_unavailable' });
  if (!/^go version go\d+\.\d+(?:\.\d+)?\s/.test(version)) throw new SidecarBuildError('sidecar_build_go_version_invalid');
  return version;
}

function outputDirectory(platform, arch) {
  const target = path.resolve(OUTPUT_ROOT, platform, arch);
  const relative = path.relative(OUTPUT_ROOT, target);
  if (!relative || relative.startsWith('..') || path.isAbsolute(relative)) throw new SidecarBuildError('sidecar_build_output_invalid');
  return target;
}

function writeJSON(file, value) {
  fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`, { encoding: 'utf8', mode: 0o600, flag: 'w' });
}

function buildSidecar(options) {
  const platform = options?.platform;
  const arch = options?.arch;
  const target = TARGETS[platform];
  if (!target || !target.arches[arch]) throw new SidecarBuildError('sidecar_build_target_unsupported');
  requireBackendModule();
  const commit = gitCommit();
  const version = goVersion();
  const sourceTreeSha256 = backendSourceHash();
  const outputDir = outputDirectory(platform, arch);
  fs.mkdirSync(outputDir, { recursive: true, mode: 0o700 });
  fs.mkdirSync(GO_BUILD_CACHE_ROOT, { recursive: true, mode: 0o700 });
  const temporaryDir = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-sidecar-build-'));
  const temporaryBinary = path.join(temporaryDir, target.binary);
  const environment = {
    ...process.env,
    GOOS: target.goos,
    GOARCH: target.arches[arch],
    GOCACHE: GO_BUILD_CACHE_ROOT,
    GOFLAGS: `${process.env.GOFLAGS || ''} -trimpath`.trim(),
  };
  // Explicitly do not pass session, database, userData, or renderer values to
  // the compiler process. The Go source and module graph are the only inputs.
  for (const key of Object.keys(environment)) {
    if (/(?:session|token|secret|userdata|database|db_path)/i.test(key)) delete environment[key];
  }
  try {
    run('go', ['build', '-trimpath', '-buildvcs=true', '-ldflags=-buildid=', '-o', temporaryBinary, './cmd/autoplan-server'], {
      cwd: BACKEND_ROOT, env: environment, errorCode: 'sidecar_build_go_failed',
    });
    const binaryInfo = safeFile(temporaryBinary, 256 * 1024 * 1024);
    if (platform !== 'win32') fs.chmodSync(temporaryBinary, 0o755);
    const destination = path.join(outputDir, target.binary);
    fs.copyFileSync(temporaryBinary, destination);
    if (platform !== 'win32') fs.chmodSync(destination, 0o755);
    const bytes = fs.readFileSync(destination);
    assertBinaryFormat(bytes, platform);
    const manifest = {
      schema_version: 1,
      kind: 'autoplan-go-sidecar-build',
      platform,
      arch,
      goos: target.goos,
      goarch: target.arches[arch],
      binary: target.binary,
      bytes: binaryInfo.size,
      sha256: sha256(bytes),
      go_version: version,
      source_commit: commit,
      source_tree_sha256: sourceTreeSha256,
      source_files: {
        'backend/go.mod': sha256(fs.readFileSync(path.join(BACKEND_ROOT, 'go.mod'))),
        'backend/go.sum': sha256(fs.readFileSync(path.join(BACKEND_ROOT, 'go.sum'))),
      },
      build_mode: 'local-sidecar-only',
    };
    writeJSON(path.join(outputDir, 'autoplan-server.manifest.json'), manifest);
    return Object.freeze({ binary: destination, manifest, manifestPath: path.join(outputDir, 'autoplan-server.manifest.json') });
  } finally {
    fs.rmSync(temporaryDir, { recursive: true, force: true });
  }
}

if (require.main === module) {
  try {
    const result = buildSidecar(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify({ status: 'built', platform: result.manifest.platform, arch: result.manifest.arch, sha256: result.manifest.sha256, bytes: result.manifest.bytes })}\n`);
  } catch (error) {
    process.stdout.write(`${JSON.stringify({ status: 'blocked', code: error?.code || 'sidecar_build_failed' })}\n`);
    process.exitCode = 2;
  }
}

module.exports = { BACKEND_ROOT, OUTPUT_ROOT, SidecarBuildError, TARGETS, assertBinaryFormat, backendSourceHash, buildSidecar, gitCommit, goVersion, outputDirectory, parseArgs, requireBackendModule, sha256 };
