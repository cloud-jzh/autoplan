'use strict';

const fs = require('node:fs');
const path = require('node:path');

const CONTRACT_PATH = 'docs/migration/p00/contract-baseline.json';
const REQUIRED_TOP_LEVEL = Object.freeze([
  'schemaVersion',
  'dtoContracts',
  'snapshotContracts',
  'stateMachines',
  'errorContracts',
  'eventOrdering',
  'sideEffectContracts',
  'sourceAssertions',
  'security',
]);

function readText(rootDir, relativePath) {
  return fs.readFileSync(path.join(rootDir, relativePath), 'utf8');
}

function normalizeType(value) {
  return String(value || '').replace(/\s+/g, '');
}

function stripComments(source) {
  return source
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|\s)\/\/.*$/gm, '$1');
}

function balancedEnd(source, openIndex, openChar = '{', closeChar = '}') {
  let depth = 0;
  let quote = null;
  let escaped = false;
  for (let index = openIndex; index < source.length; index += 1) {
    const char = source[index];
    if (quote) {
      if (escaped) escaped = false;
      else if (char === '\\') escaped = true;
      else if (char === quote) quote = null;
      continue;
    }
    if (char === '"' || char === "'" || char === '`') {
      quote = char;
      continue;
    }
    if (char === openChar) depth += 1;
    if (char === closeChar) depth -= 1;
    if (depth === 0) return index;
  }
  return -1;
}

function findFunctionBlock(source, functionName) {
  const patterns = [
    new RegExp(`function\\s+${escapeRegExp(functionName)}\\s*\\(`),
    new RegExp(`(?:^|\\n)\\s*${escapeRegExp(functionName)}\\s*\\([^)]*\\)\\s*\\{`),
  ];
  const match = patterns.map((pattern) => pattern.exec(source)).find(Boolean);
  if (!match) throw new Error(`未找到函数：${functionName}`);
  const open = source.indexOf('{', match.index);
  const close = balancedEnd(source, open);
  if (open < 0 || close < 0) throw new Error(`函数块未闭合：${functionName}`);
  return source.slice(open + 1, close);
}

function splitTopLevel(source, delimiter = ';') {
  const parts = [];
  let start = 0;
  let curly = 0;
  let square = 0;
  let paren = 0;
  let angle = 0;
  let quote = null;
  let escaped = false;
  for (let index = 0; index < source.length; index += 1) {
    const char = source[index];
    if (quote) {
      if (escaped) escaped = false;
      else if (char === '\\') escaped = true;
      else if (char === quote) quote = null;
      continue;
    }
    if (char === '"' || char === "'" || char === '`') {
      quote = char;
      continue;
    }
    if (char === '{') curly += 1;
    else if (char === '}') curly -= 1;
    else if (char === '[') square += 1;
    else if (char === ']') square -= 1;
    else if (char === '(') paren += 1;
    else if (char === ')') paren -= 1;
    else if (char === '<') angle += 1;
    else if (char === '>' && angle > 0) angle -= 1;
    else if (char === delimiter && curly === 0 && square === 0 && paren === 0 && angle === 0) {
      parts.push(source.slice(start, index));
      start = index + 1;
    }
  }
  if (source.slice(start).trim()) parts.push(source.slice(start));
  return parts;
}

function extractInterface(source, name) {
  const clean = stripComments(source);
  const marker = new RegExp(`export\\s+interface\\s+${escapeRegExp(name)}(?=[\\s<{])`);
  const match = marker.exec(clean);
  if (!match) throw new Error(`未找到 TypeScript interface：${name}`);
  const open = clean.indexOf('{', match.index);
  const close = balancedEnd(clean, open);
  if (open < 0 || close < 0) throw new Error(`interface 未闭合：${name}`);
  const header = clean.slice(match.index, open).replace(/\s+/g, ' ').trim();
  const body = clean.slice(open + 1, close);
  const fields = [];
  for (const part of splitTopLevel(body)) {
    const value = part.trim();
    if (!value || value.startsWith('[')) continue;
    const field = /^([A-Za-z_$][\w$]*)(\?)?\s*:\s*([\s\S]+)$/.exec(value);
    if (!field) continue;
    fields.push(`${field[1]}${field[2] || ''}:${normalizeType(field[3])}`);
  }
  return { header, fields };
}

function extractObjectKeys(objectBody) {
  const keys = [];
  for (const part of splitTopLevelByComma(objectBody)) {
    const value = part.trim();
    if (!value || value.startsWith('...')) continue;
    const key = /^([A-Za-z_$][\w$]*)\s*(?::[\s\S]*|,?)$/.exec(value);
    if (key) keys.push(key[1]);
  }
  return keys;
}

