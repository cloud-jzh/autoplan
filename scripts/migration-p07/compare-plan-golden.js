'use strict';

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(__dirname, '../..');
const GOLDEN_VERSION = 'p07-node-plan-golden-v1';
const EXPECTED_ERRORS_VERSION = 'p07-plan-expected-errors-v1';

class PlanGoldenContractError extends Error {
  constructor(pathname, message) {
    super(`${pathname}: ${message}`);
    this.name = 'PlanGoldenContractError';
    this.path = pathname;
  }
}

function readJSON(filePath) {
  return JSON.parse(fs.readFileSync(filePath, 'utf8'));
}

function object(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function typeOf(value) {
  if (value === null) return 'null';
  if (Array.isArray(value)) return 'array';
  return typeof value;
}

// This comparison deliberately preserves every field, null, scalar type, and
// array position. Golden artifacts are evidence, never an update mechanism.
function assertStrictEqual(expected, actual, pathname = '$') {
  if (typeOf(expected) !== typeOf(actual)) {
    throw new PlanGoldenContractError(pathname, `type ${typeOf(actual)} does not match ${typeOf(expected)}`);
  }
  if (Array.isArray(expected)) {
    if (expected.length !== actual.length) {
      throw new PlanGoldenContractError(pathname, `array length ${actual.length} does not match ${expected.length}`);
    }
    expected.forEach((entry, index) => assertStrictEqual(entry, actual[index], `${pathname}[${index}]`));
    return;
  }
  if (object(expected)) {
    const expectedKeys = Object.keys(expected).sort();
    const actualKeys = Object.keys(actual).sort();
    if (expectedKeys.length !== actualKeys.length || expectedKeys.some((key, index) => key !== actualKeys[index])) {
      throw new PlanGoldenContractError(pathname, `keys ${actualKeys.join(',')} do not match ${expectedKeys.join(',')}`);
    }
    expectedKeys.forEach((key) => assertStrictEqual(expected[key], actual[key], `${pathname}.${key}`));
    return;
  }
  if (!Object.is(expected, actual)) {
    throw new PlanGoldenContractError(pathname, `${JSON.stringify(actual)} does not match ${JSON.stringify(expected)}`);
  }
}

function validateExpectedErrors(value) {
  if (!object(value) || value.schemaVersion !== 1 || value.version !== EXPECTED_ERRORS_VERSION || !object(value.scenarios)) {
    throw new PlanGoldenContractError('expected-errors', 'invalid fixture header');
  }
  for (const [id, expected] of Object.entries(value.scenarios)) {
    if (!id || !object(expected) || typeof expected.node !== 'string' || typeof expected.go !== 'string' ||
        typeof expected.http !== 'string' || typeof expected.retryable !== 'boolean') {
      throw new PlanGoldenContractError(`expected-errors.${id}`, 'invalid stable error mapping');
    }
  }
  return value;
}

function validateNodeGolden(value) {
  if (!object(value) || value.schemaVersion !== 1 || value.version !== GOLDEN_VERSION ||
      value.source !== 'synthetic-node-reference' || !Array.isArray(value.scenarios)) {
    throw new PlanGoldenContractError('node-golden', 'invalid fixture header');
  }
  const ids = new Set();
  for (const scenario of value.scenarios) {
    if (!object(scenario) || typeof scenario.id !== 'string' || !scenario.id || ids.has(scenario.id) ||
        typeof scenario.action !== 'string' || typeof scenario.target !== 'string' || !object(scenario.prestate) ||
        !object(scenario.response) || typeof scenario.response.ok !== 'boolean') {
      throw new PlanGoldenContractError('node-golden.scenarios', 'invalid or duplicate scenario');
    }
    ids.add(scenario.id);
  }
  return value;
}

function validateGoArtifact(nodeGolden, goArtifact, expectedErrors) {
  if (!object(goArtifact) || goArtifact.schemaVersion !== 1 || goArtifact.version !== GOLDEN_VERSION ||
      !Array.isArray(goArtifact.scenarios)) {
    throw new PlanGoldenContractError('go-golden', 'invalid Go artifact header');
  }
  const nodeByID = new Map(nodeGolden.scenarios.map((scenario) => [scenario.id, scenario]));
  const goByID = new Map();
  for (const scenario of goArtifact.scenarios) {
    if (!object(scenario) || typeof scenario.id !== 'string' || !scenario.id || goByID.has(scenario.id) || !object(scenario.response)) {
      throw new PlanGoldenContractError('go-golden.scenarios', 'invalid or duplicate scenario');
    }
    goByID.set(scenario.id, scenario);
  }
  for (const [id, nodeScenario] of nodeByID) {
    const goScenario = goByID.get(id);
    if (!goScenario) throw new PlanGoldenContractError(`go-golden.scenarios.${id}`, 'scenario is missing');
    const expected = { ...nodeScenario.response };
    if (!expected.ok) {
      const mapping = expectedErrors.scenarios[id];
      if (!mapping || expected.error !== mapping.node) {
        throw new PlanGoldenContractError(`node-golden.scenarios.${id}`, 'fixture error does not match stable mapping');
      }
      expected.error = mapping.go;
      expected.retryable = mapping.retryable;
    }
    assertStrictEqual(expected, goScenario.response, `scenarios.${id}.response`);
  }
  for (const id of goByID.keys()) {
    if (!nodeByID.has(id)) throw new PlanGoldenContractError(`go-golden.scenarios.${id}`, 'unknown scenario');
  }
}

function comparePlanGolden(options = {}) {
  if (!options.goPath) throw new PlanGoldenContractError('go-golden', 'a separately reset Go artifact path is required');
  const nodePath = path.resolve(options.nodePath || path.join(ROOT, 'fixtures/migration/p07/state-machine-cases.json'));
  const goPath = path.resolve(options.goPath);
  const errorsPath = path.resolve(options.expectedErrorsPath || path.join(ROOT, 'fixtures/migration/p07/expected-errors.json'));
  const nodeGolden = validateNodeGolden(readJSON(nodePath));
  const expectedErrors = validateExpectedErrors(readJSON(errorsPath));
  validateGoArtifact(nodeGolden, readJSON(goPath), expectedErrors);
  return { scenarios: nodeGolden.scenarios.length, nodePath, goPath, errorsPath };
}

if (require.main === module) {
  try {
    process.stdout.write(`${JSON.stringify(comparePlanGolden({ goPath: process.argv[2], expectedErrorsPath: process.argv[3] }))}\n`);
  } catch (error) {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 1;
  }
}

module.exports = {
  EXPECTED_ERRORS_VERSION,
  GOLDEN_VERSION,
  PlanGoldenContractError,
  assertStrictEqual,
  comparePlanGolden,
  validateExpectedErrors,
  validateGoArtifact,
  validateNodeGolden,
};
