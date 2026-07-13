const fs = require('node:fs');
const path = require('node:path');
const intakePlanLinks = require('./loop/intakePlanLinks');
const planBackendConfig = require('./loop/planBackendConfig');
const { extractMarkdownTitle } = require('./loop/planParser');
const { executorFromRow } = require('./executors/executorStore');

const MCP_TOOL_NAMES = Object.freeze({
  LIST_PROJECTS: 'list_projects',
  GET_PROJECT: 'get_project',
  CREATE_PROJECT: 'create_project',
  LIST_REQUIREMENTS: 'list_requirements',
  CREATE_REQUIREMENT: 'create_requirement',
  GET_REQUIREMENT: 'get_requirement',
  UPDATE_REQUIREMENT: 'update_requirement',
  DELETE_REQUIREMENT: 'delete_requirement',
  LIST_REQUIREMENT_PLAN_LINKS: 'list_requirement_plan_links',
  REPLACE_REQUIREMENT_PLAN_LINKS: 'replace_requirement_plan_links',
  UPLOAD_REQUIREMENT_ATTACHMENT: 'upload_requirement_attachment',
  LIST_FEEDBACK: 'list_feedback',
  CREATE_FEEDBACK: 'create_feedback',
  GET_FEEDBACK: 'get_feedback',
  UPDATE_FEEDBACK: 'update_feedback',
  DELETE_FEEDBACK: 'delete_feedback',
  LIST_FEEDBACK_PLAN_LINKS: 'list_feedback_plan_links',
  REPLACE_FEEDBACK_PLAN_LINKS: 'replace_feedback_plan_links',
  UPLOAD_FEEDBACK_ATTACHMENT: 'upload_feedback_attachment',
  DELETE_ATTACHMENT: 'delete_attachment',
  LIST_PLANS: 'list_plans',
  GET_PLAN: 'get_plan',
  LIST_TASKS: 'list_tasks',
  LIST_EXECUTORS: 'list_executors',
  RUN_EXECUTOR: 'run_executor',
  STOP_EXECUTOR: 'stop_executor',
  START_LOOP: 'start_loop',
  STOP_LOOP: 'stop_loop',
});

// These are the P06 operations whose implementation may be switched as one
// unit to the Go application boundary. The switch is injected by the trusted
// host, never selected by an MCP tool argument.
const P06_INTAKE_TOOL_NAMES = new Set([
  MCP_TOOL_NAMES.LIST_REQUIREMENTS,
  MCP_TOOL_NAMES.CREATE_REQUIREMENT,
  MCP_TOOL_NAMES.GET_REQUIREMENT,
  MCP_TOOL_NAMES.UPDATE_REQUIREMENT,
  MCP_TOOL_NAMES.DELETE_REQUIREMENT,
  MCP_TOOL_NAMES.LIST_REQUIREMENT_PLAN_LINKS,
  MCP_TOOL_NAMES.REPLACE_REQUIREMENT_PLAN_LINKS,
  MCP_TOOL_NAMES.UPLOAD_REQUIREMENT_ATTACHMENT,
  MCP_TOOL_NAMES.LIST_FEEDBACK,
  MCP_TOOL_NAMES.CREATE_FEEDBACK,
  MCP_TOOL_NAMES.GET_FEEDBACK,
  MCP_TOOL_NAMES.UPDATE_FEEDBACK,
  MCP_TOOL_NAMES.DELETE_FEEDBACK,
  MCP_TOOL_NAMES.LIST_FEEDBACK_PLAN_LINKS,
  MCP_TOOL_NAMES.REPLACE_FEEDBACK_PLAN_LINKS,
  MCP_TOOL_NAMES.UPLOAD_FEEDBACK_ATTACHMENT,
  MCP_TOOL_NAMES.DELETE_ATTACHMENT,
]);

const P06_GO_ONLY_TOOL_NAMES = new Set([
  MCP_TOOL_NAMES.GET_REQUIREMENT,
  MCP_TOOL_NAMES.UPDATE_REQUIREMENT,
  MCP_TOOL_NAMES.DELETE_REQUIREMENT,
  MCP_TOOL_NAMES.LIST_REQUIREMENT_PLAN_LINKS,
  MCP_TOOL_NAMES.REPLACE_REQUIREMENT_PLAN_LINKS,
  MCP_TOOL_NAMES.UPLOAD_REQUIREMENT_ATTACHMENT,
  MCP_TOOL_NAMES.GET_FEEDBACK,
  MCP_TOOL_NAMES.UPDATE_FEEDBACK,
  MCP_TOOL_NAMES.DELETE_FEEDBACK,
  MCP_TOOL_NAMES.LIST_FEEDBACK_PLAN_LINKS,
  MCP_TOOL_NAMES.REPLACE_FEEDBACK_PLAN_LINKS,
  MCP_TOOL_NAMES.UPLOAD_FEEDBACK_ATTACHMENT,
  MCP_TOOL_NAMES.DELETE_ATTACHMENT,
]);

// Go owner mode exposes only MCP operations with an explicit P002 runtime
// command. Keeping SQL-backed listing/configuration tools out of discovery is
// safer than advertising an operation that could accidentally reach sql.js.
const GO_DATA_CLIENT_TOOL_NAMES = new Set([
  MCP_TOOL_NAMES.RUN_EXECUTOR,
  MCP_TOOL_NAMES.STOP_EXECUTOR,
  MCP_TOOL_NAMES.START_LOOP,
  MCP_TOOL_NAMES.STOP_LOOP,
]);

const AGENT_CLI_PROVIDERS = Object.freeze(['codex', 'claude', 'opencode', 'oh-my-pi']);
const CODEX_REASONING_EFFORTS = Object.freeze(['low', 'medium', 'high', 'xhigh']);
const PLAN_GENERATION_STRATEGIES = Object.freeze([...planBackendConfig.PLAN_GENERATION_STRATEGIES]);
const PLAN_EXECUTION_STRATEGIES = Object.freeze([...planBackendConfig.PLAN_EXECUTION_STRATEGIES]);
const PLAN_BACKEND_PROVIDERS = Object.freeze([
  ...new Set([...AGENT_CLI_PROVIDERS, ...planBackendConfig.BUILTIN_LLM_PROVIDERS]),
]);
const INTAKE_STATUSES = Object.freeze(['open', 'completed', 'closed']);
const P06_INTAKE_STATUSES = Object.freeze(['draft', 'open', 'completed', 'closed']);
const PLAN_STATUSES = Object.freeze(['pending', 'running', 'ready_for_validation', 'completed', 'interrupted', 'draft']);
const TASK_STATUSES = Object.freeze(['pending', 'running', 'completed', 'blocked', 'failed', 'stopping', 'stopped', 'interrupted']);
const EXECUTOR_STATUSES = Object.freeze(['idle', 'running', 'ok', 'bad', 'stopped']);

const LIMITS = Object.freeze({
  name: 120,
  title: 200,
  body: 100000,
  description: 5000,
  workspacePath: 1000,
  command: 1000,
  model: 200,
  query: 200,
  rows: 200,
  attachmentName: 255,
  attachmentPath: 2000,
  attachmentData: 10 * 1024 * 1024,
  attachments: 20,
});

const ATTACHMENT_INPUT_SCHEMA = Object.freeze({
  type: 'object',
  additionalProperties: true,
  properties: {
    name: { type: 'string', minLength: 1, maxLength: LIMITS.attachmentName },
    size: { type: 'integer', minimum: 0 },
    source: { type: 'string', enum: ['path', 'clipboard-image'] },
    path: { type: 'string', minLength: 1, maxLength: LIMITS.attachmentPath },
    type: { type: 'string', minLength: 1, maxLength: 200, description: 'MIME type' },
    dataUrl: { type: 'string', minLength: 1, maxLength: LIMITS.attachmentData },
    base64: { type: 'string', minLength: 1, maxLength: LIMITS.attachmentData },
    dataBase64: { type: 'string', minLength: 1, maxLength: LIMITS.attachmentData },
    bytes: {
      type: 'array',
      maxItems: LIMITS.attachmentData,
      items: { type: 'integer', minimum: 0, maximum: 255 },
    },
  },
});

const COMMON_CLI_PROPERTIES = Object.freeze({
  agentCliProvider: {
    type: 'string',
    enum: AGENT_CLI_PROVIDERS,
    description: 'Legacy external CLI provider. Project-level input maps to generation and execution defaults when new fields are omitted; intake-level input maps to plan generation only.',
  },
  agentCliCommand: {
    type: 'string',
    maxLength: LIMITS.command,
    description: 'Legacy external CLI command override.',
  },
  codexReasoningEffort: {
    type: 'string',
    enum: CODEX_REASONING_EFFORTS,
    description: 'Legacy Codex reasoning effort. Only applies when the effective provider is codex.',
  },
});

const PLAN_GENERATION_PROPERTIES = Object.freeze({
  planGenerationStrategy: {
    type: 'string',
    enum: PLAN_GENERATION_STRATEGIES,
    description: 'Plan generation strategy: legacy external Markdown, external structured PlanSpec, or builtin LLM structured PlanSpec.',
  },
  planGenerationProvider: {
    type: 'string',
    enum: PLAN_BACKEND_PROVIDERS,
    description: 'Provider for plan generation. External strategies use CLI providers; builtin LLM uses AI providers.',
  },
  planGenerationCommand: {
    type: 'string',
    maxLength: LIMITS.command,
    description: 'Command for external CLI plan generation strategies.',
  },
  planGenerationModel: {
    type: 'string',
    maxLength: LIMITS.model,
    description: 'Model name for builtin LLM plan generation.',
  },
  planGenerationCodexReasoningEffort: {
    type: 'string',
    enum: CODEX_REASONING_EFFORTS,
    description: 'Codex reasoning effort for plan generation when the effective provider is codex.',
  },
});

