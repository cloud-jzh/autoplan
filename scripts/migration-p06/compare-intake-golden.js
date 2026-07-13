'use strict';

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(__dirname, '../..');
const EXPECTED_ERRORS_PATH = 'fixtures/migration/p06/expected-errors.json';
const GOLDEN_VERSION = 'p06-node-intake-golden-v1';
const EXPECTED_ERRORS_VERSION = 'p06-intake-expected-errors-v1';
const PUBLIC_ATTACHMENT_FIELDS = Object.freeze(['id', 'display_name', 'size', 'mime_type', 'download_url']);
const FORBIDDEN_PUBLIC_ATTACHMENT_FIELDS = Object.freeze([
  'stored_path', 'hash', 'original_name', 'project_id', 'owner_id', 'owner_type',
]);

class IntakeGoldenContractError extends Error {
  constructor(pathname, message) {
    super(`${pathname}: ${message}`);
    this.name = 'IntakeGoldenContractError';
    this.path = pathname;
  }
}

function readJson(filePath) {
  return JSON.parse(fs.readFileSync(filePath, 'utf8'));
}

function own(value, key) {
  return Object.prototype.hasOwnProperty.call(value, key);
}

function plainObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function typeName(value) {
  if (value === null) return 'null';
  if (Array.isArray(value)) return 'array';
  return typeof value;
}

function assertAllowedKeys(value, allowed, pathname) {
  if (!plainObject(value)) throw new IntakeGoldenContractError(pathname, 'must be an object');
  const unknown = Object.keys(value).filter((key) => !allowed.includes(key));
  if (unknown.length) throw new IntakeGoldenContractError(pathname, `unknown keys ${unknown.sort().join(',')}`);
}

/**
 * Deep comparison is intentionally presence-sensitive: callers cannot hide a
 * compatibility drift through null/default coercion or ignored unknown keys.
 */
function assertStrictEqual(expected, actual, pathname = '$') {
  const expectedType = typeName(expected);
  const actualType = typeName(actual);
  if (expectedType !== actualType) {
    throw new IntakeGoldenContractError(pathname, `type ${actualType} does not match ${expectedType}`);
  }
  if (Array.isArray(expected)) {
    if (expected.length !== actual.length) {
      throw new IntakeGoldenContractError(pathname, `array length ${actual.length} does not match ${expected.length}`);
    }
    expected.forEach((value, index) => assertStrictEqual(value, actual[index], `${pathname}[${index}]`));
    return;
  }
  if (plainObject(expected)) {
    const expectedKeys = Object.keys(expected).sort();
    const actualKeys = Object.keys(actual).sort();
    if (expectedKeys.length !== actualKeys.length || expectedKeys.some((key, index) => key !== actualKeys[index])) {
      throw new IntakeGoldenContractError(pathname, `keys ${actualKeys.join(',')} do not match ${expectedKeys.join(',')}`);
    }
    expectedKeys.forEach((key) => assertStrictEqual(expected[key], actual[key], `${pathname}.${key}`));
    return;
  }
  if (!Object.is(expected, actual)) {
    throw new IntakeGoldenContractError(pathname, `${JSON.stringify(actual)} does not match ${JSON.stringify(expected)}`);
  }
}

function validateExpectedErrors(value) {
  if (!plainObject(value) || value.schemaVersion !== 1 || value.version !== EXPECTED_ERRORS_VERSION || !plainObject(value.scenarios)) {
    throw new IntakeGoldenContractError('expected-errors', 'invalid fixture header');
  }
  assertAllowedKeys(value, ['schemaVersion', 'version', 'normalization', 'scenarios'], 'expected-errors');
  if (!plainObject(value.normalization)) {
    throw new IntakeGoldenContractError('expected-errors.normalization', 'must be an object');
  }
  assertAllowedKeys(value.normalization, ['request_id', 'message', 'details'], 'expected-errors.normalization');
  for (const key of ['request_id', 'message', 'details']) {
    if (typeof value.normalization[key] !== 'string' || !value.normalization[key]) {
      throw new IntakeGoldenContractError(`expected-errors.normalization.${key}`, 'must be a non-empty marker');
    }
  }
  for (const [scenario, expectation] of Object.entries(value.scenarios)) {
    if (!scenario || !plainObject(expectation) || typeof expectation.retryable !== 'boolean') {
      throw new IntakeGoldenContractError(`expected-errors.${scenario}`, 'invalid scenario expectation');
    }
    assertAllowedKeys(expectation, ['node', 'go', 'http', 'retryable'], `expected-errors.${scenario}`);
    if (typeof expectation.go !== 'string' || !expectation.go || typeof expectation.http !== 'string' || !expectation.http) {
      throw new IntakeGoldenContractError(`expected-errors.${scenario}`, 'Go and HTTP codes are required');
    }
    for (const key of ['node', 'go', 'http']) {
      if (own(expectation, key) && (typeof expectation[key] !== 'string' || !expectation[key])) {
        throw new IntakeGoldenContractError(`expected-errors.${scenario}.${key}`, 'must be a non-empty code');
      }
    }
  }
  return value;
}

