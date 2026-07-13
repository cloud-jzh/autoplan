const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { join } = require('node:path');
const { describe, it } = require('node:test');

function source(...parts) {
  return readFileSync(join(process.cwd(), ...parts), 'utf8').replace(/\r\n/g, '\n');
}

describe('Workspace settings transport boundary', () => {
  it('does not call business APIs through the native-only preload bridge', () => {
    const settings = source('src', 'renderer', 'components', 'workspace', 'WorkspaceSettingsView.tsx');
    const preload = source('src', 'preload.js');

    assert.doesNotMatch(settings, /window\.autoplan\./);
    assert.doesNotMatch(preload, /onClaudeCliConfigChanged\s*[:,]/);
    assert.match(settings, /const client = useAutoplanClient\(\);/);
    assert.match(settings, /client\.onClaudeCliConfigChanged\(/);
  });

  it('keeps business and native settings operations on their injected owners', () => {
    const settings = source('src', 'renderer', 'components', 'workspace', 'WorkspaceSettingsView.tsx');

    for (const operation of [
      'client.fileAccess.get()',
      'client.fileAccess.save({',
      'client.chatGetConfig()',
      'client.aiConfigList()',
      'client.claudeCliConfigList()',
    ]) {
      assert.ok(settings.includes(operation), `missing business client operation: ${operation}`);
    }
    for (const operation of [
      'desktopBridge.pickDirectory()',
      'desktopBridge.setAutoUpdateCheck(',
      'desktopBridge.openExternal(',
    ]) {
      assert.ok(settings.includes(operation), `missing desktop bridge operation: ${operation}`);
    }
  });
});
