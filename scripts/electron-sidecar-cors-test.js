'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const os = require('node:os');
const path = require('node:path');
const { app, BrowserWindow } = require('electron');
const { GoDaemonSupervisor } = require('../src/daemon/supervisor');

const root = path.resolve(__dirname, '..');

function listenPageServer() {
  const server = http.createServer((_request, response) => {
    response.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
    response.end('<!doctype html><title>AutoPlan CORS integration</title>');
  });
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      resolve({ server, origin: `http://127.0.0.1:${address.port}` });
    });
  });
}

function installAuthentication(webContents, client, rendererOrigin) {
  const request = webContents.session.webRequest;
  request.onBeforeSendHeaders({ urls: ['http://127.0.0.1/*'] }, (details, callback) => {
    if (details.webContentsId !== webContents.id || !sameSidecarAuthority(details.url, client.baseUrl)) {
      callback({ cancel: false, requestHeaders: details.requestHeaders });
      return;
    }
    const headers = { ...(details.requestHeaders || {}) };
    for (const name of Object.keys(headers)) {
      if (name.toLowerCase() === 'x-autoplan-session') delete headers[name];
    }
    headers['X-Autoplan-Session'] = client.sessionToken;
    callback({ cancel: false, requestHeaders: headers });
  });
  request.onHeadersReceived({ urls: ['http://127.0.0.1/*'] }, (details, callback) => {
    if (details.webContentsId !== webContents.id || !sameSidecarAuthority(details.url, client.baseUrl)) {
      callback({ cancel: false, responseHeaders: details.responseHeaders });
      return;
    }
    const headers = { ...(details.responseHeaders || {}) };
    headers['Access-Control-Allow-Origin'] = [rendererOrigin];
    headers['Access-Control-Allow-Credentials'] = ['true'];
    headers['Access-Control-Expose-Headers'] = ['X-Request-ID'];
    callback({ cancel: false, responseHeaders: headers });
  });
}

function sameSidecarAuthority(rawUrl, baseUrl) {
  try {
    const requestUrl = new URL(rawUrl);
    const sidecarUrl = new URL(baseUrl);
    return requestUrl.protocol === 'http:' && requestUrl.host === sidecarUrl.host && requestUrl.hostname === '127.0.0.1' &&
      (requestUrl.pathname === '/healthz' || requestUrl.pathname === '/readyz' || requestUrl.pathname.startsWith('/api/v1/'));
  } catch {
    return false;
  }
}

async function main() {
  const runtimeRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-electron-cors-'));
  app.setPath('userData', path.join(runtimeRoot, 'electron-user-data'));
  await app.whenReady();
  let server;
  let window;
  let supervisor;
  try {
    const page = await listenPageServer();
    server = page.server;
    const dataDir = path.join(runtimeRoot, 'sidecar-data');
    fs.mkdirSync(dataDir, { recursive: true });
    supervisor = new GoDaemonSupervisor({
      executablePath: path.join(root, 'artifacts', 'sidecar', 'win32', 'x64', 'autoplan-server.exe'),
      dataDir,
      rendererOrigin: page.origin,
    });
    await supervisor.start();
    const client = supervisor.clientOptions();
    window = new BrowserWindow({ show: false, webPreferences: { contextIsolation: true, nodeIntegration: false } });
    installAuthentication(window.webContents, client, page.origin);
    await window.loadURL(page.origin);
    const result = await window.webContents.executeJavaScript(`
      (async () => {
        const create = await fetch(${JSON.stringify(client.baseUrl + '/api/v1/projects')}, {
          method: 'POST', credentials: 'include', headers: { 'content-type': 'application/json', 'idempotency-key': 'electron-cors-create-1' },
          body: JSON.stringify({ name: 'Electron CORS', workspace_path: '', description: '' }),
        });
        const created = await create.json();
        const id = created?.data?.activeProjectId;
        const requirement = await fetch(${JSON.stringify(client.baseUrl)} + '/api/v1/projects/' + id + '/requirements', {
          method: 'POST', credentials: 'include', headers: { 'content-type': 'application/json', 'idempotency-key': 'electron-cors-requirement-1' },
          body: JSON.stringify({ title: 'Electron requirement', body: 'Verify the authenticated CORS mutation route.', status: 'draft' }),
        });
        const snapshot = await fetch(${JSON.stringify(client.baseUrl)} + '/api/v1/projects/' + id + '/snapshot', { credentials: 'include' });
        return {
          createStatus: create.status,
          createRequestId: create.headers.get('x-request-id'),
          requirementStatus: requirement.status,
          requirementRequestId: requirement.headers.get('x-request-id'),
          snapshotStatus: snapshot.status,
          snapshot: await snapshot.json(),
        };
      })()
    `, true);
    assert.equal(result.createStatus, 201);
    assert.match(result.createRequestId || '', /^req_/);
    assert.equal(result.requirementStatus, 201);
    assert.match(result.requirementRequestId || '', /^req_/);
    assert.equal(result.snapshotStatus, 200);
    assert.equal(result.snapshot.data.activeProjectId, 1);
    process.stdout.write('electron_sidecar_cors_ok\n');
  } finally {
    if (window && !window.isDestroyed()) window.destroy();
    await supervisor?.stop().catch(() => undefined);
    await new Promise((resolve) => server?.close(() => resolve()) || resolve());
    app.quit();
  }
}

main().catch((error) => {
  process.stderr.write(`${error?.stack || error}\n`);
  app.quit();
  process.exitCode = 1;
});
