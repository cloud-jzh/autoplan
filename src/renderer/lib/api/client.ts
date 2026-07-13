import type {
  AcceptanceItemInput,
  AcceptanceRedoInput,
  AcceptBatchInput,
  AppEvent,
  AppSnapshot,
  AiConfig,
  Attachment,
  AutoplanApi,
  CreateIntakeInput,
  Feedback,
  ClaudeCliConfig,
  ChatClearPayload,
  ChatDoneEvent,
  ChatHistoryPayload,
  ChatMessage,
  ChatQueueItem,
  ChatQueuePayload,
  ChatSendPayload,
  ChatStopPayload,
  ConversationCreateInput,
  ConversationDeleteInput,
  ConversationListInput,
  ConversationUpdateInput,
  IntakeAcceptanceInput,
  IntakeType,
  Plan,
  PlanIdInput,
  PlanTask,
  Conversation,
  ReorderPlansInput,
  Requirement,
  TerminalRestCreateInput,
  TerminalRestEnvelope,
  TerminalRestReplay,
  TerminalRestSession,
  TerminalEvent,
  UpdateFeedbackInput,
  UpdateRequirementInput,
} from '../../types';
import type {
  AutoplanClientEvents,
  ConnectOperationEvents,
  ConnectProjectEvents,
  EventHandler,
  RendererResyncReason,
  ResumableEventSubscription,
} from './events';

/**
 * P00 go-api capabilities. Keep this list explicit: transport adapters and
 * migration guards use it to detect an unmapped business capability.
 */
export const AUTOPLAN_CLIENT_OPERATION_KEYS = [
  'mcpToolNames',
  'snapshot',
  'createProject',
  'updateProject',
  'deleteProject',
  'configureLoop',
  'startLoop',
  'stopLoop',
  'runOnce',
  'startMcp',
  'stopMcp',
  'mcpStatus',
  'readMcpAuthToken',
  'saveMcpConfig',
  'readPlan',
  'reorderPlans',
  'stopPlan',
  'resumePlan',
  'updatePlanExecutionConfig',
  'reExecutePlan',
  'recreatePlanFromIntake',
  'appendPlanTask',
  'deletePlan',
  'runTask',
  'runTaskBatches',
  'stopTask',
  'acceptItem',
  'unacceptItem',
  'redoAcceptanceItem',
  'acceptItems',
  'unacceptItems',
  'acceptIntake',
  'unacceptIntake',
  'createRequirement',
  'updateRequirement',
  'deleteRequirement',
  'createFeedback',
  'updateFeedback',
  'deleteFeedback',
  'interruptIntake',
  'resumeIntake',
  'appendIntakeTask',
  'retryIntakePlanGeneration',
  'createScript',
  'updateScript',
  'deleteScript',
  'toggleScript',
  'runScript',
  'stopScript',
  'createExecutor',
  'updateExecutor',
  'deleteExecutor',
  'toggleExecutor',
  'runExecutor',
  'stopExecutor',
  'runExecutorAction',
  'importTasksJson',
  'createTerminal',
  'listTerminals',
  'writeTerminal',
  'resizeTerminal',
  'killTerminal',
  'closeTerminal',
  'renameTerminal',
  'replayTerminal',
  'clearTerminal',
  'chatSend',
  'chatStop',
  'chatClear',
  'chatHistory',
  'chatSaveConfig',
  'chatGetConfig',
  'chatQueueList',
  'chatQueueCancel',
  'chatQueueEdit',
  'chatQueueClear',
  'aiConfigList',
  'aiConfigCreate',
  'aiConfigUpdate',
  'aiConfigDelete',
  'aiConfigGet',
  'claudeCliConfigList',
  'claudeCliConfigCreate',
  'claudeCliConfigUpdate',
  'claudeCliConfigDelete',
  'claudeCliConfigGet',
  'claudeCliConfigSetDefault',
  'conversationList',
  'conversationCreate',
  'conversationUpdate',
  'conversationDelete',
  'fileAccess',
] as const satisfies readonly (keyof AutoplanApi)[];

export type AutoplanClientOperationKey = (typeof AUTOPLAN_CLIENT_OPERATION_KEYS)[number];
export type AutoplanClientOperations = Pick<AutoplanApi, AutoplanClientOperationKey>;

