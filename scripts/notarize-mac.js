const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { notarize } = require('@electron/notarize');

const temporaryDirectories = new Set();

function env(...names) {
  for (const name of names) {
    const value = process.env[name];
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return null;
}

function isEnabled(...names) {
  const value = env(...names);
  return value === '1' || value === 'true' || value === 'yes';
}

function releaseMode() {
  const value = env('MACOS_RELEASE_MODE');
  if (!value) return isEnabled('MAC_NOTARIZE_REQUIRED', 'APPLE_NOTARIZE_REQUIRED', 'NOTARIZE_REQUIRED')
    ? 'signed-notarized'
    : 'unsigned-test';
  if (value === 'signed-notarized' || value === 'unsigned-test') return value;
  throw new Error('[notarize] invalid MACOS_RELEASE_MODE.');
}

function buildAppPath(context) {
  const appName = context.packager.appInfo.productFilename;
  return path.join(context.appOutDir, `${appName}.app`);
}

function writeApiKey(contents, isBase64) {
  const directory = fs.mkdtempSync(path.join(env('RUNNER_TEMP') || os.tmpdir(), 'autoplan-notarize-'));
  temporaryDirectories.add(directory);
  const keyPath = path.join(directory, 'AuthKey.p8');
  const buffer = isBase64 ? Buffer.from(contents, 'base64') : Buffer.from(contents);
  if (!buffer.length) throw new Error('[notarize] App Store Connect API key is empty.');
  fs.writeFileSync(keyPath, buffer, { mode: 0o600 });
  return keyPath;
}

function resolveApiKeyPath() {
  const keyPath = env('APPLE_API_KEY_PATH', 'APP_STORE_CONNECT_API_KEY_PATH', 'ASC_API_KEY_PATH');
  if (keyPath) return keyPath;

  const keyContents = env('APPLE_API_KEY', 'APP_STORE_CONNECT_API_KEY', 'ASC_API_KEY');
  if (keyContents) {
    if (fs.existsSync(keyContents)) return keyContents;
    return writeApiKey(keyContents, false);
  }

  const base64Contents = env('APPLE_API_KEY_BASE64', 'APP_STORE_CONNECT_API_KEY_BASE64', 'ASC_API_KEY_BASE64');
  if (base64Contents) return writeApiKey(base64Contents, true);

  return null;
}

function resolveCredentials() {
  const keychainProfile = env('APPLE_KEYCHAIN_PROFILE', 'NOTARYTOOL_KEYCHAIN_PROFILE');
  if (keychainProfile) {
    const credentials = { keychainProfile };
    const keychain = env('APPLE_KEYCHAIN', 'NOTARYTOOL_KEYCHAIN');
    if (keychain) credentials.keychain = keychain;
    return { credentials, strategy: 'keychain-profile' };
  }

  const appleApiKey = resolveApiKeyPath();
  const appleApiKeyId = env('APPLE_API_KEY_ID', 'APP_STORE_CONNECT_API_KEY_ID', 'ASC_API_KEY_ID');
  const appleApiIssuer = env('APPLE_API_ISSUER', 'APP_STORE_CONNECT_API_ISSUER', 'ASC_API_ISSUER');
  if (appleApiKey || appleApiKeyId || appleApiIssuer) {
    const missing = [];
    if (!appleApiKey) missing.push('APPLE_API_KEY_PATH|APPLE_API_KEY');
    if (!appleApiKeyId) missing.push('APPLE_API_KEY_ID');
    if (!appleApiIssuer) missing.push('APPLE_API_ISSUER');
    if (missing.length) return { missing, strategy: 'app-store-connect-api-key' };
    return {
      credentials: { appleApiKey, appleApiKeyId, appleApiIssuer },
      strategy: 'app-store-connect-api-key',
    };
  }

  const appleId = env('APPLE_ID', 'APPLEID');
  const appleIdPassword = env('APPLE_APP_SPECIFIC_PASSWORD', 'APPLE_ID_PASSWORD', 'APPLE_PASSWORD');
  const teamId = env('APPLE_TEAM_ID', 'APPLE_TEAMID');
  if (appleId || appleIdPassword || teamId) {
    const missing = [];
    if (!appleId) missing.push('APPLE_ID');
    if (!appleIdPassword) missing.push('APPLE_APP_SPECIFIC_PASSWORD');
    if (!teamId) missing.push('APPLE_TEAM_ID');
    if (missing.length) return { missing, strategy: 'apple-id-password' };
    return {
      credentials: { appleId, appleIdPassword, teamId },
      strategy: 'apple-id-password',
    };
  }

  return {
    missing: ['APPLE_KEYCHAIN_PROFILE|APPLE_API_KEY_PATH|APPLE_ID'],
    strategy: 'notarization-credentials',
  };
}

function staple(appPath) {
  for (const command of [['stapler', 'staple', appPath], ['stapler', 'validate', appPath]]) {
    const result = spawnSync('xcrun', command, { encoding: 'utf8', windowsHide: true });
    if (result.error || result.status !== 0) {
      throw new Error(`[notarize] xcrun ${command[0]} ${command[1]} failed.`);
    }
  }
}

function cleanupTemporaryKeys() {
  for (const directory of temporaryDirectories) {
    fs.rmSync(directory, { recursive: true, force: true });
    temporaryDirectories.delete(directory);
  }
}

async function notarizeMac(context) {
  const packagePlatform = context.electronPlatformName || process.platform;
  if (process.platform !== 'darwin' || packagePlatform !== 'darwin') {
    console.log('[notarize] status=not-applicable reason=non-darwin-package');
    return;
  }

  const mode = releaseMode();
  if (mode === 'unsigned-test') {
    console.log('[notarize] status=blocked reason=unsigned-test-mode');
    return;
  }
  if (isEnabled('SKIP_NOTARIZE', 'MAC_NOTARIZE_SKIP')) {
    throw new Error('[notarize] signed-notarized mode cannot skip notarization.');
  }

  const appPath = buildAppPath(context);
  if (!fs.existsSync(appPath)) throw new Error('[notarize] app bundle is missing.');

  try {
    const resolved = resolveCredentials();
    if (!resolved.credentials) {
      throw new Error(`[notarize] signed-notarized mode missing ${resolved.strategy}: ${resolved.missing.join(', ')}.`);
    }

    const options = { appPath, ...resolved.credentials };
    const notarytoolPath = env('APPLE_NOTARYTOOL_PATH', 'NOTARYTOOL_PATH');
    if (notarytoolPath) options.notarytoolPath = notarytoolPath;

    console.log(`[notarize] status=submitting credential_strategy=${resolved.strategy}`);
    await notarize(options);
    staple(appPath);
    console.log('[notarize] status=complete staple=validated');
  } finally {
    cleanupTemporaryKeys();
  }
}

module.exports = notarizeMac;
module.exports.default = notarizeMac;
