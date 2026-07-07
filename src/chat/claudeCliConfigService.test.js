'use strict';

const { describe, it } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { AppDatabase } = require('../database');
const {
  createClaudeCliConfig,
  deleteClaudeCliConfig,
  getClaudeCliConfig,
  listClaudeCliConfigs,
  resolveDefaultClaudeCliConfig,
  setDefaultClaudeCliConfig,
  updateClaudeCliConfig,
} = require('./claudeCliConfigService');

describe('claude_cli_configs global table', () => {
  it('creates the table with required columns on an existing database', async () => {
    const fixture = await createDatabaseFixture();
    try {
      const columns = fixture.db
        .all('PRAGMA table_info(claude_cli_configs)')
        .map((column) => column.name);
      for (const required of [
        'id',
        'name',
        'base_url',
        'auth_token',
        'model',
        'is_default',
        'created_at',
        'updated_at',
      ]) {
        assert.ok(columns.includes(required), `claude_cli_configs 应包含 ${required} 列`);
      }
    } finally {
      fixture.cleanup();
    }
  });

  it('keeps all stored configs global (project_id IS NULL)', async () => {
    const fixture = await createDatabaseFixture();
    try {
      createClaudeCliConfig(fixture.db, { name: 'A', authToken: 'tok-a-1111' });
      createClaudeCliConfig(fixture.db, { name: 'B', authToken: 'tok-b-2222' });
      const rows = fixture.db.all('SELECT project_id FROM claude_cli_configs');
      assert.ok(rows.every((row) => row.project_id === null), 'Claude CLI 配置应为全局配置');
    } finally {
      fixture.cleanup();
    }
  });
});

