'use strict';

const fs = require('node:fs');
const path = require('node:path');

class DaemonBinaryPathError extends Error {
  constructor(code) {
    super(code);
    this.name = 'DaemonBinaryPathError';
    this.code = code;
  }
}

function binaryName(platform) {
  if (!['win32', 'darwin', 'linux'].includes(platform)) throw new DaemonBinaryPathError('daemon_platform_unsupported');
  return platform === 'win32' ? 'autoplan-server.exe' : 'autoplan-server';
}

function isWithin(target, root) {
  const relative = path.relative(path.resolve(root), path.resolve(target));
  return relative !== '' && relative !== '..' && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative);
}

function verifyBinary(candidate, trustedRoot, platform) {
  if (!candidate || !path.isAbsolute(candidate)) throw new DaemonBinaryPathError('daemon_binary_invalid');
  const resolved = path.resolve(candidate);
  const expectedName = binaryName(platform);
  const actualName = path.basename(resolved);
  if ((platform === 'win32' ? actualName.toLowerCase() : actualName) !==
      (platform === 'win32' ? expectedName.toLowerCase() : expectedName)) {
    throw new DaemonBinaryPathError('daemon_binary_name_invalid');
  }
  if (trustedRoot && !isWithin(resolved, trustedRoot)) throw new DaemonBinaryPathError('daemon_binary_outside_trusted_root');
  const info = fs.lstatSync(resolved, { throwIfNoEntry: false });
  if (!info?.isFile() || info.isSymbolicLink() || info.size <= 0) throw new DaemonBinaryPathError('daemon_binary_invalid');
  let real;
  try { real = fs.realpathSync.native(resolved); } catch { throw new DaemonBinaryPathError('daemon_binary_invalid'); }
  if (trustedRoot) {
    let realRoot;
    try { realRoot = fs.realpathSync.native(path.resolve(trustedRoot)); } catch { throw new DaemonBinaryPathError('daemon_binary_trusted_root_invalid'); }
    if (!isWithin(real, realRoot)) throw new DaemonBinaryPathError('daemon_binary_outside_trusted_root');
  }
  if (platform !== 'win32' && (info.mode & 0o111) === 0) throw new DaemonBinaryPathError('daemon_binary_not_executable');
  return path.resolve(real);
}

// P003 places exactly one platform/architecture sidecar below this fixed
// resource root. Packaged builds never honour an environment override.
function resolveDaemonBinary(options = {}) {
  const platform = options.platform || process.platform;
  const arch = options.arch || process.arch;
  if (!/^[a-z0-9_]+$/.test(arch)) throw new DaemonBinaryPathError('daemon_arch_invalid');
  const name = binaryName(platform);
  if (options.isPackaged === true) {
    const resourcesPath = String(options.resourcesPath || '').trim();
    if (!resourcesPath || !path.isAbsolute(resourcesPath)) throw new DaemonBinaryPathError('daemon_resources_path_invalid');
    const root = path.resolve(resourcesPath, 'sidecar');
    return Object.freeze({
      path: verifyBinary(path.join(root, platform, arch, name), root, platform),
      source: 'packaged', platform, arch,
    });
  }
  const developmentPath = String(options.developmentPath || '').trim();
  if (!developmentPath) throw new DaemonBinaryPathError('daemon_development_binary_required');
  return Object.freeze({
    path: verifyBinary(developmentPath, null, platform),
    source: 'development', platform, arch,
  });
}

module.exports = { DaemonBinaryPathError, binaryName, isWithin, resolveDaemonBinary, verifyBinary };
