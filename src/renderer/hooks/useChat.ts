import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useAutoplanClient, useHttpChatOperations } from '../lib/api/provider';
import type {
  AiConfig,
  ChatConfig,
  ChatDoneEvent,
  ChatKnownToolResult,
  ChatMessage,
  ChatStreamPhase,
  ChatToolCall,
  Conversation,
  WorkspaceChatState,
} from '../types';
import { isChatConfigAvailableForSend } from '../utils/workspaceForms';
import { useChatQueue } from './useChatQueue';

function makeTempId(counter: { value: number }) {
  return -(counter.value += 1);
}

function newMessage(
  projectId: number,
  role: ChatMessage['role'],
  overrides: Partial<ChatMessage> = {},
): ChatMessage {
  return {
    id: 0,
    projectId,
    role,
    content: '',
    toolCalls: null,
    toolResult: null,
    status: 'done',
    createdAt: new Date().toISOString(),
    ...overrides,
  };
}

function selectPreferredAiConfig(configs: AiConfig[]): AiConfig | null {
  return configs.find((config) => config.hasApiKey)
    ?? configs.find((config) => isChatConfigAvailableForSend(config))
    ?? configs[0]
    ?? null;
}

function getAiConfigName(configs: AiConfig[], configId: number | null): string {
  const fallback = selectPreferredAiConfig(configs);
  if (configId == null) {
    return fallback ? fallback.name : '默认配置';
  }
  const found = configs.find((c) => c.id === configId);
  return found ? found.name : fallback ? fallback.name : '默认配置';
}

function getConversationAiConfigId(conversation: Conversation | null | undefined): number | null {
  return conversation?.ai_config_id ?? conversation?.aiConfigId ?? null;
}

const AUTO_TITLE_PLACEHOLDERS = new Set(['新对话', '默认对话']);
const DEFAULT_OPENAI_MODEL = 'gpt-5.5';

function normalizeDoneConversationId(value: unknown): number | null {
  const id = Number(value || 0);
  return Number.isInteger(id) && id > 0 ? id : null;
}

function shouldApplyAutoTitle(currentTitle: string | null | undefined): boolean {
  const title = String(currentTitle || '').trim();
  return title === '' || AUTO_TITLE_PLACEHOLDERS.has(title);
}

export type ChatThinkingDepth = AiConfig['thinkingDepth'];

export type WorkspaceChatComposerActions = {
  updateConversationAiConfig: (configId: number | null) => Promise<void>;
  updateActiveAiConfigThinkingDepth: (thinkingDepth: ChatThinkingDepth) => Promise<void>;
};

export type CreateConversationOptions = {
  activate?: boolean;
};

function resolveCurrentAiConfig(
  configs: AiConfig[],
  conversations: Conversation[],
  activeConversationId: number | null,
): AiConfig | null {
  const activeConversation = activeConversationId
    ? conversations.find((c) => c.id === activeConversationId)
    : null;
  const boundConfigId = getConversationAiConfigId(activeConversation);

  if (boundConfigId != null) {
    const bound = configs.find((c) => c.id === boundConfigId);
    if (bound) return bound;
  }

  return selectPreferredAiConfig(configs);
}

function normalizeConversationAiConfigBinding(conversation: Conversation, configs: AiConfig[]): Conversation {
  const boundConfigId = getConversationAiConfigId(conversation);
  if (boundConfigId == null || configs.some((config) => config.id === boundConfigId)) {
    return conversation;
  }
  return {
    ...conversation,
    ai_config_id: null,
    aiConfigId: null,
  };
}

function normalizeConversationAiConfigBindings(conversations: Conversation[], configs: AiConfig[]): Conversation[] {
  return conversations.map((conversation) => normalizeConversationAiConfigBinding(conversation, configs));
}

function toChatConfig(aiConfig: AiConfig | null): ChatConfig {
  if (!aiConfig) {
    return {
      aiConfigId: null,
      name: '默认配置',
      provider: 'openai',
      baseUrl: 'https://api.openai.com',
      hasApiKey: false,
      maskedKey: '',
      model: DEFAULT_OPENAI_MODEL,
      temperature: '0.3',
      thinkingDepth: null,
      thinkingBudgetTokens: null,
    };
  }

  return {
    aiConfigId: aiConfig.id,
    name: aiConfig.name,
    provider: aiConfig.provider,
    baseUrl: aiConfig.baseUrl,
    hasApiKey: aiConfig.hasApiKey,
    maskedKey: aiConfig.maskedKey,
    model: aiConfig.model,
    temperature: aiConfig.temperature,
    thinkingDepth: aiConfig.thinkingDepth,
    thinkingBudgetTokens: aiConfig.thinkingBudgetTokens,
  };
}

