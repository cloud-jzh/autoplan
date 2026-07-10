const INTAKE_MENTION_RE = /@(需求|反馈|requirement|feedback)\s*[#＃]\s*(\d+)/gi;
const INTAKE_CONTEXT_SUMMARY_MAX_LENGTH = 500;
const INTAKE_CONTEXT_TOTAL_MAX_LENGTH = 4000;

const INTAKE_TYPES = {
  requirement: {
    label: '需求',
    table: 'requirements',
  },
  feedback: {
    label: '反馈',
    table: 'feedback',
  },
};

function parseIntakeMentions(text) {
  const input = String(text || '');
  if (!input) return [];

  const mentions = [];
  INTAKE_MENTION_RE.lastIndex = 0;
  let match = INTAKE_MENTION_RE.exec(input);
  while (match) {
    const type = normalizeIntakeMentionType(match[1]);
    const id = normalizePositiveInteger(match[2]);
    const start = match.index;
    const rawText = match[0];
    if (type && id !== null && !isBlockedMentionBoundary(input[start - 1])) {
      mentions.push({ type, id, rawText, start, end: start + rawText.length });
    }
    match = INTAKE_MENTION_RE.exec(input);
  }
  return mentions;
}

function buildIntakeMentionPromptContext(service, projectId, intake = {}) {
  const mentions = parseIntakeMentions(intake.body);
  if (!mentions.length) return '';

  const seen = new Set();
  const lines = ['@ 引用上下文（仅注入当前项目内可读取的需求/反馈摘要）：'];
  for (const mention of mentions) {
    const key = mentionKey(mention);
    if (seen.has(key)) continue;
    seen.add(key);

    const label = formatMentionLabel(mention.type, mention.id);
    if (isSelfMention(intake, mention)) {
      lines.push(`- ${label}：引用自身，已跳过正文注入以避免重复。`);
      continue;
    }

    const target = readMentionTarget(service, projectId, mention);
    if (!target) {
      lines.push(`- ${label}：未找到当前项目内对应记录，按普通文本引用处理。`);
      continue;
    }

    lines.push(`- ${label}：${targetTitle(mention.type, target)}`);
    lines.push(`  - 状态：${normalizeText(target.status) || 'unknown'}`);
    lines.push(`  - 更新时间：${normalizeText(target.updated_at) || 'unknown'}`);
    lines.push(`  - 摘要：${summarizeBody(target.body)}`);
  }

  return limitContextLength(lines.join('\n'));
}

function readMentionTarget(service, projectId, mention) {
  const config = INTAKE_TYPES[mention.type];
  if (!config || !service?.db) return null;
  const sql = `SELECT id, project_id, title, body, status, updated_at FROM ${config.table} WHERE project_id = ? AND id = ?`;
  const params = [projectId, mention.id];
  if (typeof service.db.get === 'function') {
    return service.db.get(sql, params) || null;
  }
  if (typeof service.db.all === 'function') {
    const rows = service.db.all(sql, params);
    return Array.isArray(rows) ? rows[0] || null : null;
  }
  return null;
}

function isSelfMention(intake, mention) {
  const intakeType = intake.__type === 'feedback' ? 'feedback' : 'requirement';
  return intakeType === mention.type && Number(intake.id) === Number(mention.id);
}

function targetTitle(type, target) {
  const config = INTAKE_TYPES[type] || INTAKE_TYPES.requirement;
  const title = normalizeText(target.title);
  return `${config.label} #${target.id}${title ? ` · ${title}` : ''}`;
}

function summarizeBody(value) {
  const text = normalizeText(value);
  if (!text) return '（正文为空）';
  if (text.length <= INTAKE_CONTEXT_SUMMARY_MAX_LENGTH) return text;
  return `${text.slice(0, INTAKE_CONTEXT_SUMMARY_MAX_LENGTH - 1)}…`;
}

function limitContextLength(text) {
  if (text.length <= INTAKE_CONTEXT_TOTAL_MAX_LENGTH) return text;
  return `${text.slice(0, INTAKE_CONTEXT_TOTAL_MAX_LENGTH - 1)}…`;
}

function normalizeIntakeMentionType(value) {
  const normalized = String(value || '').trim().toLowerCase();
  if (normalized === '需求' || normalized === 'requirement') return 'requirement';
  if (normalized === '反馈' || normalized === 'feedback') return 'feedback';
  return null;
}

function normalizePositiveInteger(value) {
  const numberValue = Number(value);
  return Number.isInteger(numberValue) && numberValue > 0 ? numberValue : null;
}

function normalizeText(value) {
  return String(value || '').trim().replace(/\s+/g, ' ');
}

function formatMentionLabel(type, id) {
  const config = INTAKE_TYPES[type] || INTAKE_TYPES.requirement;
  return `@${config.label}#${id}`;
}

function mentionKey(mention) {
  return `${mention.type}:${mention.id}`;
}

function isBlockedMentionBoundary(value) {
  return Boolean(value && /[A-Za-z0-9_.-]/.test(value));
}

module.exports = {
  buildIntakeMentionPromptContext,
  parseIntakeMentions,
};