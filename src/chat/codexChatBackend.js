'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const {
  codexNewSessionArgs,
  codexResumeSessionArgs,
  createChunkDecoder,
  agentCliExecutionSpec,
} = require('../agentCli');
const { isCodexResumeFailure } = require('../loop/agentCliConfig');
const { CodexJsonPrinter } = require('./codexJsonPrinter');

/**
 * 把 codexNewSessionArgs / codexResumeSessionArgs 返回的 arg 数组追加 --json。
 * --json 是 exec 子命令的标志，需要出现在 exec 之后、其余 flag 与位置参数之前。
 */
function injectJsonFlag(baseArgs) {
  if (baseArgs[0] === 'exec') {
    return ['exec', '--json', ...baseArgs.slice(1)];
  }
  return [...baseArgs, '--json'];
}

/**
 * 自建轻量 spawn + CodexJsonPrinter 翻译，不复用 runAgentCliAttempt（避免 runtime / operation 状态机
 * 对对话路径过重）。单次 codex exec --json 完成一轮对话；可选 abort 与 resume 回退。
 *
 * @param {object} opts
 * @param {string} opts.workspacePath   - 工作区路径（spawn cwd）
 * @param {string} opts.prompt          - 用户消息（经 child.stdin 投递）
 * @param {string} [opts.sessionId]     - 非空时用于 codex exec resume 恢复上一轮 codex session
 * @param {string} [opts.reasoningEffort] - 推理深度 low/medium/high/xhigh，默认 medium
 * @param {string} [opts.command]       - codex CLI 命令路径，默认 'codex'
 * @param {AbortSignal} [opts.signal]   - 取消信号
 * @param {Function} [opts.onEvent]     - 事件回调 (event) => void；透传 codexJsonPrinter 翻译后的事件
 * @returns {Promise<{sessionId:string, content:string, aborted:boolean, error:string}>}
 */
async function runCodexChat({
  workspacePath,
  prompt,
  sessionId,
  reasoningEffort,
  command,
  signal,
  onEvent,
}) {
  const effectiveCommand = String(command || 'codex').trim() || 'codex';
  const effectiveReasoning = String(reasoningEffort || 'medium');
  const effectiveSessionId = String(sessionId || '').trim();
  const isResume = effectiveSessionId.length > 0;
  const effectiveOnEvent = typeof onEvent === 'function' ? onEvent : () => {};

  // lastFile：codexNewSessionArgs / codexResumeSessionArgs 要求该参数（-o 输出路径）；
  // 对话路径下不需要持久化结果文件，使用临时路径兜底，并在退出时清理。
  const lastFile = path.join(workspacePath, '.autoplan-codex-chat-tmp');

  const { sessionId: finalSessionId, content, aborted, error } = await spawnOnce({
    isResume,
    effectiveCommand,
    effectiveSessionId,
    effectiveReasoning,
    lastFile,
    workspacePath,
    prompt,
    signal,
    effectiveOnEvent,
  });

  // resume 失败检测：exit code 非零且输出包含 Codex resume 失败特征 → 回退新会话，最多重试一次
  if (isResume && aborted === false && content.startsWith('__RESUME_FAILED__') && effectiveSessionId) {
    const fallback = await spawnOnce({
      isResume: false,
      effectiveCommand,
      effectiveSessionId,
      effectiveReasoning,
      lastFile,
      workspacePath,
      prompt,
      signal,
      effectiveOnEvent,
    });
    if (fallback.content) {
      return { sessionId: fallback.sessionId || finalSessionId, content: fallback.content, aborted: false, error: '' };
    }
  }

  // 清理临时文件
  try { fs.unlinkSync(lastFile); } catch { /* best-effort */ }

  // 过滤内部回退标记（fallback 失败时 content 可能为 '__RESUME_FAILED__'）
  const cleanContent = content && !content.startsWith('__RESUME_FAILED__') ? content : '';
  return { sessionId: finalSessionId || '', content: cleanContent, aborted, error };
}

/**
 * 执行单次 spawn：拼装参数、启动子进程、解码 chunk、喂入 printer、投递 prompt、等待 exit。
 * 内部使用，避免 resume 回退代码重复。
 */
async function spawnOnce({
  isResume,
  effectiveCommand,
  effectiveSessionId,
  effectiveReasoning,
  lastFile,
  workspacePath,
  prompt,
  signal,
  effectiveOnEvent,
}) {
  const baseArgs = isResume
    ? codexResumeSessionArgs(effectiveSessionId, lastFile, { reasoningEffort: effectiveReasoning })
    : codexNewSessionArgs(lastFile, { reasoningEffort: effectiveReasoning });
  const args = injectJsonFlag(baseArgs);

  const spawnSpec = { command: effectiveCommand, args, useShell: false };
  const executionSpec = agentCliExecutionSpec(spawnSpec);

  let child;
  try {
    child = spawn(executionSpec.command, executionSpec.args, {
      shell: executionSpec.shell,
      windowsVerbatimArguments: executionSpec.windowsVerbatimArguments,
      cwd: workspacePath,
      env: process.env,
    });
  } catch (spawnError) {
    return {
      sessionId: isResume ? effectiveSessionId : '',
      content: '',
      aborted: false,
      error: `spawn failed: ${spawnError.message}`,
    };
  }

  // ── Abort ──
  const abortHandler = () => {
    if (child && !child.killed) {
      try { child.kill('SIGTERM'); } catch { /* best-effort */ }
    }
  };
  if (signal) {
    if (signal.aborted) {
      abortHandler();
    } else {
      signal.addEventListener('abort', abortHandler, { once: true });
    }
  }

  const printer = new CodexJsonPrinter((event) => {
    effectiveOnEvent(event);
  });

  const chunkDecoders = {
    stdout: createChunkDecoder(),
    stderr: createChunkDecoder(),
  };

  let output = '';

  if (child.stdout) {
    child.stdout.on('data', (chunk) => {
      const text = chunkDecoders.stdout.decode(chunk);
      output += text;
      printer.offer(text);
    });
  }

  if (child.stderr) {
    child.stderr.on('data', (chunk) => {
      const text = chunkDecoders.stderr.decode(chunk);
      output += text;
      printer.offer(text);
    });
  }

  if (child.stdin) {
    child.stdin.setDefaultEncoding('utf8');
    child.stdin.end(String(prompt || ''));
  }

  const exitCode = await new Promise((resolve) => {
    child.once('close', (code) => resolve(code !== null && code !== undefined ? code : -1));
    child.once('error', () => resolve(-1));
  });

  // 清理 abort 监听
  if (signal && !signal.aborted) {
    signal.removeEventListener('abort', abortHandler);
  }

  const aborted = signal ? signal.aborted : false;

  printer.flush();

  const resultContent = printer.getResultText();
  const printerSessionId = printer.getSessionId();

  let combinedSessionId = isResume ? effectiveSessionId : printerSessionId;

  // resume 失败：回退标记（由 runCodexChat 外层消费）
  if (isResume && exitCode !== 0 && isCodexResumeFailure(output)) {
    return {
      sessionId: effectiveSessionId,
      content: '__RESUME_FAILED__',
      aborted: false,
      error: `resume failed; exitCode=${exitCode}, sessionId=${effectiveSessionId}`,
    };
  }

  const error = exitCode !== 0 && !aborted ? `codex exit code: ${exitCode}` : '';

  return {
    sessionId: combinedSessionId || printerSessionId || '',
    content: resultContent || '',
    aborted,
    error,
  };
}

module.exports = { runCodexChat };
