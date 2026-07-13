'use strict';

// Compares independently produced static artifacts. This gate is deliberately
// fail-closed: it writes neither golden and ignores only reset-copy identity
// metadata, which must differ between the two independently produced files.
const fs = require('fs');
const path = require('path');
const { containsSensitive } = require('./scan-sensitive-output');

function parseArgs(argv) {
  const result = {};
  for (let index = 0; index < argv.length; index += 1) {
    const flag = argv[index];
    if (!['--allow-root', '--node', '--go'].includes(flag) || result[flag] !== undefined || index + 1 >= argv.length) {
      return null;
    }
    result[flag] = argv[index + 1];
    index += 1;
  }
  return result['--allow-root'] && result['--node'] && result['--go'] ? result : null;
}

function within(root, candidate) {
  const relative = path.relative(root, candidate);
  return relative !== '' && relative !== '..' && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative);
}

function samePath(left, right) {
  const normalizedLeft = path.normalize(left);
  const normalizedRight = path.normalize(right);
  return process.platform === 'win32'
    ? normalizedLeft.toLowerCase() === normalizedRight.toLowerCase()
    : normalizedLeft === normalizedRight;
}

function readArtifact(root, candidate) {
  const absoluteRoot = path.resolve(root);
  const absoluteCandidate = path.resolve(candidate);
  const rootStat = fs.lstatSync(absoluteRoot);
  if (!rootStat.isDirectory() || rootStat.isSymbolicLink() || !samePath(fs.realpathSync.native(absoluteRoot), absoluteRoot)) {
    throw new Error('unsafe_input');
  }
  if (!within(absoluteRoot, absoluteCandidate)) throw new Error('unsafe_input');
  const stat = fs.lstatSync(absoluteCandidate);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size <= 0 || stat.size > 4 * 1024 * 1024 ||
      !samePath(fs.realpathSync.native(absoluteCandidate), absoluteCandidate)) {
    throw new Error('unsafe_input');
  }
  const content = fs.readFileSync(absoluteCandidate, 'utf8');
  if (containsSensitive(content)) throw new Error('sensitive_output');
  let parsed;
  try {
    parsed = JSON.parse(content);
  } catch (_) {
    throw new Error('invalid_json');
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('invalid_shape');
  return parsed;
}

function artifactCopyID(value) {
  const source = value && typeof value === 'object' ? value.source : null;
  if (!source || typeof source !== 'object') return '';
  return typeof source.databaseCopy === 'string' ? source.databaseCopy : '';
}

function compareStaticGolden(nodeArtifact, goArtifact) {
  if (!nodeArtifact || !goArtifact || typeof nodeArtifact !== 'object' || typeof goArtifact !== 'object' ||
      Array.isArray(nodeArtifact) || Array.isArray(goArtifact)) {
    throw new Error('invalid_shape');
  }
  const nodeCopy = artifactCopyID(nodeArtifact);
  const goCopy = artifactCopyID(goArtifact);
  if (nodeCopy && goCopy && nodeCopy === goCopy) throw new Error('shared_database_copy');
  const comparable = (artifact) => {
    const copy = JSON.parse(JSON.stringify(artifact));
    if (copy.source && typeof copy.source === 'object') copy.source.databaseCopy = '<independent-reset-copy>';
    return canonicalize(copy);
  };
  if (JSON.stringify(comparable(nodeArtifact)) !== JSON.stringify(comparable(goArtifact))) throw new Error('golden_mismatch');
}

function canonicalize(value) {
  if (Array.isArray(value)) return value.map(canonicalize);
  if (!value || typeof value !== 'object') return value;
  return Object.keys(value).sort().reduce((result, key) => {
    result[key] = canonicalize(value[key]);
    return result;
  }, {});
}

function emit(status, code) {
  process.stdout.write(`${JSON.stringify({ schema_version: 1, status, code })}\n`);
}

function run(argv) {
  const options = parseArgs(argv);
  if (!options) {
    emit('blocked', 'invalid_arguments');
    process.exitCode = 2;
    return;
  }
  try {
    const nodeArtifact = readArtifact(options['--allow-root'], options['--node']);
    const goArtifact = readArtifact(options['--allow-root'], options['--go']);
    compareStaticGolden(nodeArtifact, goArtifact);
    emit('ok', 'match');
  } catch (error) {
    const code = error instanceof Error && /^[a-z_]+$/.test(error.message) ? error.message : 'comparison_failed';
    emit('blocked', code);
    process.exitCode = 3;
  }
}

if (require.main === module) run(process.argv.slice(2));

module.exports = { compareStaticGolden, parseArgs, readArtifact, run };
