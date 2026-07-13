'use strict';

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(__dirname, '../..');
const RENDERER_ROOT = 'src/renderer';
const ALLOWED_DIRECT_ACCESS = new Set([
  'src/renderer/lib/api/ipcClient.ts',
  'src/renderer/lib/desktop/ipcBridge.ts',
]);

function toPosix(value) {
  return String(value).replace(/\\/g, '/');
}

function tokenizeJavaScript(source) {
  const tokens = [];
  let index = 0;
  const push = (type, value, start) => tokens.push({ type, value, start });
  while (index < source.length) {
    const start = index;
    const char = source[index];
    const next = source[index + 1];
    if (/\s/.test(char)) {
      index += 1;
      continue;
    }
    if (char === '/' && next === '/') {
      index += 2;
      while (index < source.length && source[index] !== '\n') index += 1;
      continue;
    }
    if (char === '/' && next === '*') {
      index += 2;
      while (index < source.length && !(source[index] === '*' && source[index + 1] === '/')) index += 1;
      index = Math.min(source.length, index + 2);
      continue;
    }
    if (char === "'" || char === '"' || char === '`') {
      const quote = char;
      let value = '';
      index += 1;
      while (index < source.length) {
        if (source[index] === '\\') {
          value += source[index + 1] || '';
          index += 2;
        } else if (source[index] === quote) {
          index += 1;
          break;
        } else {
          value += source[index];
          index += 1;
        }
      }
      push('string', value, start);
      if (quote === '`') {
        for (const expression of value.matchAll(/\$\{([^{}]*)\}/g)) {
          const expressionStart = start + 1 + expression.index + 2;
          for (const token of tokenizeJavaScript(expression[1])) {
            tokens.push({ ...token, start: token.start + expressionStart });
          }
        }
      }
      continue;
    }
    const identifier = /^[A-Za-z_$][\w$]*/.exec(source.slice(index));
    if (identifier) {
      push('identifier', identifier[0], start);
      index += identifier[0].length;
      continue;
    }
    if (char === '?' && next === '.') {
      push('punctuator', '?.', start);
      index += 2;
      continue;
    }
    push('punctuator', char, start);
    index += 1;
  }
  return tokens.sort((left, right) => left.start - right.start || (left.type === 'string' ? -1 : 1));
}

function rootLength(tokens, index, aliases) {
  const token = tokens[index];
  if (!token || token.type !== 'identifier') return 0;
  if (token.value === 'window' || aliases.has(token.value)) return 1;
  if (token.value === 'globalThis'
      && (tokens[index + 1]?.value === '.' || tokens[index + 1]?.value === '?.')
      && tokens[index + 2]?.value === 'window') return 3;
  if (token.value === 'globalThis'
      && tokens[index + 1]?.value === '['
      && tokens[index + 2]?.type === 'string'
      && tokens[index + 2]?.value === 'window'
      && tokens[index + 3]?.value === ']') return 4;
  return 0;
}

function lineAt(source, offset) {
  return source.slice(0, offset).split('\n').length;
}

function scanSource(relativePath, source) {
  const normalizedPath = toPosix(relativePath);
  if (ALLOWED_DIRECT_ACCESS.has(normalizedPath)) return [];
  const tokens = tokenizeJavaScript(source);
  const aliases = new Set();

  let changed = true;
  while (changed) {
    changed = false;
    for (let index = 0; index < tokens.length - 2; index += 1) {
      const candidate = tokens[index];
      if (candidate.type !== 'identifier' || tokens[index + 1]?.value !== '=') continue;
      if (rootLength(tokens, index + 2, aliases) && !aliases.has(candidate.value)) {
        aliases.add(candidate.value);
        changed = true;
      }
    }
  }

  const violations = [];
  const add = (token, reason) => {
    const line = lineAt(source, token.start);
    if (!violations.some((item) => item.line === line && item.reason === reason)) {
      violations.push({ file: normalizedPath, line, reason });
    }
  };

  for (let index = 0; index < tokens.length; index += 1) {
    const length = rootLength(tokens, index, aliases);
    if (!length) continue;
    const access = tokens[index + length];
    const property = tokens[index + length + 1];
    if ((access?.value === '.' || access?.value === '?.') && property?.value === 'autoplan') {
      add(tokens[index], 'direct autoplan property access');
    } else if (access?.value === '?.' && property?.value === '['
        && tokens[index + length + 2]?.type === 'string'
        && tokens[index + length + 2]?.value === 'autoplan') {
      add(tokens[index], 'optional bracket autoplan property access');
    } else if (access?.value === '[') {
      if (property?.type === 'string' && property.value === 'autoplan') {
        add(tokens[index], 'bracket autoplan property access');
      } else {
        add(tokens[index], 'dynamic global window property access');
      }
    }
  }

  for (let index = 0; index < tokens.length; index += 1) {
    if (tokens[index]?.value !== '{') continue;
    let cursor = index + 1;
    let ownsAutoplan = false;
    while (cursor < tokens.length && tokens[cursor]?.value !== '}') {
      if (tokens[cursor]?.value === 'autoplan') ownsAutoplan = true;
      cursor += 1;
    }
    if (ownsAutoplan && tokens[cursor]?.value === '}' && tokens[cursor + 1]?.value === '='
        && rootLength(tokens, cursor + 2, aliases)) {
      add(tokens[index], 'destructured autoplan access');
    }
  }
  return violations;
}

function walkFiles(directory) {
  if (!fs.existsSync(directory)) return [];
  const files = [];
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    const target = path.join(directory, entry.name);
    if (entry.isDirectory()) files.push(...walkFiles(target));
    else if (entry.isFile()) files.push(target);
  }
  return files;
}

function scanRenderer(rootDir = ROOT) {
  const rendererRoot = path.join(rootDir, RENDERER_ROOT);
  const violations = [];
  for (const filePath of walkFiles(rendererRoot)) {
    const relative = toPosix(path.relative(rootDir, filePath));
    if (!/\.(?:ts|tsx|js|jsx)$/.test(relative) || /\.(?:test|spec)\.[^.]+$/.test(relative)) continue;
    violations.push(...scanSource(relative, fs.readFileSync(filePath, 'utf8')));
  }
  return violations.sort((left, right) => left.file.localeCompare(right.file) || left.line - right.line);
}

function run(rootDir = ROOT) {
  const violations = scanRenderer(rootDir);
  if (violations.length) {
    const lines = violations.map((item) => `${item.file}:${item.line} ${item.reason}`);
    const error = new Error(`renderer boundary violations (${violations.length})\n- ${lines.join('\n- ')}`);
    error.violations = violations;
    throw error;
  }
  return { ok: true, allowlist: [...ALLOWED_DIRECT_ACCESS].sort(), violations: 0 };
}

if (require.main === module) {
  try {
    const result = run(process.argv[2] ? path.resolve(process.argv[2]) : ROOT);
    process.stdout.write(`${JSON.stringify(result)}\n`);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}

module.exports = {
  ALLOWED_DIRECT_ACCESS,
  run,
  scanRenderer,
  scanSource,
  tokenizeJavaScript,
};
