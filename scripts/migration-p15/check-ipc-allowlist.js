'use strict';

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(__dirname, '..', '..');
const ALLOWLIST_PATH = 'docs/migration/p15/ipc-allowlist.json';
const LEGACY_MANIFEST_PATH = 'docs/migration/p15/legacy-removal-manifest.json';
const IPC_SOURCE_FILES = Object.freeze(['src/main.js', 'src/preload.js', 'src/terminal/terminalIpc.js', 'src/terminal/terminalTypes.js']);
const ALLOWED_CATEGORIES = new Set(['native_bridge', 'updates', 'go_lifecycle']);
const ALLOWED_DIRECTIONS = new Set(['renderer_to_main', 'renderer_to_main_sync', 'main_to_renderer']);
const BUSINESS_CHANNEL = /^(?:snapshot|plans:|projects:(?:create|update|delete)|loop:|tasks:|acceptance:|intake:|requirements:|feedback:|mcp:|chat:|ai-config:|claude-cli-config:|conversation:|file-access:|scripts:(?!pickFile$)|executors:(?!pickTasksJson$)|terminal:)/;

function readJSON(rootDir, relative) {
  try { return JSON.parse(fs.readFileSync(path.join(rootDir, relative), 'utf8')); }
  catch { return null; }
}

function channelName(value) {
  return typeof value === 'string' && (value === 'snapshot' || /^[a-z][a-z0-9-]{0,63}:[a-z][A-Za-z0-9-]{0,63}$/.test(value)) ? value : null;
}

