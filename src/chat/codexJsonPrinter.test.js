'use strict';

const { describe, it } = require('node:test');
const assert = require('node:assert/strict');
const { CodexJsonPrinter } = require('./codexJsonPrinter');

/* ------------------------------------------------------------------ 测试辅助 ------------------------------------------------------------------ */

/** 收集 emit 事件的录音机 */
function recorder() {
  const events = [];
  const emit = (event) => events.push(event);
  return { events, emit };
}

/** 把若干 NDJSON 行拼成一整块（带尾部换行），模拟一次性到达的完整流 */
function block(...lines) {
  return `${lines.join('\n')}\n`;
}

/** 真实样例事件：覆盖 session_configured / reasoning / item(message) / item(function_call) / item(function_call_output) / task_complete */
function sampleLines() {
  return [
    JSON.stringify({ type: 'session_configured', payload: { session_id: 'sess_codex_42', model: 'gpt-5' } }),
    JSON.stringify({ type: 'reasoning', payload: { text: 'Let me check the file.' } }),
    JSON.stringify({ type: 'item', payload: { type: 'function_call', name: 'read_file', call_id: 'call_1', arguments: JSON.stringify({ file_path: '/src/a.js' }) } }),
    JSON.stringify({ type: 'item', payload: { type: 'function_call_output', call_id: 'call_1', output: 'file contents here' } }),
    JSON.stringify({ type: 'reasoning', payload: { text: 'Now I will answer.' } }),
    JSON.stringify({ type: 'item', payload: { type: 'message', content: [{ type: 'output_text', text: 'Hello ' }] } }),
    JSON.stringify({ type: 'item', payload: { type: 'message', content: [{ type: 'output_text', text: 'world.' }] } }),
    JSON.stringify({ type: 'task_complete', payload: { last_agent_message: 'Hello world.' } }),
  ];
}

/* ------------------------------------------------------------------ 用例 ------------------------------------------------------------------ */

describe('CodexJsonPrinter 事件翻译', () => {
  it('把真实样例 NDJSON 翻译为对齐的 emit 调用序列', () => {
    const { events, emit } = recorder();
    const printer = new CodexJsonPrinter(emit);
    for (const line of sampleLines()) printer.offer(`${line}\n`);
    printer.flush();

    const sequence = events.map((e) => e.type);
    assert.deepEqual(
      sequence,
      [
        'thinking_start',
        'thinking_delta',
        'thinking_end',
        'tool_call',
        'tool_start',
        'tool_result',
        'thinking_start',
        'thinking_delta',
        'thinking_end',
        'text_delta',
        'text_delta',
        'done',
      ],
      `实际序列: ${JSON.stringify(sequence)}`,
    );
  });

  it('首个 reasoning 之前不重复发 thinking_start，第二次 reasoning 续接 thinking_delta', () => {
    const { events, emit } = recorder();
    const printer = new CodexJsonPrinter(emit);
    printer.offer(`${JSON.stringify({ type: 'reasoning', payload: { text: 'A' } })}\n`);
    printer.offer(`${JSON.stringify({ type: 'reasoning', payload: { text: 'B' } })}\n`);
    printer.flush();

    // A、B 同属首个思考段：仅一次 thinking_start、两次 delta；message/flush 触发 thinking_end
    assert.deepEqual(events.map((e) => e.type), ['thinking_start', 'thinking_delta', 'thinking_delta', 'thinking_end', 'done']);
    assert.equal(events[1].content, 'A');
    assert.equal(events[2].content, 'B');
  });

  it('reasoning 后续到达 message 会补发 thinking_end，再发 text_delta', () => {
    const { events, emit } = recorder();
    const printer = new CodexJsonPrinter(emit);
    printer.offer(`${JSON.stringify({ type: 'reasoning', payload: { text: 'think' } })}\n`);
    printer.offer(`${JSON.stringify({ type: 'item', payload: { type: 'message', text: 'answer' } })}\n`);
    printer.flush();

    assert.deepEqual(events.map((e) => e.type), ['thinking_start', 'thinking_delta', 'thinking_end', 'text_delta', 'done']);
  });

  it('function_call 翻译为 tool_call + tool_start，并解析 arguments 为对象', () => {
    const { events, emit } = recorder();
    const printer = new CodexJsonPrinter(emit);
    printer.offer(
      `${JSON.stringify({ type: 'item', payload: { type: 'function_call', name: 'shell', call_id: 'call_9', arguments: '{"command":["ls","-la"]}' } })}\n`,
    );
    printer.flush();

    const toolCall = events.find((e) => e.type === 'tool_call');
    const toolStart = events.find((e) => e.type === 'tool_start');
    assert.equal(toolCall.id, 'call_9');
    assert.equal(toolCall.name, 'shell');
    assert.equal(toolCall.arguments, '{"command":["ls","-la"]}');
    assert.equal(toolStart.name, 'shell');
    assert.deepEqual(toolStart.args, { command: ['ls', '-la'] });
  });

  it('function_call_output 翻译为 tool_result，JSON 输出解析为对象、纯文本保持字符串', () => {
    const { events, emit } = recorder();
    const printer = new CodexJsonPrinter(emit);

    printer.offer(`${JSON.stringify({ type: 'item', payload: { type: 'function_call_output', call_id: 'c1', output: '{"ok":true}' } })}\n`);
    printer.offer(`${JSON.stringify({ type: 'item', payload: { type: 'function_call_output', call_id: 'c2', output: 'plain text' } })}\n`);
    printer.flush();

    const results = events.filter((e) => e.type === 'tool_result');
    assert.equal(results[0].tool_call_id, 'c1');
    assert.deepEqual(results[0].result, { ok: true });
    assert.equal(results[1].tool_call_id, 'c2');
    assert.equal(results[1].result, 'plain text');
  });
});