/** 格式化相对时间 */
function formatRelativeTime(iso: string): string {
  if (!iso) return '';
  const diff = Date.now() - new Date(iso).getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return '刚刚';
  if (mins < 60) return `${mins} 分钟前`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours} 小时前`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days} 天前`;
  return new Date(iso).toLocaleDateString('zh-CN');
}

/** Chat 对话、多会话、配置和流式事件状态管理（需求 #26 / #28）。 */
export function useChat(projectId: number): WorkspaceChatState {
  const client = useAutoplanClient();
  const chatHttp = useHttpChatOperations();
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [isStreaming, setIsStreaming] = useState(false);
  const [streamingContent, setStreamingContent] = useState('');
  const [streamingToolCall, setStreamingToolCall] = useState<ChatToolCall | null>(null);

  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [aiConfigs, setAiConfigs] = useState<AiConfig[]>([]);
  const [activeConversationId, setActiveConversationId] = useState<number | null>(null);
  const config = useMemo(
    () => toChatConfig(resolveCurrentAiConfig(aiConfigs, conversations, activeConversationId)),
    [activeConversationId, aiConfigs, conversations],
  );

  const [isThinking, setIsThinking] = useState(false);
  const [thinkingContent, setThinkingContent] = useState('');
  const [streamPhase, setStreamPhase] = useState<ChatStreamPhase>('idle');

  const tempIdRef = useRef({ value: 0 });
  const projectIdRef = useRef(projectId);
  projectIdRef.current = projectId;
  const conversationsRequestRef = useRef(0);
  const mountedRef = useRef(false);
  const stateRef = useRef({
    isStreaming: false,
    streamingContent: '',
    streamingToolCall: null as ChatToolCall | null,
    messages: [] as ChatMessage[],
    activeConversationId: null as number | null,
    awaitingResponse: false,
    isThinking: false,
    thinkingContent: '',
    streamPhase: 'idle' as ChatStreamPhase,
    projectId,
  });

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      conversationsRequestRef.current += 1;
    };
  }, []);

  const resetTransientState = useCallback(() => {
    stateRef.current.isStreaming = false;
    stateRef.current.streamingContent = '';
    stateRef.current.streamingToolCall = null;
    stateRef.current.awaitingResponse = false;
    stateRef.current.isThinking = false;
    stateRef.current.thinkingContent = '';
    stateRef.current.streamPhase = 'idle';
    setIsStreaming(false);
    setStreamingContent('');
    setStreamingToolCall(null);
    setIsThinking(false);
    setThinkingContent('');
    setStreamPhase('idle');
  }, []);

  const resetMessages = useCallback(() => {
    stateRef.current.messages = [];
    setMessages([]);
  }, []);

  const resetActiveConversation = useCallback(
    (conversationId: number | null) => {
      stateRef.current.activeConversationId = conversationId;
      setActiveConversationId(conversationId);
      resetMessages();
      resetTransientState();
    },
    [resetMessages, resetTransientState],
  );

  const loadConversations = useCallback(async () => {
    if (!projectId) return;
    const requestId = (conversationsRequestRef.current += 1);
    const loadingProjectId = projectId;
    try {
      const [convs, cfgs] = await Promise.all([
        client.conversationList({ projectId }),
        client.aiConfigList().catch(() => [] as AiConfig[]),
      ]);
      if (!mountedRef.current || requestId !== conversationsRequestRef.current || loadingProjectId !== projectIdRef.current) return;
      const normalizedConversations = normalizeConversationAiConfigBindings(convs, cfgs);
      setConversations(normalizedConversations);
      setAiConfigs(cfgs);

      const currentId = stateRef.current.activeConversationId;
      const currentExists = currentId != null && normalizedConversations.some((conv) => conv.id === currentId);
      const nextId = normalizedConversations.length === 0 ? null : currentExists ? currentId : normalizedConversations[0].id;

      if (nextId !== currentId) {
        resetActiveConversation(nextId);
      }
    } catch {
      /* 加载失败忽略 */
    }
  }, [client, projectId, resetActiveConversation]);

  const refreshAiConfigState = useCallback(async (eventConfigs?: AiConfig[]) => {
    if (!mountedRef.current) return;
    if (eventConfigs) {
      setAiConfigs(eventConfigs);
      setConversations((current) => normalizeConversationAiConfigBindings(current, eventConfigs));
    }
    if (projectIdRef.current) {
      await loadConversations();
      return;
    }
    if (!eventConfigs) {
      const cfgs = await client.aiConfigList().catch(() => [] as AiConfig[]);
      if (!mountedRef.current) return;
      setAiConfigs(cfgs);
      setConversations((current) => normalizeConversationAiConfigBindings(current, cfgs));
    }
  }, [client, loadConversations]);

  useEffect(() => {
    const previousProjectId = stateRef.current.projectId;
    const previousConversationId = stateRef.current.activeConversationId;
    conversationsRequestRef.current += 1;
    if (
      (stateRef.current.isStreaming ||
        stateRef.current.awaitingResponse ||
        stateRef.current.streamPhase !== 'idle') &&
      previousConversationId &&
      previousProjectId
    ) {
      client
        .chatStop({ projectId: previousProjectId, conversationId: previousConversationId })
        .catch(() => {});
    }
    stateRef.current.projectId = projectId;
    setConversations([]);
    resetActiveConversation(null);
    if (projectId) {
      loadConversations();
    } else {
      void refreshAiConfigState();
    }
  }, [client, projectId, loadConversations, refreshAiConfigState, resetActiveConversation]);

  useEffect(() => {
    let active = true;
    const unsubscribe = client.onAiConfigChanged((event) => {
      if (!active) return;
      void refreshAiConfigState(Array.isArray(event.configs) ? event.configs : undefined);
    });
    return () => {
      active = false;
      unsubscribe();
    };
  }, [client, refreshAiConfigState]);

  const loadHistory = useCallback(async (cid: number) => {
    if (!projectId) return;
    const loadingProjectId = projectId;
    try {
      const history = await client.chatHistory({ projectId: loadingProjectId, conversationId: cid });
      if (!mountedRef.current || stateRef.current.activeConversationId !== cid || projectIdRef.current !== loadingProjectId) return;
      setMessages(history);
      stateRef.current.messages = history;
    } catch {
      if (!mountedRef.current || stateRef.current.activeConversationId !== cid || projectIdRef.current !== loadingProjectId) return;
      setMessages([]);
      stateRef.current.messages = [];
    }
  }, [client, projectId]);

  const syncDoneConversationTitle = useCallback((conversationId: number | null, title: string | null | undefined) => {
    const nextTitle = String(title || '').trim();
    if (!conversationId || !nextTitle) return;

    setConversations((current) => {
      let changed = false;
      const next = current.map((conversation) => {
        if (conversation.id !== conversationId || !shouldApplyAutoTitle(conversation.title)) {
          return conversation;
        }
        changed = true;
        return { ...conversation, title: nextTitle };
      });
      return changed ? next : current;
    });
  }, []);

  useEffect(() => {
    if (activeConversationId) loadHistory(activeConversationId);
    else {
      resetMessages();
    }
  }, [activeConversationId, loadHistory, resetMessages]);

  useEffect(() => {
    const s = stateRef;
    let active = true;

    const onChunk = (event: { type: string; data: Record<string, unknown> }) => {
      if (!active) return;
      const cur = s.current;
      if (!cur.activeConversationId && event.type !== 'status') return;
      if (
        event.type !== 'status' &&
        !cur.awaitingResponse &&
        !cur.isStreaming &&
        cur.streamPhase === 'idle'
      ) {
        return;
      }

      switch (event.type) {
        /* -------- text_delta -------- */
        case 'text_delta': {
          const text = String(event.data?.content || '');
          cur.streamingContent += text;
          setStreamingContent(cur.streamingContent);

          // 首个 text_delta → 切换为回复阶段
          if (cur.streamPhase !== 'replying') {
            cur.streamPhase = 'replying';
            setStreamPhase('replying');
          }

          let streamingAssistantIndex = -1;
          for (let i = cur.messages.length - 1; i >= 0; i -= 1) {
            if (cur.messages[i].role === 'assistant' && cur.messages[i].status === 'streaming') {
              streamingAssistantIndex = i;
              break;
            }
          }

          if (!cur.isStreaming || streamingAssistantIndex === -1) {
            cur.isStreaming = true;
            setIsStreaming(true);
            const placeholder = newMessage(projectId, 'assistant', {
              id: makeTempId(tempIdRef.current),
              content: text,
              status: 'streaming',
            });
            cur.messages = [...cur.messages, placeholder];
            setMessages(cur.messages);
          } else {
            const msgs = [...cur.messages];
            msgs[streamingAssistantIndex] = { ...msgs[streamingAssistantIndex], content: cur.streamingContent };
            cur.messages = msgs;
            setMessages(msgs);
          }
          break;
        }

        /* -------- tool_start -------- */
        case 'tool_start': {
          const tc: ChatToolCall = {
            name: String(event.data?.name || ''),
            args: (event.data?.args || {}) as Record<string, unknown>,
          };
          cur.streamingToolCall = tc;
          setStreamingToolCall(tc);

          const toolMsg = newMessage(projectId, 'tool', {
            id: makeTempId(tempIdRef.current),
            toolResult: { name: tc.name, args: tc.args, loading: true },
            status: 'streaming',
          });
          cur.messages = [...cur.messages, toolMsg];
          setMessages(cur.messages);
          break;
        }

        /* -------- tool_result -------- */
        case 'tool_result': {
          const result = (event.data?.result || {}) as ChatKnownToolResult;
          cur.streamingToolCall = null;
          setStreamingToolCall(null);

          const msgs = [...cur.messages];
          for (let i = msgs.length - 1; i >= 0; i -= 1) {
            if (msgs[i].role === 'tool' && msgs[i].status === 'streaming') {
              const existing = (msgs[i].toolResult || {}) as Record<string, unknown>;
              msgs[i] = { ...msgs[i], toolResult: { ...existing, result, loading: false }, status: 'done' };
              break;
            }
          }
          cur.messages = msgs;
          setMessages(msgs);
          break;
        }

        /* -------- thinking_*（需求 #28）-------- */
        case 'thinking_start':
          cur.isThinking = true;
          cur.thinkingContent = '';
          cur.streamPhase = 'thinking';
          setIsThinking(true);
          setThinkingContent('');
          setStreamPhase('thinking');
          break;

        case 'thinking_delta':
          cur.thinkingContent += String(event.data?.content || '');
          setThinkingContent(cur.thinkingContent);
          break;

        case 'thinking_end':
          cur.isThinking = false;
          cur.streamPhase = 'replying';
          setIsThinking(false);
          setStreamPhase('replying');
          break;

        /* -------- error -------- */
        case 'error': {
          const errMsg = newMessage(projectId, 'system', {
            id: makeTempId(tempIdRef.current),
            content: String(event.data?.message || '发生未知错误'),
            status: 'error',
          });
          cur.messages = [...cur.messages, errMsg];
          setMessages(cur.messages);
          break;
        }

        /* -------- status（仅日志，不落消息） -------- */
        case 'status':
        default:
          break;
      }
    };

    const onDone = (event: ChatDoneEvent) => {
      if (!active) return;
      const cur = s.current;
      const doneConversationId = normalizeDoneConversationId(event.conversationId);
      const cid = cur.activeConversationId;
      if (event.status === 'done') {
        syncDoneConversationTitle(doneConversationId ?? cid, event.title);
      }

      if (doneConversationId !== null && doneConversationId !== cid) {
        return;
      }

      const hadActiveStream =
        cur.awaitingResponse ||
        cur.isStreaming ||
        cur.streamPhase !== 'idle' ||
        cur.messages.some((message) => message.status === 'streaming');
      resetTransientState();
      if (!cid || !hadActiveStream) return;

      // 固化流式 assistant 消息的状态
      const msgs = [...cur.messages];
      const finalStatus: ChatMessage['status'] =
        event.status === 'done' ? 'done' : event.status === 'aborted' ? 'aborted' : 'error';
      for (let i = msgs.length - 1; i >= 0; i -= 1) {
        if (msgs[i].role === 'assistant' && msgs[i].status === 'streaming') {
          msgs[i] = { ...msgs[i], status: finalStatus };
          break;
        }
        if (msgs[i].role === 'tool' && msgs[i].status === 'streaming') {
          msgs[i] = { ...msgs[i], status: 'done' };
        }
      }
      cur.messages = msgs;
      setMessages(msgs);

      // 重新加载历史以获取服务端真实 ID
      const historyProjectId = cur.projectId;
      client
        .chatHistory({ projectId: historyProjectId, conversationId: cid })
        .then((history) => {
          if (!active || !mountedRef.current || stateRef.current.activeConversationId !== cid || stateRef.current.projectId !== historyProjectId) return;
          cur.messages = history;
          setMessages(history);
        })
        .catch(() => {
          /* 刷新失败保留本地消息 */
        });
    };

    if (chatHttp && projectId && activeConversationId) {
      let stream: ReturnType<typeof chatHttp.connectChatEvents> | null = null;
      stream = chatHttp.connectChatEvents(projectId, activeConversationId, {
        onChunk,
        onDone,
        onResync: () => {
          const conversationId = activeConversationId;
          void loadHistory(conversationId).finally(() => stream?.completeResync());
        },
      });
      return () => {
        active = false;
        stream?.();
      };
    }

    const unsubChunk = client.onChatChunk(onChunk);
    const unsubDone = client.onChatDone(onDone);

    return () => {
      active = false;
      unsubChunk();
      unsubDone();
    };
  }, [activeConversationId, chatHttp, client, loadHistory, projectId, resetTransientState, syncDoneConversationTitle]);

  useEffect(() => {
    return () => {
      if (
        (stateRef.current.isStreaming ||
          stateRef.current.awaitingResponse ||
          stateRef.current.streamPhase !== 'idle') &&
        stateRef.current.activeConversationId
      ) {
        client
          .chatStop({
            projectId: stateRef.current.projectId,
            conversationId: stateRef.current.activeConversationId,
          })
          .catch(() => {});
      }
    };
  }, [client]);

  const sendMessage = useCallback(
    async (message: string) => {
      const text = String(message || '').trim();
      const cid = stateRef.current.activeConversationId;
      // 移除流式中发送守卫：回复中也可继续输入并入队（需求 #37）
      if (!text || !cid || !projectId) return;

      // 乐观以排队态追加用户消息（onChatDone 后 reload history 以服务端真实 id 协调，避免重复/错位）
      const userMsg = newMessage(projectId, 'user', {
        id: makeTempId(tempIdRef.current),
        content: text,
        status: 'queued',
      });
      const next = [...stateRef.current.messages, userMsg];
      stateRef.current.messages = next;
      setMessages(next);

      // 首次发送（非流式中）乐观进入流式态；流式中再发送仅入队，不重置流式态
      if (!stateRef.current.isStreaming) {
        stateRef.current.isStreaming = true;
        stateRef.current.awaitingResponse = true;
        setIsStreaming(true);
      }

      try {
        await client.chatSend({
          projectId,
          conversationId: cid,
          message: text,
        });
      } catch {
        if (stateRef.current.projectId === projectId && stateRef.current.activeConversationId === cid) resetTransientState();
        /* 错误经 onChatDone 推送处理 */
      }
    },
    [client, projectId, resetTransientState],
  );

  const stopGeneration = useCallback(async () => {
    const cid = stateRef.current.activeConversationId;
    const pid = stateRef.current.projectId || projectId;
    if (!cid || !pid) return;
    try {
      await client.chatStop({ projectId: pid, conversationId: cid });
    } catch {
      /* 中止失败忽略 */
    }
  }, [client, projectId]);

  const clearSession = useCallback(async () => {
    const cid = stateRef.current.activeConversationId;
    const pid = stateRef.current.projectId || projectId;
    if (!cid || !pid) return;
    try {
      await client.chatClear({ projectId: pid, conversationId: cid });
    } catch {
      /* 清空失败忽略 */
    }
    if (stateRef.current.projectId !== pid || stateRef.current.activeConversationId !== cid) return;
    resetMessages();
    resetTransientState();
  }, [client, projectId, resetMessages, resetTransientState]);

  /** 切换到指定对话 */
  const switchConversation = useCallback(
    async (cid: number) => {
      if (cid === stateRef.current.activeConversationId) return;

      // 中止当前对话的流式生成
      if (
        (stateRef.current.isStreaming ||
          stateRef.current.awaitingResponse ||
          stateRef.current.streamPhase !== 'idle') &&
        stateRef.current.activeConversationId
      ) {
        try {
          await client.chatStop({
            projectId,
            conversationId: stateRef.current.activeConversationId,
          });
        } catch {
          /* 忽略 */
        }
      }

      resetActiveConversation(cid);
    },
    [client, projectId, resetActiveConversation],
  );

  /** 创建新对话 */
  const createConversation = useCallback(async (options: CreateConversationOptions = {}) => {
    if (!projectId) return;
    try {
      const cfgs = await client.aiConfigList().catch(() => aiConfigs);
      const selectedConfig = selectPreferredAiConfig(cfgs);
      const conv = await client.conversationCreate({
        projectId,
        aiConfigId: selectedConfig?.id ?? null,
      });
      if (!mountedRef.current || projectIdRef.current !== projectId) return;
      setAiConfigs(cfgs);
      setConversations((prev) => [conv, ...prev]);
      if (options.activate !== false) {
        resetActiveConversation(conv.id);
      }
    } catch {
      /* 创建失败忽略 */
    }
  }, [aiConfigs, client, projectId, resetActiveConversation]);

  /** 删除对话 */
  const deleteConversation = useCallback(
    async (cid: number) => {
      try {
        if (
          stateRef.current.activeConversationId === cid &&
          (stateRef.current.isStreaming ||
            stateRef.current.awaitingResponse ||
            stateRef.current.streamPhase !== 'idle')
        ) {
          await client.chatStop({ projectId, conversationId: cid }).catch(() => {});
        }
        await client.conversationDelete({ projectId, conversationId: cid });
        await loadConversations();
      } catch {
        /* 删除失败忽略 */
      }
    },
    [client, loadConversations, projectId],
  );

  /** 重命名对话 */
  const renameConversation = useCallback(
    async (cid: number, title: string) => {
      try {
        await client.conversationUpdate({ projectId, conversationId: cid, title });
        if (!mountedRef.current || projectIdRef.current !== projectId) return;
        setConversations((prev) =>
          prev.map((c) => (c.id === cid ? { ...c, title } : c)),
        );
      } catch {
        /* 重命名失败忽略 */
      }
    },
    [client, projectId],
  );

  /** 切换当前对话绑定的 AI 配置 */
  const updateConversationAiConfig = useCallback(async (configId: number | null) => {
    const cid = stateRef.current.activeConversationId;
    if (!cid || !projectId) return;
    const updated = await client.conversationUpdate({
      projectId,
      conversationId: cid,
      aiConfigId: configId,
    });
    if (!mountedRef.current || stateRef.current.activeConversationId !== cid || projectIdRef.current !== projectId) return;
    setConversations((prev) =>
      prev.map((conversation) => (conversation.id === cid ? updated : conversation)),
    );
  }, [client, projectId]);

  /** 更新当前会话所绑定 AI 配置的思考深度 */
  const updateActiveAiConfigThinkingDepth = useCallback(
    async (thinkingDepth: ChatThinkingDepth) => {
      const targetConfig = resolveCurrentAiConfig(
        aiConfigs,
        conversations,
        stateRef.current.activeConversationId,
      );
      if (!targetConfig) return;
      const updated = await client.aiConfigUpdate({
        configId: targetConfig.id,
        thinkingDepth,
      });
      if (!mountedRef.current) return;
      setAiConfigs((prev) =>
        prev.map((item) => (item.id === updated.id ? updated : item)),
      );
    },
    [aiConfigs, client, conversations],
  );

  // 队列发送（需求 #37）：组合队列状态 hook（会话隔离快照 + 管理动作）
  const queue = useChatQueue(projectId, activeConversationId);

  const chatState: WorkspaceChatState & WorkspaceChatComposerActions = {
    messages,
    isStreaming,
    streamingContent,
    streamingToolCall,
    config,
    sendMessage,
    stopGeneration,
    clearSession,
    loadHistory,
    // 多对话扩展（需求 #28）
    conversations,
    aiConfigs,
    activeConversationId,
    switchConversation,
    createConversation,
    deleteConversation,
    renameConversation,
    getAiConfigName: (configId: number | null) => getAiConfigName(aiConfigs, configId),
    formatRelativeTime,
    // 思考状态（需求 #28）
    isThinking,
    thinkingContent,
    streamPhase,
    // 队列发送（需求 #37）
    queue: queue.items,
    queueCount: queue.count,
    cancelQueueItem: queue.cancelQueueItem,
    editQueueItem: queue.editQueueItem,
    clearQueue: queue.clearQueue,
    updateConversationAiConfig,
    updateActiveAiConfigThinkingDepth,
  };

  return chatState;
}
