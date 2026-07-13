'use strict';

// Prepares a caller-owned SQLite copy for the Go secret migration. It does not
// discover a database, inspect Electron directories, or print filesystem paths.
const crypto = require('crypto');
const fs = require('fs');
const path = require('path');

const DATABASE_FILE = 'p08-secrets.sqlite.copy';
const AUTHORIZATION_FILE = 'p08-secret-copy.authorization.json';
const BACKUP_DIRECTORY = 'immutable-backups';
const SECRET_DIRECTORY = 'secret-store';
const KEY_DIRECTORY = 'secret-key';

function parseArgs(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === '--sanitized-copy') {
      values.sanitizedCopy = true;
      continue;
    }
    if (!argument.startsWith('--') || index + 1 >= argv.length) {
      return null;
    }
    const key = argument.slice(2);
    if (!['source', 'allow-root', 'output-root'].includes(key) || values[key] !== undefined) {
      return null;
    }
    values[key] = argv[index + 1];
    index += 1;
  }
  if (!values.source || !values['allow-root'] || !values['output-root'] || !values.sanitizedCopy) {
    return null;
  }
  return values;
}

function fail(code) {
  process.stdout.write(`${JSON.stringify({ schema_version: 1, status: 'blocked', code })}\n`);
  process.exitCode = 2;
}

function isWithin(value, root) {
  const relative = path.relative(root, value);
  return relative === '' || (!relative.startsWith(`..${path.sep}`) && relative !== '..' && !path.isAbsolute(relative));
}

function samePath(left, right) {
  const normalizedLeft = path.normalize(left);
  const normalizedRight = path.normalize(right);
  return process.platform === 'win32'
    ? normalizedLeft.toLowerCase() === normalizedRight.toLowerCase()
    : normalizedLeft === normalizedRight;
}

function existingDirectory(value) {
  const absolute = path.resolve(value);
  const stat = fs.lstatSync(absolute);
  if (!stat.isDirectory() || stat.isSymbolicLink()) {
    throw new Error('unsafe');
  }
  return fs.realpathSync.native(absolute);
}

function existingRegularFile(value) {
  const absolute = path.resolve(value);
  const stat = fs.lstatSync(absolute);
  if (!stat.isFile() || stat.isSymbolicLink()) {
    throw new Error('unsafe');
  }
  const resolved = fs.realpathSync.native(absolute);
  if (!samePath(resolved, absolute)) {
    throw new Error('unsafe');
  }
  return resolved;
}

function ensureNewDirectory(value, root) {
  const absolute = path.resolve(value);
  if (!isWithin(absolute, root) || absolute === root || fs.existsSync(absolute)) {
    throw new Error('unsafe');
  }
  const parent = path.dirname(absolute);
  const parentResolved = existingDirectory(parent);
  if (!isWithin(parentResolved, root)) {
    throw new Error('unsafe');
  }
  fs.mkdirSync(absolute, { mode: 0o700 });
  const resolved = existingDirectory(absolute);
  if (!samePath(resolved, absolute) || !isWithin(resolved, root)) {
    throw new Error('unsafe');
  }
  return resolved;
}

function hasActiveSidecar(source) {
  return ['-wal', '-shm', '-journal', '.autoplan-owner.lock', '.p04-migrate.lock']
    .some((suffix) => fs.existsSync(`${source}${suffix}`));
}

function hashFile(file) {
  const digest = crypto.createHash('sha256');
  const input = fs.openSync(file, 'r');
  try {
    const buffer = Buffer.allocUnsafe(128 * 1024);
    for (;;) {
      const count = fs.readSync(input, buffer, 0, buffer.length, null);
      if (count === 0) {
        break;
      }
      digest.update(buffer.subarray(0, count));
    }
  } finally {
    fs.closeSync(input);
  }
  return digest.digest('hex');
}

function run(argv) {
  const options = parseArgs(argv);
  if (!options) {
    fail('invalid_arguments');
    return;
  }
  try {
    const allowedRoot = existingDirectory(options['allow-root']);
    const source = existingRegularFile(options.source);
    const sourceName = path.basename(source);
    if (!isWithin(source, allowedRoot) || sourceName === 'autoplan.sqlite' || !sourceName.endsWith('.sqlite.copy') || hasActiveSidecar(source)) {
      throw new Error('unsafe');
    }
    const outputRoot = ensureNewDirectory(options['output-root'], allowedRoot);
    const database = path.join(outputRoot, DATABASE_FILE);
    fs.copyFileSync(source, database, fs.constants.COPYFILE_EXCL);
    fs.chmodSync(database, 0o600);
    const databaseSHA256 = hashFile(database);
    fs.mkdirSync(path.join(outputRoot, BACKUP_DIRECTORY), { mode: 0o700 });
    fs.mkdirSync(path.join(outputRoot, SECRET_DIRECTORY), { mode: 0o700 });
    fs.mkdirSync(path.join(outputRoot, KEY_DIRECTORY), { mode: 0o700 });
    const authorization = {
      schema_version: 1,
      purpose: 'p08-secret-copy',
      sanitized_copy: true,
      database_file: DATABASE_FILE,
      database_sha256: databaseSHA256,
      backup_directory: BACKUP_DIRECTORY,
      secret_storage_directory: SECRET_DIRECTORY,
      key_directory: KEY_DIRECTORY,
      prepared_at: new Date().toISOString(),
      node_sqljs_closed: true,
      production_database_opened: false,
    };
    const authorizationPath = path.join(outputRoot, AUTHORIZATION_FILE);
    fs.writeFileSync(authorizationPath, `${JSON.stringify(authorization, null, 2)}\n`, { encoding: 'utf8', mode: 0o600, flag: 'wx' });
    process.stdout.write(`${JSON.stringify({
      schema_version: 1,
      status: 'ok',
      code: 'prepared',
      database: 'authorized_copy',
      authorization: 'authorization_manifest',
      source_sha256: databaseSHA256,
    })}\n`);
  } catch (_) {
    fail('unsafe_copy');
  }
}

if (require.main === module) {
  run(process.argv.slice(2));
}

module.exports = { DATABASE_FILE, AUTHORIZATION_FILE, run };