export interface HttpRequestOptions {
  signal?: AbortSignal;
}

/**
 * P14's sole owner switch. A false or missing flag preserves the legacy
 * Terminal adapter; a true flag enables REST control and the private
 * WebSocket data plane together.
 */
export const TERMINAL_RUNTIME_FEATURE = 'go_terminal_api' as const;
export interface TerminalRestOperations {
  createTerminalRest: (projectId: number, input: TerminalRestCreateInput, options?: HttpRequestOptions) => Promise<TerminalRestEnvelope<TerminalRestSession>>;
  listTerminalsRest: (projectId: number, options?: HttpRequestOptions) => Promise<TerminalRestEnvelope<TerminalRestSession[]>>;
  deleteTerminalRest: (sessionId: string, options?: HttpRequestOptions) => Promise<TerminalRestEnvelope<TerminalRestSession>>;
  replayTerminalRest: (sessionId: string, lastSeq: number, options?: HttpRequestOptions) => Promise<TerminalRestEnvelope<TerminalRestReplay>>;
}

/**
 * The terminal data plane is intentionally session-scoped. It is not a
 * project SSE subscription and has no generic event-bus representation.
 */
export interface TerminalConnectionHandlers {
  onData?: (event: TerminalEvent & { data: string; seq: number }) => void;
  onExit?: (event: TerminalEvent) => void;
  onStatus?: (event: TerminalEvent) => void;
  onClosed?: (event: TerminalEvent & { closed: true }) => void;
  onUnavailable?: (reason: string) => void;
}

export interface TerminalConnectionOperations {
  connectTerminal?: (
    projectId: number,
    sessionId: string,
    lastSeq: number,
    handlers: TerminalConnectionHandlers,
  ) => () => void;
}

/** Observable byte boundaries for multipart uploads. Values never contain file data or paths. */
export interface HttpUploadProgress {
  loaded: number;
  total: number;
}

/** HTTP mutation controls. They are transport-only and never enter an IPC DTO or JSON body. */
export interface HttpMutationOptions extends HttpRequestOptions {
  onUploadProgress?: (progress: HttpUploadProgress) => void;
}

export interface ProjectPageRequest extends HttpRequestOptions {
  page?: number;
  pageSize?: number;
}

export interface ProjectPagination {
  page: number;
  page_size: number;
  total: number;
  next_page: number | null;
}

export interface ProjectPage {
  data: Project[];
  pagination: ProjectPagination;
  request_id: string;
}

export interface ProbeResult {
  status: 'ok' | 'ready';
  request_id: string;
}

export interface HttpReadonlyOperations {
  health: (options?: HttpRequestOptions) => Promise<ProbeResult>;
  ready: (options?: HttpRequestOptions) => Promise<ProbeResult>;
  listProjects: (request?: ProjectPageRequest) => Promise<ProjectPage>;
  getProject: (projectId: number, options?: HttpRequestOptions) => Promise<Project>;
  getProjectSnapshot: (
    projectId: number,
    options?: HttpRequestOptions,
  ) => Promise<AppSnapshot>;
  getLoopConfig: (
    projectId: number,
    options?: HttpRequestOptions,
  ) => Promise<AppSnapshot>;
  connectProjectEvents: ConnectProjectEvents;
  connectOperationEvents: ConnectOperationEvents;
}

export interface HttpCapableAutoplanClient extends AutoplanClient, HttpReadonlyOperations, RuntimeActionOperations {}

export interface IntakePageRequest extends HttpRequestOptions {
  projectId: number;
  status?: string;
  page?: number;
  pageSize?: number;
}

export interface IntakePagination {
  page: number;
  page_size: number;
  total: number;
  next_page: number | null;
}

export interface IntakePage<TItem> {
  data: TItem[];
  pagination: IntakePagination;
  request_id: string;
}

export interface IntakePlanLink {
  link_id: number | null;
  plan_id: number;
  phase_index: number;
  phase_title: string;
}

export interface IntakePlanLinkInput {
  planId: number;
  phaseIndex: number;
  phaseTitle: string;
}

export interface AttachmentUploadResult {
  attachment: Attachment;
  state: string;
  recovery_required: boolean;
}

export interface AttachmentDeleteResult {
  attachment_id: number;
  state: string;
  recovery_required: boolean;
}

