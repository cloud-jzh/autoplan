'use strict';

/**
 * Claude CLI 配置服务（需求 #93）：全局多条命名 Claude CLI 连接配置的 CRUD、
 * 默认配置切换与回退解析、auth_token 脱敏。
 *
 * 整体复用既有 ai_configs 多配置范式（aiConfigService）：
 * - 全局表（project_id IS NULL）
 * - CRUD 接受明文 auth_token 入库，list/get 输出脱敏（hasAuthToken + maskedKey 末 4 位）
 * - 默认配置：同一时刻全局至多一条 is_default=1；resolveDefault 在无默认时回退首条
 *
 * 后端在 provider=claude 时按「所选配置 ID → 默认配置 → 既有内联字段」优先级解析连接，
 * 其中「默认配置」由 resolveDefaultClaudeCliConfig 提供（返回对象含原始 auth_token，不脱敏）。
 */

const { nowIso } = require('../database');
// 复用 aiConfigService 的脱敏实现，保证 maskedKey 约定一致（末 4 位）。
const { maskApiKey } = require('./aiConfigService');

const GLOBAL_WHERE = 'project_id IS NULL';

/**
 * 列出全局 Claude CLI 配置（脱敏）。
 */
function listClaudeCliConfigs(db) {
  const rows = db.all(`SELECT * FROM claude_cli_configs WHERE ${GLOBAL_WHERE} ORDER BY id ASC`);
  return rows.map(sanitizeClaudeCliConfig);
}

/**
 * 获取单条全局 Claude CLI 配置（脱敏）。
 */
function getClaudeCliConfig(db, id) {
  const row = db.get(`SELECT * FROM claude_cli_configs WHERE id = ? AND ${GLOBAL_WHERE}`, [id]);
  return row ? sanitizeClaudeCliConfig(row) : null;
}

/**
 * 创建 Claude CLI 配置。name 必填且去空白；接受明文 auth_token 入库。
 */
