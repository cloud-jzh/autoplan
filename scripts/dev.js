const http = require('node:http');
const path = require('node:path');
const { spawn } = require('node:child_process');

const root = path.join(__dirname, '..');
const dependencyRoot = dependencyRootFor(root);
const isWindows = process.platform === 'win32';
const rendererUrl = process.env.ELECTRON_RENDERER_URL || 'http://127.0.0.1:5173';
const children = new Set();
let shuttingDown = false;
let electronProcess = null;

function dependencyRootFor(projectRoot) {
  if (process.env.AUTOPLAN_NODE_MODULES) {
    return process.env.AUTOPLAN_NODE_MODULES;
  }
  return path.join(projectRoot, 'node_modules');
}

function bin(name) {
  return path.join(dependencyRoot, '.bin', `${name}${isWindows ? '.cmd' : ''}`);
}

function spawnChild(command, args, options = {}) {
  const child = spawn(isWindows ? 'cmd.exe' : command, isWindows ? ['/d', '/c', 'call', command, ...args] : args, {
    cwd: root,
    stdio: 'inherit',
    env: { ...process.env, NODE_PATH: dependencyRoot, ...options.env },
  });

  children.add(child);
  child.on('exit', (code) => {
    children.delete(child);
    if (!shuttingDown && child === electronProcess) {
      shutdown(code ?? 0);
    }
  });
  return child;
}

async function main() {
  if (!(await isReachable(rendererUrl))) {
    const renderer = rendererServerArgs(rendererUrl);
    spawnChild(bin('vite'), ['--host', renderer.host, '--port', String(renderer.port)]);
  }
  await waitFor(rendererUrl, 30000);
  electronProcess = spawnChild(bin('electron'), ['.'], {
    env: { ELECTRON_RENDERER_URL: rendererUrl },
  });
}

function rendererServerArgs(url) {
  try {
    const parsed = new URL(url);
    return {
      host: parsed.hostname || '127.0.0.1',
      port: Number(parsed.port || (parsed.protocol === 'https:' ? 443 : 80)),
    };
  } catch {
    return { host: '127.0.0.1', port: 5173 };
  }
}

function isReachable(url) {
  return new Promise((resolve) => {
    const request = http.get(url, (response) => {
      response.resume();
      resolve(true);
    });

    request.setTimeout(1000, () => {
      request.destroy();
      resolve(false);
    });
    request.on('error', () => resolve(false));
  });
}

function waitFor(url, timeoutMs) {
  const startedAt = Date.now();

  return new Promise((resolve, reject) => {
    const check = () => {
      const request = http.get(url, (response) => {
        response.resume();
        resolve();
      });

      request.on('error', () => {
        if (Date.now() - startedAt > timeoutMs) {
          reject(new Error(`Timed out waiting for ${url}`));
          return;
        }
        setTimeout(check, 250);
      });
    };

    check();
  });
}

function shutdown(code) {
  if (shuttingDown) return;
  shuttingDown = true;
  for (const child of children) {
    child.kill();
  }
  setTimeout(() => process.exit(code), 150).unref();
}

process.on('SIGINT', () => shutdown(130));
process.on('SIGTERM', () => shutdown(143));

main().catch((error) => {
  console.error(error);
  shutdown(1);
});
