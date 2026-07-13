'use strict';

const http = require('node:http');
const fs = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');

const root = path.resolve(__dirname, '..');
const dependencyRoot = dependencyRootFor(root);
const isWindows = process.platform === 'win32';
const platform = process.platform;
const arch = normalizeArch(process.arch);
const children = new Set();
let shuttingDown = false;
let electronProcess = null;
let viteProcess = null;

function dependencyRootFor(projectRoot) {
  return process.env.AUTOPLAN_NODE_MODULES || path.join(projectRoot, 'node_modules');
}

function normalizeArch(value) {
  if (value === 'x64' || value === 'arm64') return value;
  throw new Error('development_arch_unsupported');
}

function bin(name) {
  return path.join(dependencyRoot, '.bin', `${name}${isWindows ? '.cmd' : ''}`);
}

function spawnChild(command, args, options = {}) {
  const child = spawn(isWindows ? 'cmd.exe' : command, isWindows ? ['/d', '/c', 'call', command, ...args] : args, {
    cwd: root,
    stdio: options.stdio || 'inherit',
    detached: !isWindows,
    windowsHide: true,
    shell: false,
    env: { ...process.env, NODE_PATH: dependencyRoot, ...options.env },
  });
  children.add(child);
  child.once('exit', (code, signal) => {
    children.delete(child);
    if (!shuttingDown && child === electronProcess) shutdown(exitCode(code, signal));
    if (!shuttingDown && child === viteProcess) shutdown(exitCode(code, signal) || 1);
  });
  child.once('error', () => { if (!shuttingDown) shutdown(1); });
  return child;
}

function exitCode(code, signal) {
  if (Number.isInteger(code)) return code;
  return signal ? 1 : 0;
}

function runNodeScript(script, args) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [script, ...args], {
      cwd: root,
      stdio: 'inherit',
      windowsHide: true,
      shell: false,
      env: { ...process.env, NODE_PATH: dependencyRoot },
    });
    child.once('error', () => reject(new Error('development_sidecar_build_failed')));
    child.once('exit', (code) => code === 0 ? resolve() : reject(new Error('development_sidecar_build_failed')));
  });
}

async function requiredDevelopmentDataDirectory() {
  const configured = String(process.env.AUTOPLAN_GO_DATA_DIR || '').trim();
  const candidate = configured || path.join(os.tmpdir(), 'autoplan-dev-sidecar');
  if (!path.isAbsolute(candidate)) throw new Error('development_sidecar_data_dir_invalid');
  if (!configured) fs.mkdirSync(candidate, { recursive: true });
  const info = fs.lstatSync(candidate, { throwIfNoEntry: false });
  if (!info?.isDirectory() || info.isSymbolicLink()) throw new Error('development_sidecar_data_dir_invalid');
  // The sidecar owns schema migration. Never inspect or remove its SQLite
  // files here: a live WAL can make a valid database look incomplete and
  // deleting it loses every project created during the previous dev run.
  return path.resolve(candidate);
}

function sidecarPath() {
  const binary = platform === 'win32' ? 'autoplan-server.exe' : 'autoplan-server';
  return path.join(root, 'artifacts', 'sidecar', platform, arch, binary);
}

function developmentOwnerEnvironment(rendererUrl, dataDir) {
  const binary = sidecarPath();
  const info = fs.lstatSync(binary, { throwIfNoEntry: false });
  if (!info?.isFile() || info.isSymbolicLink() || info.size <= 0) throw new Error('development_sidecar_binary_missing');
  return {
    ELECTRON_RENDERER_URL: rendererUrl,
    AUTOPLAN_DATABASE_OWNER: 'go',
    // This non-routable ownership marker is consumed only by the legacy guard;
    // P002 replaces it with the supervisor's random readiness authority.
    AUTOPLAN_GO_API_URL: 'http://127.0.0.1:1',
    AUTOPLAN_GO_DAEMON_PATH: binary,
    AUTOPLAN_GO_DATA_DIR: dataDir,
    AUTOPLAN_SIDECAR_RENDERER_ORIGIN: rendererUrl,
    // Keep Chromium's cache out of a potentially locked user profile while
    // developing. This affects only disposable browser session data; the Go
    // database above remains stable across restarts.
    AUTOPLAN_ELECTRON_SESSION_DATA_DIR: fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-dev-electron-')),
  };
}