function collectLiteralChannels(text) {
  const channels = new Set();
  const pattern = /(?:ipcMain\.(?:handle|on)|ipcRenderer\.(?:invoke|sendSync|on)|legacyChat(?:Invoke|Subscribe))\(\s*['"]([^'"]+)['"]/g;
  for (const match of text.matchAll(pattern)) {
    const channel = channelName(match[1]);
    if (channel) channels.add(channel);
  }
  return channels;
}

function collectTerminalChannels(text) {
  const result = new Set();
  const object = text.match(/TERMINAL_CHANNELS\s*=\s*Object\.freeze\(\{([\s\S]*?)\}\);/);
  if (!object) return result;
  for (const match of object[1].matchAll(/:\s*['"]([^'"]+)['"]/g)) {
    const channel = channelName(match[1]);
    if (channel) result.add(channel);
  }
  return result;
}

function discoverIpcChannels(rootDir = ROOT) {
  const channels = new Set();
  const missing = [];
  for (const relative of IPC_SOURCE_FILES) {
    const target = path.join(rootDir, relative);
    const info = fs.lstatSync(target, { throwIfNoEntry: false });
    if (!info?.isFile() || info.isSymbolicLink()) { missing.push(relative); continue; }
    const text = fs.readFileSync(target, 'utf8');
    for (const channel of collectLiteralChannels(text)) channels.add(channel);
    if (relative.endsWith('terminalTypes.js')) for (const channel of collectTerminalChannels(text)) channels.add(channel);
  }
  return { channels: [...channels].sort(), missing };
}

function validateAllowlist(value) {
  if (!value || value.schema_version !== 1 || value.kind !== 'p15-ipc-allowlist' ||
      value.default_action !== 'deny' || !Array.isArray(value.channels)) {
    return { ok: false, code: 'ipc_allowlist_shape_invalid' };
  }
  const names = new Set();
  const entryKeys = new Set();
  for (const entry of value.channels) {
    const entryKey = `${entry?.channel || ''}:${entry?.direction || ''}`;
    if (!entry || typeof entry !== 'object' || !channelName(entry.channel) || entryKeys.has(entryKey) ||
        !ALLOWED_CATEGORIES.has(entry.category) || !ALLOWED_DIRECTIONS.has(entry.direction) ||
        !Array.isArray(entry.callers) || entry.callers.length === 0 || typeof entry.authorization !== 'object' ||
        entry.authorization?.source_validation !== 'required' || entry.authorization?.schema_validation !== 'required' ||
        !Number.isInteger(entry.max_input_bytes) || entry.max_input_bytes < 0 || entry.max_input_bytes > 1024 * 1024 ||
        !Number.isInteger(entry.max_output_bytes) || entry.max_output_bytes < 0 || entry.max_output_bytes > 1024 * 1024 ||
        entry.secret_policy !== 'deny') {
      return { ok: false, code: 'ipc_allowlist_entry_invalid' };
    }
    if (BUSINESS_CHANNEL.test(entry.channel)) return { ok: false, code: 'ipc_business_channel_allowlisted', channel: entry.channel };
    names.add(entry.channel);
    entryKeys.add(entryKey);
  }
  return { ok: true, code: 'ipc_allowlist_valid', channels: names };
}

function legacyChannels(manifest) {
  if (!manifest || manifest.schema_version !== 1 || manifest.kind !== 'p15-legacy-removal-manifest' || !Array.isArray(manifest.legacy_ipc)) {
    return { ok: false, code: 'legacy_manifest_shape_invalid', channels: new Set() };
  }
  const channels = new Set();
  for (const domain of manifest.legacy_ipc) {
    if (!domain || typeof domain.domain !== 'string' || !Array.isArray(domain.channels) || !Array.isArray(domain.replacement)) {
      return { ok: false, code: 'legacy_manifest_entry_invalid', channels };
    }
    for (const value of domain.channels) {
      const channel = channelName(value);
      if (!channel || channels.has(channel)) return { ok: false, code: 'legacy_manifest_channel_invalid', channels };
      channels.add(channel);
    }
  }
  return { ok: true, code: 'legacy_manifest_valid', channels };
}

function checkIpcAllowlist(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  const allowlist = readJSON(rootDir, options.allowlistPath || ALLOWLIST_PATH);
  const manifest = readJSON(rootDir, options.legacyManifestPath || LEGACY_MANIFEST_PATH);
  const allowed = validateAllowlist(allowlist);
  const legacy = legacyChannels(manifest);
  const discovered = discoverIpcChannels(rootDir);
  if (!allowed.ok || !legacy.ok || discovered.missing.length) {
    return {
      schema_version: 1, status: 'blocked', ok: false,
      code: !allowed.ok ? allowed.code : !legacy.ok ? legacy.code : 'ipc_source_missing',
      missing_sources: discovered.missing,
    };
  }
  const unknown = discovered.channels.filter((channel) => !allowed.channels.has(channel) && !legacy.channels.has(channel));
  const conflicted = discovered.channels.filter((channel) => allowed.channels.has(channel) && legacy.channels.has(channel));
  const observedLegacy = discovered.channels.filter((channel) => legacy.channels.has(channel));
  const forbiddenAllowed = [...allowed.channels].filter((channel) => BUSINESS_CHANNEL.test(channel));
  const thinShellReady = unknown.length === 0 && conflicted.length === 0 && observedLegacy.length === 0 && forbiddenAllowed.length === 0;
  const contractValid = unknown.length === 0 && conflicted.length === 0 && forbiddenAllowed.length === 0;
  const requireThinShell = options.requireThinShell === true;
  const ok = contractValid && (!requireThinShell || thinShellReady);
  return {
    schema_version: 1,
    kind: 'p15-ipc-allowlist-check',
    status: ok ? (thinShellReady ? 'ready' : 'baseline_frozen') : 'blocked',
    ok,
    code: !contractValid ? (unknown.length ? 'ipc_unknown_channel' : conflicted.length ? 'ipc_channel_conflicted' : 'ipc_business_channel_allowlisted') :
      requireThinShell && !thinShellReady ? 'legacy_ipc_removal_pending' : thinShellReady ? 'ipc_allowlist_ready' : 'ipc_legacy_inventory_frozen',
    thin_shell_ready: thinShellReady,
    discovered_channels: discovered.channels,
    legacy_channels_observed: observedLegacy,
    unknown_channels: unknown,
    conflicted_channels: conflicted,
  };
}

function parseArgs(argv) {
  if (argv.length === 0) return { requireThinShell: false };
  if (argv.length === 1 && argv[0] === '--require-thin-shell') return { requireThinShell: true };
  throw new Error('arguments_invalid');
}

if (require.main === module) {
  try {
    const result = checkIpcAllowlist(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify({ status: result.status, ok: result.ok, code: result.code, thin_shell_ready: result.thin_shell_ready, unknown_channels: result.unknown_channels?.length || 0, legacy_channels: result.legacy_channels_observed?.length || 0 })}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch {
    process.stdout.write('{"status":"blocked","code":"p15_ipc_allowlist_arguments_invalid"}\n');
    process.exitCode = 2;
  }
}

module.exports = { ALLOWLIST_PATH, BUSINESS_CHANNEL, IPC_SOURCE_FILES, LEGACY_MANIFEST_PATH, ROOT, checkIpcAllowlist, channelName, collectLiteralChannels, collectTerminalChannels, discoverIpcChannels, legacyChannels, parseArgs, validateAllowlist };
