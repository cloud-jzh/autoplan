'use strict';

/**
 * CodexJsonPrinter —— Codex CLI `codex exec --json` 的事件流解析器。
 *
 * Codex 在 `exec --json` 下以 NDJSON（每行一个 JSON 对象）实时流式输出事件，
 * 事件通常形如 { type, payload:{...} }（个别版本字段直接挂在顶层）。本类把这些
 * 事件翻译成 chatController 现有事件协议（与 llmClient 流式事件词汇对齐）：
 * - session_configured        → 记录 session_id（供 getSessionId 返回，作 resume/落库值）
 * - reasoning                 → thinking_start / thinking_delta / thinking_end
 * - item(message)             → text_delta（累计为最终助手文本）
 * - item(function_call)       → tool_call + tool_start
 * - item(function_call_output)→ tool_result
 * - task_complete / 流结束     → done
 *
 * API 仿 ClaudeStreamJsonPrinter（src/claudeActivity.js）：
 * - offer(rawChunk)    喂入原始流（可含多行、可跨 chunk 拆断）
 * - flush()            处理尾部残行，并补发未闭合的 thinking_end 与缺失的 done
 * - getResultText()    返回累计的最终助手文本（供 chatController 落库为 assistant 消息）
 * - getSessionId()     返回 session_configured 的 session_id（非法/缺失返回空串）
 *
 * 通过构造函数注入 emit(event) 回调透传翻译后的事件；未知事件静默跳过，不抛错，
 * 最坏退化为 getResultText() 返回单条文本。不引入新的运行时依赖。
 */

const { normalizeSessionId } = require('../claudeActivity');