function createClaudeCliConfig(db, input = {}) {
  const config = normalizeCreateInput(input);

  const now = nowIso();
  const id = db.insert(
    `INSERT INTO claude_cli_configs (project_id, name, base_url, auth_token, model, is_default, created_at, updated_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
    [null, config.name, config.baseUrl, config.authToken, config.model, 0, now, now],
  );
  return getClaudeCliConfig(db, id);
}

/**
 * 更新 Claude CLI 配置。auth_token 留空/省略表示不改动（保留旧值）。
 */
function updateClaudeCliConfig(db, id, fields = {}) {
  const existing = db.get(`SELECT * FROM claude_cli_configs WHERE id = ? AND ${GLOBAL_WHERE}`, [id]);
  if (!existing) throw new Error('Claude CLI 配置不存在');

  const now = nowIso();
  db.run(
    `UPDATE claude_cli_configs
     SET name = ?, base_url = ?, auth_token = ?, model = ?, updated_at = ?
     WHERE id = ?`,
    [
      resolveUpdateName(fields.name, existing.name),
      fields.baseUrl !== undefined ? normalizeText(fields.baseUrl) : existing.base_url,
      fields.authToken !== undefined ? normalizeText(fields.authToken) : existing.auth_token,
      fields.model !== undefined ? normalizeText(fields.model) : existing.model,
      now,
      id,
    ],
  );
  return getClaudeCliConfig(db, id);
}

/**
 * 删除 Claude CLI 配置。若删除的是当前默认配置，则回退提升首条剩余配置为新默认
 * （若无剩余配置则保持无默认）。
 */
function deleteClaudeCliConfig(db, id) {
  const existing = db.get(`SELECT * FROM claude_cli_configs WHERE id = ? AND ${GLOBAL_WHERE}`, [id]);
  if (!existing) throw new Error('Claude CLI 配置不存在');

  const wasDefault = Number(existing.is_default) === 1;
  db.run(`DELETE FROM claude_cli_configs WHERE id = ? AND ${GLOBAL_WHERE}`, [id]);

  if (wasDefault) {
    const first = db.get(
      `SELECT id FROM claude_cli_configs WHERE ${GLOBAL_WHERE} ORDER BY id ASC LIMIT 1`,
    );
    if (first) {
      db.run('UPDATE claude_cli_configs SET is_default = 1, updated_at = ? WHERE id = ?', [
        nowIso(),
        first.id,
      ]);
    }
  }
  return { deleted: true };
}

/**
 * 将指定配置设为默认。先把全局其余配置置 0，再把目标置 1，保证同一时刻至多一条默认。
 */
function setDefaultClaudeCliConfig(db, id) {
  const existing = db.get(`SELECT * FROM claude_cli_configs WHERE id = ? AND ${GLOBAL_WHERE}`, [id]);
  if (!existing) throw new Error('Claude CLI 配置不存在');

  const now = nowIso();
  db.runBatch([
    {
      sql: `UPDATE claude_cli_configs SET is_default = 0, updated_at = ? WHERE ${GLOBAL_WHERE}`,
      params: [now],
    },
    {
      sql: `UPDATE claude_cli_configs SET is_default = 1, updated_at = ? WHERE id = ? AND ${GLOBAL_WHERE}`,
      params: [now, id],
    },
  ]);
  return getClaudeCliConfig(db, id);
}

/**
 * 解析当前默认 Claude CLI 配置，供后端连接解析复用。
 * 优先级：显式 is_default=1 配置 → 全局首条配置 → null。
 * 返回对象含原始 auth_token（不脱敏）。
 */
function resolveDefaultClaudeCliConfig(db) {
  const row = db.get(
    `SELECT * FROM claude_cli_configs WHERE ${GLOBAL_WHERE} AND is_default = 1 ORDER BY id ASC LIMIT 1`,
  );
  if (row) return rowToConfig(row);

  const first = db.get(`SELECT * FROM claude_cli_configs WHERE ${GLOBAL_WHERE} ORDER BY id ASC LIMIT 1`);
  return first ? rowToConfig(first) : null;
}

/* ------------------------------------------------------------------ 工具函数 ------------------------------------------------------------------ */

function normalizeCreateInput(input = {}) {
  const name = normalizeText(input.name);
  if (!name) throw new Error('配置名称不能为空');

  return {
    name,
    baseUrl: normalizeText(input.baseUrl),
    authToken: normalizeText(input.authToken),
    model: normalizeText(input.model),
  };
}

function resolveUpdateName(value, fallback) {
  if (value === undefined) return fallback;
  const trimmed = normalizeText(value);
  if (!trimmed) throw new Error('配置名称不能为空');
  return trimmed;
}

function normalizeText(value) {
  if (value === undefined || value === null) return '';
  return String(value).trim();
}

/**
 * 将数据库行转换为配置对象（含原始 auth_token，供后端解析使用）。
 */
function rowToConfig(row) {
  return {
    id: row.id,
    projectId: row.project_id,
    name: row.name,
    baseUrl: row.base_url,
    authToken: row.auth_token || '',
    model: row.model || '',
    isDefault: Number(row.is_default) === 1,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  };
}

/**
 * 脱敏 Claude CLI 配置行：隐藏 auth_token，输出 hasAuthToken + maskedKey（末 4 位）。
 * 与 aiConfigService 的 sanitize 约定一致。
 */
function sanitizeClaudeCliConfig(row) {
  if (!row) return null;
  const authToken = row.auth_token || '';
  return {
    id: row.id,
    projectId: row.project_id,
    name: row.name,
    baseUrl: row.base_url,
    hasAuthToken: Boolean(authToken),
    maskedKey: maskAuthToken(authToken),
    model: row.model || '',
    isDefault: Number(row.is_default) === 1,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  };
}

/**
 * 脱敏 auth_token：保留末 4 位，其余用 ···· 替代（与 aiConfigService.maskApiKey 一致）。
 */
function maskAuthToken(value) {
  return maskApiKey(value);
}

module.exports = {
  listClaudeCliConfigs,
  getClaudeCliConfig,
  createClaudeCliConfig,
  updateClaudeCliConfig,
  deleteClaudeCliConfig,
  setDefaultClaudeCliConfig,
  resolveDefaultClaudeCliConfig,
  sanitizeClaudeCliConfig,
  maskAuthToken,
};
