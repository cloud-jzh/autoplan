/**
 * 移植自 run_advanced_plan_with_codex.dart 的 CodexActivityPrinter。
 * 从 codex 的 stdout 原始流中提取**人类可读的活动时间线**：
 * - 跳过 user 回显、版本头、session 信息等噪音
 * - 识别 codex/exec/thinking 等段落
 * - 抽取含关键词的活动行（思考/执行/完成/验证/报错等）
 * - 节流输出 Tail 行
 *
 * offer(rawChunk) 喂入原始流（可能含多行），getLines() 返回当前活动行列表。
 */

const ANSI_RE = /\x1B\[[0-?]*[ -/]*[@-~]/g;
const SPINNER_RE = /^[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]\s*/;
const LEAD_RE = /^[>•*+\-\s]+/;

const ACTIVITY_KEYWORDS = [
  'thinking', 'plan', 'running', 'exec', 'command', 'test', 'analyze',
  'flutter', 'dart', 'complete', 'completed', 'summary', 'verification',
  'blocked', 'error', 'failed', 'warning', 'reconnect',
  '思考', '计划', '执行', '运行', '命令', '测试', '分析', '检查',
  '验证', '完成', '阻塞', '摘要', '推进', '报错', '失败', '重连',
];

const NOISE_PREFIXES = [
  'openai codex', 'workdir:', 'model:', 'provider:', 'approval:', 'sandbox:',
  'reasoning effort:', 'reasoning summaries:', 'session id:', 'usage:',
  'options:', 'warning:', 'succeeded in', 'exitcode',
];

const MODEL_START_RE = /^(codex|exec|assistant|\*\*|Planning |我|正在|我会|先|接下来|完成|修改|运行)/;

class CodexActivityPrinter {
  constructor(maxLines = 200) {
    this.maxLines = maxLines;
    this.lines = [];
    this.recent = new Set();
    this.skippingUserEcho = false;
    this.currentRole = 'info';
    this.pendingLine = '';
  }

  /** 喂入一段原始文本（可含多行） */
  offer(rawChunk) {
    const text = String(rawChunk || '').replace(ANSI_RE, '').replace(/\r/g, '');
    this.pendingLine += text;
    while (true) {
      const idx = this.pendingLine.indexOf('\n');
      if (idx < 0) break;
      const line = this.pendingLine.slice(0, idx);
      this.pendingLine = this.pendingLine.slice(idx + 1);
      this.processLine(line);
    }
  }

  flush() {
    const line = this.pendingLine;
    this.pendingLine = '';
    if (line.trim()) this.processLine(line);
  }

  processLine(rawLine) {
    const trimmed = rawLine.trim();
    // 段落角色标记
    if (['user', 'codex', 'exec', 'assistant', 'thinking', 'system', 'error'].includes(trimmed)) {
      if (trimmed === 'user') {
        this.skippingUserEcho = true;
        return;
      }
      this.skippingUserEcho = false;
      this.currentRole = trimmed === 'assistant' ? 'codex' : trimmed;
      return;
    }
    // 跳过 user 回显
    if (this.skippingUserEcho) {
      if (MODEL_START_RE.test(trimmed)) {
        this.skippingUserEcho = false;
      } else {
        return;
      }
    }
    const clean = this.cleanLine(rawLine);
    if (!clean) return;
    if (this.isActivity(clean)) {
      this.push(this.currentRole, clean);
    }
  }

  push(role, line) {
    const compact = line.length > 200 ? `${line.slice(0, 197)}...` : line;
    const key = `${role}:${compact}`;
    if (this.recent.has(key)) return;
    this.recent.add(key);
    if (this.lines.length >= this.maxLines) {
      this.lines.shift();
      // 限制 recent 集合大小
      if (this.recent.size > this.maxLines * 2) {
        this.recent = new Set(this.lines.map((l) => `${l.role}:${l.text}`));
      }
    }
    this.lines.push({ role, text: compact, at: new Date().toISOString() });
  }

  cleanLine(rawLine) {
    let line = String(rawLine || '').trim();
    if (!line) return null;
    line = line.replace(ANSI_RE, '').replace(LEAD_RE, '').replace(SPINNER_RE, '').trim();
    if (!line) return null;
    if (this.looksLikeNoise(line) || this.looksLikeFileContent(line)) return null;
    return line;
  }

  isActivity(line) {
    const lower = line.toLowerCase();
    return ACTIVITY_KEYWORDS.some((kw) => lower.includes(kw));
  }

  looksLikeNoise(line) {
    const lower = line.toLowerCase();
    if (NOISE_PREFIXES.some((p) => lower.startsWith(p))) return true;
    if (line === 'codex' || line === 'exec' || line === 'user') return true;
    if (/^\d{1,2}:\d{2}(:\d{2})?$/.test(line)) return true;
    if (/^[=\-_]{3,}$/.test(line)) return true;
    return false;
  }

  looksLikeFileContent(line) {
    const lower = line.toLowerCase();
    if (
      line.startsWith('::') ||
      line.startsWith('diff --git') ||
      line.startsWith('@@') ||
      line.startsWith('--- ') ||
      line.startsWith('+++ ') ||
      (line.startsWith('+') && !line.startsWith('+ ')) ||
      (line.startsWith('-') && !line.startsWith('- '))
    ) {
      return true;
    }
    if (/^(import |class |final |const |return |if |for |void )/i.test(line)) return true;
    if (line === '}' || line === '{') return true;
    if (/^(lib|test|docs|tool|scripts|example)[/\\].+/i.test(line)) return true;
    // 代码标点密度
    const punct = (line.match(/[{}();=<>]/g) || []).length;
    if (punct >= 6) return true;
    // Dart/代码特征
    if (/(=>|;\s*$|\$\{|Widget|BuildContext|StringBuffer)/.test(line)) return true;
    return false;
  }

  /** 返回当前活动行（供 snapshot） */
  getLines() {
    return this.lines.slice(-120);
  }
}

module.exports = { CodexActivityPrinter };
