import { useCallback, useEffect, useState } from 'react';
import { useAutoplanClient, useHttpChatOperations } from '../lib/api/provider';
import type { ChatQueueItem } from '../types';

export interface UseChatQueueResult {
  items: ChatQueueItem[];
  count: number;
  cancelQueueItem: (id: number) => Promise<void>;
  editQueueItem: (id: number, text: string) => Promise<void>;
  clearQueue: () => Promise<void>;
}

/**
 * 对话队列状态 hook（需求 #37）：维护当前会话的排队消息快照与管理动作。
 * - mount/会话切换时经 chatQueueList 拉取初始快照；订阅 onChatQueue 增量更新
 * - 按 conversationId 隔离事件（与 onChatChunk 同模式），仅更新当前活跃会话
 * - 暴露取消/编辑/清空动作：乐观更新本地快照 + IPC 落盘，onChatQueue 广播保证最终一致
 */
export function useChatQueue(projectId: number, activeConversationId: number | null): UseChatQueueResult {
  const client = useAutoplanClient();
  const chatHttp = useHttpChatOperations();
  const [items, setItems] = useState<ChatQueueItem[]>([]);

  useEffect(() => {
    if (!projectId || !activeConversationId) {
      setItems([]);
      return;
    }
    const cid = activeConversationId;
    let active = true;
    client
      .chatQueueList({ projectId, conversationId: cid })
      .then((list) => {
        if (active) setItems(Array.isArray(list) ? list : []);
      })
      .catch(() => {
        /* 拉取失败保留空快照 */
      });
    const onQueue = (snapshot: { conversationId: number; items: ChatQueueItem[]; count: number }) => {
      if (!active) return;
      if (snapshot.conversationId !== cid) return; // 会话隔离
      setItems(Array.isArray(snapshot.items) ? snapshot.items : []);
    };
    if (chatHttp) {
      let stream: ReturnType<typeof chatHttp.connectChatEvents> | null = null;
      stream = chatHttp.connectChatEvents(projectId, cid, {
        onQueue,
        onResync: () => {
          client.chatQueueList({ projectId, conversationId: cid })
            .then((list) => { if (active) setItems(Array.isArray(list) ? list : []); })
            .catch(() => undefined)
            .finally(() => stream?.completeResync());
        },
      });
      return () => {
        active = false;
        stream?.();
      };
    }
    const unsubscribe = client.onChatQueue(onQueue);
    return () => {
      active = false;
      unsubscribe();
    };
  }, [activeConversationId, chatHttp, client, projectId]);

  const cancelQueueItem = useCallback(
    async (id: number) => {
      if (!projectId || !activeConversationId) return;
      setItems((cur) => cur.filter((it) => it.id !== id));
      try {
        await client.chatQueueCancel({ projectId, conversationId: activeConversationId, id });
      } catch {
        /* 失败经 onChatQueue 广播修正 */
      }
    },
    [activeConversationId, client, projectId],
  );

  const editQueueItem = useCallback(
    async (id: number, text: string) => {
      const content = String(text || '').trim();
      if (!projectId || !activeConversationId || !content) return;
      setItems((cur) => cur.map((it) => (it.id === id ? { ...it, content } : it)));
      try {
        await client.chatQueueEdit({ projectId, conversationId: activeConversationId, id, message: content });
      } catch {
        /* 失败经 onChatQueue 广播修正 */
      }
    },
    [activeConversationId, client, projectId],
  );

  const clearQueue = useCallback(
    async () => {
      if (!projectId || !activeConversationId) return;
      setItems([]);
      try {
        await client.chatQueueClear({ projectId, conversationId: activeConversationId });
      } catch {
        /* 失败经 onChatQueue 广播修正 */
      }
    },
    [activeConversationId, client, projectId],
  );

  return { items, count: items.length, cancelQueueItem, editQueueItem, clearQueue };
}