function validatePublicAttachment(value, pathname) {
  if (!plainObject(value)) throw new IntakeGoldenContractError(pathname, 'attachment must be an object');
  const keys = Object.keys(value).sort();
  const expected = [...PUBLIC_ATTACHMENT_FIELDS].sort();
  if (keys.length !== expected.length || keys.some((key, index) => key !== expected[index])) {
    throw new IntakeGoldenContractError(pathname, 'attachment fields are not the public allowlist');
  }
  for (const key of FORBIDDEN_PUBLIC_ATTACHMENT_FIELDS) {
    if (own(value, key)) throw new IntakeGoldenContractError(pathname, `forbidden field ${key}`);
  }
  if (!Number.isSafeInteger(value.id) || value.id <= 0 || typeof value.display_name !== 'string' || !value.display_name ||
      !Number.isSafeInteger(value.size) || value.size <= 0 || typeof value.mime_type !== 'string' || !value.mime_type ||
      typeof value.download_url !== 'string' || !/^\/api\/v1\/attachments\/[1-9][0-9]*\/content$/.test(value.download_url)) {
    throw new IntakeGoldenContractError(pathname, 'attachment DTO has invalid public values');
  }
  const id = Number(value.download_url.match(/\/attachments\/([1-9][0-9]*)\/content$/)[1]);
  if (id !== value.id) throw new IntakeGoldenContractError(pathname, 'download URL id does not match attachment id');
}