function reserveDevelopmentPort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    let settled = false;
    const finish = (error, port) => {
      if (settled) return;
      settled = true;
      if (error) reject(new Error('development_vite_port_unavailable')); else resolve(port);
    };
    server.unref?.();
    server.once('error', (error) => finish(error));
    server.listen({ host: '127.0.0.1', port: 0, exclusive: true }, () => {
      const address = server.address();
      const port = address && typeof address === 'object' ? address.port : 0;
      server.close((error) => {
        if (error || !Number.isInteger(port) || port < 1 || port > 65535) finish(error || new Error('invalid_port'));
        else finish(null, port);
      });
    });
  });
}

async function startVite() {
  const port = await reserveDevelopmentPort();
  return new Promise((resolve, reject) => {
    const child = spawnChild(bin('vite'), ['--host', '127.0.0.1', '--port', String(port), '--strictPort'], {
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    viteProcess = child;
    let settled = false;
    let output = '';
    const finish = (error, value) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      child.stdout?.removeListener('data', onOutput);
      child.stderr?.removeListener('data', onOutput);
      if (error) reject(error); else resolve(value);
    };
    const onOutput = (chunk) => {
      const text = chunk.toString('utf8');
      output = `${output}${text}`.slice(-8192);
      process.stdout.write(text);
      const plainOutput = output.replace(/\u001B\[[0-?]*[ -/]*[@-~]/g, '');
      const match = plainOutput.match(/https?:\/\/127\.0\.0\.1:(\d{2,5})(?:\/|\s|$)/);
      if (match) finish(null, `http://127.0.0.1:${match[1]}`);
    };
    const timer = setTimeout(() => finish(new Error('development_vite_readiness_timeout')), 30_000);
    timer.unref?.();
    child.stdout?.on('data', onOutput);
    child.stderr?.on('data', onOutput);
    child.once('exit', () => finish(new Error('development_vite_exited_early')));
    child.once('error', () => finish(new Error('development_vite_spawn_failed')));
  });
}

async function main() {
  const dataDir = await requiredDevelopmentDataDirectory();
  await runNodeScript('scripts/build-go-sidecar.js', ['--platform', platform, '--arch', arch]);
  const rendererUrl = await startVite();
  electronProcess = spawnChild(bin('electron'), ['.'], {
    env: developmentOwnerEnvironment(rendererUrl, dataDir),
  });
}

function terminateChild(child) {
  if (!child?.pid || child.exitCode !== null || child.signalCode) return;
  if (isWindows) {
    const killer = spawn('taskkill', ['/pid', String(child.pid), '/t', '/f'], { windowsHide: true, stdio: 'ignore', shell: false });
    killer.unref?.();
    return;
  }
  try { process.kill(-child.pid, 'SIGTERM'); } catch { try { child.kill('SIGTERM'); } catch { /* already exited */ } }
}

function shutdown(code) {
  if (shuttingDown) return;
  shuttingDown = true;
  for (const child of children) terminateChild(child);
  setTimeout(() => process.exit(code), 500).unref();
}

process.on('SIGINT', () => shutdown(130));
process.on('SIGTERM', () => shutdown(143));

if (require.main === module) {
  main().catch((error) => {
    const code = /^[a-z0-9_]{1,96}$/i.test(String(error?.message || '')) ? error.message : 'development_start_failed';
    process.stderr.write(`${code}\n`);
    shutdown(1);
  });
}

module.exports = {
  developmentOwnerEnvironment,
  normalizeArch,
  requiredDevelopmentDataDirectory,
  reserveDevelopmentPort,
  sidecarPath,
  startVite,
};
