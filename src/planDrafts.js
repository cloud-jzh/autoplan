const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { nowIso } = require('./database');

function createPlanDraft(db, { projectId, sourceType, sourceId, body, attachments = [] }) {
  const markdown = buildPlanMarkdown({ sourceType, sourceId, body, attachments });
  const now = nowIso();
  return db.insert(
    `INSERT INTO plan_drafts (project_id, source_type, source_id, markdown, status, created_at, updated_at)
     VALUES (?, ?, ?, ?, ?, ?, ?)`,
    [projectId, sourceType, sourceId, markdown, 'draft', now, now],
  );
}

function updatePlanDraft(db, id, markdown) {
  db.run('UPDATE plan_drafts SET markdown = ?, updated_at = ? WHERE id = ?', [
    markdown || '',
    nowIso(),
    id,
  ]);
}

function acceptPlanDraft(db, loop, workspace, draftId) {
  const draft = db.get('SELECT * FROM plan_drafts WHERE id = ?', [draftId]);
  if (!draft) throw new Error('计划草稿不存在');
  if (!workspace) throw new Error('请先在任务模块设置工作区路径');

  const planDir = path.join(workspace, 'docs', 'plan');
  fs.mkdirSync(planDir, { recursive: true });

  const planFile = path.join(planDir, `ui_plan_${timestampForPath()}_${draft.id}.md`);
  fs.writeFileSync(planFile, draft.markdown, 'utf8');
  const issueHash = `draft-${draft.id}-${hashText(draft.markdown).slice(0, 16)}`;
  const now = nowIso();
  const planId = db.insert(
    `INSERT INTO plans
     (project_id, issue_hash, file_path, hash, status, total_tasks, completed_tasks, validation_passed, created_at, updated_at)
     VALUES (?, ?, ?, ?, ?, 0, 0, 0, ?, ?)`,
    [draft.project_id, issueHash, normalizeRelative(workspace, planFile), hashFile(planFile), 'pending', now, now],
  );

  loop.syncPlanTasks(planId, planFile);
  db.run('UPDATE plan_drafts SET status = ?, linked_plan_id = ?, updated_at = ? WHERE id = ?', [
    'accepted',
    planId,
    nowIso(),
    draftId,
  ]);
  return planId;
}

function buildPlanMarkdown({ sourceType, sourceId, body, attachments }) {
  const sourceName = sourceType === 'feedback' ? '反馈' : '需求';
  const cleanBody = String(body || '').trim();
  const summary = firstMeaningfulLine(cleanBody) || `${sourceName} #${sourceId}`;
  const lines = cleanBody.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);

  return [
    `# ${sourceName}开发计划：${summary.slice(0, 48)}`,
    '',
    '## 来源',
    `- 类型：${sourceName}`,
    `- 编号：${sourceId}`,
    '',
    '## 原始内容',
    cleanBody || '仅包含附件，正文为空。',
    '',
    '## 附件链接',
    ...attachmentLines(attachments),
    '',
    '## 目标拆解',
    ...goalLines(lines),
    '',
    '## 任务计划',
    '- [ ] P001: 明确范围与影响面 <!-- scope: unknown -->',
    '  - 验收：确认涉及的数据、界面、持久化、任务流转和兼容性影响。',
    '- [ ] P002: 完成核心实现 <!-- scope: unknown -->',
    '  - 验收：需求或反馈描述的主流程可在应用内完成，附件链接保持可访问。',
    '- [ ] P003: 完成交互与异常状态 <!-- scope: unknown -->',
    '  - 验收：空内容、重复附件、缺少工作区、执行失败等状态有明确反馈。',
    '- [ ] P004: 补充验证 <!-- scope: unknown -->',
    '  - 验收：新增或更新 smoke 测试，覆盖数据写入、附件、计划同步和验收状态。',
    '- [ ] P005: 更新文档或进度记录 <!-- scope: unknown -->',
    '  - 验收：相关计划、进度或使用说明能反映本次变更。',
    '',
    '## 总体验收标准',
    '- 计划中的 checkbox 任务全部完成。',
    '- 相关附件仅通过 Markdown 链接引用，不把文件正文直接嵌入计划。',
    '- 验收命令通过，失败时记录原因并生成修复任务。',
    '',
    '## 进度区',
    '- 当前状态：draft',
    '- 下一步：确认或调整计划后加入任务系统。',
    '',
  ].join('\n');
}

function attachmentLines(attachments) {
  if (!attachments.length) return ['- 无附件。'];
  return attachments.map((attachment) => {
    const url = pathToFileURL(attachment.stored_path).toString();
    const name = attachment.original_name || path.basename(attachment.stored_path);
    if (String(attachment.mime_type || '').startsWith('image/')) {
      return `- 图片：![${escapeMarkdown(name)}](${url})`;
    }
    return `- 文件：[${escapeMarkdown(name)}](${url})`;
  });
}

function goalLines(lines) {
  if (!lines.length) return ['- 从附件内容和后续反馈中确认具体目标。'];
  return lines.slice(0, 8).map((line) => `- ${line}`);
}

function firstMeaningfulLine(text) {
  return text.split(/\r?\n/).map((line) => line.trim()).find(Boolean);
}

function escapeMarkdown(value) {
  return String(value).replace(/[[\]()]/g, '\\$&');
}

function normalizeRelative(root, fullPath) {
  return path.relative(root, fullPath).replaceAll(path.sep, '/');
}

function hashFile(filePath) {
  return crypto.createHash('sha256').update(fs.readFileSync(filePath)).digest('hex');
}

function hashText(text) {
  return crypto.createHash('sha256').update(text).digest('hex');
}

function timestampForPath() {
  const now = new Date();
  const pad = (n) => String(n).padStart(2, '0');
  return `${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}-${pad(
    now.getHours(),
  )}${pad(now.getMinutes())}${pad(now.getSeconds())}`;
}

module.exports = { acceptPlanDraft, buildPlanMarkdown, createPlanDraft, updatePlanDraft };
