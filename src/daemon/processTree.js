'use strict';

const { spawn } = require('node:child_process');
const { closeSessionPipe } = require('./session');

function waitForExit(child, timeoutMs) {
  if (!child || child.exitCode !== null || child.signalCode) return Promise.resolve();
  return new Promise((resolve) => {
    let timer = null;
    const done = () => {
      if (timer) clearTimeout(timer);
      child.removeListener('exit', done);
      child.removeListener('error', done);
      resolve();
    };
    child.once('exit', done);
    child.once('error', done);
    timer = setTimeout(done, timeoutMs);
    timer.unref?.();
  });
}

async function terminateProcessTree(child, options = {}) {
  if (!child?.pid || child.exitCode !== null || child.signalCode) return;
  const timeoutMs = boundedTimeout(options.timeoutMs, 5000);
  if (process.platform === 'win32') {
    await runTaskkill(child.pid, timeoutMs);
  } else {
    try {
      process.kill(-child.pid, 'SIGKILL');
    } catch {
      try { child.kill('SIGKILL'); } catch { /* process already exited */ }
    }
  }
  await waitForExit(child, timeoutMs);
}

async function stopProcessTree(child, options = {}) {
  if (!child?.pid || child.exitCode !== null || child.signalCode) return;
  const gracefulTimeoutMs = boundedTimeout(options.gracefulTimeoutMs, 5000);
  // stdin remains open after the one-shot session handoff. Closing it is the
  // cooperative parent-death signal understood by the Go lifecycle and works
  // even when a platform does not propagate SIGTERM to a child tree.
  closeSessionPipe(child.stdin);
  signalProcessTree(child, 'SIGTERM');
  await waitForExit(child, gracefulTimeoutMs);
  if (child.exitCode === null && !child.signalCode) {
    await terminateProcessTree(child, { timeoutMs: options.forceTimeoutMs });
  }
}

function signalProcessTree(child, signal) {
  if (!child?.pid || child.exitCode !== null || child.signalCode) return false;
  if (process.platform !== 'win32') {
    try {
      process.kill(-child.pid, signal);
      return true;
    } catch {
      // A failed process-group signal can happen if startup failed before the
      // group formed. The direct child signal is still bounded by the force
      // tree cleanup path below.
    }
  }
  try { return child.kill(signal); } catch { return false; }
}

function runTaskkill(pid, timeoutMs) {
  return new Promise((resolve) => {
    let completed = false;
    const child = spawn('taskkill', ['/pid', String(pid), '/t', '/f'], {
      windowsHide: true,
      stdio: 'ignore',
    });
    const done = () => {
      if (completed) return;
      completed = true;
      resolve();
    };
    child.once('error', done);
    child.once('exit', done);
    const timer = setTimeout(done, timeoutMs);
    timer.unref?.();
  });
}

function boundedTimeout(value, fallback) {
  const timeout = value === undefined ? fallback : Number(value);
  return Number.isInteger(timeout) && timeout >= 100 && timeout <= 60000 ? timeout : fallback;
}

module.exports = { signalProcessTree, stopProcessTree, terminateProcessTree, waitForExit };
