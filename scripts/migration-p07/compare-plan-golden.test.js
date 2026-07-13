'use strict';

const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { join } = require('node:path');
const { describe, it } = require('node:test');
const {
  PlanGoldenContractError,
  assertStrictEqual,
  validateExpectedErrors,
  validateGoArtifact,
  validateNodeGolden,
} = require('./compare-plan-golden');

function nodeGolden() {
  return validateNodeGolden(JSON.parse(readFileSync(join(process.cwd(), 'fixtures/migration/p07/state-machine-cases.json'), 'utf8')));
}

function errors() {
  return validateExpectedErrors(JSON.parse(readFileSync(join(process.cwd(), 'fixtures/migration/p07/expected-errors.json'), 'utf8')));
}

function goGolden() {
  const source = nodeGolden();
  return {
    schemaVersion: 1,
    version: 'p07-node-plan-golden-v1',
    scenarios: source.scenarios.map((scenario) => {
      const response = { ...scenario.response };
      if (!response.ok) {
        response.error = errors().scenarios[scenario.id].go;
        response.retryable = errors().scenarios[scenario.id].retryable;
      }
      return { id: scenario.id, response };
    }),
  };
}

describe('P07 Plan golden comparator', () => {
  it('keeps the synthetic legal and illegal state machine matrix versioned', () => {
    const source = nodeGolden();
    assert.ok(source.scenarios.some((scenario) => scenario.id === 'reorder-complete-project-set'));
    assert.ok(source.scenarios.some((scenario) => scenario.id === 'delete-plan-with-running-task-protected'));
    assert.equal(errors().scenarios['long-action-disabled'].http, 'not_implemented');
  });

  it('preserves presence, nulls, types, and collection order', () => {
    assert.equal(assertStrictEqual({ accepted_at: null, ids: [2, 1] }, { accepted_at: null, ids: [2, 1] }), undefined);
    assert.throws(() => assertStrictEqual({ accepted_at: null }, { accepted_at: '' }), PlanGoldenContractError);
    assert.throws(() => assertStrictEqual({ ids: [2, 1] }, { ids: [1, 2] }), PlanGoldenContractError);
    assert.throws(() => assertStrictEqual({ ok: true }, { ok: true, ignored: true }), PlanGoldenContractError);
  });

  it('maps each recorded Node failure to a stable Go error and rejects drift', () => {
    const node = nodeGolden();
    const go = goGolden();
    assert.equal(validateGoArtifact(node, go, errors()), undefined);
    const stale = go.scenarios.find((scenario) => scenario.id === 'stale-reorder-rejected');
    stale.response.error = 'internal_error';
    assert.throws(() => validateGoArtifact(node, go, errors()), PlanGoldenContractError);
  });
});
