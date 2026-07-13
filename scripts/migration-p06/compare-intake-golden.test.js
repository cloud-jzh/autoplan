'use strict';

const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { join } = require('node:path');
const { describe, it } = require('node:test');
const {
  IntakeGoldenContractError,
  assertStrictEqual,
  validateExpectedErrors,
  validateGoArtifact,
  validatePublicAttachment,
} = require('./compare-intake-golden');

function expectedErrors() {
  return validateExpectedErrors({
    schemaVersion: 1,
    version: 'p06-intake-expected-errors-v1',
    normalization: { request_id: '<request-id>', message: 'catalog-only', details: 'transport-safe-only' },
    scenarios: {
      'normalized-duplicate': { node: 'DUPLICATE_INTAKE', go: 'duplicate_intake', http: 'duplicate_intake', retryable: false },
    },
  });
}

function snapshot() {
  return { activeProjectId: 1, requirementIds: [2, 1], feedbackIds: [], attachmentIds: [7], planIds: [] };
}

function publicAttachment() {
  return {
    id: 7, display_name: 'fixture.txt', size: 5, mime_type: 'text/plain',
    download_url: '/api/v1/attachments/7/content',
  };
}

function nodeGolden() {
  return {
    schemaVersion: 1,
    version: 'p06-node-intake-golden-v1',
    scenarios: [
      {
        id: 'default-title-and-attachment', response: { ok: true, snapshot: snapshot() },
        observations: { attachment: { proposed_public_dto: publicAttachment() } },
      },
      { id: 'normalized-duplicate', response: { ok: false, error: { code: 'DUPLICATE_INTAKE' } } },
    ],
  };
}

function goGolden() {
  return {
    schemaVersion: 1,
    version: 'p06-node-intake-golden-v1',
    scenarios: [
      {
        id: 'default-title-and-attachment', response: {
          ok: true,
          snapshot: snapshot(),
          attachment: publicAttachment(),
        },
      },
      { id: 'normalized-duplicate', response: { ok: false, error: { code: 'duplicate_intake', retryable: false } } },
    ],
  };
}

describe('P06 Intake golden comparator', () => {

  it('keeps the recorded stable error matrix synthetically scoped and schema-valid', () => {
    const fixture = JSON.parse(readFileSync(join(process.cwd(), 'fixtures/migration/p06/expected-errors.json'), 'utf8'));
    const validated = validateExpectedErrors(fixture);
    assert.equal(validated.scenarios['normalized-duplicate'].go, 'duplicate_intake');
    assert.equal(validated.scenarios['attachment-recovery-required'].retryable, true);
  });

  it('keeps key presence, scalar types, nulls, and collection order strict', () => {
    assert.deepStrictEqual(assertStrictEqual({ a: null, b: [1, 2] }, { a: null, b: [1, 2] }), undefined);
    assert.throws(
      () => assertStrictEqual({ a: null }, { a: undefined }),
      (error) => error instanceof IntakeGoldenContractError && error.path === '$.a',
    );
    assert.throws(
      () => assertStrictEqual({ a: [1, 2] }, { a: [2, 1] }),
      (error) => error instanceof IntakeGoldenContractError && error.path === '$.a[0]',
    );
    assert.throws(
      () => assertStrictEqual({ a: 1 }, { a: 1, ignored: true }),
      (error) => error instanceof IntakeGoldenContractError && error.path === '$',
    );
  });

  it('accepts the public attachment allowlist and rejects legacy/private fields', () => {
    const attachment = publicAttachment();
    assert.equal(validatePublicAttachment(attachment, '$.attachment'), undefined);
    assert.throws(
      () => validatePublicAttachment({ ...attachment, stored_path: '<fixture-root>/ready/7.blob' }, '$.attachment'),
      IntakeGoldenContractError,
    );
    assert.throws(
      () => validatePublicAttachment({ ...attachment, download_url: '/api/v1/attachments/8/content' }, '$.attachment'),
      IntakeGoldenContractError,
    );
  });

  it('compares success snapshots and maps recorded Node failures to stable Go errors', () => {
    assert.equal(validateGoArtifact(nodeGolden(), goGolden(), expectedErrors()), undefined);
    const unknown = goGolden();
    unknown.scenarios.push({ id: 'unknown', response: { ok: true, snapshot: snapshot() } });
    assert.throws(() => validateGoArtifact(nodeGolden(), unknown, expectedErrors()), IntakeGoldenContractError);

    const wrongError = goGolden();
    wrongError.scenarios[1].response.error.code = 'internal_error';
    assert.throws(() => validateGoArtifact(nodeGolden(), wrongError, expectedErrors()), IntakeGoldenContractError);
  });

  it('preserves an intentional absence of snapshot data for recorded side-effect-only scenarios', () => {
    const node = {
      schemaVersion: 1,
      version: 'p06-node-intake-golden-v1',
      scenarios: [{ id: 'delete-side-effect-only', response: { ok: true } }],
    };
    const go = {
      schemaVersion: 1,
      version: 'p06-node-intake-golden-v1',
      scenarios: [{ id: 'delete-side-effect-only', response: { ok: true } }],
    };
    assert.equal(validateGoArtifact(node, go, expectedErrors()), undefined);
    go.scenarios[0].response.snapshot = snapshot();
    assert.throws(() => validateGoArtifact(node, go, expectedErrors()), IntakeGoldenContractError);
  });
});