/**
 * The P06 methods switched by the explicit HTTP transport flag. They remain
 * available in addition to the legacy AutoplanClient surface so callers can
 * read individual Intake records and mutate plan links without IPC access.
 */
export interface HttpIntakeOperations {
  listRequirements: (request: IntakePageRequest) => Promise<IntakePage<Requirement>>;
  getRequirement: (projectId: number, intakeId: number, options?: HttpRequestOptions) => Promise<Requirement>;
  listFeedback: (request: IntakePageRequest) => Promise<IntakePage<Feedback>>;
  getFeedback: (projectId: number, intakeId: number, options?: HttpRequestOptions) => Promise<Feedback>;
  replaceRequirementPlanLinks: (
    projectId: number,
    intakeId: number,
    links: IntakePlanLinkInput[],
    options?: HttpMutationOptions,
  ) => Promise<AppSnapshot>;
  replaceFeedbackPlanLinks: (
    projectId: number,
    intakeId: number,
    links: IntakePlanLinkInput[],
    options?: HttpMutationOptions,
  ) => Promise<AppSnapshot>;
  listRequirementPlanLinks: (
    projectId: number,
    intakeId: number,
    options?: HttpRequestOptions,
  ) => Promise<IntakePlanLink[]>;
  listFeedbackPlanLinks: (
    projectId: number,
    intakeId: number,
    options?: HttpRequestOptions,
  ) => Promise<IntakePlanLink[]>;
  uploadIntakeAttachment: (
    type: IntakeType,
    input: CreateIntakeInput | UpdateRequirementInput | UpdateFeedbackInput,
    intakeId: number,
    attachmentIndex: number,
    options?: HttpMutationOptions,
  ) => Promise<AttachmentUploadResult>;
  deleteAttachment: (
    projectId: number,
    attachmentId: number,
    options?: HttpMutationOptions,
  ) => Promise<AttachmentDeleteResult>;
  getAttachmentDownloadUrl: (projectId: number, attachmentId: number) => string;
  acceptIntake: (input: IntakeAcceptanceInput, options?: HttpMutationOptions) => Promise<AppSnapshot>;
  unacceptIntake: (input: IntakeAcceptanceInput, options?: HttpMutationOptions) => Promise<AppSnapshot>;
}

export interface HttpIntakeAutoplanClient extends HttpCapableAutoplanClient, HttpIntakeOperations {}

/** Opaque, server-advertised P07 owner switch. It never contains runtime details. */
export interface HttpCapability {
  id: string;
  enabled: boolean;
}

export interface HttpCapabilityDiscovery {
  version: 'v1';
  capabilities: HttpCapability[];
  request_id: string;
}

/**
 * Runtime migration gates are intentionally independent. A false or missing
 * flag selects the explicit Node compatibility owner for only that family;
 * it never changes the owner of an already accepted Operation.
 */
export const RUNTIME_FEATURE_FLAGS = [
  'go_loop_actions',
  'go_plan_actions',
  'go_task_actions',
  'go_acceptance_retry_actions',
  /** Script runtime is independently owned by the Go P12 process service. */
  'go_scripts_api',
  /** Executor runtime is independently owned by the Go P12 process service. */
  'go_executors_api',
  /** Chat REST/SSE has its own P13A owner and rollback switch. */
  'go_chat_api',
  /** Terminal REST and its private WebSocket data plane switch atomically. */
  TERMINAL_RUNTIME_FEATURE,
  /** Legacy compatibility gate; it no longer selects Script/Executor routes. */
  'go_agent_cli_runtime',
] as const;

export type RuntimeFeatureFlag = (typeof RUNTIME_FEATURE_FLAGS)[number];
export type RuntimeFeatureFlags = Readonly<Record<RuntimeFeatureFlag, boolean>>;
export type RuntimeOperationOwner = 'go' | 'node';

/** The accepted Operation DTO shared by Runtime REST, MCP, and SSE. */
export interface RuntimeOperationAccepted {
  operation_id: string;
  type: string;
  status: string;
  request_id: string;
  accepted_at: string;
}

export interface RuntimeActionOperations {
  getRuntimeFeatureFlags: () => RuntimeFeatureFlags;
  getRuntimeOperationOwner: (operationId: string) => RuntimeOperationOwner | null;
}