describe('claudeCliConfigService CRUD', () => {
  it('creates a config with trimmed fields, stores plaintext auth_token, and returns sanitized output', async () => {
    const fixture = await createDatabaseFixture();
    try {
      const created = createClaudeCliConfig(fixture.db, {
        name: '  Primary Claude  ',
        baseUrl: '  https://api.anthropic.com  ',
        authToken: 'sk-ant-primary-1234',
        model: '  claude-sonnet-4-6  ',
      });
      assert.equal(created.name, 'Primary Claude');
      assert.equal(created.baseUrl, 'https://api.anthropic.com');
      assert.equal(created.model, 'claude-sonnet-4-6');
      assert.equal(created.hasAuthToken, true);
      assert.equal(created.maskedKey, '····1234');
      assert.equal(created.isDefault, false);
      assert.equal(Object.prototype.hasOwnProperty.call(created, 'authToken'), false);
      assert.equal(Object.prototype.hasOwnProperty.call(created, 'auth_token'), false);

      const raw = fixture.db.get(
        'SELECT auth_token, project_id FROM claude_cli_configs WHERE id = ?',
        [created.id],
      );
      assert.equal(raw.auth_token, 'sk-ant-primary-1234', '明文 auth_token 应入库保存');
      assert.equal(raw.project_id, null, '应保存为全局配置');
    } finally {
      fixture.cleanup();
    }
  });

  it('rejects empty names on create', async () => {
    const fixture = await createDatabaseFixture();
    try {
      assert.throws(
        () => createClaudeCliConfig(fixture.db, { name: '   ' }),
        /配置名称不能为空/,
      );
    } finally {
      fixture.cleanup();
    }
  });

  it('allows duplicate names', async () => {
    const fixture = await createDatabaseFixture();
    try {
      const a = createClaudeCliConfig(fixture.db, { name: 'Same Name', authToken: 'tok-a-1234' });
      const b = createClaudeCliConfig(fixture.db, { name: 'Same Name', authToken: 'tok-b-5678' });
      assert.equal(a.name, 'Same Name');
      assert.equal(b.name, 'Same Name');
      assert.notEqual(a.id, b.id);
    } finally {
      fixture.cleanup();
    }
  });

  it('lists and gets configs with sanitized auth_token', async () => {
    const fixture = await createDatabaseFixture();
    try {
      const created = createClaudeCliConfig(fixture.db, {
        name: 'Listed',
        authToken: 'sk-listed-abcd',
      });
      const list = listClaudeCliConfigs(fixture.db);
      assert.equal(list.length, 1);
      assert.equal(list[0].id, created.id);
      assert.equal(list[0].maskedKey, '····abcd');
      assert.equal(list[0].hasAuthToken, true);
      assert.equal(Object.prototype.hasOwnProperty.call(list[0], 'authToken'), false);

      const one = getClaudeCliConfig(fixture.db, created.id);
      assert.equal(one.hasAuthToken, true);
      assert.equal(one.maskedKey, '····abcd');
      assert.equal(getClaudeCliConfig(fixture.db, 999999), null);
    } finally {
      fixture.cleanup();
    }
  });

  it('preserves auth_token when update omits it and rejects empty name', async () => {
    const fixture = await createDatabaseFixture();
    try {
      const created = createClaudeCliConfig(fixture.db, {
        name: 'Editable',
        baseUrl: 'https://a.example',
        authToken: 'sk-keep-1234',
        model: 'claude-sonnet-4-6',
      });

      const updated = updateClaudeCliConfig(fixture.db, created.id, {
        name: 'Editable Renamed',
        baseUrl: 'https://b.example',
        model: 'claude-opus-4-8',
      });
      assert.equal(updated.name, 'Editable Renamed');
      assert.equal(updated.baseUrl, 'https://b.example');
      assert.equal(updated.model, 'claude-opus-4-8');
      assert.equal(updated.maskedKey, '····1234', '未提供 auth_token 时应保留旧值');
      assert.equal(
        fixture.db.get('SELECT auth_token FROM claude_cli_configs WHERE id = ?', [created.id])
          .auth_token,
        'sk-keep-1234',
      );

      const updatedToken = updateClaudeCliConfig(fixture.db, created.id, { authToken: 'sk-new-9876' });
      assert.equal(updatedToken.maskedKey, '····9876');
      assert.equal(
        fixture.db.get('SELECT auth_token FROM claude_cli_configs WHERE id = ?', [created.id])
          .auth_token,
        'sk-new-9876',
      );

      assert.throws(
        () => updateClaudeCliConfig(fixture.db, created.id, { name: '   ' }),
        /配置名称不能为空/,
      );
      assert.throws(
        () => updateClaudeCliConfig(fixture.db, 999999, { name: 'x' }),
        /Claude CLI 配置不存在/,
      );
    } finally {
      fixture.cleanup();
    }
  });

  it('deletes a config and throws for unknown ids', async () => {
    const fixture = await createDatabaseFixture();
    try {
      const created = createClaudeCliConfig(fixture.db, { name: 'Deletable', authToken: 'tok-del-0000' });
      const result = deleteClaudeCliConfig(fixture.db, created.id);
      assert.deepEqual(result, { deleted: true });
      assert.equal(getClaudeCliConfig(fixture.db, created.id), null);
      assert.throws(() => deleteClaudeCliConfig(fixture.db, 999999), /Claude CLI 配置不存在/);
    } finally {
      fixture.cleanup();
    }
  });
});