const PLAN_EXECUTION_PROPERTIES = Object.freeze({
  planExecutionStrategy: {
    type: 'string',
    enum: PLAN_EXECUTION_STRATEGIES,
    description: 'Plan execution strategy. external-cli is supported; builtin-llm is accepted but task execution currently returns a clear unsupported error.',
  },
  planExecutionProvider: {
    type: 'string',
    enum: PLAN_BACKEND_PROVIDERS,
    description: 'Provider for plan execution. external-cli uses CLI providers.',
  },
  planExecutionCommand: {
    type: 'string',
    maxLength: LIMITS.command,
    description: 'Command for external CLI plan execution.',
  },
  planExecutionModel: {
    type: 'string',
    maxLength: LIMITS.model,
    description: 'Reserved model name for builtin LLM plan execution.',
  },
  planExecutionCodexReasoningEffort: {
    type: 'string',
    enum: CODEX_REASONING_EFFORTS,
    description: 'Codex reasoning effort for plan execution when the effective provider is codex.',
  },
});

const PROJECT_ID_SCHEMA = Object.freeze({ projectId: { type: 'integer', minimum: 1 } });
const LIMIT_SCHEMA = Object.freeze({ limit: { type: 'integer', minimum: 1, maximum: LIMITS.rows } });

const MCP_TOOL_DEFINITIONS = Object.freeze([
  {
    name: MCP_TOOL_NAMES.LIST_PROJECTS,
    title: 'List AutoPlan projects',
    description: 'List projects with runtime state summaries.',
    inputSchema: {
      type: 'object',
      additionalProperties: false,
      properties: {
        query: { type: 'string', maxLength: LIMITS.query },
        ...LIMIT_SCHEMA,
      },
    },
  },
  {
    name: MCP_TOOL_NAMES.GET_PROJECT,
    title: 'Get AutoPlan project',
    description: 'Get one project and its snapshot summary.',
    inputSchema: {
      type: 'object',
      additionalProperties: false,
      required: ['projectId'],
      properties: PROJECT_ID_SCHEMA,
    },
  },
  {
    name: MCP_TOOL_NAMES.CREATE_PROJECT,
    title: 'Create AutoPlan project',
    description: 'Create a project and return its id plus a snapshot summary.',
    inputSchema: {
      type: 'object',
      additionalProperties: false,
      required: ['name', 'workspacePath'],
      properties: {
        name: { type: 'string', minLength: 1, maxLength: LIMITS.name },
        workspacePath: { type: 'string', minLength: 1, maxLength: LIMITS.workspacePath },
        description: { type: 'string', maxLength: LIMITS.description },
        ...COMMON_CLI_PROPERTIES,
        ...PLAN_GENERATION_PROPERTIES,
        ...PLAN_EXECUTION_PROPERTIES,
      },
    },
  },
  {
    name: MCP_TOOL_NAMES.LIST_REQUIREMENTS,
    title: 'List AutoPlan requirements',
    description: 'List requirements for a project.',
    inputSchema: intakeListSchema(INTAKE_STATUSES),
  },
  {
    name: MCP_TOOL_NAMES.CREATE_REQUIREMENT,
    title: 'Create AutoPlan requirement',
    description: 'Create a requirement, optionally save attachments and start the project loop. Duplicate non-closed requirements with the same normalized title and body in the project are rejected with code DUPLICATE_INTAKE and existingRequirementId.',
    inputSchema: {
      type: 'object',
      additionalProperties: false,
      required: ['projectId', 'title', 'body'],
      properties: {
        ...PROJECT_ID_SCHEMA,
        title: { type: 'string', minLength: 1, maxLength: LIMITS.title },
        body: { type: 'string', minLength: 1, maxLength: LIMITS.body },
        attachments: attachmentArraySchema(),
        autoRun: { type: 'boolean' },
        status: { type: 'string', enum: INTAKE_STATUSES },
        ...COMMON_CLI_PROPERTIES,
        ...PLAN_GENERATION_PROPERTIES,
      },
    },
  },
  {
    name: MCP_TOOL_NAMES.GET_REQUIREMENT,
    title: 'Get AutoPlan requirement',
    description: 'Read one requirement through the P06 shared Intake service.',
    inputSchema: intakeItemSchema(),
  },
  {
    name: MCP_TOOL_NAMES.UPDATE_REQUIREMENT,
    title: 'Update AutoPlan requirement',
    description: 'Update one requirement through the P06 shared Intake service.',
    inputSchema: intakeUpdateSchema(false),
  },
  {
    name: MCP_TOOL_NAMES.DELETE_REQUIREMENT,
    title: 'Delete AutoPlan requirement',
    description: 'Delete one requirement through the P06 shared Intake service.',
    inputSchema: intakeItemSchema(),
  },
  {
    name: MCP_TOOL_NAMES.LIST_REQUIREMENT_PLAN_LINKS,
    title: 'List requirement plan links',
    description: 'List ordered plan links for one requirement.',
    inputSchema: intakeItemSchema(),
  },
  {
    name: MCP_TOOL_NAMES.REPLACE_REQUIREMENT_PLAN_LINKS,
    title: 'Replace requirement plan links',
    description: 'Atomically replace ordered plan links for one requirement.',
    inputSchema: replacePlanLinksSchema(),
  },
  {
    name: MCP_TOOL_NAMES.UPLOAD_REQUIREMENT_ATTACHMENT,
    title: 'Upload requirement attachment',
    description: 'Upload one P06 policy-checked attachment for an existing requirement.',
    inputSchema: intakeAttachmentSchema(),
  },
  {
    name: MCP_TOOL_NAMES.LIST_FEEDBACK,
    title: 'List AutoPlan feedback',
    description: 'List feedback for a project.',
    inputSchema: intakeListSchema(INTAKE_STATUSES),
  },
  {
    name: MCP_TOOL_NAMES.CREATE_FEEDBACK,
    title: 'Create AutoPlan feedback',
    description: 'Create feedback, optionally associate it with a requirement and start the project loop. Duplicate non-closed feedback with the same normalized title and body in the same project and requirement association is rejected with code DUPLICATE_INTAKE and existingFeedbackId.',
    inputSchema: {
      type: 'object',
      additionalProperties: false,
      required: ['projectId', 'title', 'body'],
      properties: {
        ...PROJECT_ID_SCHEMA,
        requirementId: { type: ['integer', 'null'], minimum: 1 },
        title: { type: 'string', minLength: 1, maxLength: LIMITS.title },
        body: { type: 'string', minLength: 1, maxLength: LIMITS.body },
        attachments: attachmentArraySchema(),
        autoRun: { type: 'boolean' },
        status: { type: 'string', enum: INTAKE_STATUSES },
        ...COMMON_CLI_PROPERTIES,
        ...PLAN_GENERATION_PROPERTIES,
      },
    },
  },
  {
    name: MCP_TOOL_NAMES.GET_FEEDBACK,
    title: 'Get AutoPlan feedback',
    description: 'Read one feedback record through the P06 shared Intake service.',
    inputSchema: intakeItemSchema(),
  },
  {
    name: MCP_TOOL_NAMES.UPDATE_FEEDBACK,
    title: 'Update AutoPlan feedback',
    description: 'Update one feedback record through the P06 shared Intake service.',
    inputSchema: intakeUpdateSchema(true),
  },
  {
    name: MCP_TOOL_NAMES.DELETE_FEEDBACK,
    title: 'Delete AutoPlan feedback',
    description: 'Delete one feedback record through the P06 shared Intake service.',
    inputSchema: intakeItemSchema(),
  },
  {
    name: MCP_TOOL_NAMES.LIST_FEEDBACK_PLAN_LINKS,
    title: 'List feedback plan links',
    description: 'List ordered plan links for one feedback record.',
    inputSchema: intakeItemSchema(),
  },
  {
    name: MCP_TOOL_NAMES.REPLACE_FEEDBACK_PLAN_LINKS,
    title: 'Replace feedback plan links',
    description: 'Atomically replace ordered plan links for one feedback record.',
    inputSchema: replacePlanLinksSchema(),
  },
  {
    name: MCP_TOOL_NAMES.UPLOAD_FEEDBACK_ATTACHMENT,
    title: 'Upload feedback attachment',
    description: 'Upload one P06 policy-checked attachment for existing feedback.',
    inputSchema: intakeAttachmentSchema(),
  },
  {
    name: MCP_TOOL_NAMES.DELETE_ATTACHMENT,
    title: 'Delete attachment',
    description: 'Delete one P06 attachment by project-scoped identifier.',
    inputSchema: attachmentDeleteSchema(),
  },
  {
    name: MCP_TOOL_NAMES.LIST_PLANS,
    title: 'List AutoPlan plans',
    description: 'List plans for a project.',
    inputSchema: intakeListSchema(PLAN_STATUSES),
  },
  {
    name: MCP_TOOL_NAMES.GET_PLAN,
    title: 'Get AutoPlan plan',
    description: 'Get one plan and its tasks.',
    inputSchema: {
      type: 'object',
      additionalProperties: false,
      required: ['projectId', 'planId'],
      properties: {
        ...PROJECT_ID_SCHEMA,
        planId: { type: 'integer', minimum: 1 },
      },
    },
  },
  {
    name: MCP_TOOL_NAMES.LIST_TASKS,
    title: 'List AutoPlan tasks',
    description: 'List tasks for a project, optionally filtered by plan or status.',
    inputSchema: {
      type: 'object',
      additionalProperties: false,
      required: ['projectId'],
      properties: {
        ...PROJECT_ID_SCHEMA,
        planId: { type: 'integer', minimum: 1 },
        status: { type: 'string', enum: TASK_STATUSES },
        ...LIMIT_SCHEMA,
      },
    },
  },
  {
    name: MCP_TOOL_NAMES.LIST_EXECUTORS,
    title: 'List AutoPlan executors',
    description: 'List configured workspace executors for a project, including recent status and log tails. Executors run only saved commands in the project workspace; debug launch/configuration is not supported.',
    inputSchema: {
      type: 'object',
      additionalProperties: false,
      required: ['projectId'],
      properties: {
        ...PROJECT_ID_SCHEMA,
        label: { type: 'string', maxLength: LIMITS.title, description: 'Optional label substring filter.' },
        group: { type: 'string', maxLength: LIMITS.name, description: 'Optional exact group kind filter, for example build or test.' },
        status: { type: 'string', enum: EXECUTOR_STATUSES, description: 'Optional current or recent run status filter.' },
        enabled: { type: 'boolean', description: 'Optional enabled-state filter.' },
        ...LIMIT_SCHEMA,
      },
    },
  },
  {
    name: MCP_TOOL_NAMES.RUN_EXECUTOR,
    title: 'Run AutoPlan executor',
    description: 'Run an existing executor by executorId or exact label. This tool does not accept arbitrary command strings, returns status with a log tail, and has no debug support.',
    inputSchema: executorActionSchema(),
  },
  {
    name: MCP_TOOL_NAMES.STOP_EXECUTOR,
    title: 'Stop AutoPlan executor',
    description: 'Stop a running executor by executorId or exact label. This only targets existing executor operations and has no debug support.',
    inputSchema: executorActionSchema(),
  },
  {
    name: MCP_TOOL_NAMES.START_LOOP,
    title: 'Start AutoPlan loop',
    description: 'Start the project loop.',
    inputSchema: projectActionSchema(),
  },
  {
    name: MCP_TOOL_NAMES.STOP_LOOP,
    title: 'Stop AutoPlan loop',
    description: 'Stop the project loop.',
    inputSchema: projectActionSchema(),
  },
]);