export interface PlanQueryOptions extends HttpRequestOptions {
  limit?: number;
  offset?: number;
}

export interface PlanTaskQueryOptions extends HttpRequestOptions {}

export interface PlanEventsQueryOptions extends HttpRequestOptions {
  limit?: number;
  offset?: number;
}

/** Shared Plan/Task/Event reads, implemented from the same AppSnapshot in IPC mode. */
export interface PlanQueryOperations {
  listPlans: (projectId: number, options?: PlanQueryOptions) => Promise<Plan[]>;
  getPlan: (input: PlanIdInput, options?: PlanQueryOptions) => Promise<Plan>;
  listPlanTasks: (input: PlanIdInput, options?: PlanTaskQueryOptions) => Promise<PlanTask[]>;
  getPlanTask: (
    input: PlanIdInput & { taskId: number },
    options?: PlanTaskQueryOptions,
  ) => Promise<PlanTask>;
  listPlanEvents: (projectId: number, options?: PlanEventsQueryOptions) => Promise<AppEvent[]>;
}

/**
 * P07 Plan/Task persistence operations. These signatures retain the existing
 * IPC mutation surface and add read helpers. Long-running actions remain on
 * their independent P11 runtime owner gates rather than this persistence set.
 */
export interface HttpPlanOperations {
  getCapabilities: (options?: HttpRequestOptions) => Promise<HttpCapabilityDiscovery>;
  reorderPlans: (input: ReorderPlansInput) => Promise<AppSnapshot>;
  deletePlan: (input: PlanIdInput) => Promise<AppSnapshot>;
  acceptItem: (input: AcceptanceItemInput) => Promise<AppSnapshot>;
  unacceptItem: (input: AcceptanceItemInput) => Promise<AppSnapshot>;
  acceptItems: (input: AcceptBatchInput) => Promise<AppSnapshot>;
  unacceptItems: (input: AcceptBatchInput) => Promise<AppSnapshot>;
  redoAcceptanceItem: (input: AcceptanceRedoInput) => Promise<AppSnapshot>;
}

export interface HttpPlanAutoplanClient extends HttpIntakeAutoplanClient, HttpPlanOperations {}

/** P006 static persistence API. Responses deliberately omit executable text,
 * environment values, message bodies, tool data, and credential material. */
export interface HttpStaticScript {
  id: number;
  project_id: number;
  name: string;
  runtime: string;
  description: string;
  trigger_mode: string;
  enabled: boolean;
  timeout_seconds: number;
  fail_aborts: boolean;
  context_inject: string;
  sort_order: number;
  source_type: string;
  has_path: boolean;
  has_body: boolean;
  has_work_dir: boolean;
  has_last_log: boolean;
  version: number;
}

export interface HttpStaticExecutor {
  id: number;
  project_id: number;
  label: string;
  type: string;
  enabled: boolean;
  sort_order: number;
  depends_on: string[];
  depends_order: string;
  has_command: boolean;
  options_env_key_count: number;
  version: number;
}

export interface HttpCursorPage<T> {
  data: T[];
  next_cursor: string;
  request_id: string;
}

export interface HttpMessageMetadata {
  id: number;
  project_id: number;
  conversation_id: number;
  role: string;
  status: string | null;
  created_at: string;
  has_content: boolean;
  has_tool_calls: boolean;
  has_tool_result: boolean;
}

export interface HttpMCPConfig {
  enabled: boolean;
  transport: 'http' | 'stdio';
  host: string;
  port: number;
  path: string;
  port_explicit: boolean;
  has_auth_token: boolean;
  auth_token_masked: string;
}