function splitTopLevelByComma(source) {
  const parts = [];
  let start = 0;
  let curly = 0;
  let square = 0;
  let paren = 0;
  let quote = null;
  let escaped = false;
  for (let index = 0; index < source.length; index += 1) {
    const char = source[index];
    if (quote) {
      if (escaped) escaped = false;
      else if (char === '\\') escaped = true;
      else if (char === quote) quote = null;
      continue;
    }
    if (char === '"' || char === "'" || char === '`') {
      quote = char;
      continue;
    }
    if (char === '{') curly += 1;
    else if (char === '}') curly -= 1;
    else if (char === '[') square += 1;
    else if (char === ']') square -= 1;
    else if (char === '(') paren += 1;
    else if (char === ')') paren -= 1;
    else if (char === ',' && curly === 0 && square === 0 && paren === 0) {
      parts.push(source.slice(start, index));
      start = index + 1;
    }
  }
  if (source.slice(start).trim()) parts.push(source.slice(start));
  return parts;
}

function returnObjectCandidates(functionBody) {
  const candidates = [];
  const pattern = /return\s*\{/g;
  for (const match of functionBody.matchAll(pattern)) {
    const open = functionBody.indexOf('{', match.index);
    const close = balancedEnd(functionBody, open);
    if (close < 0) continue;
    candidates.push(extractObjectKeys(functionBody.slice(open + 1, close)));
  }
  return candidates;
}

function extractLargestReturnObjectKeys(source, functionName) {
  const candidates = returnObjectCandidates(findFunctionBlock(stripComments(source), functionName));
  if (!candidates.length) throw new Error(`函数没有对象返回值：${functionName}`);
  return candidates.sort((left, right) => right.length - left.length)[0];
}

function extractConstObjectValues(source, constName) {
  const clean = stripComments(source);
  const marker = new RegExp(`(?:const|export\\s+const)\\s+${escapeRegExp(constName)}\\s*=`);
  const match = marker.exec(clean);
  if (!match) throw new Error(`未找到常量对象：${constName}`);
  const open = clean.indexOf('{', match.index);
  const close = balancedEnd(clean, open);
  if (open < 0 || close < 0) throw new Error(`常量对象未闭合：${constName}`);
  const values = [];
  for (const matchValue of clean.slice(open + 1, close).matchAll(/:\s*'([^']+)'/g)) values.push(matchValue[1]);
  return values;
}

function escapeRegExp(value) {
  return String(value).replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function compareArray(label, actual, expected, errors) {
  if (actual.length !== expected.length || actual.some((value, index) => value !== expected[index])) {
    errors.push(`${label} 漂移：源码=${JSON.stringify(actual)} 基线=${JSON.stringify(expected)}`);
  }
}

function validateShape(contract, errors) {
  for (const key of REQUIRED_TOP_LEVEL) {
    if (!(key in contract)) errors.push(`契约缺少顶层字段 ${key}`);
  }
  if (contract.schemaVersion !== 1) errors.push('contract-baseline schemaVersion 必须为 1');
  for (const key of REQUIRED_TOP_LEVEL.filter((item) => !['schemaVersion', 'security'].includes(item))) {
    if (!Array.isArray(contract[key]) || contract[key].length === 0) errors.push(`${key} 必须是非空数组`);
  }
  const ids = [];
  for (const section of REQUIRED_TOP_LEVEL.filter((item) => Array.isArray(contract[item]))) {
    for (const item of contract[section]) {
      if (!item.id) errors.push(`${section} 存在无 id 条目`);
      ids.push(item.id);
    }
  }
  const duplicates = ids.filter((id, index) => id && ids.indexOf(id) !== index);
  if (duplicates.length) errors.push(`契约 id 重复：${[...new Set(duplicates)].join(', ')}`);

  for (const item of contract.errorContracts || []) {
    for (const field of ['domain', 'codeCandidate', 'currentBehavior', 'retryable']) {
      if (!(field in item) || item[field] === '') errors.push(`${item.id} 缺少错误契约字段 ${field}`);
    }
  }
  for (const item of contract.eventOrdering || []) {
    for (const field of ['source', 'producer', 'consumer', 'payload', 'ordering']) {
      if (!(field in item) || item[field] === '') errors.push(`${item.id} 缺少事件契约字段 ${field}`);
    }
    if (!Array.isArray(item.ordered) || !item.ordered.length) errors.push(`${item.id} 缺少可校验 ordered marker`);
  }
  for (const item of contract.sideEffectContracts || []) {
    for (const field of ['domain', 'success', 'failure', 'idempotency', 'cancellation', 'recovery']) {
      if (!(field in item) || item[field] === '') errors.push(`${item.id} 缺少副作用契约字段 ${field}`);
    }
  }
  if (!contract.security || !Array.isArray(contract.security.redactionRules) || !contract.security.redactionRules.length) {
    errors.push('security.redactionRules 必须是非空数组');
  }
  if (!contract.security?.defaultPolicy) errors.push('security.defaultPolicy 不能为空');
}

function validateDtoContracts(rootDir, contract, errors) {
  const cache = new Map();
  for (const item of contract.dtoContracts || []) {
    const source = cache.get(item.source) || readText(rootDir, item.source);
    cache.set(item.source, source);
    let declaration;
    try {
      declaration = extractInterface(source, item.declaration);
    } catch (error) {
      errors.push(`${item.id}: ${error.message}`);
      continue;
    }
    if (item.header && declaration.header !== item.header) {
      errors.push(`${item.id} header 漂移：源码=${declaration.header} 基线=${item.header}`);
    }
    compareArray(`${item.id} 字段`, declaration.fields, item.fields || [], errors);
  }
}

function validateSnapshotContracts(rootDir, contract, errors) {
  for (const item of contract.snapshotContracts || []) {
    try {
      const source = readText(rootDir, item.source);
      const actual = item.interface
        ? extractInterface(source, item.interface).fields.map((field) => field.split(':', 1)[0])
        : extractLargestReturnObjectKeys(source, item.function);
      compareArray(`${item.id} 字段顺序`, actual, item.keys || [], errors);
    } catch (error) {
      errors.push(`${item.id}: ${error.message}`);
    }
  }
}

function validateStateMachines(rootDir, contract, errors) {
  for (const item of contract.stateMachines || []) {
    if (!Array.isArray(item.states) || !item.states.length || !item.source) {
      errors.push(`${item.id} 状态机定义不完整`);
      continue;
    }
    if (item.const) {
      try {
        compareArray(`${item.id} 状态`, extractConstObjectValues(readText(rootDir, item.source), item.const), item.states, errors);
      } catch (error) {
        errors.push(`${item.id}: ${error.message}`);
      }
    } else {
      const source = readText(rootDir, item.source);
      for (const marker of item.markers || []) {
        if (!source.includes(marker)) errors.push(`${item.id} 缺少状态转换 marker：${marker}`);
      }
    }
  }
}

function validateSourceAssertions(rootDir, assertions, errors) {
  for (const item of assertions || []) {
    const source = readText(rootDir, item.source);
    for (const marker of item.contains || []) {
      if (!source.includes(marker)) errors.push(`${item.id} 缺少源码事实：${marker}`);
    }
    for (const sequence of item.ordered || []) {
      let cursor = -1;
      for (const marker of sequence) {
        const next = source.indexOf(marker, cursor + 1);
        if (next < 0) {
          errors.push(`${item.id} 顺序漂移或缺少 marker：${marker}`);
          break;
        }
        cursor = next;
      }
    }
  }
}

function scanContractForSecrets(contract) {
  const errors = [];
  const text = JSON.stringify(contract);
  const patterns = [
    { label: '绝对 Windows 路径', pattern: /[A-Za-z]:[\\/](?:Users|Documents and Settings|AppData)[\\/]/i },
    { label: 'OpenAI 风格密钥', pattern: /\bsk-[A-Za-z0-9_-]{12,}\b/ },
    { label: 'GitHub token', pattern: /\b(?:ghp|github_pat)_[A-Za-z0-9_]{12,}\b/i },
    { label: 'Bearer 凭据', pattern: /Bearer\s+(?!<token>|token\b)[A-Za-z0-9._~+/-]{12,}/i },
    { label: 'PEM 私钥', pattern: /BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY/ },
  ];
  for (const item of patterns) {
    if (item.pattern.test(text)) errors.push(`契约包含${item.label}`);
  }
  return errors;
}

function validateContract(rootDir, contract) {
  const errors = [];
  validateShape(contract, errors);
  validateDtoContracts(rootDir, contract, errors);
  validateSnapshotContracts(rootDir, contract, errors);
  validateStateMachines(rootDir, contract, errors);
  validateSourceAssertions(rootDir, [
    ...(contract.sourceAssertions || []),
    ...(contract.eventOrdering || []),
  ], errors);
  errors.push(...scanContractForSecrets(contract));
  return errors;
}

function loadContract(rootDir) {
  return JSON.parse(readText(rootDir, CONTRACT_PATH));
}

function run(rootDir = path.resolve(__dirname, '../..')) {
  const contract = loadContract(rootDir);
  const errors = validateContract(rootDir, contract);
  if (errors.length) {
    const error = new Error(`P00 契约基线漂移（${errors.length} 项）\n- ${errors.join('\n- ')}`);
    error.errors = errors;
    throw error;
  }
  return {
    ok: true,
    dtoContracts: contract.dtoContracts.length,
    snapshotContracts: contract.snapshotContracts.length,
    stateMachines: contract.stateMachines.length,
    eventOrdering: contract.eventOrdering.length,
    sideEffectContracts: contract.sideEffectContracts.length,
  };
}

if (require.main === module) {
  try {
    process.stdout.write(`${JSON.stringify(run(process.argv[2] ? path.resolve(process.argv[2]) : undefined))}\n`);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}

module.exports = {
  CONTRACT_PATH,
  extractConstObjectValues,
  extractInterface,
  extractLargestReturnObjectKeys,
  loadContract,
  scanContractForSecrets,
  validateContract,
  run,
};
