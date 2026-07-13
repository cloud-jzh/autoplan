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

describe('intake acceptance contracts', () => {
  it('keeps Requirement and Feedback accepted_at fields exposed to the renderer', () => {
    const sourceText = readFileSync(typesPath, 'utf8');

    assert.match(
      sourceText,
      /export interface Requirement[\s\S]*accepted_at: string \| null;[\s\S]*}/,
    );
    assert.match(
      sourceText,
      /export interface Feedback[\s\S]*accepted_at: string \| null;[\s\S]*}/,
    );
  });

  it('keeps intake acceptance input, handler and AutoplanApi methods aligned', () => {
    const sourceText = readFileSync(typesPath, 'utf8');

    assert.match(
      sourceText,
      /export interface IntakeAcceptanceInput extends ProjectIdInput\s*{[\s\S]*type: IntakeType;[\s\S]*id: number;[\s\S]*}/,
    );
    assert.match(
      sourceText,
      /export type IntakeAcceptanceHandler = \(type: IntakeType, id: number\) => Promise<void> \| void;/,
    );
    assert.match(sourceText, /acceptIntake: \(input: IntakeAcceptanceInput\) => Promise<AppSnapshot>;/);
    assert.match(sourceText, /unacceptIntake: \(input: IntakeAcceptanceInput\) => Promise<AppSnapshot>;/);
  });

  it('keeps preload, controller and workspace page wired to the same intake acceptance API names', () => {
    const preload = readFileSync(join(process.cwd(), 'src', 'preload.js'), 'utf8');
    const controller = readFileSync(join(process.cwd(), 'src', 'renderer', 'hooks', 'useWorkspaceController.ts'), 'utf8');
    const page = readFileSync(join(process.cwd(), 'src', 'renderer', 'pages', 'WorkspacePage.tsx'), 'utf8');

    assert.match(preload, /acceptIntake: \(input\) => ipcRenderer\.invoke\('intake:accept', input\)/);
    assert.match(preload, /unacceptIntake: \(input\) => ipcRenderer\.invoke\('intake:unaccept', input\)/);
    assert.match(controller, /window\.autoplan\.acceptIntake\(\{ projectId, type, id \}\)/);
    assert.match(controller, /window\.autoplan\.unacceptIntake\(\{ projectId, type, id \}\)/);
    assert.match(page, /onAcceptIntake=\{acceptIntake\}/);
    assert.match(page, /onUnacceptIntake=\{unacceptIntake\}/);
  });
});
describe('IntakePanel intake acceptance source contracts', () => {
  it('renders acceptance status below intake content and before item actions', () => {
    const panel = readFileSync(join(process.cwd(), 'src', 'renderer', 'components', 'IntakePanel.tsx'), 'utf8');
    const loopStart = panel.indexOf('{visibleItems.map((item) => {');
    const loopEnd = panel.indexOf('{hasMoreItems ? (', loopStart);
    assert.ok(loopStart >= 0, 'should locate visible intake render loop');
    assert.ok(loopEnd > loopStart, 'should locate end of visible intake render loop');
    const renderBody = panel.slice(loopStart, loopEnd);

    const bodyIndex = renderBody.indexOf('{item.body ? <div className="item-body plain-text">{item.body}</div> : null}');
    const attachmentIndex = renderBody.indexOf('<AttachmentGrid attachments={itemAttachments} />');
    const acceptanceIndex = renderBody.indexOf('<IntakeAcceptanceStatus');
    const footIndex = renderBody.indexOf('<div className="item-foot">');

    assert.ok(bodyIndex >= 0, 'item body should remain in the render loop');
    assert.ok(attachmentIndex > bodyIndex, 'attachments should render after body');
    assert.ok(acceptanceIndex > attachmentIndex, 'acceptance status should render after content and attachments');
    assert.ok(footIndex > acceptanceIndex, 'acceptance status should render before item actions');
    assert.match(renderBody, /onToggle=\{\(\) => void toggleIntakeAcceptance\(item, Boolean\(item\.accepted_at\)\)\}/);
  });

  it('keeps accepted/pending copy, loading copy and stable button sizing in IntakePanel', () => {
    const panel = readFileSync(join(process.cwd(), 'src', 'renderer', 'components', 'IntakePanel.tsx'), 'utf8');
    const start = panel.indexOf('function IntakeAcceptanceStatus({');
    const end = panel.indexOf('function IntakeGenerateFailureCard({', start);
    assert.ok(start >= 0 && end > start, 'should locate IntakeAcceptanceStatus component');
    const statusBody = panel.slice(start, end);

    assert.match(statusBody, /const acceptedAt = String\(item\.accepted_at \|\| ''\)\.trim\(\);/);
    assert.match(statusBody, /const label = accepted \? '已验收' : '未验收';/);
    assert.match(statusBody, /人工验收/);
    assert.match(statusBody, /formatChinaDateTime\(acceptedAt\)/);
    assert.match(statusBody, /正在取消\.\.\./);
    assert.match(statusBody, /取消验收/);
    assert.match(statusBody, /正在标记\.\.\./);
    assert.match(statusBody, /标记验收/);
    assert.match(statusBody, /disabled=\{loading\}/);
    assert.match(statusBody, /minWidth: 104/);
    assert.match(statusBody, /whiteSpace: 'nowrap'/);
  });

  it('routes acceptance clicks to mark/cancel handlers and guards duplicate clicks', () => {
    const panel = readFileSync(join(process.cwd(), 'src', 'renderer', 'components', 'IntakePanel.tsx'), 'utf8');
    const start = panel.indexOf('const toggleIntakeAcceptance = async (item: IntakeItem, accepted: boolean) => {');
    const end = panel.indexOf('  };', start);
    assert.ok(start >= 0 && end > start, 'should locate toggleIntakeAcceptance handler');
    const toggleBody = panel.slice(start, end);

    assert.match(panel, /const acceptingKeyRef = useRef<string \| null>\(null\);/);
    assert.match(toggleBody, /const action = accepted \? 'unaccept' : 'accept';/);
    assert.match(toggleBody, /if \(acceptingKeyRef\.current\) return;/);
    assert.match(toggleBody, /if \(accepted\) await onUnacceptIntake\(type, item\.id\);/);
    assert.match(toggleBody, /else await onAcceptIntake\(type, item\.id\);/);
    assert.match(toggleBody, /acceptingKeyRef\.current = null;/);
    assert.match(panel, /function intakeAcceptanceKey\(type: IntakeType, id: number, action: 'accept' \| 'unaccept'\)/);
    assert.match(panel, /return `\$\{type\}:\$\{id\}:\$\{action\}`;/);
  });

  it('keeps P005 pages behind the injected client and desktop boundaries', () => {
    const rendererRoot = join(process.cwd(), 'src', 'renderer');
    const projectsPage = readFileSync(join(rendererRoot, 'pages', 'ProjectsPage.tsx'), 'utf8');
    const workspacePage = readFileSync(join(rendererRoot, 'pages', 'WorkspacePage.tsx'), 'utf8');
    const controller = readFileSync(join(rendererRoot, 'hooks', 'useWorkspaceController.ts'), 'utf8');
    const acceptance = readFileSync(join(rendererRoot, 'components', 'workspace', 'AcceptanceView.tsx'), 'utf8');

    for (const sourceText of [projectsPage, workspacePage, controller, acceptance]) {
      assert.doesNotMatch(sourceText, /window\.autoplan/);
    }
    assert.match(projectsPage, /const client = useAutoplanClient\(\)/);
    assert.match(projectsPage, /const desktopBridge = useDesktopBridge\(\)/);
    assert.match(workspacePage, /client\.runTaskBatches\(\{ projectId, planId: plan\.id, batches, manual: true \}\)/);
    assert.match(controller, /desktopBridge\.openWorkspaceFile\(\{/);
    assert.match(controller, /client\.retryIntakePlanGeneration\(\{ projectId, type, id, \.\.\.options \}\)/);
  });
});