function validateGoArtifact(nodeGolden, goArtifact, expectedErrors) {
  if (!plainObject(nodeGolden) || nodeGolden.version !== GOLDEN_VERSION || !Array.isArray(nodeGolden.scenarios)) {
    throw new IntakeGoldenContractError('node-golden', 'invalid source golden');
  }
  if (!plainObject(goArtifact) || goArtifact.schemaVersion !== 1 || goArtifact.version !== GOLDEN_VERSION ||
      !Array.isArray(goArtifact.scenarios)) {
    throw new IntakeGoldenContractError('go-golden', 'invalid Go artifact header');
  }
  const nodeByID = new Map(nodeGolden.scenarios.map((scenario) => [scenario?.id, scenario]));
  if (nodeByID.size !== nodeGolden.scenarios.length || nodeByID.has(undefined)) {
    throw new IntakeGoldenContractError('node-golden.scenarios', 'scenario ids must be unique');
  }
  const goByID = new Map(goArtifact.scenarios.map((scenario) => [scenario?.id, scenario]));
  if (goByID.size !== goArtifact.scenarios.length || goByID.has(undefined)) {
    throw new IntakeGoldenContractError('go-golden.scenarios', 'scenario ids must be unique');
  }
  for (const [id, nodeScenario] of nodeByID) {
    const goScenario = goByID.get(id);
    if (!goScenario) throw new IntakeGoldenContractError(`go-golden.scenarios.${id}`, 'scenario is missing');
    if (!plainObject(goScenario) || typeof goScenario.id !== 'string' || !own(goScenario, 'response')) {
      throw new IntakeGoldenContractError(`go-golden.scenarios.${id}`, 'invalid scenario shape');
    }
    assertAllowedKeys(goScenario, ['id', 'response'], `go-golden.scenarios.${id}`);
    assertAllowedKeys(goScenario.response, ['ok', 'snapshot', 'attachment', 'error'], `go-golden.scenarios.${id}.response`);
    if (nodeScenario.response?.ok === true) {
      if (goScenario.response?.ok !== true) {
        throw new IntakeGoldenContractError(`scenarios.${id}.response`, 'successful scenario is not successful in Go');
      }
      if (own(goScenario.response, 'error')) {
        throw new IntakeGoldenContractError(`scenarios.${id}.response`, 'successful scenario contains an error');
      }
      if (own(nodeScenario.response, 'snapshot')) {
        if (!own(goScenario.response, 'snapshot')) {
          throw new IntakeGoldenContractError(`scenarios.${id}.response`, 'successful scenario is missing a Go snapshot');
        }
        assertStrictEqual(nodeScenario.response.snapshot, goScenario.response.snapshot, `scenarios.${id}.response.snapshot`);
      } else if (own(goScenario.response, 'snapshot')) {
        throw new IntakeGoldenContractError(`scenarios.${id}.response`, 'Go added a snapshot where Node recorded none');
      }
    }
    if (nodeScenario.response?.ok === false) {
      const expectation = expectedErrors.scenarios[id];
      assertAllowedKeys(goScenario.response.error, ['code', 'retryable'], `go-golden.scenarios.${id}.response.error`);
      if (!expectation || nodeScenario.response?.error?.code !== expectation.node || goScenario.response?.ok !== false ||
          goScenario.response?.error?.code !== expectation.go || goScenario.response?.error?.retryable !== expectation.retryable) {
        throw new IntakeGoldenContractError(`scenarios.${id}.response.error`, 'stable Go error code drifted');
      }
      if (own(goScenario.response, 'snapshot') || own(goScenario.response, 'attachment')) {
        throw new IntakeGoldenContractError(`scenarios.${id}.response`, 'failed scenario contains success data');
      }
    }
    const expectedAttachment = nodeScenario.observations?.attachment?.proposed_public_dto;
    const attachment = goScenario.response?.attachment;
    if (expectedAttachment !== undefined) {
      if (attachment === undefined) {
        throw new IntakeGoldenContractError(`scenarios.${id}.response.attachment`, 'Go public attachment is missing');
      }
      assertStrictEqual(expectedAttachment, attachment, `scenarios.${id}.response.attachment`);
    } else if (attachment !== undefined) {
      throw new IntakeGoldenContractError(`scenarios.${id}.response.attachment`, 'Go added an attachment outside the recorded scenario');
    }
    if (attachment !== undefined) validatePublicAttachment(attachment, `scenarios.${id}.response.attachment`);
  }
  for (const id of goByID.keys()) {
    if (!nodeByID.has(id)) throw new IntakeGoldenContractError(`go-golden.scenarios.${id}`, 'unknown scenario');
  }
}

function compareIntakeGolden(options = {}) {
  const nodePath = path.resolve(options.nodePath || path.join(ROOT, 'fixtures/migration/p06/node-intake.golden.json'));
  const goPath = path.resolve(options.goPath || '');
  const expectedErrorsPath = path.resolve(options.expectedErrorsPath || path.join(ROOT, EXPECTED_ERRORS_PATH));
  if (!options.goPath) throw new IntakeGoldenContractError('go-golden', 'a separately reset Go artifact path is required');
  const nodeGolden = readJson(nodePath);
  const goArtifact = readJson(goPath);
  const expectedErrors = validateExpectedErrors(readJson(expectedErrorsPath));
  validateGoArtifact(nodeGolden, goArtifact, expectedErrors);
  return { scenarios: nodeGolden.scenarios.length, nodePath, goPath, expectedErrorsPath };
}

if (require.main === module) {
  try {
    const result = compareIntakeGolden({ goPath: process.argv[2], expectedErrorsPath: process.argv[3] });
    process.stdout.write(`${JSON.stringify(result)}\n`);
  } catch (error) {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 1;
  }
}

module.exports = {
  EXPECTED_ERRORS_VERSION,
  GOLDEN_VERSION,
  IntakeGoldenContractError,
  PUBLIC_ATTACHMENT_FIELDS,
  assertStrictEqual,
  compareIntakeGolden,
  validateExpectedErrors,
  validateGoArtifact,
  validatePublicAttachment,
};