/** 解析 JSON 字符串，失败/非字符串返回 null */
function safeParseJson(value) {
  if (typeof value !== 'string') return null;
  const text = value.trim();
  if (!text) return null;
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

function safeStringify(value) {
  try {
    return JSON.stringify(value);
  } catch {
    return '';
  }
}

/**
 * 从 message / reasoning item 提取文本：
 * 优先 payload.text，其次 content 数组里的 text 块，再退化为 content 字符串。
 * 兼容 Codex 不同版本里 message.content 为 [{type:'output_text',text}] 或直接 .text 的形态。
 */
function extractItemText(payload) {
  if (!payload || typeof payload !== 'object') return '';
  if (typeof payload.text === 'string') return payload.text;
  const content = payload.content;
  if (typeof content === 'string') return content;
  if (Array.isArray(content)) {
    const parts = [];
    for (const block of content) {
      if (block && typeof block === 'object' && typeof block.text === 'string') {
        parts.push(block.text);
      } else if (typeof block === 'string') {
        parts.push(block);
      }
    }
    return parts.join('');
  }
  return '';
}

class CodexJsonPrinter {
  /**
   * @param {Function} [emit] 翻译后事件的透传回调 (event) => void；缺省为空操作
   */
  constructor(emit) {
    this.emit = typeof emit === 'function' ? emit : () => {};
    this.pendingLine = '';
    this.sessionId = '';
    this.resultText = '';
    this.assistantText = '';
    this.thinkingOpen = false;
    this.doneEmitted = false;
  }

  /** 喂入一段原始文本（可含多行，可跨 chunk 拆断） */
  offer(rawChunk) {
    const text = String(rawChunk || '').replace(/\r/g, '');
    this.pendingLine += text;
    while (true) {
      const idx = this.pendingLine.indexOf('\n');
      if (idx < 0) break;
      const line = this.pendingLine.slice(0, idx);
      this.pendingLine = this.pendingLine.slice(idx + 1);
      this.processLine(line);
    }
  }

  /** 处理尾部残行，并补发未闭合的 thinking_end 与缺失的 done（流结束 → done） */
  flush() {
    const line = this.pendingLine;
    this.pendingLine = '';
    if (line.trim()) this.processLine(line);
    this.closeThinking();
    if (!this.doneEmitted) this.emitDone();
  }

  processLine(rawLine) {
    const trimmed = String(rawLine || '').trim();
    if (!trimmed) return;
    let event;
    try {
      event = JSON.parse(trimmed);
    } catch {
      // 非 JSON 行（stderr 噪声 / 分片 / 进度行）静默跳过，不抛错
      return;
    }
    if (!event || typeof event !== 'object') return;
    this.handleEvent(event);
  }

  handleEvent(event) {
    switch (event.type) {
      case 'session_configured':
        this.handleSessionConfigured(event);
        break;
      case 'reasoning':
        this.handleReasoning(event);
        break;
      case 'item':
        this.handleItem(event);
        break;
      case 'task_complete':
        this.handleTaskComplete(event);
        break;
      default:
        // 未知事件静默跳过容错
        break;
    }
  }

  /** 兼容 {type, payload:{...}} 与字段直接挂顶层两种形态，返回承载字段的对象 */
  payloadOf(event) {
    if (event && typeof event.payload === 'object' && event.payload !== null) {
      return event.payload;
    }
    return event;
  }

  handleSessionConfigured(event) {
    const payload = this.payloadOf(event);
    const sid = normalizeSessionId(payload.session_id);
    if (sid) this.sessionId = sid;
  }

  handleReasoning(event) {
    const payload = this.payloadOf(event);
    const text = extractItemText(payload);
    if (!text) return;
    if (!this.thinkingOpen) {
      this.thinkingOpen = true;
      this.emit({ type: 'thinking_start' });
    }
    this.emit({ type: 'thinking_delta', content: text });
  }

  handleItem(event) {
    const payload = this.payloadOf(event);
    if (!payload || typeof payload !== 'object') return;
    switch (payload.type) {
      case 'message':
        this.handleItemMessage(payload);
        break;
      case 'function_call':
        this.handleItemFunctionCall(payload);
        break;
      case 'function_call_output':
        this.handleItemFunctionCallOutput(payload);
        break;
      default:
        // 未知 item 类型静默跳过
        break;
    }
  }

  handleItemMessage(payload) {
    this.closeThinking();
    const text = extractItemText(payload);
    if (!text) return;
    if (this.assistantText) this.assistantText += '\n';
    this.assistantText += text;
    this.emit({ type: 'text_delta', content: text });
  }

  handleItemFunctionCall(payload) {
    this.closeThinking();
    const name = String(payload.name || '');
    const id = String(payload.call_id || payload.id || '');
    const argumentsText =
      typeof payload.arguments === 'string'
        ? payload.arguments
        : payload.arguments == null
          ? '{}'
          : safeStringify(payload.arguments) || '{}';
    const args = safeParseJson(argumentsText) ?? {};
    this.emit({ type: 'tool_call', id, name, arguments: argumentsText });
    this.emit({ type: 'tool_start', name, args });
  }

  handleItemFunctionCallOutput(payload) {
    this.closeThinking();
    const toolCallId = String(payload.call_id || payload.tool_call_id || '');
    const output = payload.output == null ? '' : payload.output;
    const result = this.coerceToolResult(output);
    this.emit({ type: 'tool_result', tool_call_id: toolCallId, result });
  }

  handleTaskComplete(event) {
    this.closeThinking();
    const payload = this.payloadOf(event);
    const finalText =
      extractItemText(payload) ||
      (typeof payload.last_agent_message === 'string' ? payload.last_agent_message : '');
    // task_complete 携带的最终文本作为 getResultText() 的优先来源（仅当未累计到 message 文本时）
    if (finalText && !this.assistantText.trim()) {
      this.resultText = finalText;
    }
    this.emitDone();
  }

  /** 若处于思考段，补发 thinking_end 并复位标记 */
  closeThinking() {
    if (!this.thinkingOpen) return;
    this.thinkingOpen = false;
    this.emit({ type: 'thinking_end' });
  }

  emitDone() {
    if (this.doneEmitted) return;
    this.doneEmitted = true;
    this.emit({ type: 'done' });
  }

  /** 工具输出规整：JSON 字符串解析为对象，纯文本保持字符串，对象原样返回 */
  coerceToolResult(output) {
    if (typeof output === 'string') {
      const parsed = safeParseJson(output);
      return parsed === null ? output : parsed;
    }
    return output;
  }

  /** 最终助手文本：优先 task_complete 的最终文本，回退累计 message 文本，再回退空串 */
  getResultText() {
    if (this.resultText && this.resultText.trim()) return this.resultText.trim();
    if (this.assistantText && this.assistantText.trim()) return this.assistantText.trim();
    return '';
  }

  /** session_configured 的 session_id（非法/缺失返回空串） */
  getSessionId() {
    return this.sessionId;
  }
}

module.exports = { CodexJsonPrinter };
