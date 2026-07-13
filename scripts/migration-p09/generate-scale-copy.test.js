'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { describe, it } = require('node:test');
const { cleanupScaleCopy, generateScaleCopy, readFixtureManifest, writeScaleCopy } = require('./generate-scale-copy');

describe('P09 sanitized scale copy generator', () => {
  it('is deterministic and matches the declared near-real row counts', () => {
    const recipe = readFixtureManifest();
    const first = generateScaleCopy({ recipe });
    const second = generateScaleCopy({ recipe });
    assert.deepEqual(first, second);
    assert.equal(first.copy.rows.projects.length, recipe.expected_rows.projects);
    assert.equal(first.copy.rows.plans.length, recipe.expected_rows.plans);
    assert.equal(first.copy.rows.plan_tasks.length, recipe.expected_rows.plan_tasks);
    assert.equal(first.copy.rows.events.length, recipe.expected_rows.events);
    assert.equal(first.copy.rows.events[1000].sequence, 1000);
    assert.equal(first.copy.rows.chat_messages.length, recipe.expected_rows.chat_messages);
    assert.equal(first.copy.rows.relation_anomalies.length, recipe.expected_rows.projects);
    assert.equal(new Set(first.copy.rows.events.map((event) => event.created_at)).size < first.copy.rows.events.length, true);
    assert.equal(new Set(first.copy.rows.events.map((event) => event.created_at)).size > 1, true);
  });

  it('writes only an explicit fixture output and permits explicit marker-gated cleanup', () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p09-fixture-'));
    const output = path.join(root, 'sanitized-scale-copy');
    try {
      const result = writeScaleCopy({ outputDirectory: output, scale: compactScale() });
      assert.equal(fs.existsSync(path.join(output, 'scale-copy.json')), true);
      assert.match(result.manifest.sha256, /^[a-f0-9]{64}$/);
      const serialized = fs.readFileSync(path.join(output, 'scale-copy.json'), 'utf8');
      assert.equal(/(?:api[_-]?key|secret|token|password|appdata)/i.test(serialized), false);
      cleanupScaleCopy(output);
      assert.equal(fs.existsSync(output), false);
    } finally {
      fs.rmSync(root, { recursive: true, force: true });
    }
  });

  it('changes deterministic synthetic identifiers when the seed changes without changing scale', () => {
    const first = generateScaleCopy({ seed: 'p09-seed-a', scale: compactScale() });
    const second = generateScaleCopy({ seed: 'p09-seed-b', scale: compactScale() });
    assert.notEqual(first.manifest.sha256, second.manifest.sha256);
    assert.equal(first.copy.rows.plan_tasks.length, second.copy.rows.plan_tasks.length);
  });
});

function compactScale() {
  return { projects: 2, plansPerProject: 2, tasksPerPlan: 3, eventsPerProject: 8, messagesPerProject: 6, scriptsPerProject: 2, executorsPerProject: 2 };
}