async function registerMcpTools(server, context = {}) {
  const { CallToolRequestSchema, ListToolsRequestSchema } = await import('@modelcontextprotocol/sdk/types.js');
  server.setRequestHandler(ListToolsRequestSchema, async () => ({
    tools: (context?.legacyAdapterDisabled ? [] : visibleMcpToolDefinitions(context)).map((tool) => ({
      name: tool.name,
      title: tool.title,
      description: tool.description,
      inputSchema: tool.inputSchema,
    })),
  }));
  server.setRequestHandler(CallToolRequestSchema, async (request) => callMcpTool(
    request.params?.name,
    request.params?.arguments || {},
    context,
  ));
}

async function callMcpTool(name, input = {}, context = {}) {
  if (context?.legacyAdapterDisabled) return toolError(serviceUnavailableError());
  const tool = visibleMcpToolDefinitions(context).find((item) => item.name === name);
  if (!tool) throw new Error(`Unknown MCP tool: ${name || ''}`);

  try {
    const validated = validateToolInput(tool.name, input, {
      p06Transport: Boolean(context?.goIntakeTransport),
    });
    const result = await runTool(tool.name, validated, context);
    return toolResult(result);
  } catch (error) {
    return toolError(error);
  }
}

function serviceUnavailableError() {
  const error = new Error('service_unavailable');
  error.code = 'service_unavailable';
  return error;
}

async function runTool(name, input, context) {
  if (context?.goDataClient) return runGoDataClientTool(context.goDataClient, name, input);
  const p06Transport = resolveP06IntakeTransport(context, name);
  if (p06Transport) return runP06IntakeTool(p06Transport, name, input, context);
  if (name === MCP_TOOL_NAMES.LIST_PROJECTS) {
    const db = requiredDb(context);
    return {
      projects: listProjectRows(db, input).map((project) => projectSummary(project, context.loop)),
    };
  }
  if (name === MCP_TOOL_NAMES.GET_PROJECT) {
    const loop = requiredLoop(context);
    const project = requiredProject(loop, input.projectId);
    return {
      project: projectSummary(project, loop),
      snapshot: snapshotSummary(loop.snapshot(input.projectId)),
    };
  }
  if (name === MCP_TOOL_NAMES.CREATE_PROJECT) {
    const service = requiredIntakeService(context);
    const snapshot = service.createProject(input);
    return {
      projectId: Number(snapshot.activeProjectId || snapshot.activeProject?.id || 0),
      snapshot: snapshotSummary(snapshot),
    };
  }
  if (name === MCP_TOOL_NAMES.LIST_REQUIREMENTS) {
    requiredProject(requiredLoop(context), input.projectId);
    const db = requiredDb(context);
    return {
      projectId: input.projectId,
      requirements: listIntakeRows(db, 'requirements', input).map((row) => intakeDetail(row, db, 'requirement')),
    };
  }
  if (name === MCP_TOOL_NAMES.CREATE_REQUIREMENT) {
    const service = requiredIntakeService(context);
    const snapshot = service.createRequirement(input);
    const db = context.db || service.db || null;
    const requirement = latestIntake(db, 'requirements', input.projectId);
    return {
      projectId: input.projectId,
      requirementId: requirement?.id || null,
      requirement: intakeSummary(requirement, db, 'requirement'),
      openable: intakeOpenable('requirement', input.projectId, requirement),
      snapshot: snapshotSummary(snapshot),
    };
  }
  if (name === MCP_TOOL_NAMES.LIST_FEEDBACK) {
    requiredProject(requiredLoop(context), input.projectId);
    const db = requiredDb(context);
    return {
      projectId: input.projectId,
      feedback: listIntakeRows(db, 'feedback', input).map((row) => intakeDetail(row, db, 'feedback')),
    };
  }
  if (name === MCP_TOOL_NAMES.CREATE_FEEDBACK) {
    const service = requiredIntakeService(context);
    const snapshot = service.createFeedback(input);
    const db = context.db || service.db || null;
    const feedback = latestIntake(db, 'feedback', input.projectId);
    return {
      projectId: input.projectId,
      feedbackId: feedback?.id || null,
      feedback: intakeSummary(feedback, db, 'feedback'),
      openable: intakeOpenable('feedback', input.projectId, feedback),
      snapshot: snapshotSummary(snapshot),
    };
  }
  if (name === MCP_TOOL_NAMES.LIST_PLANS) {
    requiredProject(requiredLoop(context), input.projectId);
    return {
      projectId: input.projectId,
      plans: listPlanRows(requiredDb(context), input).map((plan) => planSummary(plan)),
    };
  }
  if (name === MCP_TOOL_NAMES.GET_PLAN) {
    requiredProject(requiredLoop(context), input.projectId);
    const db = requiredDb(context);
    const plan = requiredPlan(db, input.projectId, input.planId);
    return {
      projectId: input.projectId,
      plan: planSummary(plan),
      tasks: listTaskRows(db, { projectId: input.projectId, planId: input.planId, limit: LIMITS.rows }).map((task) => taskSummary(task)),
    };
  }
  if (name === MCP_TOOL_NAMES.LIST_TASKS) {
    requiredProject(requiredLoop(context), input.projectId);
    const db = requiredDb(context);
    if (input.planId) requiredPlan(db, input.projectId, input.planId);
    return {
      projectId: input.projectId,
      tasks: listTaskRows(db, input).map((task) => taskSummary(task)),
    };
  }
  if (name === MCP_TOOL_NAMES.LIST_EXECUTORS) {
    const loop = requiredLoop(context);
    requiredProject(loop, input.projectId);
    const executors = listExecutorRows(requiredDb(context), input)
      .map((executor) => executorSummary(executor, loop))
      .filter((executor) => !input.status || executor.status === input.status)
      .slice(0, input.limit || 100);
    return {
      projectId: input.projectId,
      executors,
    };
  }
  if (name === MCP_TOOL_NAMES.RUN_EXECUTOR) {
    const loop = requiredLoop(context);
    requiredProject(loop, input.projectId);
    const executor = requiredExecutor(requiredDb(context), input.projectId, input);
    const result = await loop.runExecutor(input.projectId, executor.id);
    return executorRunResultSummary(result, executor, loop);
  }
  if (name === MCP_TOOL_NAMES.STOP_EXECUTOR) {
    const loop = requiredLoop(context);
    requiredProject(loop, input.projectId);
    const executor = requiredExecutor(requiredDb(context), input.projectId, input);
    const result = loop.stopExecutor(input.projectId, executor.id);
    return {
      projectId: input.projectId,
      executor: executorSummary(executor, loop),
      stopped: Number(result?.stopped || 0),
      snapshot: snapshotSummary(loop.snapshot(input.projectId)),
    };
  }
  if (name === MCP_TOOL_NAMES.START_LOOP) {
    const loop = requiredLoop(context);
    requiredProject(loop, input.projectId);
    loop.start(input.projectId);
    return {
      projectId: input.projectId,
      snapshot: snapshotSummary(loop.snapshot(input.projectId)),
    };
  }
  if (name === MCP_TOOL_NAMES.STOP_LOOP) {
    const loop = requiredLoop(context);
    requiredProject(loop, input.projectId);
    loop.stop(input.projectId);
    return {
      projectId: input.projectId,
      snapshot: snapshotSummary(loop.snapshot(input.projectId)),
    };
  }
  throw new Error(`Unknown MCP tool: ${name}`);
}

function visibleMcpToolDefinitions(context = {}) {
  if (context?.goDataClient) {
    return MCP_TOOL_DEFINITIONS.filter((tool) => GO_DATA_CLIENT_TOOL_NAMES.has(tool.name));
  }
  const goTransportEnabled = Boolean(context?.goIntakeTransport);
  const tools = goTransportEnabled
    ? MCP_TOOL_DEFINITIONS
    : MCP_TOOL_DEFINITIONS.filter((tool) => !P06_GO_ONLY_TOOL_NAMES.has(tool.name));
  return goTransportEnabled ? tools.map(p06TransportDefinition) : tools;
}

