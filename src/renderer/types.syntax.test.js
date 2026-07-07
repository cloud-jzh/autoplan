const { describe, it } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { join } = require('node:path');
const ts = require('typescript');

const typesPath = join(process.cwd(), 'src', 'renderer', 'types.ts');

function formatParseDiagnostic(diagnostic, sourceFile) {
  const message = ts.flattenDiagnosticMessageText(diagnostic.messageText, '\n');
  if (typeof diagnostic.start === 'number') {
    const { line, character } = sourceFile.getLineAndCharacterOfPosition(diagnostic.start);
    return `${sourceFile.fileName}:${line + 1}:${character + 1} TS${diagnostic.code}: ${message}`;
  }
  return `${sourceFile.fileName} TS${diagnostic.code}: ${message}`;
}

describe('renderer types syntax', () => {
  it('parses types.ts without syntax diagnostics', () => {
    const sourceText = readFileSync(typesPath, 'utf8');
    const sourceFile = ts.createSourceFile(typesPath, sourceText, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
    const diagnostics = sourceFile.parseDiagnostics || [];

    assert.equal(
      diagnostics.length,
      0,
      diagnostics.map((diagnostic) => formatParseDiagnostic(diagnostic, sourceFile)).join('\n'),
    );
  });

  it('keeps AiThinkingDepth union aligned with OpenAI xhigh support', () => {
    const sourceText = readFileSync(typesPath, 'utf8');

    assert.match(
      sourceText,
      /export type AiThinkingDepth = 'low' \| 'medium' \| 'high' \| 'xhigh';/,
    );
  });

  it('keeps update installer download and opening contracts exposed', () => {
    const sourceText = readFileSync(typesPath, 'utf8');

    assert.match(
      sourceText,
      /export interface UpdateInstallerAsset\s*{[\s\S]*name: string;[\s\S]*downloadUrl: string;[\s\S]*}/,
    );
    assert.match(
      sourceText,
      /export type UpdateDownloadPhase = 'idle' \| 'unavailable' \| 'skipped' \| 'pending' \| 'downloading' \| 'downloaded' \| 'failed';/,
    );
    assert.match(sourceText, /downloadPhase\?: UpdateDownloadPhase;/);
    assert.match(sourceText, /localInstallerPath\?: string;/);
    assert.match(sourceText, /downloadedInstallerPath\?: string;/);
    assert.match(sourceText, /export interface UpdateInstallerOpenResult\s*{[\s\S]*ok: boolean;[\s\S]*filePath\?: string;[\s\S]*}/);
    assert.match(sourceText, /openUpdateInstaller: \(\) => Promise<UpdateInstallerOpenResult>;/);
  });
});