describe('claudeCliConfigService default handling', () => {
  it('keeps at most one default when setting default', async () => {
    const fixture = await createDatabaseFixture();
    try {
      const a = createClaudeCliConfig(fixture.db, { name: 'A', authToken: 'tok-a-1111' });
      const b = createClaudeCliConfig(fixture.db, { name: 'B', authToken: 'tok-b-2222' });
      const c = createClaudeCliConfig(fixture.db, { name: 'C', authToken: 'tok-c-3333' });

      setDefaultClaudeCliConfig(fixture.db, a.id);
      assert.equal(getClaudeCliConfig(fixture.db, a.id).isDefault, true);
      setDefaultClaudeCliConfig(fixture.db, c.id);
      assert.equal(getClaudeCliConfig(fixture.db, a.id).isDefault, false);
      assert.equal(getClaudeCliConfig(fixture.db, b.id).isDefault, false);
      assert.equal(getClaudeCliConfig(fixture.db, c.id).isDefault, true);

      const defaults = fixture.db.all(
        'SELECT id FROM claude_cli_configs WHERE is_default = 1',
      );
      assert.equal(defaults.length, 1, '同一时刻全局至多一条 is_default=1');
      assert.equal(defaults[0].id, c.id);

      assert.throws(() => setDefaultClaudeCliConfig(fixture.db, 999999), /Claude CLI 配置不存在/);
    } finally {
      fixture.cleanup();
    }
  });

  it('promotes the first remaining config to default after deleting the default', async () => {
    const fixture = await createDatabaseFixture();
    try {
      const a = createClaudeCliConfig(fixture.db, { name: 'A', authToken: 'tok-a-1111' });
      const b = createClaudeCliConfig(fixture.db, { name: 'B', authToken: 'tok-b-2222' });
      setDefaultClaudeCliConfig(fixture.db, a.id);

      deleteClaudeCliConfig(fixture.db, a.id);
      assert.equal(getClaudeCliConfig(fixture.db, a.id), null);
      const defaults = fixture.db.all('SELECT id FROM claude_cli_configs WHERE is_default = 1');
      assert.equal(defaults.length, 1);
      assert.equal(defaults[0].id, b.id, '删除默认后应回退提升首条剩余配置为新默认');

      // 删除非默认配置不应影响当前默认
      const cc = createClaudeCliConfig(fixture.db, { name: 'C', authToken: 'tok-c-3333' });
      deleteClaudeCliConfig(fixture.db, cc.id);
      assert.equal(getClaudeCliConfig(fixture.db, b.id).isDefault, true);
    } finally {
      fixture.cleanup();
    }
  });

  it('leaves no default when the last config is deleted', async () => {
    const fixture = await createDatabaseFixture();
    try {
      const a = createClaudeCliConfig(fixture.db, { name: 'A', authToken: 'tok-a-1111' });
      setDefaultClaudeCliConfig(fixture.db, a.id);
      deleteClaudeCliConfig(fixture.db, a.id);
      assert.equal(fixture.db.all('SELECT id FROM claude_cli_configs').length, 0);
      assert.equal(
        fixture.db.all('SELECT id FROM claude_cli_configs WHERE is_default = 1').length,
        0,
      );
    } finally {
      fixture.cleanup();
    }
  });

  it('resolves the default config with raw auth_token, falling back to first then null', async () => {
    const fixture = await createDatabaseFixture();
    try {
      assert.equal(resolveDefaultClaudeCliConfig(fixture.db), null, '无配置时返回 null');

      const a = createClaudeCliConfig(fixture.db, {
        name: 'A',
        authToken: 'tok-a-1111',
        baseUrl: 'https://a.example',
        model: 'model-a',
      });
      // 无显式默认时回退首条
      const fallback = resolveDefaultClaudeCliConfig(fixture.db);
      assert.equal(fallback.id, a.id);
      assert.equal(fallback.authToken, 'tok-a-1111', 'resolveDefault 返回对象含原始 auth_token（不脱敏）');
      assert.equal(fallback.isDefault, false);

      const b = createClaudeCliConfig(fixture.db, {
        name: 'B',
        authToken: 'tok-b-2222',
        baseUrl: 'https://b.example',
        model: 'model-b',
      });
      setDefaultClaudeCliConfig(fixture.db, b.id);
      const resolved = resolveDefaultClaudeCliConfig(fixture.db);
      assert.equal(resolved.id, b.id, '优先返回显式默认配置');
      assert.equal(resolved.authToken, 'tok-b-2222');
      assert.equal(resolved.baseUrl, 'https://b.example');
      assert.equal(resolved.model, 'model-b');
      assert.equal(resolved.isDefault, true);
    } finally {
      fixture.cleanup();
    }
  });
});

async function createDatabaseFixture() {
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-claude-cli-config-test-'));
  const dbPath = path.join(tempRoot, 'data', 'autoplan.sqlite');
  const db = new AppDatabase(dbPath);
  await db.init();
  return {
    db,
    cleanup() {
      try {
        db.db?.close?.();
      } catch {
        // sql.js close is best-effort in tests.
      }
      fs.rmSync(tempRoot, { recursive: true, force: true });
    },
  };
}