async function runGoDataClientTool(client, name, input) {
  if (!client || typeof client !== 'object') {
    const error = new Error('service_unavailable');
    error.code = 'service_unavailable';
    throw error;
  }
  let result;
  switch (name) {
    case MCP_TOOL_NAMES.START_LOOP:
      result = await client.startLoop(input.projectId);
      break;
    case MCP_TOOL_NAMES.STOP_LOOP:
      result = await client.stopLoop(input.projectId);
      break;
    case MCP_TOOL_NAMES.RUN_EXECUTOR:
      result = await client.runExecutor(input.projectId, input.executorId);
      break;
    case MCP_TOOL_NAMES.STOP_EXECUTOR:
      result = await client.stopExecutor(input.projectId, input.executorId);
      break;
    default: {
      const error = new Error('service_unavailable');
      error.code = 'service_unavailable';
      throw error;
    }
  }
  return {
    projectId: input.projectId,
    operation: result?.operation || null,
    snapshot: result?.snapshot ? snapshotSummary(result.snapshot) : null,
  };
}

function p06TransportDefinition(tool) {
  if (!P06_INTAKE_TOOL_NAMES.has(tool.name) || !tool.inputSchema?.properties?.status) return tool;
  return {
    ...tool,
    inputSchema: {
      ...tool.inputSchema,
      properties: {
        ...tool.inputSchema.properties,
        status: { ...tool.inputSchema.properties.status, enum: P06_INTAKE_STATUSES },
      },
    },
  };
}

function resolveP06IntakeTransport(context, name) {
  if (!P06_INTAKE_TOOL_NAMES.has(name)) return null;
  const transport = context?.goIntakeTransport;
  if (transport === undefined || transport === null) return null;
  if (typeof transport.call !== 'function') {
    const error = new Error('P06 Intake MCP transport is unavailable');
    error.code = 'service_unavailable';
    throw error;
  }
  return transport;
}

async function runP06IntakeTool(transport, name, input, context) {
  if ((name === MCP_TOOL_NAMES.CREATE_REQUIREMENT || name === MCP_TOOL_NAMES.CREATE_FEEDBACK)
      && Array.isArray(input.attachments) && input.attachments.length > 0) {
    const error = new Error('P06 attachment upload requires an existing Intake owner');
    error.code = 'invalid_attachment';
    throw error;
  }
  // The migration transport is authoritative once present. In particular, a
  // failure here must never fall through to Node/sql.js and create a second
  // writer for the same intent.
  try {
    const result = await transport.call(name, input, {
      callerScope: String(context?.mcpCallerScope || 'mcp-local'),
      localCaller: Boolean(context?.mcpLocalCaller),
    });
    return sanitizeP06Result(result);
  } catch (error) {
    // A Go-side adapter is allowed to surface only its stable code. Transport
    // failures can contain endpoint URLs, request bodies, credentials, or a
    // caller path, so turn every unknown failure into one generic code before
    // it reaches the MCP response or a host logger.
    const code = p06FailureCode(error);
    const failure = new Error(code);
    failure.code = code;
    throw failure;
  }
}

function p06FailureCode(error) {
  return isP06ToolError(error) ? error.code : 'service_unavailable';
}

