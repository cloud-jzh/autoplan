'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const {
  FIXTURE_NAMES,
  acquireLock,
  assertSanitizedArtifacts,
  extractEnsureColumns,
  extractInitialDdl,
  generateFixtures,
  looksLikeUserData,
  parseArgs,
  releaseLock,
  resolveOutput,
} = require('./fixtures');

const ROOT = path.resolve(__dirname, '../..');

function makeTempRoot() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p00-fixtures-test-'));
}

function removeTempRoot(root) {
  if (root && path.dirname(root).startsWith(path.resolve(os.tmpdir()))) {
    fs.rmSync(root, { recursive: true, force: true });
  }
}

test('recipe manifest defines exactly five synthetic fixture categories', () => {
  const manifest = JSON.parse(fs.readFileSync(path.join(ROOT, 'fixtures/migration/p00/manifest.json'), 'utf8'));
  assert.deepEqual(manifest.fixtures.map((item) => item.name), FIXTURE_NAMES);
  assert.equal(manifest.outputPolicy.overwrite, 'never');
  assert.equal(manifest.fixtures.every((item) => item.expectedAudit.length > 0), true);
  assert.equal(manifest.pathCases.some((item) => item.id === 'missing-windows'), true);
  assert.equal(manifest.pathCases.some((item) => item.id === 'symlink-policy'), true);
});

test('database source extraction captures initial schema and compatibility columns', () => {
  const source = fs.readFileSync(path.join(ROOT, 'src/database.js'), 'utf8');
  const ddl = extractInitialDdl(source);
  const ensure = extractEnsureColumns(source);
  assert(ddl.includes('CREATE TABLE IF NOT EXISTS projects'));
  assert(ddl.includes('CREATE TABLE IF NOT EXISTS loop_state'));
  assert(ensure.some((item) => item.table === 'chat_messages' && item.column === 'conversation_id'));
  assert(ensure.some((item) => item.table === 'plans' && item.column === 'plan_execution_claude_config_id'));
});

test('CLI parsing has no force/overwrite mode', () => {
  assert.deepEqual(parseArgs(['--seed', '7', '--large-count', '9', '--output', 'target', '--allow-root', 'root']), {
    seed: '7',
    largeCount: '9',
    outputDir: 'target',
    allowRoot: 'root',
  });
  assert.throws(() => parseArgs(['--force']), /未知参数/);
  assert.throws(() => parseArgs(['--output']), /参数缺少值/);
});

test('output safety rejects existing targets, unauthorized roots, and userData-shaped paths', () => {
  const tempRoot = makeTempRoot();
  try {
    const target = path.join(tempRoot, 'new-target');
    const resolved = resolveOutput({ allowRoot: tempRoot, outputDir: target, seed: 1, largeCount: 1 });
    assert.equal(resolved.target, target);
    fs.mkdirSync(target);
    assert.throws(() => resolveOutput({ allowRoot: tempRoot, outputDir: target }), /拒绝覆盖/);
    const outside = path.join(path.parse(tempRoot).root, 'autoplan-not-authorized-output');
    assert.throws(() => resolveOutput({ allowRoot: tempRoot, outputDir: outside }), /系统临时目录|授权根/);
    const fakeUserData = path.join(tempRoot, 'AppData', 'Roaming', 'autoplan', 'data');
    assert.equal(looksLikeUserData(fakeUserData), true);
    assert.throws(() => resolveOutput({ allowRoot: tempRoot, outputDir: fakeUserData }), /userData/);
  } finally {
    removeTempRoot(tempRoot);
  }
});

test('per-target lock refuses a concurrent generator', () => {
  const tempRoot = makeTempRoot();
  let lock;
  try {
    const target = path.join(tempRoot, 'locked-target');
    lock = acquireLock(tempRoot, target);
    assert.throws(() => acquireLock(tempRoot, target), /EEXIST/);
  } finally {
    releaseLock(lock);
    removeTempRoot(tempRoot);
  }
});

test('generator creates five openable deterministic databases with expected row counts', async () => {
  const tempRoot = makeTempRoot();
  try {
    const first = await generateFixtures({ rootDir: ROOT, allowRoot: tempRoot, outputDir: path.join(tempRoot, 'run-a'), seed: 77, largeCount: 12 });
    const second = await generateFixtures({ rootDir: ROOT, allowRoot: tempRoot, outputDir: path.join(tempRoot, 'run-b'), seed: 77, largeCount: 12 });
    assert.deepEqual(first.manifest.artifacts.map((item) => item.name), FIXTURE_NAMES);
    assert.deepEqual(
      first.manifest.artifacts.map((item) => item.sha256),
      second.manifest.artifacts.map((item) => item.sha256),
    );
    const byName = Object.fromEntries(first.manifest.artifacts.map((item) => [item.name, item]));
    assert.equal(byName.empty.rowCounts.projects, 0);
    assert.equal(byName['legacy-normal'].rowCounts.requirements, 1);
    assert.equal(byName['orphan-cross-project'].rowCounts.projects, 2);
    assert.equal(byName['invalid-paths'].rowCounts.scripts, 1);
    assert.equal(byName.large.rowCounts.requirements, 12);
    assert.equal(byName.large.rowCounts.events, 12);
    for (const name of FIXTURE_NAMES) assert.equal(fs.existsSync(path.join(first.outputDir, `${name}.sqlite`)), true);
    assert.equal(fs.existsSync(path.join(first.outputDir, 'generated-manifest.json')), true);
    assert.equal(fs.existsSync(path.join(first.outputDir, 'invalid-path-cases.json')), true);
  } finally {
    removeTempRoot(tempRoot);
  }
});

test('different seeds change large fixture content without introducing credentials', async () => {
  const tempRoot = makeTempRoot();
  try {
    const first = await generateFixtures({ rootDir: ROOT, allowRoot: tempRoot, outputDir: path.join(tempRoot, 'seed-a'), seed: 1, largeCount: 3 });
    const second = await generateFixtures({ rootDir: ROOT, allowRoot: tempRoot, outputDir: path.join(tempRoot, 'seed-b'), seed: 2, largeCount: 3 });
    const firstLarge = first.manifest.artifacts.find((item) => item.name === 'large');
    const secondLarge = second.manifest.artifacts.find((item) => item.name === 'large');
    assert.notEqual(firstLarge.sha256, secondLarge.sha256);
    assertSanitizedArtifacts(first.outputDir);
    assertSanitizedArtifacts(second.outputDir);
  } finally {
    removeTempRoot(tempRoot);
  }
});

test('sanitization rejects credential-shaped content', () => {
  const tempRoot = makeTempRoot();
  try {
    fs.writeFileSync(path.join(tempRoot, 'bad.txt'), 'sk-syntheticCredentialShape123456789');
    assert.throws(() => assertSanitizedArtifacts(tempRoot), /凭据形态/);
  } finally {
    removeTempRoot(tempRoot);
  }
});
