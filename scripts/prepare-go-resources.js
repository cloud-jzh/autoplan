'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(__dirname, '..');
const ARTIFACT_ROOT = path.join(ROOT, 'artifacts', 'sidecar');
const RESOURCE_ROOT = path.join(ROOT, 'resources', 'sidecar');
const BACKEND_ROOT = path.join(ROOT, 'backend');
const TARGETS = Object.freeze({
  win32: Object.freeze({ binary: 'autoplan-server.exe', arches: new Set(['x64']) }),
  darwin: Object.freeze({ binary: 'autoplan-server', arches: new Set(['x64', 'arm64']) }),
  linux: Object.freeze({ binary: 'autoplan-server', arches: new Set(['x64']) }),
});

class SidecarResourceError extends Error {
  constructor(code) {
    super(code);
    this.name = 'SidecarResourceError';
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
      else throw new SidecarResourceError('sidecar_resource_source_invalid');
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
  throw new SidecarResourceError('sidecar_resource_binary_platform_invalid');
}

function parseArgs(argv) {
  if (argv.length === 0 || argv.length % 4 !== 0) throw new SidecarResourceError('sidecar_resource_arguments_invalid');
  const targets = [];
  for (let index = 0; index < argv.length; index += 4) {
    if (argv[index] !== '--platform' || argv[index + 2] !== '--arch') throw new SidecarResourceError('sidecar_resource_arguments_invalid');
    const platform = argv[index + 1];
    const arch = argv[index + 3];
    if (!TARGETS[platform] || !TARGETS[platform].arches.has(arch)) throw new SidecarResourceError('sidecar_resource_target_unsupported');
    if (targets.some((target) => target.platform === platform && target.arch === arch)) throw new SidecarResourceError('sidecar_resource_target_duplicate');
    targets.push({ platform, arch });
  }
  return { targets };
}

function within(target, root) {
  const relative = path.relative(path.resolve(root), path.resolve(target));
  return relative !== '' && relative !== '..' && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative);
}

function requireRegularFile(file, code) {
  const info = fs.lstatSync(file, { throwIfNoEntry: false });
  if (!info?.isFile() || info.isSymbolicLink() || info.size <= 0) throw new SidecarResourceError(code);
  return info;
}

function readBuildManifest(platform, arch) {
  const target = TARGETS[platform];
  const source = path.join(ARTIFACT_ROOT, platform, arch);
  if (!within(source, ARTIFACT_ROOT)) throw new SidecarResourceError('sidecar_resource_source_invalid');
  const manifestPath = path.join(source, 'autoplan-server.manifest.json');
  requireRegularFile(manifestPath, 'sidecar_resource_manifest_missing');
  let manifest;
  try { manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8')); } catch { throw new SidecarResourceError('sidecar_resource_manifest_invalid'); }
  if (!manifest || manifest.schema_version !== 1 || manifest.kind !== 'autoplan-go-sidecar-build' ||
      manifest.platform !== platform || manifest.arch !== arch || manifest.binary !== target.binary ||
      !/^[a-f0-9]{64}$/.test(manifest.sha256 || '') || !/^[a-f0-9]{64}$/.test(manifest.source_tree_sha256 || '') || !Number.isSafeInteger(manifest.bytes) || manifest.bytes <= 0 ||
      !/^[a-f0-9]{40}$/.test(manifest.source_commit || '')) {
    throw new SidecarResourceError('sidecar_resource_manifest_invalid');
  }
  const binary = path.join(source, target.binary);
  const info = requireRegularFile(binary, 'sidecar_resource_binary_missing');
  const content = fs.readFileSync(binary);
  if (info.size !== manifest.bytes || sha256(content) !== manifest.sha256) throw new SidecarResourceError('sidecar_resource_checksum_invalid');
  assertBinaryFormat(content, platform);
  if (manifest.source_tree_sha256 !== backendSourceHash()) throw new SidecarResourceError('sidecar_resource_stale');
  return { binary, manifest, manifestPath };
}

function safeTargetDirectory(platform, arch) {
  const target = path.resolve(RESOURCE_ROOT, platform, arch);
  if (!within(target, RESOURCE_ROOT)) throw new SidecarResourceError('sidecar_resource_target_invalid');
  return target;
}

function removeTarget(target) {
  const parent = path.resolve(RESOURCE_ROOT);
  if (!within(target, parent)) throw new SidecarResourceError('sidecar_resource_target_invalid');
  fs.rmSync(target, { recursive: true, force: true, maxRetries: 1 });
}

function writeJSON(file, value) {
  fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`, { encoding: 'utf8', mode: 0o600, flag: 'w' });
}

function prepareTarget(platform, arch) {
  const source = readBuildManifest(platform, arch);
  const target = safeTargetDirectory(platform, arch);
  removeTarget(target);
  fs.mkdirSync(target, { recursive: true, mode: 0o700 });
  const binary = path.join(target, source.manifest.binary);
  fs.copyFileSync(source.binary, binary);
  if (platform !== 'win32') fs.chmodSync(binary, 0o755);
  const copied = fs.readFileSync(binary);
  if (sha256(copied) !== source.manifest.sha256) throw new SidecarResourceError('sidecar_resource_copy_invalid');
  const manifest = {
    schema_version: 1,
    kind: 'autoplan-packaged-sidecar-resource',
    platform,
    arch,
    binary: source.manifest.binary,
    bytes: source.manifest.bytes,
    sha256: source.manifest.sha256,
    source_commit: source.manifest.source_commit,
    source_tree_sha256: source.manifest.source_tree_sha256,
    go_version: source.manifest.go_version,
    source_manifest_sha256: sha256(fs.readFileSync(source.manifestPath)),
  };
  writeJSON(path.join(target, 'autoplan-server.manifest.json'), manifest);
  return { target, manifest };
}

function prepareResources(options) {
  if (!options?.targets?.length) throw new SidecarResourceError('sidecar_resource_arguments_invalid');
  return options.targets.map((target) => prepareTarget(target.platform, target.arch));
}

if (require.main === module) {
  try {
    const results = prepareResources(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify({ status: 'prepared', targets: results.map((result) => ({ platform: result.manifest.platform, arch: result.manifest.arch, sha256: result.manifest.sha256 })) })}\n`);
  } catch (error) {
    process.stdout.write(`${JSON.stringify({ status: 'blocked', code: error?.code || 'sidecar_resource_failed' })}\n`);
    process.exitCode = 2;
  }
}

module.exports = { ARTIFACT_ROOT, BACKEND_ROOT, RESOURCE_ROOT, SidecarResourceError, TARGETS, assertBinaryFormat, backendSourceHash, parseArgs, prepareResources, prepareTarget, readBuildManifest, sha256, within };