function sanitizeP06Result(value, depth = 0) {
  if (depth > 32 || value === null || value === undefined ||
      typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') return value;
  if (Buffer.isBuffer(value) || value instanceof ArrayBuffer || ArrayBuffer.isView(value)) return null;
  if (Array.isArray(value)) return value.map((item) => sanitizeP06Result(item, depth + 1));
  if (typeof value !== 'object') return null;
  const result = {};
  for (const [key, item] of Object.entries(value)) {
    const normalized = key.replace(/[^a-z0-9]/gi, '').toLowerCase();
    if (normalized.includes('storedpath') || normalized === 'hash' || normalized.endsWith('hash') ||
        normalized.includes('token') || normalized.includes('secret') || normalized.includes('credential') ||
        normalized === 'path' || normalized.endsWith('filepath') || normalized.endsWith('sourcepath') ||
        normalized === 'bytes' || normalized.endsWith('bytes') || normalized === 'base64' ||
        normalized.endsWith('base64') || normalized === 'dataurl') continue;
    result[key] = sanitizeP06Result(item, depth + 1);
  }
  return result;
}

function validateToolInput(name, input, options = {}) {
  assertPlainObject(input, 'Input must be an object');
  const intakeStatuses = options.p06Transport ? P06_INTAKE_STATUSES : INTAKE_STATUSES;
  if (name === MCP_TOOL_NAMES.LIST_PROJECTS) return validateListProjectsInput(input);
  if (name === MCP_TOOL_NAMES.GET_PROJECT) return validateProjectIdInput(input);
  if (name === MCP_TOOL_NAMES.CREATE_PROJECT) return validateCreateProjectInput(input);
  if (name === MCP_TOOL_NAMES.LIST_REQUIREMENTS) return validateListIntakesInput(input, intakeStatuses);
  if (name === MCP_TOOL_NAMES.CREATE_REQUIREMENT) return validateCreateRequirementInput(input, intakeStatuses);
  if (name === MCP_TOOL_NAMES.GET_REQUIREMENT || name === MCP_TOOL_NAMES.DELETE_REQUIREMENT ||
      name === MCP_TOOL_NAMES.LIST_REQUIREMENT_PLAN_LINKS) return validateIntakeItemInput(input);
  if (name === MCP_TOOL_NAMES.UPDATE_REQUIREMENT) return validateIntakeUpdateInput(input, false);
  if (name === MCP_TOOL_NAMES.REPLACE_REQUIREMENT_PLAN_LINKS) return validateReplacePlanLinksInput(input);
  if (name === MCP_TOOL_NAMES.UPLOAD_REQUIREMENT_ATTACHMENT) return validateIntakeAttachmentInput(input);
  if (name === MCP_TOOL_NAMES.LIST_FEEDBACK) return validateListIntakesInput(input, intakeStatuses);
  if (name === MCP_TOOL_NAMES.CREATE_FEEDBACK) return validateCreateFeedbackInput(input, intakeStatuses);
  if (name === MCP_TOOL_NAMES.GET_FEEDBACK || name === MCP_TOOL_NAMES.DELETE_FEEDBACK ||
      name === MCP_TOOL_NAMES.LIST_FEEDBACK_PLAN_LINKS) return validateIntakeItemInput(input);
  if (name === MCP_TOOL_NAMES.UPDATE_FEEDBACK) return validateIntakeUpdateInput(input, true);
  if (name === MCP_TOOL_NAMES.REPLACE_FEEDBACK_PLAN_LINKS) return validateReplacePlanLinksInput(input);
  if (name === MCP_TOOL_NAMES.UPLOAD_FEEDBACK_ATTACHMENT) return validateIntakeAttachmentInput(input);
  if (name === MCP_TOOL_NAMES.DELETE_ATTACHMENT) return validateAttachmentDeleteInput(input);
  if (name === MCP_TOOL_NAMES.LIST_PLANS) return validateListPlansInput(input);
  if (name === MCP_TOOL_NAMES.GET_PLAN) return validateGetPlanInput(input);
  if (name === MCP_TOOL_NAMES.LIST_TASKS) return validateListTasksInput(input);
  if (name === MCP_TOOL_NAMES.LIST_EXECUTORS) return validateListExecutorsInput(input);
  if (name === MCP_TOOL_NAMES.RUN_EXECUTOR) return validateExecutorActionInput(input);
  if (name === MCP_TOOL_NAMES.STOP_EXECUTOR) return validateExecutorActionInput(input);
  if (name === MCP_TOOL_NAMES.START_LOOP) return validateProjectIdInput(input);
  if (name === MCP_TOOL_NAMES.STOP_LOOP) return validateProjectIdInput(input);
  throw new Error(`Unknown MCP tool: ${name}`);
}

function validateListProjectsInput(input) {
  return stripUndefined({
    query: optionalString(input, 'query', LIMITS.query),
    limit: optionalLimit(input),
  });
}

function validateProjectIdInput(input) {
  return {
    projectId: requiredPositiveInteger(input, 'projectId'),
  };
}

function validateCreateProjectInput(input) {
  return stripUndefined({
    name: requiredString(input, 'name', LIMITS.name),
    workspacePath: requiredString(input, 'workspacePath', LIMITS.workspacePath),
    description: optionalString(input, 'description', LIMITS.description),
    ...validatedAgentCliInput(input),
    ...validatedPlanGenerationInput(input),
    ...validatedPlanExecutionInput(input),
  });
}

function validateListIntakesInput(input, statuses = INTAKE_STATUSES) {
  return stripUndefined({
    projectId: requiredPositiveInteger(input, 'projectId'),
    status: optionalEnum(input, 'status', statuses),
    limit: optionalLimit(input),
  });
}

function validateCreateRequirementInput(input, statuses = INTAKE_STATUSES) {
  assertNoPlanExecutionInput(input);
  return stripUndefined({
    projectId: requiredPositiveInteger(input, 'projectId'),
    title: requiredString(input, 'title', LIMITS.title),
    body: requiredString(input, 'body', LIMITS.body),
    attachments: optionalAttachments(input),
    autoRun: optionalBoolean(input, 'autoRun'),
    status: optionalEnum(input, 'status', statuses),
    ...validatedAgentCliInput(input),
    ...validatedPlanGenerationInput(input),
  });
}

function validateCreateFeedbackInput(input, statuses = INTAKE_STATUSES) {
  assertNoPlanExecutionInput(input);
  return stripUndefined({
    projectId: requiredPositiveInteger(input, 'projectId'),
    requirementId: optionalPositiveInteger(input, 'requirementId', { nullable: true }),
    title: requiredString(input, 'title', LIMITS.title),
    body: requiredString(input, 'body', LIMITS.body),
    attachments: optionalAttachments(input),
    autoRun: optionalBoolean(input, 'autoRun'),
    status: optionalEnum(input, 'status', statuses),
    ...validatedAgentCliInput(input),
    ...validatedPlanGenerationInput(input),
  });
}

function validateListPlansInput(input) {
  return stripUndefined({
    projectId: requiredPositiveInteger(input, 'projectId'),
    status: optionalEnum(input, 'status', PLAN_STATUSES),
    limit: optionalLimit(input),
  });
}

function validateGetPlanInput(input) {
  return {
    projectId: requiredPositiveInteger(input, 'projectId'),
    planId: requiredPositiveInteger(input, 'planId'),
  };
}

function validateListTasksInput(input) {
  return stripUndefined({
    projectId: requiredPositiveInteger(input, 'projectId'),
    planId: optionalPositiveInteger(input, 'planId'),
    status: optionalEnum(input, 'status', TASK_STATUSES),
    limit: optionalLimit(input),
  });
}

function validateListExecutorsInput(input) {
  assertOnlyKeys(input, ['projectId', 'label', 'group', 'status', 'enabled', 'limit']);
  return stripUndefined({
    projectId: requiredPositiveInteger(input, 'projectId'),
    label: optionalString(input, 'label', LIMITS.title),
    group: optionalString(input, 'group', LIMITS.name),
    status: optionalEnum(input, 'status', EXECUTOR_STATUSES),
    enabled: optionalBoolean(input, 'enabled'),
    limit: optionalLimit(input),
  });
}

function validateExecutorActionInput(input) {
  assertOnlyKeys(input, ['projectId', 'executorId', 'label']);
  const validated = stripUndefined({
    projectId: requiredPositiveInteger(input, 'projectId'),
    executorId: optionalPositiveInteger(input, 'executorId'),
    label: optionalString(input, 'label', LIMITS.title),
  });
  if (!validated.executorId && !validated.label) throw new Error('executorId or label is required');
  return validated;
}

function validatedAgentCliInput(input) {
  return stripUndefined({
    agentCliProvider: optionalEnum(input, 'agentCliProvider', AGENT_CLI_PROVIDERS),
    agentCliCommand: optionalString(input, 'agentCliCommand', LIMITS.command),
    codexReasoningEffort: optionalEnum(input, 'codexReasoningEffort', CODEX_REASONING_EFFORTS),
  });
}

function validatedPlanGenerationInput(input) {
  return stripUndefined({
    planGenerationStrategy: optionalEnum(input, 'planGenerationStrategy', PLAN_GENERATION_STRATEGIES),
    planGenerationProvider: optionalEnum(input, 'planGenerationProvider', PLAN_BACKEND_PROVIDERS),
    planGenerationCommand: optionalString(input, 'planGenerationCommand', LIMITS.command),
    planGenerationModel: optionalString(input, 'planGenerationModel', LIMITS.model),
    planGenerationCodexReasoningEffort: optionalEnum(
      input,
      'planGenerationCodexReasoningEffort',
      CODEX_REASONING_EFFORTS,
    ),
  });
}

function validatedPlanExecutionInput(input) {
  return stripUndefined({
    planExecutionStrategy: optionalEnum(input, 'planExecutionStrategy', PLAN_EXECUTION_STRATEGIES),
    planExecutionProvider: optionalEnum(input, 'planExecutionProvider', PLAN_BACKEND_PROVIDERS),
    planExecutionCommand: optionalString(input, 'planExecutionCommand', LIMITS.command),
    planExecutionModel: optionalString(input, 'planExecutionModel', LIMITS.model),
    planExecutionCodexReasoningEffort: optionalEnum(
      input,
      'planExecutionCodexReasoningEffort',
      CODEX_REASONING_EFFORTS,
    ),
  });
}

function optionalAttachments(input) {
  if (input.attachments === undefined) return [];
  if (!Array.isArray(input.attachments)) throw new Error('attachments must be an array');
  if (input.attachments.length > LIMITS.attachments) throw new Error(`attachments supports at most ${LIMITS.attachments} items`);
  return input.attachments.map((attachment, index) => validateAttachment(attachment, index));
}

function validateAttachment(attachment, index) {
  assertPlainObject(attachment, `attachments[${index}] must be an object`);
  const normalized = stripUndefined({
    name: optionalString(attachment, 'name', LIMITS.attachmentName),
    size: optionalNonNegativeInteger(attachment, 'size'),
    source: optionalEnum(attachment, 'source', ['path', 'clipboard-image']),
    path: optionalString(attachment, 'path', LIMITS.attachmentPath),
    type: optionalString(attachment, 'type', 200),
    dataUrl: optionalString(attachment, 'dataUrl', LIMITS.attachmentData),
    base64: optionalString(attachment, 'base64', LIMITS.attachmentData),
    dataBase64: optionalString(attachment, 'dataBase64', LIMITS.attachmentData),
    bytes: optionalByteArray(attachment, 'bytes'),
  });
  if (!normalized.path && !normalized.dataUrl && !normalized.base64 && !normalized.dataBase64 && !normalized.bytes && !normalized.name) {
    throw new Error(`attachments[${index}] needs name, path, dataUrl, base64, dataBase64, or bytes`);
  }
  return normalized;
}

function requiredString(input, key, maxLength) {
  if (!Object.prototype.hasOwnProperty.call(input, key)) throw new Error(`${key} is required`);
  const value = stringValue(input[key], key, maxLength);
  if (!value) throw new Error(`${key} cannot be empty`);
  return value;
}

function optionalString(input, key, maxLength) {
  if (input[key] === undefined || input[key] === null) return undefined;
  return stringValue(input[key], key, maxLength);
}

function stringValue(value, key, maxLength) {
  if (typeof value !== 'string') throw new Error(`${key} must be a string`);
  const trimmed = value.trim();
  if (trimmed.length > maxLength) throw new Error(`${key} cannot exceed ${maxLength} characters`);
  return trimmed;
}

function requiredPositiveInteger(input, key) {
  const value = positiveIntegerValue(input[key], key);
  if (!value) throw new Error(`${key} must be an integer greater than 0`);
  return value;
}

function optionalPositiveInteger(input, key, options = {}) {
  if (input[key] === undefined) return undefined;
  if (input[key] === null && options.nullable) return null;
  const value = positiveIntegerValue(input[key], key);
  if (!value) throw new Error(`${key} must be an integer greater than 0`);
  return value;
}

function positiveIntegerValue(value, key) {
  if (typeof value !== 'number' || !Number.isInteger(value)) throw new Error(`${key} must be an integer`);
  return value > 0 ? value : 0;
}

function optionalLimit(input) {
  if (input.limit === undefined || input.limit === null) return 100;
  if (typeof input.limit !== 'number' || !Number.isInteger(input.limit) || input.limit < 1 || input.limit > LIMITS.rows) {
    throw new Error(`limit must be an integer from 1 to ${LIMITS.rows}`);
  }
  return input.limit;
}

function optionalBoolean(input, key) {
  if (input[key] === undefined || input[key] === null) return undefined;
  if (typeof input[key] !== 'boolean') throw new Error(`${key} must be a boolean`);
  return input[key];
}

function optionalNonNegativeInteger(input, key) {
  if (input[key] === undefined || input[key] === null) return undefined;
  if (typeof input[key] !== 'number' || !Number.isInteger(input[key]) || input[key] < 0) {
    throw new Error(`${key} must be a non-negative integer`);
  }
  return input[key];
}

function optionalEnum(input, key, values) {
  if (input[key] === undefined || input[key] === null) return undefined;
  if (typeof input[key] !== 'string') throw new Error(`${key} must be a string`);
  if (!values.includes(input[key])) throw new Error(`${key} must be one of: ${values.join(', ')}`);
  return input[key];
}

function optionalByteArray(input, key) {
  if (input[key] === undefined || input[key] === null) return undefined;
  if (!Array.isArray(input[key])) throw new Error(`${key} must be a byte array`);
  if (input[key].length > LIMITS.attachmentData) throw new Error(`${key} cannot exceed ${LIMITS.attachmentData} bytes`);
  for (const value of input[key]) {
    if (!Number.isInteger(value) || value < 0 || value > 255) throw new Error(`${key} items must be integers from 0 to 255`);
  }
  return input[key];
}

function assertPlainObject(value, message) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(message);
}

function assertOnlyKeys(input, allowedKeys) {
  const allowed = new Set(allowedKeys);
  const unknown = Object.keys(input || {}).filter((key) => !allowed.has(key));
  if (unknown.length > 0) throw new Error(`Unsupported input fields: ${unknown.join(', ')}`);
}

function assertNoPlanExecutionInput(input = {}) {
  const planExecutionKeys = new Set([
    ...planBackendConfig.PLAN_EXECUTION_STRATEGY_KEYS,
    ...planBackendConfig.PLAN_EXECUTION_PROVIDER_KEYS,
    ...planBackendConfig.PLAN_EXECUTION_COMMAND_KEYS,
    ...planBackendConfig.PLAN_EXECUTION_MODEL_KEYS,
    ...planBackendConfig.PLAN_EXECUTION_CODEX_REASONING_EFFORT_KEYS,
  ]);
  const unsupported = Object.keys(input || {}).filter((key) => planExecutionKeys.has(key));
  if (unsupported.length > 0) {
    throw new Error(`Plan execution overrides are not supported for single intake creation: ${unsupported.join(', ')}`);
  }
}

function stripUndefined(value) {
  return Object.fromEntries(Object.entries(value).filter((entry) => entry[1] !== undefined));
}

function requiredDb(context) {
  if (!context?.db) throw new Error('MCP database is not initialized');
  return context.db;
}

function requiredLoop(context) {
  if (!context?.loop) throw new Error('MCP loop service is not initialized');
  return context.loop;
}

function requiredIntakeService(context) {
  if (!context?.intakeService) throw new Error('MCP intake service is not initialized');
  return context.intakeService;
}

function requiredProject(loop, projectId) {
  if (!loop) throw new Error('MCP loop service is not initialized');
  const project = loop.project(projectId);
  if (!project) throw new Error('Project not found');
  return project;
}

function requiredPlan(db, projectId, planId) {
  const plan = db.get('SELECT * FROM plans WHERE id = ? AND project_id = ?', [planId, projectId]);
  if (!plan) throw new Error('Plan not found');
  return plan;
}

function latestIntake(db, table, projectId) {
  if (!db) return null;
  return db.get(`SELECT * FROM ${table} WHERE project_id = ? ORDER BY id DESC LIMIT 1`, [projectId]);
}

