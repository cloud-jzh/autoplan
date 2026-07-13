'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { describe, it } = require('node:test');
const { compareStaticGolden, parseArgs, readArtifact } = require('./compare-static-golden');

function artifact(copy, extra = {}) {
  return {
    schemaVersion: 1,
    version: 'p08-static-contract-v1',
    source: { databaseCopy: copy },
    scripts: [],
    executors: [],
    conversations: [],
    messages: [],
    configs: { ai: [], claude: [], mcp: {} },
    ...extra,
  };
}

describe('P008 static golden comparison', () => {
  it('requires the complete artifact to match', () => {
    const node = artifact('node-reset-copy');
    const go = artifact('go-reset-copy');
    compareStaticGolden(node, go);
    assert.throws(() => compareStaticGolden(node, artifact('go-reset-copy', { scripts: [{ id: 1 }] })), /golden_mismatch/);
  });

  it('rejects a shared source copy and ambiguous arguments', () => {
    assert.throws(() => compareStaticGolden(artifact('shared-copy'), artifact('shared-copy')), /shared_database_copy/);
    assert.equal(parseArgs(['--allow-root', 'fixtures', '--node', 'node.json', '--go', 'go.json']).go, 'go.json');
    assert.equal(parseArgs(['--node', 'node.json', '--node', 'again.json']), null);
  });

  it('blocks a golden artifact containing unredacted sensitive output', () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p08-'));
    const artifactPath = path.join(root, 'artifact.json');
    try {
      fs.writeFileSync(artifactPath, JSON.stringify({ secret: 'unredacted-material' }), 'utf8');
      assert.throws(() => readArtifact(root, artifactPath), /sensitive_output/);
    } finally {
      fs.rmSync(root, { recursive: true, force: true });
    }
  });
});