describe('CodexJsonPrinter session / 文本累计', () => {
  it('session_configured 提取 session_id 供 getSessionId 返回', () => {
    const { emit } = recorder();
    const printer = new CodexJsonPrinter(emit);
    printer.offer(`${JSON.stringify({ type: 'session_configured', payload: { session_id: 'abc-123_DEF.4' } })}\n`);
    printer.flush();
    assert.equal(printer.getSessionId(), 'abc-123_DEF.4');
  });

  it('非法 / 缺失 session_id 时 getSessionId 返回空串', () => {
    const { emit } = recorder();
    const printer = new CodexJsonPrinter(emit);
    printer.offer(`${JSON.stringify({ type: 'session_configured', payload: { session_id: 'has space' } })}\n`);
    printer.flush();
    assert.equal(printer.getSessionId(), '');
  });

  it('getResultText 累计 message 文本，多段以换行拼接', () => {
    const { emit } = recorder();
    const printer = new CodexJsonPrinter(emit);
    printer.offer(`${JSON.stringify({ type: 'item', payload: { type: 'message', text: 'line one' } })}\n`);
    printer.offer(`${JSON.stringify({ type: 'item', payload: { type: 'message', text: 'line two' } })}\n`);
    printer.flush();
    assert.equal(printer.getResultText(), 'line one\nline two');
  });

  it('无 message 文本时，getResultText 回退 task_complete 的 last_agent_message', () => {
    const { emit } = recorder();
    const printer = new CodexJsonPrinter(emit);
    printer.offer(`${JSON.stringify({ type: 'task_complete', payload: { last_agent_message: 'final answer' } })}\n`);
    printer.flush();
    assert.equal(printer.getResultText(), 'final answer');
  });
});

describe('CodexJsonPrinter 容错与跨 chunk', () => {
  it('跨 chunk 拆断的行能正确拼接解析', () => {
    const { events, emit } = recorder();
    const printer = new CodexJsonPrinter(emit);
    const full = block(...sampleLines());
    const mid = Math.floor(full.length / 2);
    printer.offer(full.slice(0, mid));
    printer.offer(full.slice(mid));
    printer.flush();

    assert.equal(printer.getSessionId(), 'sess_codex_42');
    assert.equal(printer.getResultText(), 'Hello \nworld.');
    assert.ok(events.some((e) => e.type === 'done'));
  });

  it('尾部残行（无换行结尾）由 flush 处理', () => {
    const { events, emit } = recorder();
    const printer = new CodexJsonPrinter(emit);
    printer.offer(`${JSON.stringify({ type: 'item', payload: { type: 'message', text: 'trailing' } })}`); // 故意不换行
    assert.deepEqual(events.map((e) => e.type), []);
    printer.flush();
    assert.deepEqual(events.map((e) => e.type), ['text_delta', 'done']);
    assert.equal(printer.getResultText(), 'trailing');
  });

  it('未知事件与非 JSON 行静默跳过，不抛错', () => {
    const { events, emit } = recorder();
    const printer = new CodexJsonPrinter(emit);
    printer.offer('this is not json\n');
    printer.offer(`${JSON.stringify({ type: 'some_future_event', payload: { foo: 'bar' } })}\n`);
    printer.offer(`${JSON.stringify({ type: 'item', payload: { type: 'unknown_kind', text: 'x' } })}\n`);
    printer.flush();
    // 仅 flush 补发的 done，无其它事件
    assert.deepEqual(events.map((e) => e.type), ['done']);
  });

  it('流结束（无 task_complete）也翻译为 done，且不重复发 done', () => {
    const { events, emit } = recorder();
    const printer = new CodexJsonPrinter(emit);
    printer.offer(`${JSON.stringify({ type: 'item', payload: { type: 'message', text: 'hi' } })}\n`);
    printer.flush();
    const dones = events.filter((e) => e.type === 'done');
    assert.equal(dones.length, 1);
  });

  it('构造时不传 emit 也不抛错（默认空操作）', () => {
    const printer = new CodexJsonPrinter();
    assert.doesNotThrow(() => {
      printer.offer(`${JSON.stringify({ type: 'item', payload: { type: 'message', text: 'ok' } })}\n`);
      printer.flush();
    });
    assert.equal(printer.getResultText(), 'ok');
  });

  it('兼容字段直接挂顶层（无 payload 包裹）的 session_configured / reasoning 形态', () => {
    const { events, emit } = recorder();
    const printer = new CodexJsonPrinter(emit);
    printer.offer(`${JSON.stringify({ type: 'session_configured', session_id: 'top-level-sess' })}\n`);
    printer.offer(`${JSON.stringify({ type: 'reasoning', text: 'topthink' })}\n`);
    printer.flush();
    assert.equal(printer.getSessionId(), 'top-level-sess');
    assert.deepEqual(
      events.map((e) => e.type),
      ['thinking_start', 'thinking_delta', 'thinking_end', 'done'],
    );
    assert.equal(events[1].content, 'topthink');
  });
});