function listProjectRows(db, input) {
  const params = [];
  let where = '';
  if (input.query) {
    where = 'WHERE name LIKE ? OR workspace_path LIKE ? OR description LIKE ?';
    const query = `%${input.query}%`;
    params.push(query, query, query);
  }
  params.push(input.limit || 100);
  return db.all(`SELECT * FROM projects ${where} ORDER BY updated_at DESC, id DESC LIMIT ?`, params);
}

function listIntakeRows(db, table, input) {
  const params = [input.projectId];
  let statusClause = '';
  if (input.status) {
    statusClause = ' AND status = ?';
    params.push(input.status);
  }
  params.push(input.limit || 100);
  return db.all(
    `SELECT * FROM ${table}
     WHERE project_id = ?${statusClause}
     ORDER BY updated_at DESC, id DESC
     LIMIT ?`,
    params,
  );
}

function listPlanRows(db, input) {
  const params = [input.projectId];
  let statusClause = '';
  if (input.status) {
    statusClause = ' AND status = ?';
    params.push(input.status);
  }
  params.push(input.limit || 100);
  return db.all(
    `SELECT * FROM plans
     WHERE project_id = ?${statusClause}
     ORDER BY sort_order ASC, created_at ASC, id ASC
     LIMIT ?`,
    params,
  );
}

function listTaskRows(db, input) {
  const params = [input.projectId];
  let filterClause = '';
  if (input.planId) {
    filterClause += ' AND plan_tasks.plan_id = ?';
    params.push(input.planId);
  }
  if (input.status) {
    filterClause += ' AND plan_tasks.status = ?';
    params.push(input.status);
  }
  params.push(input.limit || 100);
  return db.all(
    `SELECT plan_tasks.*, plans.project_id, plans.file_path AS plan_file_path
     FROM plan_tasks
     JOIN plans ON plans.id = plan_tasks.plan_id
     WHERE plans.project_id = ?${filterClause}
     ORDER BY plans.sort_order ASC, plans.created_at ASC, plans.id ASC,
              plan_tasks.sort_order ASC, plan_tasks.id ASC
     LIMIT ?`,
    params,
  );
}

function listExecutorRows(db, input) {
  const params = [input.projectId];
  let filterClause = '';
  if (input.label) {
    filterClause += ' AND label LIKE ?';
    params.push(`%${input.label}%`);
  }
  if (input.group) {
    filterClause += ' AND group_kind = ?';
    params.push(input.group);
  }
  if (input.enabled !== undefined) {
    filterClause += ' AND enabled = ?';
    params.push(input.enabled ? 1 : 0);
  }
  return db.all(
    `SELECT * FROM executors
     WHERE project_id = ?${filterClause}
     ORDER BY sort_order ASC, id ASC`,
    params,
  );
}

function requiredExecutor(db, projectId, input) {
  let row = null;
  if (input.executorId) {
    row = db.get('SELECT * FROM executors WHERE id = ? AND project_id = ?', [input.executorId, projectId]);
  } else if (input.label) {
    row = db.get(
      `SELECT * FROM executors
       WHERE project_id = ? AND label = ?
       ORDER BY sort_order ASC, id ASC
       LIMIT 1`,
      [projectId, input.label],
    );
  }
  if (!row) throw new Error('Executor not found in project');
  return executorFromRow(row);
}

function projectSummary(project, loop = null) {
  const state = loop?.status ? loop.status(project.id) : null;
  return {
    id: project.id,
    name: project.name,
    workspacePath: project.workspace_path || '',
    description: project.description || '',
    createdAt: project.created_at,
    updatedAt: project.updated_at,
    state: state ? stateSummary(state) : null,
  };
}

function stateSummary(state) {
  return {
    running: Boolean(state.running),
    phase: state.phase || 'idle',
    intervalSeconds: Number(state.interval_seconds || 5),
    validationCommand: state.validation_command || '',
    agentCliProvider: state.agent_cli_provider || null,
    agentCliCommand: state.agent_cli_command || '',
    codexReasoningEffort: state.codex_reasoning_effort || null,
    ...planGenerationSummaryFields(state),
    ...planExecutionSummaryFields(state),
  };
}

function intakeSummary(record, db = null, intakeType = 'requirement') {
  if (!record) return null;
  return {
    id: record.id,
    projectId: record.project_id,
    requirementId: record.requirement_id || null,
    linkedPlanId: record.linked_plan_id || null,
    linkedPlans: linkedPlanSummariesForIntake(record, db, intakeType),
    title: record.title,
    status: record.status,
    agentCliProvider: record.agent_cli_provider || null,
    agentCliCommand: record.agent_cli_command || '',
    codexReasoningEffort: record.codex_reasoning_effort || null,
    ...planGenerationSummaryFields(record),
    createdAt: record.created_at,
    updatedAt: record.updated_at,
  };
}

function intakeDetail(record, db = null, intakeType = 'requirement') {
  if (!record) return null;
  return {
    ...intakeSummary(record, db, intakeType),
    body: record.body || '',
  };
}

function linkedPlanSummariesForIntake(record, db = null, intakeType = 'requirement') {
  if (!record || !db) return [];
  const projectId = normalizePositiveInteger(record.project_id);
  const intakeId = normalizePositiveInteger(record.id);
  if (!projectId || !intakeId) return [];

  const workspacePath = projectWorkspacePath(db, projectId);
  let links = [];
  try {
    links = intakePlanLinks.getPlansForIntake({ db }, projectId, intakeType, intakeId, {
      includeMissingPlans: true,
    });
  } catch {
    links = [];
  }

  const linkedPlans = links
    .map((link) => linkedPlanSummaryFromLink(link, workspacePath))
    .filter(Boolean);
  if (linkedPlans.length > 0) return markCurrentLinkedPlanSummaries(linkedPlans);

  const legacy = legacyLinkedPlanSummary(record, db, workspacePath);
  return legacy ? markCurrentLinkedPlanSummaries([legacy]) : [];
}

function linkedPlanSummaryFromLink(link = {}, workspacePath = '') {
  const planId = normalizePositiveInteger(link.planId ?? link.plan_id ?? link.id);
  if (!planId) return null;
  const plan = link.plan || {};
  const filePath = String(plan.file_path || '').trim();
  const phaseTitle = String(link.phaseTitle || link.phase_title || '').trim();
  return {
    linkId: normalizePositiveInteger(link.linkId ?? link.link_id),
    planId,
    phaseIndex: normalizePositiveInteger(link.phaseIndex ?? link.phase_index) || 1,
    phaseTitle,
    title: readPlanMarkdownTitle(workspacePath, filePath) || phaseTitle || filePath || `Plan #${planId}`,
    filePath,
    status: plan.status || null,
    completedTasks: normalizeNullableNumber(plan.completed_tasks),
    totalTasks: normalizeNullableNumber(plan.total_tasks),
    validationPassed: Boolean(plan.validation_passed),
    ...planGenerationSummaryFields(plan),
    ...planExecutionSummaryFields(plan),
    current: false,
  };
}

function legacyLinkedPlanSummary(record = {}, db = null, workspacePath = '') {
  const planId = normalizePositiveInteger(record.linked_plan_id);
  if (!planId) return null;
  let plan = null;
  try {
    plan = db?.get ? db.get('SELECT * FROM plans WHERE id = ? AND project_id = ?', [planId, record.project_id]) : null;
  } catch {
    plan = null;
  }
  const fallbackPlan = {
    file_path: record.plan_file_path || '',
    status: record.plan_status || null,
    completed_tasks: record.plan_completed,
    total_tasks: record.plan_total,
    validation_passed: record.plan_validation_passed,
  };
  return linkedPlanSummaryFromLink({
    planId,
    phaseIndex: 1,
    phaseTitle: '',
    plan: plan || fallbackPlan,
  }, workspacePath);
}

function markCurrentLinkedPlanSummaries(linkedPlans = []) {
  const current = currentLinkedPlanSummary(linkedPlans);
  return linkedPlans.map((linkedPlan) => ({
    ...linkedPlan,
    current: Boolean(
      current
      && Number(linkedPlan.planId) === Number(current.planId)
      && Number(linkedPlan.phaseIndex || 0) === Number(current.phaseIndex || 0),
    ),
  }));
}

function currentLinkedPlanSummary(linkedPlans = []) {
  if (!Array.isArray(linkedPlans) || linkedPlans.length === 0) return null;
  return linkedPlans.find((linkedPlan) => {
    const status = String(linkedPlan.status || '').toLowerCase();
    return status && !['completed', 'interrupted', 'draft'].includes(status);
  }) || linkedPlans.find((linkedPlan) => String(linkedPlan.status || '').toLowerCase() !== 'completed') || linkedPlans[0];
}

function projectWorkspacePath(db, projectId) {
  try {
    return db?.get ? String(db.get('SELECT workspace_path FROM projects WHERE id = ?', [projectId])?.workspace_path || '') : '';
  } catch {
    return '';
  }
}

function readPlanMarkdownTitle(workspacePath, filePath) {
  const planPath = resolveWorkspaceFilePath(workspacePath, filePath);
  if (!planPath) return '';
  try {
    if (!fs.existsSync(planPath) || fs.statSync(planPath).isDirectory()) return '';
    return extractMarkdownTitle(fs.readFileSync(planPath, 'utf8').slice(0, 64 * 1024));
  } catch {
    return '';
  }
}

function resolveWorkspaceFilePath(workspacePath, filePath) {
  if (!workspacePath || !filePath) return '';
  try {
    const workspaceRoot = path.resolve(workspacePath);
    const planPath = path.resolve(workspaceRoot, filePath);
    const relative = path.relative(workspaceRoot, planPath);
    if (!relative || relative.startsWith('..') || path.isAbsolute(relative)) return '';
    return planPath;
  } catch {
    return '';
  }
}