export interface HttpStaticOperations {
  listStaticScripts: (projectId: number, options?: PlanQueryOptions) => Promise<HttpStaticScript[]>;
  getStaticScript: (projectId: number, scriptId: number, options?: HttpRequestOptions) => Promise<HttpStaticScript>;
  createStaticScript: (projectId: number, input: Record<string, unknown>, options?: HttpMutationOptions) => Promise<HttpStaticScript>;
  updateStaticScript: (projectId: number, scriptId: number, version: number, input: Record<string, unknown>, options?: HttpMutationOptions) => Promise<HttpStaticScript>;
  deleteStaticScript: (projectId: number, scriptId: number, version: number, options?: HttpMutationOptions) => Promise<HttpStaticScript>;
  listStaticExecutors: (projectId: number, options?: PlanQueryOptions) => Promise<HttpStaticExecutor[]>;
  getStaticExecutor: (projectId: number, executorId: number, options?: HttpRequestOptions) => Promise<HttpStaticExecutor>;
  createStaticExecutor: (projectId: number, input: Record<string, unknown>, options?: HttpMutationOptions) => Promise<HttpStaticExecutor>;
  updateStaticExecutor: (projectId: number, executorId: number, version: number, input: Record<string, unknown>, options?: HttpMutationOptions) => Promise<HttpStaticExecutor>;
  deleteStaticExecutor: (projectId: number, executorId: number, version: number, options?: HttpMutationOptions) => Promise<HttpStaticExecutor>;
  listStaticConversations: (projectId: number, cursor?: string, options?: HttpRequestOptions) => Promise<HttpCursorPage<Conversation>>;
  listStaticMessages: (projectId: number, conversationId: number, cursor?: string, options?: HttpRequestOptions) => Promise<HttpCursorPage<HttpMessageMetadata>>;
  listStaticAIConfigs: (options?: HttpRequestOptions) => Promise<AiConfig[]>;
  listStaticClaudeConfigs: (options?: HttpRequestOptions) => Promise<ClaudeCliConfig[]>;
  getStaticMCPConfig: (options?: HttpRequestOptions) => Promise<HttpMCPConfig>;
}

export interface HttpStaticAutoplanClient extends HttpPlanAutoplanClient, HttpStaticOperations {}

/** P13A Chat routes. Their owner is selected independently from the HTTP shell. */
export interface HttpChatOperations {
  isChatHTTPEnabled: () => boolean;
  chatSend: (payload: ChatSendPayload) => Promise<{ accepted: boolean; conversationId?: number; error?: string }>;
  chatStop: (payload: ChatStopPayload) => Promise<{ stopped: boolean; error?: string }>;
  chatClear: (payload: ChatClearPayload) => Promise<{ cleared: boolean; error?: string }>;
  chatHistory: (payload: ChatHistoryPayload) => Promise<ChatMessage[]>;
  chatQueueList: (payload: ChatQueuePayload) => Promise<ChatQueueItem[]>;
  chatQueueCancel: (payload: ChatQueuePayload) => Promise<{ ok: boolean }>;
  chatQueueEdit: (payload: ChatQueuePayload) => Promise<{ ok: boolean }>;
  chatQueueClear: (payload: ChatQueuePayload) => Promise<{ ok: boolean }>;
  conversationList: (payload: ConversationListInput) => Promise<Conversation[]>;
  conversationCreate: (payload: ConversationCreateInput) => Promise<Conversation>;
  conversationUpdate: (payload: ConversationUpdateInput) => Promise<Conversation>;
  conversationDelete: (payload: ConversationDeleteInput) => Promise<{ deleted: boolean; id: number }>;
  connectChatEvents: (
    projectId: number,
    conversationId: number,
    handlers: {
      onChunk?: EventHandler<{ type: string; data: Record<string, unknown> }>;
      onDone?: EventHandler<ChatDoneEvent>;
      onQueue?: EventHandler<{ conversationId: number; items: ChatQueueItem[]; count: number }>;
      onResync?: EventHandler<RendererResyncReason>;
    },
  ) => ResumableEventSubscription;
}

export function getHttpChatOperations(client: AutoplanClient): HttpChatOperations | null {
  const candidate = client as Partial<HttpChatOperations>;
  return typeof candidate.isChatHTTPEnabled === 'function' && candidate.isChatHTTPEnabled() &&
    typeof candidate.connectChatEvents === 'function'
    ? candidate as HttpChatOperations
    : null;
}

/**
 * Transport-neutral renderer boundary for all P00 go-api operations/events.
 * Signatures are selected directly from AutoplanApi so DTO names, nullability,
 * Promise rejection and compatibility AppSnapshot returns stay unchanged.
 */
export interface AutoplanClient extends AutoplanClientOperations, AutoplanClientEvents, PlanQueryOperations, TerminalConnectionOperations {}

export function getTerminalConnectionOperations(client: AutoplanClient): TerminalConnectionOperations | null {
  return typeof client.connectTerminal === 'function' ? client : null;
}
