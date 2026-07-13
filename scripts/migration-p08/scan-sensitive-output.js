'use strict';

// Scans only explicit evidence files below an explicit root. Findings are
// classified without echoing a path, value, identifier, or matching fragment.
const fs = require('fs');
const path = require('path');

const sensitiveKey = /(?:api[_-]?key|auth[_-]?token|access[_-]?token|secret|password|env[_-]?vars?)/i;
const usableToken = /(?:\b(?:sk|rk|pk)_[A-Za-z0-9_-]{16,}\b|\bBearer\s+[A-Za-z0-9._~+\/-]{16,}\b|-----BEGIN [A-Z ]+PRIVATE KEY-----)/;
const absolutePath = /(?:[A-Za-z]:[\\/]|\/(?:Users|home|var|tmp)\/)/;
const environmentAssignment = /(?:^|\n)\s*[A-Za-z_][A-Za-z0-9_]*\s*=\s*[^\s<][^\n]*/;

function parseArgs(argv) {
  const result = { files: [] };
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === '--allow-root' && result.root === undefined && index + 1 < argv.length) {
      result.root = argv[index + 1];
      index += 1;
      continue;
    }
    if (argument === '--file' && index + 1 < argv.length) {
      result.files.push(argv[index + 1]);
      index += 1;
      continue;
    }
    return null;
  }
  return result.root && result.files.length > 0 ? result : null;
}

function within(value, root) {
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

function regularFile(value, root) {
  const absolute = path.resolve(value);
  if (!within(absolute, root)) {
    throw new Error('unsafe');
  }
  const stat = fs.lstatSync(absolute);
  if (!stat.isFile() || stat.isSymbolicLink() || !samePath(fs.realpathSync.native(absolute), absolute)) {
    throw new Error('unsafe');
  }
  return absolute;
}

function allowedRedaction(value) {
  return value === '' || value === '<redacted>' || value === '<masked-secret>' || value === '<fixture-secret>';
}

function containsSensitiveJSON(value) {
  if (Array.isArray(value)) {
    return value.some(containsSensitiveJSON);
  }
  if (value && typeof value === 'object') {
    return Object.entries(value).some(([key, child]) => {
      if (sensitiveKey.test(key) && typeof child === 'string' && !allowedRedaction(child)) {
        return true;
      }
      return containsSensitiveJSON(child);
    });
  }
  return false;
}

function containsSensitive(content) {
  if (usableToken.test(content) || absolutePath.test(content) || environmentAssignment.test(content)) {
    return true;
  }
  try {
    return containsSensitiveJSON(JSON.parse(content));
  } catch (_) {
    return false;
  }
}

function output(status, code, files, findings) {
  process.stdout.write(`${JSON.stringify({ schema_version: 1, status, code, files, findings })}\n`);
}

function run(argv) {
  const options = parseArgs(argv);
  if (!options) {
    output('blocked', 'invalid_arguments', 0, 0);
    process.exitCode = 2;
    return;
  }
  try {
    const root = path.resolve(options.root);
    const rootStat = fs.lstatSync(root);
    if (!rootStat.isDirectory() || rootStat.isSymbolicLink() || !samePath(fs.realpathSync.native(root), root)) {
      throw new Error('unsafe');
    }
    let findings = 0;
    for (const candidate of options.files) {
      const file = regularFile(candidate, root);
      const stat = fs.statSync(file);
      if (stat.size > 4 * 1024 * 1024) {
        throw new Error('unsafe');
      }
      if (containsSensitive(fs.readFileSync(file, 'utf8'))) {
        findings += 1;
      }
    }
    output(findings === 0 ? 'ok' : 'blocked', findings === 0 ? 'clean' : 'sensitive_output', options.files.length, findings);
    process.exitCode = findings === 0 ? 0 : 3;
  } catch (_) {
    output('blocked', 'unsafe_input', 0, 0);
    process.exitCode = 2;
  }
}

if (require.main === module) {
  run(process.argv.slice(2));
}

module.exports = { containsSensitive, run };