function normalizePositiveInteger(value) {
  const number = Number(value);
  return Number.isInteger(number) && number > 0 ? number : null;
}

function normalizeNullableNumber(value) {
  if (value === null || typeof value === 'undefined' || value === '') return null;
  const number = Number(value);
  return Number.isFinite(number) ? number : null;
}

function planGenerationSummaryFields(record = {}) {
  const config = planBackendSnapshotSummary(record, 'plan_generation');
  return {
    planGenerationConfig: config,
    planGenerationStrategy: config.strategy,
    planGenerationProvider: config.provider,
    planGenerationCommand: config.command,
    planGenerationModel: config.model,
    planGenerationCodexReasoningEffort: config.codexReasoningEffort,
  };
}

function planExecutionSummaryFields(record = {}) {
  const config = planBackendSnapshotSummary(record, 'plan_execution');
  return {
    planExecutionConfig: config,
    planExecutionStrategy: config.strategy,
    planExecutionProvider: config.provider,
    planExecutionCommand: config.command,
    planExecutionModel: config.model,
    planExecutionCodexReasoningEffort: config.codexReasoningEffort,
  };
}

function planBackendSnapshotSummary(record = {}, prefix) {
  return {
    strategy: nullableText(record[`${prefix}_strategy`]),
    provider: nullableText(record[`${prefix}_provider`]),
    command: textValue(record[`${prefix}_command`]),
    model: textValue(record[`${prefix}_model`]),
    codexReasoningEffort: nullableText(record[`${prefix}_codex_reasoning_effort`]),
  };
}

function nullableText(value) {
  if (value === null || value === undefined) return null;
  const text = String(value).trim();
  return text || null;
}

function textValue(value) {
  if (value === null || value === undefined) return '';
  return String(value);
}

/**
 * 可打开引用：供外部 MCP 客户端（codex/claude CLI 等）展示入口。
 * 含应用内深链（HashRouter 路由串）与该条目详情片段（标题/状态）。
 * record 缺失（创建未确认）时 id/link 为 null，仅做增量、向后兼容。
 */
function intakeOpenable(type, projectId, record) {
  const id = record?.id || null;
  return {
    type,
    projectId,
    id,
    title: record ? record.title || null : null,
    status: record ? record.status || null : null,
    link: id ? `#/projects/${projectId}?tab=${type}&focus=${type}-${id}` : null,
  };
}

function planSummary(plan) {
  if (!plan) return null;
  return {
    id: plan.id,
    projectId: plan.project_id,
    issueHash: plan.issue_hash || '',
    filePath: plan.file_path || '',
    hash: plan.hash || '',
    status: plan.status || 'pending',
    sortOrder: Number(plan.sort_order || 0),
    totalTasks: Number(plan.total_tasks || 0),
    completedTasks: Number(plan.completed_tasks || 0),
    validationPassed: Boolean(plan.validation_passed),
    agentCliProvider: plan.agent_cli_provider || null,
    agentCliCommand: plan.agent_cli_command || '',
    agentCliSessionId: plan.agent_cli_session_id || null,
    codexReasoningEffort: plan.codex_reasoning_effort || null,
    ...planGenerationSummaryFields(plan),
    ...planExecutionSummaryFields(plan),
    createdAt: plan.created_at,
    updatedAt: plan.updated_at,
  };
}

function taskSummary(task) {
  if (!task) return null;
  return {
    id: task.id,
    projectId: task.project_id || null,
    planId: task.plan_id,
    planFilePath: task.plan_file_path || task.file_path || '',
    taskKey: task.task_key,
    title: task.title,
    rawLine: task.raw_line,
    scope: task.scope || '',
    status: task.status || 'pending',
    sortOrder: Number(task.sort_order || 0),
    startedAt: task.started_at || null,
    finishedAt: task.finished_at || null,
    durationMs: Number(task.duration_ms || 0),
    codexSessionId: task.codex_session_id || null,
  };
}

function executorSummary(record, loop = null) {
  const executor = normalizeExecutorRecord(record);
  if (!executor) return null;
  const activeOperation = activeExecutorOperation(loop, executor.projectId, executor.id);
  const running = Boolean(activeOperation);
  const status = running ? 'running' : (executor.lastStatus || 'idle');
  const logSource = running && activeOperation?.logBuffer ? activeOperation.logBuffer : executor.lastLog;
  return {
    id: executor.id,
    projectId: executor.projectId,
    label: executor.label,
    type: executor.type,
    command: executor.command,
    args: executor.args,
    group: executor.group,
    enabled: executor.enabled,
    dependsOn: executor.dependsOn,
    dependsOrder: executor.dependsOrder,
    status,
    running,
    exitCode: running ? null : executor.lastExitCode,
    durationMs: running ? null : executor.lastDurationMs,
    lastRunAt: executor.lastRunAt,
    logTail: executorLogTail(logSource),
    openable: executorOpenable(executor.projectId, executor),
    activeOperation: running ? {
      label: activeOperation.label || executor.label,
      startedAt: activeOperation.startedAt || null,
      logTail: executorLogTail(activeOperation.logBuffer),
    } : null,
  };
}

function normalizeExecutorRecord(record) {
  if (!record) return null;
  if (record.projectId !== undefined && record.label !== undefined) return record;
  return executorFromRow(record);
}

function executorRunResultSummary(result = {}, executor = null, loop = null) {
  const baseExecutor = normalizeExecutorRecord(executor) || {
    id: normalizePositiveInteger(result.executorId),
    projectId: result.snapshot?.activeProjectId || null,
    label: result.label || '',
    type: '',
    command: '',
    args: [],
    group: { kind: null, isDefault: false },
    enabled: true,
    dependsOn: [],
    dependsOrder: 'parallel',
    lastStatus: result.status || null,
    lastExitCode: result.exitCode ?? null,
    lastDurationMs: result.durationMs ?? null,
    lastLog: result.log || '',
    lastRunAt: null,
  };
  const mergedExecutor = {
    ...baseExecutor,
    lastStatus: result.status || baseExecutor.lastStatus,
    lastExitCode: result.exitCode ?? baseExecutor.lastExitCode,
    lastDurationMs: result.durationMs ?? baseExecutor.lastDurationMs,
    lastLog: result.log || baseExecutor.lastLog,
  };
  return {
    projectId: mergedExecutor.projectId,
    executorId: result.executorId || mergedExecutor.id,
    label: result.label || mergedExecutor.label,
    executor: executorSummary(mergedExecutor, loop),
    status: result.status || null,
    exitCode: typeof result.exitCode === 'number' ? result.exitCode : null,
    durationMs: typeof result.durationMs === 'number' ? result.durationMs : null,
    logTail: executorLogTail(result.log),
    logFile: result.logFile || null,
    timedOut: Boolean(result.timedOut),
    error: result.error || null,
    dependencyResults: Array.isArray(result.dependencyResults)
      ? result.dependencyResults.map(executorDependencyResultSummary)
      : [],
    snapshot: snapshotSummary(result.snapshot),
  };
}

function executorDependencyResultSummary(result = {}) {
  return {
    executorId: result.executorId || null,
    label: result.label || result.dependencyLabel || null,
    status: result.status || 'bad',
    exitCode: typeof result.exitCode === 'number' ? result.exitCode : null,
    durationMs: typeof result.durationMs === 'number' ? result.durationMs : null,
    error: result.errorMessage || result.error || null,
    logTail: executorLogTail(result.log, 1200),
  };
}

function activeExecutorOperation(loop, projectId, executorId) {
  let runtime = null;
  try {
    runtime = typeof loop?.existingRuntime === 'function' ? loop.existingRuntime(projectId) : null;
  } catch {
    runtime = null;
  }
  if (!runtime?.activeOperations) return null;
  for (const operation of runtime.activeOperations.values()) {
    if (
      Number(operation?.projectId) === Number(projectId)
      && operation?.operationType === 'executor'
      && Number(operation?.executorId) === Number(executorId)
    ) {
      return operation;
    }
  }
  return null;
}

function executorLogTail(value, maxLength = 4000) {
  const text = value === undefined || value === null ? '' : String(value);
  if (!text) return '';
  return text.length > maxLength ? text.slice(-maxLength) : text;
}

function executorOpenable(projectId, executor) {
  const id = normalizePositiveInteger(executor?.id);
  const anchorId = id ? `workspace-executor-${id}` : null;
  return {
    type: 'executor',
    projectId,
    id,
    label: executor?.label || null,
    tab: 'executors',
    anchorId,
    link: id ? `#/projects/${projectId}?tab=executors&anchor=${anchorId}` : null,
  };
}

function snapshotSummary(snapshot = {}) {
  return {
    activeProjectId: snapshot.activeProjectId || null,
    activeProject: snapshot.activeProject ? {
      id: snapshot.activeProject.id,
      name: snapshot.activeProject.name,
      workspacePath: snapshot.activeProject.workspace_path || '',
      description: snapshot.activeProject.description || '',
    } : null,
    state: snapshot.state ? stateSummary(snapshot.state) : null,
    counts: {
      projects: countOf(snapshot.projects),
      requirements: countOf(snapshot.requirements),
      feedback: countOf(snapshot.feedback),
      plans: countOf(snapshot.plans),
      tasks: countOf(snapshot.tasks),
      executors: countOf(snapshot.executors),
      events: countOf(snapshot.events),
    },
  };
}

function countOf(value) {
  return Array.isArray(value) ? value.length : 0;
}

function toolResult(result) {
  return {
    content: [{ type: 'text', text: JSON.stringify(result, null, 2) }],
    structuredContent: result,
  };
}

function toolError(error) {
  if (isDuplicateIntakeError(error)) return duplicateIntakeToolError(error);
  if (isP06ToolError(error)) return p06ToolError(error);
  const message = error?.message || String(error || 'Unknown error');
  return {
    isError: true,
    content: [{ type: 'text', text: message }],
    structuredContent: { error: message },
  };
}

