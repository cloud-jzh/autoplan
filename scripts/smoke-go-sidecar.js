'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { GoDaemonSupervisor } = require('../src/daemon/supervisor');
const { binaryName, resolveDaemonBinary } = require('../src/daemon/binaryPath');

const ROOT = path.resolve(__dirname, '..');
const RESOURCE_ROOT = path.join(ROOT, 'resources');
const PLATFORM = Object.freeze({ windows: 'win32', macos: 'darwin', linux: 'linux' });
const ARCHES = Object.freeze({ win32: new Set(['x64']), darwin: new Set(['x64', 'arm64']), linux: new Set(['x64']) });

class SidecarSmokeError extends Error {
  constructor(code) {
    super(code);
    this.name = 'SidecarSmokeError';
    this.code = code;
  }
}

function parseArgs(argv) {
  if (argv.length !== 2 || argv[0] !== '--platform' || !PLATFORM[argv[1]]) {
    throw new SidecarSmokeError('sidecar_smoke_arguments_invalid');
  }
  return { platform: argv[1] };
}

function resourceBinaryPath(resourceRoot, platform, arch) {
  if (!ARCHES[platform]?.has(arch)) throw new SidecarSmokeError('sidecar_smoke_target_unsupported');
  return path.join(path.resolve(resourceRoot), 'sidecar', platform, arch, binaryName(platform));
}

function removeRuntimeDirectory(directory) {
  const root = path.resolve(os.tmpdir());
  const target = path.resolve(directory);
  const relative = path.relative(root, target);
  if (!relative || relative === '..' || relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative)) {
    throw new SidecarSmokeError('sidecar_smoke_cleanup_path_invalid');
  }
  fs.rmSync(target, { recursive: true, force: true, maxRetries: 2 });
}

async function smokeSidecar(options) {
  const platform = PLATFORM[options?.platform];
  if (!platform) throw new SidecarSmokeError('sidecar_smoke_arguments_invalid');
  if (platform !== process.platform) throw new SidecarSmokeError('sidecar_smoke_host_mismatch');
  const arch = process.arch;
  resourceBinaryPath(RESOURCE_ROOT, platform, arch);
  const binary = resolveDaemonBinary({
    isPackaged: true,
    resourcesPath: RESOURCE_ROOT,
    platform,
    arch,
  });
  const runtimeDirectory = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-sidecar-smoke-'));
  let supervisor;
  try {
    supervisor = new GoDaemonSupervisor({
      executablePath: binary.path,
      dataDir: runtimeDirectory,
      readyTimeoutMs: 15000,
    });
    const status = await supervisor.start();
    if (!status.ready) throw new SidecarSmokeError('sidecar_smoke_not_ready');
    return {
      status: 'verified',
      code: 'sidecar_runtime_ready',
      platform: options.platform,
      arch,
    };
  } finally {
    await supervisor?.stop();
    removeRuntimeDirectory(runtimeDirectory);
  }
}

if (require.main === module) {
  smokeSidecar(parseArgs(process.argv.slice(2)))
    .then((result) => process.stdout.write(`${JSON.stringify(result)}\n`))
    .catch((error) => {
      process.stdout.write(`${JSON.stringify({ status: 'blocked', code: error?.code || 'sidecar_runtime_smoke_failed' })}\n`);
      process.exitCode = 1;
    });
}

module.exports = { ARCHES, PLATFORM, RESOURCE_ROOT, SidecarSmokeError, parseArgs, resourceBinaryPath, smokeSidecar };