function validateIntakeItemInput(input) {
  assertOnlyKeys(input, ['projectId', 'id']);
  return {
    projectId: requiredPositiveInteger(input, 'projectId'),
    id: requiredPositiveInteger(input, 'id'),
  };
}

function validateIntakeUpdateInput(input, feedback) {
  const allowed = [
    'projectId', 'id', 'expectedUpdatedAt', 'title', 'body', 'status',
    'agentCliProvider', 'agentCliCommand', 'codexReasoningEffort',
    'planGenerationStrategy', 'planGenerationProvider', 'planGenerationCommand',
    'planGenerationModel', 'planGenerationCodexReasoningEffort',
  ];
  if (feedback) allowed.push('requirementId');
  assertOnlyKeys(input, allowed);
  const result = stripUndefined({
    projectId: requiredPositiveInteger(input, 'projectId'),
    id: requiredPositiveInteger(input, 'id'),
    expectedUpdatedAt: optionalString(input, 'expectedUpdatedAt', 64),
    title: optionalString(input, 'title', LIMITS.title),
    body: optionalString(input, 'body', LIMITS.body),
    status: optionalEnum(input, 'status', P06_INTAKE_STATUSES),
    ...(feedback ? { requirementId: optionalPositiveInteger(input, 'requirementId', { nullable: true }) } : {}),
    ...validatedAgentCliInput(input),
    ...validatedPlanGenerationInput(input),
  });
  if (!['title', 'body', 'status', 'requirementId', 'agentCliProvider', 'agentCliCommand', 'codexReasoningEffort',
    'planGenerationStrategy', 'planGenerationProvider', 'planGenerationCommand', 'planGenerationModel',
    'planGenerationCodexReasoningEffort'].some((key) => Object.prototype.hasOwnProperty.call(result, key))) {
    throw new Error('at least one mutable Intake field is required');
  }
  return result;
}

function validateReplacePlanLinksInput(input) {
  assertOnlyKeys(input, ['projectId', 'id', 'links']);
  if (!Array.isArray(input.links) || input.links.length > LIMITS.rows) throw new Error('links must be an array with at most 200 items');
  const links = input.links.map((link, index) => {
    assertPlainObject(link, `links[${index}] must be an object`);
    assertOnlyKeys(link, ['planId', 'phaseIndex', 'phaseTitle']);
    return {
      planId: requiredPositiveInteger(link, 'planId'),
      phaseIndex: optionalNonNegativeInteger(link, 'phaseIndex') ?? 0,
      phaseTitle: optionalString(link, 'phaseTitle', 500) || '',
    };
  });
  return { projectId: requiredPositiveInteger(input, 'projectId'), id: requiredPositiveInteger(input, 'id'), links };
}

function validateIntakeAttachmentInput(input) {
  assertOnlyKeys(input, ['projectId', 'id', 'name', 'type', 'path', 'base64', 'dataBase64', 'dataUrl', 'bytes']);
  const attachment = validateAttachment(input, 0);
  if (!attachment.name || (!attachment.path && !attachment.base64 && !attachment.dataBase64 && !attachment.dataUrl && !attachment.bytes)) {
    throw new Error('attachment name and exactly one byte source are required');
  }
  const sourceCount = [attachment.path, attachment.base64, attachment.dataBase64, attachment.dataUrl, attachment.bytes]
    .filter((value) => value !== undefined).length;
  if (sourceCount !== 1) throw new Error('attachment requires exactly one byte source');
  return { projectId: requiredPositiveInteger(input, 'projectId'), id: requiredPositiveInteger(input, 'id'), attachment };
}

function validateAttachmentDeleteInput(input) {
  assertOnlyKeys(input, ['projectId', 'attachmentId']);
  return { projectId: requiredPositiveInteger(input, 'projectId'), attachmentId: requiredPositiveInteger(input, 'attachmentId') };
}

function isP06ToolError(error) {
  return typeof error?.code === 'string' && [
    'invalid_intake', 'invalid_attachment', 'attachment_path_denied', 'attachment_recovery_required',
    'not_found', 'duplicate_intake', 'precondition_failed', 'relation_conflict', 'idempotency_key_reused',
    'request_in_progress', 'unsupported_media_type', 'request_timeout', 'service_unavailable', 'internal_error',
  ].includes(error.code);
}

function p06ToolError(error) {
  const code = error.code;
  const message = code === 'not_found' ? 'Requested Intake resource was not found.'
    : code === 'attachment_path_denied' ? 'Attachment path input is not authorized.'
      : code === 'service_unavailable' ? 'P06 Intake service is unavailable.'
        : 'P06 Intake request failed.';
  return {
    isError: true,
    content: [{ type: 'text', text: message }],
    structuredContent: { error: message, code, errorCode: code },
  };
}

function isDuplicateIntakeError(error) {
  return error?.code === 'DUPLICATE_INTAKE';
}

function duplicateIntakeToolError(error) {
  const intakeType = error.intakeType === 'feedback' ? 'feedback' : 'requirement';
  const existingId = Number(error.existingId || error.existing?.id || 0) || null;
  const typeLabel = intakeType === 'feedback' ? 'Feedback' : 'Requirement';
  const idField = intakeType === 'feedback' ? 'existingFeedbackId' : 'existingRequirementId';
  const message = existingId
    ? `${typeLabel} already exists: ${intakeType} #${existingId}.`
    : `${typeLabel} already exists.`;
  const duplicate = stripUndefined({
    code: error.code,
    intakeType,
    existingId,
    [idField]: existingId,
  });
  return {
    isError: true,
    content: [{ type: 'text', text: message }],
    structuredContent: stripUndefined({
      error: message,
      code: error.code,
      errorCode: error.code,
      intakeType,
      existingId,
      [idField]: existingId,
      duplicate,
      detail: error.message,
    }),
  };
}

function attachmentArraySchema() {
  return {
    type: 'array',
    maxItems: LIMITS.attachments,
    items: ATTACHMENT_INPUT_SCHEMA,
  };
}

function intakeListSchema(statusValues) {
  return {
    type: 'object',
    additionalProperties: false,
    required: ['projectId'],
    properties: {
      ...PROJECT_ID_SCHEMA,
      status: { type: 'string', enum: statusValues },
      ...LIMIT_SCHEMA,
    },
  };
}

function intakeItemSchema() {
  return {
    type: 'object', additionalProperties: false, required: ['projectId', 'id'],
    properties: { ...PROJECT_ID_SCHEMA, id: { type: 'integer', minimum: 1 } },
  };
}

function intakeUpdateSchema(feedback) {
  const properties = {
    ...PROJECT_ID_SCHEMA,
    id: { type: 'integer', minimum: 1 },
    expectedUpdatedAt: { type: 'string', maxLength: 64 },
    title: { type: 'string', maxLength: LIMITS.title },
    body: { type: 'string', maxLength: LIMITS.body },
    status: { type: 'string', enum: P06_INTAKE_STATUSES },
    ...COMMON_CLI_PROPERTIES,
    ...PLAN_GENERATION_PROPERTIES,
  };
  if (feedback) properties.requirementId = { type: ['integer', 'null'], minimum: 1 };
  return { type: 'object', additionalProperties: false, required: ['projectId', 'id'], properties };
}

function replacePlanLinksSchema() {
  return {
    type: 'object', additionalProperties: false, required: ['projectId', 'id', 'links'],
    properties: {
      ...PROJECT_ID_SCHEMA,
      id: { type: 'integer', minimum: 1 },
      links: {
        type: 'array', maxItems: LIMITS.rows,
        items: {
          type: 'object', additionalProperties: false, required: ['planId'],
          properties: {
            planId: { type: 'integer', minimum: 1 },
            phaseIndex: { type: 'integer', minimum: 0 },
            phaseTitle: { type: 'string', maxLength: 500 },
          },
        },
      },
    },
  };
}

function intakeAttachmentSchema() {
  return {
    type: 'object', additionalProperties: false, required: ['projectId', 'id', 'name'],
    properties: {
      ...PROJECT_ID_SCHEMA,
      id: { type: 'integer', minimum: 1 },
      name: { type: 'string', minLength: 1, maxLength: 120 },
      type: { type: 'string', maxLength: 200 },
      path: { type: 'string', minLength: 1, maxLength: LIMITS.attachmentPath },
      base64: { type: 'string', minLength: 1, maxLength: 36 * 1024 * 1024 },
      dataBase64: { type: 'string', minLength: 1, maxLength: 36 * 1024 * 1024 },
      dataUrl: { type: 'string', minLength: 1, maxLength: 36 * 1024 * 1024 },
      bytes: { type: 'array', maxItems: 25 * 1024 * 1024, items: { type: 'integer', minimum: 0, maximum: 255 } },
    },
  };
}

function attachmentDeleteSchema() {
  return {
    type: 'object', additionalProperties: false, required: ['projectId', 'attachmentId'],
    properties: { ...PROJECT_ID_SCHEMA, attachmentId: { type: 'integer', minimum: 1 } },
  };
}

function projectActionSchema() {
  return {
    type: 'object',
    additionalProperties: false,
    required: ['projectId'],
    properties: PROJECT_ID_SCHEMA,
  };
}

function executorActionSchema() {
  return {
    type: 'object',
    additionalProperties: false,
    required: ['projectId'],
    properties: {
      ...PROJECT_ID_SCHEMA,
      executorId: {
        type: 'integer',
        minimum: 1,
        description: 'Existing executor id in the project. Provide executorId or label.',
      },
      label: {
        type: 'string',
        minLength: 1,
        maxLength: LIMITS.title,
        description: 'Exact existing executor label in the project. Provide executorId or label. Command/debug/launch fields are not accepted.',
      },
    },
  };
}

module.exports = {
  MCP_TOOL_DEFINITIONS,
  MCP_TOOL_NAMES,
  P06_INTAKE_TOOL_NAMES,
  P06_GO_ONLY_TOOL_NAMES,
  callMcpTool,
  registerMcpTools,
  visibleMcpToolDefinitions,
};
