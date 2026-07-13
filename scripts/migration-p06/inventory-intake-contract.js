'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(__dirname, '../..');
const OUTPUT_PATH = 'docs/migration/p06/intake-contract.json';
const FORMAT_VERSION = 1;
const CONTRACT_VERSION = 'p06-node-intake-contract-v1';

const SOURCE_MARKERS = Object.freeze({
  'src/intakeService.js': [
    'createRequirement(input = {})',
    'createFeedback(input = {})',
    "this.rejectDuplicateRequirement(projectId, title, body)",
    "this.rejectDuplicateFeedback(projectId, requirementId, title, body)",
    ".replace(/\\r\\n?/g, '\\n')",
    "this.code = 'DUPLICATE_INTAKE'",
  ],
  'src/attachments.js': [
    'function saveAttachments(db, attachmentsRoot, ownerType, ownerId, files = [], projectId = null)',
    'COPYFILE_EXCL',
    "fs.writeFileSync(storedPath, prepared.buffer, { flag: 'wx' })",
    'function safeFileName(name)',
  ],
  'src/main.js': [
    "ipcMain.handle('intake:accept'",
    "ipcMain.handle('requirements:update'",
    "ipcMain.handle('requirements:delete'",
    "ipcMain.handle('feedback:update'",
    "ipcMain.handle('feedback:delete'",
    'function normalizeDraftIntakeInput(input = {})',
  ],
  'src/loop/intakeAttachments.js': [
    "return intakeType === 'feedback' ? ['feedback'] : ['requirement', 'requirements']",
    '持久化本地路径',
    'function resolveAttachmentPath(workspace, storedPath)',
  ],
  'src/loop/intakeDeletion.js': [
    'function deleteIntake(service, projectId, intakeType, intakeId, options = {})',
    'UPDATE feedback SET requirement_id = NULL',
    'service.db.runBatch(statements)',
    "service.addEvent(projectId, 'intake.deleted'",
    'function deletePlan(service, projectId, planId, options = {})',
  ],
  'src/loop/intakePlanLinks.js': [
    'INSERT OR IGNORE INTO intake_plan_links',
    'ORDER BY links.phase_index ASC, links.plan_id ASC',
    'function writeIntakePlanLinks(service, projectId, intakeType, intakeId, links = [], options = {})',
    'function syncLegacyLinkedPlanStatement(projectId, intakeType, intakeId, updatedAt, options = {})',
  ],
  'src/loop/snapshots.js': [
    'ORDER BY requirements.updated_at DESC',
    'ORDER BY feedback.updated_at DESC',
    'ORDER BY created_at DESC, id DESC',
    'function intakeLinkedPlanSnapshotRow',
    'function intakeGenerateFailureSnapshotFields',
  ],
  'src/mcpTools.js': [
    "LIST_REQUIREMENTS: 'list_requirements'",
    "CREATE_REQUIREMENT: 'create_requirement'",
    "LIST_FEEDBACK: 'list_feedback'",
    "CREATE_FEEDBACK: 'create_feedback'",
    "const INTAKE_STATUSES = Object.freeze(['open', 'completed', 'closed'])",
    'function duplicateIntakeToolError(error)',
  ],
});

function sha256(value) {
  return crypto.createHash('sha256').update(value).digest('hex');
}

function sortValue(value) {
  if (Array.isArray(value)) return value.map(sortValue);
  if (!value || typeof value !== 'object') return value;
  return Object.fromEntries(Object.keys(value).sort().map((key) => [key, sortValue(value[key])]));
}

function stableJson(value) {
  return `${JSON.stringify(sortValue(value), null, 2)}\n`;
}

function sourceEvidence(rootDir = ROOT) {
  return Object.entries(SOURCE_MARKERS).map(([relativePath, markers]) => {
    const absolutePath = path.join(rootDir, relativePath);
    if (!fs.existsSync(absolutePath)) throw new Error(`contract source missing: ${relativePath}`);
    const bytes = fs.readFileSync(absolutePath);
    const source = bytes.toString('utf8');
    const missing = markers.filter((marker) => !source.includes(marker));
    if (missing.length) throw new Error(`contract marker missing in ${relativePath}: ${missing.join(', ')}`);
    return {
      path: relativePath,
      sha256: sha256(bytes),
      markers: markers.map((marker) => ({ marker, line: source.slice(0, source.indexOf(marker)).split(/\r?\n/).length })),
    };
  });
}

function deriveContract(rootDir = ROOT) {
  return {
    schemaVersion: FORMAT_VERSION,
    version: CONTRACT_VERSION,
    status: 'frozen-with-recorded-legacy-gaps',
    purpose: 'Freeze the current Node requirements, feedback, attachment, plan-link, deletion and post-mutation AppSnapshot behavior for the P06 Go migration.',
    evidence: sourceEvidence(rootDir),
    prerequisites: {
      p05Gate: 'P05 completion evidence, schema/checksum, Files policy and database owner lock must pass before any P06 writer opens a fixture copy.',
      databaseCopies: 'Node/sql.js may open only a generator-owned sanitized OS-temporary database or a caller-explicit sanitized copy; Go receives a separately reset copy only after Node closes.',
      productionSafety: 'Never discover or open Electron userData, a production autoplan.sqlite, home-profile content or an unapproved attachment root.',
    },
    namingAndTime: {
      storageAndSnapshot: 'database/domain intake rows are snake_case; historical AppSnapshot top-level keys remain camelCase',
      mcp: 'MCP summaries use camelCase and are a distinct compatibility surface',
      utc: 'nowIso() is new Date().toISOString(); created_at, updated_at and accepted_at are UTC RFC3339/ISO-8601 Z strings',
      presence: 'null, empty string and missing are distinct; collections remain present even when empty; strict golden comparison rejects unknown or omitted keys',
    },
    statuses: {
      persistedDefault: 'open',
      knownLifecycleValues: ['draft', 'open', 'completed', 'closed'],
      mcpCreateAndFilterEnum: ['open', 'completed', 'closed'],
      ipcCreateDraftRule: 'createAsDraft or draft truthy forces status=draft; otherwise a supplied status is persisted verbatim and falsy falls back to open',
      ipcUpdateRule: 'status is input.status ?? current.status ?? open; legacy IPC does not enum-validate updates',
      duplicateBlocking: 'all values except exactly closed block an equivalent create; SQL uses COALESCE(status, open) <> closed',
      linkedCompletion: 'when all linked plans complete, non-terminal intake becomes completed; completed and closed remain terminal',
      migrationRule: 'Preserve existing stored values on reads. New public transports accept only the frozen API enum and return a stable validation error for other values.',
    },
    fields: {
      requirement: intakeFields(false),
      feedback: intakeFields(true),
      attachmentStorage: attachmentStorageFields(),
      intakePlanLink: planLinkFields(),
    },
    mutations: mutationContracts(),
    duplicateRule: {
      scope: {
        requirement: 'same project_id',
        feedback: 'same project_id and the same nullable requirement_id group',
      },
      candidates: 'only rows whose COALESCE(status, open) is not exactly closed, ordered id ASC; first match is returned',
      normalization: [
        'String(value ?? empty)',
        'convert CRLF and lone CR to LF',
        'for each line, collapse one-or-more non-newline whitespace characters to one ASCII space, then trim the line',
        'join lines with LF and trim the whole value',
        'line boundaries are significant: line one LF line two differs from line one space line two',
      ],
      equality: 'normalized title and normalized body must both be equal',
      error: {
        code: 'DUPLICATE_INTAKE',
        name: 'DuplicateIntakeError',
        legacyMessage: '<需求|反馈>已存在：#<existing id>',
        properties: ['intakeType', 'existingId', 'existing'],
        sideEffects: 'rejected before intake INSERT, attachment writes, creation event and autoRun',
        mcpStructuredFields: ['error', 'code', 'errorCode', 'intakeType', 'existingId', 'existingRequirementId|existingFeedbackId', 'duplicate', 'detail'],
      },
      updateGap: 'legacy requirements:update and feedback:update do not run duplicate detection; this is frozen as an observed gap, not a permission to weaken new create semantics',
    },
    requirementAssociation: {
      create: 'missing/falsy requirementId becomes null; otherwise the requirement must exist globally and its project_id must equal the feedback project_id',
      errors: [
        { code: 'REQUIREMENT_NOT_FOUND', legacyMessage: '关联需求不存在' },
        { code: 'REQUIREMENT_PROJECT_MISMATCH', legacyMessage: '关联需求不属于当前项目' },
      ],
      updateLegacyGap: 'feedback:update maps undefined to current requirement_id, and any other falsy value to null, but does not validate a truthy id. P06 mutations must close this IDOR/integrity gap and require existence plus same-project ownership.',
      requirementDelete: 'all same-project feedback rows pointing at the deleted requirement are retained with requirement_id=NULL and updated_at set to the deletion timestamp',
    },
    planLinks: {
      intakeTypes: ['requirement', 'feedback'],
      uniqueKeys: [
        ['project_id', 'intake_type', 'intake_id', 'plan_id'],
        ['project_id', 'intake_type', 'intake_id', 'phase_index'],
      ],
      normalization: 'invalid type defaults to requirement in legacy internal helpers; planId and intakeId require positive integers; phase_index defaults to input order starting at 1; phase_title is trimmed',
      writeOrder: 'input links sort by phase_index ASC then plan_id ASC and deduplicate by plan_id, retaining the first',
      readOrder: 'phase_index ASC then plan_id ASC; project-wide grouping additionally uses intake_type ASC then intake_id ASC',
      legacySync: 'a successful non-empty write sets linked_plan_id to the first normalized link; deleting all intake links clears it; deleting one plan reselects the first remaining link by phase_index/plan_id only when legacy linked_plan_id referenced the deleted plan',
      fallback: 'when no intake_plan_links rows exist, a positive legacy linked_plan_id is exposed as phase 1, empty phase title and null link timestamps/id',
      currentPlan: 'first linked plan whose non-empty status is not completed/interrupted/draft; else first non-completed; else first link',
      crossProjectLegacyGap: 'legacy writeIntakePlanLinks does not prevalidate intake/plan existence or ownership. P06 repository/application writes must reject missing or cross-project intake/plan rather than reproduce invalid links.',
    },
    deletion: deletionContract(),
    attachments: attachmentContract(),
    snapshot: snapshotContract(),
    mcp: mcpContract(),
    errors: errorContract(),
    golden: {
      generator: 'scripts/migration-p06/generate-node-golden.js',
      artifact: 'fixtures/migration/p06/node-intake.golden.json',
      manifest: 'fixtures/migration/p06/manifest.json',
      scenarios: [
        'default-title-and-attachment', 'normalized-duplicate', 'closed-duplicate',
        'feedback-association', 'feedback-duplicate-groups', 'cross-project-feedback',
        'update-null-and-empty', 'accept-unaccept', 'multi-phase-links',
        'delete-plan-keep-intake', 'delete-requirement-cascade',
      ],
      comparison: 'deep strict comparison of every projected key/type/null/missing/value/order; no ignored or unknown fields',
      safety: 'serial Node-only execution on a fresh sanitized OS-temporary database; close sql.js before artifact commit; attachment bytes and logical/physical database before/after hashes are recorded',
      normalization: 'only generator-owned ids, UTC timestamps and generator-owned paths are replaced consistently; legacy unsafe path/hash values are represented as risk metadata rather than copied into public DTOs',
    },
  };
}

function buildContract(rootDir = ROOT) {
  const contractPath = path.join(rootDir, OUTPUT_PATH);
  if (!fs.existsSync(contractPath) || !fs.statSync(contractPath).isFile()) {
    throw new Error(`frozen contract missing: ${OUTPUT_PATH}`);
  }
  const contract = JSON.parse(fs.readFileSync(contractPath, 'utf8'));
  if (contract.schemaVersion !== FORMAT_VERSION || contract.version !== CONTRACT_VERSION) {
    throw new Error('frozen contract version mismatch');
  }
  const observedEvidence = sourceEvidence(rootDir);
  const declaredEvidence = new Set(Array.isArray(contract.evidence) ? contract.evidence : []);
  for (const item of observedEvidence) {
    if (!declaredEvidence.has(item.path)) throw new Error(`frozen contract evidence missing: ${item.path}`);
  }
  return contract;
}

function intakeFields(feedback) {
  const fields = [
    field('id', 'integer', null, false), field('project_id', 'nullable-in-legacy/integer-in-P06', null, true),
  ];
  if (feedback) fields.push(field('requirement_id', 'nullable-integer', null, true));
  fields.push(
    field('title', 'string', null, false), field('body', 'string', '', false), field('status', 'string', 'open', false),
    field('agent_cli_provider', 'nullable-string', null, true), field('agent_cli_command', 'string', '', false),
    field('codex_reasoning_effort', 'nullable-string', null, true), field('plan_generation_strategy', 'nullable-string', null, true),
    field('plan_generation_provider', 'nullable-string', null, true), field('plan_generation_command', 'string', '', false),
    field('plan_generation_model', 'string', '', false), field('plan_generation_codex_reasoning_effort', 'nullable-string', null, true),
    field('plan_generation_claude_base_url', 'string', '', false, 'sensitive-configuration'),
    field('plan_generation_claude_auth_token', 'string', '', false, 'secret-never-public'),
    field('plan_generation_claude_model', 'string', '', false, 'sensitive-configuration'),
    field('plan_generation_claude_config_id', 'integer', 0, false, 'sensitive-reference'),
  );
  if (feedback) fields.push(field('agent_cli_session_id', 'nullable-string', null, true, 'internal'));
  fields.push(
    field('generate_fail_count', 'integer', 0, true), field('last_generate_fail_at', 'nullable-string', null, true),
    field('last_generate_error', 'nullable-string', null, true, 'user-content'),
    field('last_generate_log_file', 'nullable-string', null, true, 'path-sensitive'),
    field('last_generate_agent_cli_provider', 'nullable-string', null, true),
    field('last_generate_codex_reasoning_effort', 'nullable-string', null, true),
  );
  if (!feedback) {
    fields.push(field('source_path', 'nullable-string', null, true, 'path-sensitive'), field('source_hash', 'nullable-string', null, true, 'internal'));
  }
  fields.push(
    field('linked_plan_id', 'nullable-integer', null, true), field('created_at', 'utc-string', null, false),
    field('updated_at', 'utc-string', null, false), field('accepted_at', 'nullable-utc-string', null, true),
  );
  return fields;
}

function field(name, type, defaultValue, nullable, sensitivity = 'public') {
  return { name, type, default: defaultValue, nullable, sensitivity };
}

function attachmentStorageFields() {
  return [
    field('id', 'integer', null, false), field('project_id', 'nullable-in-legacy/integer-in-P06', null, true),
    field('owner_type', 'string', null, false), field('owner_id', 'integer', null, false),
    field('original_name', 'string', null, false), field('stored_path', 'string', null, false, 'server-path-never-public'),
    field('mime_type', 'nullable-string', null, true), field('size', 'integer', null, false),
    field('hash', 'sha256-string', null, false, 'integrity-never-public'), field('created_at', 'utc-string', null, false),
  ];
}

function planLinkFields() {
  return [
    field('id', 'integer', null, false), field('project_id', 'integer', null, false), field('intake_type', 'requirement|feedback', null, false),
    field('intake_id', 'integer', null, false), field('plan_id', 'integer', null, false), field('phase_index', 'integer', 1, false),
    field('phase_title', 'string', '', false), field('created_at', 'utc-string', null, false), field('updated_at', 'utc-string', null, false),
  ];
}

function mutationContracts() {
  return [
    {
      id: 'requirements:create',
      projectIdAliases: ['projectId', 'id'],
      title: 'String(title ?? empty).trim(); otherwise first trimmed non-empty body line truncated to 80 code units; otherwise 未命名需求',
      body: 'input.body || empty string',
      status: 'input.status || open after normalizeDraftIntakeInput draft override',
      config: 'effective intake CLI and plan-generation configuration is snapshotted at create time',
      order: ['validate project', 'derive values', 'reject duplicate', 'INSERT intake', 'persist attachments one by one', 'add requirement.created event', 'optional autoRun', 'reread AppSnapshot'],
      event: { type: 'requirement.created', message: '需求 #<id> 已创建，等待循环扫描生成计划' },
    },
    {
      id: 'feedback:create',
      projectIdAliases: ['projectId', 'id'],
      title: 'same rule as requirement with fallback 未命名反馈',
      requirementId: 'falsy/null -> null; positive id must exist and belong to the same project',
      body: 'input.body || empty string',
      status: 'input.status || open after draft override',
      order: ['validate project', 'validate requirement association', 'derive values', 'reject duplicate in association group', 'INSERT intake', 'persist attachments', 'add feedback.created event', 'optional autoRun', 'reread AppSnapshot'],
      event: { type: 'feedback.created', message: '反馈 #<id> 已创建，等待循环扫描生成计划' },
    },
    {
      id: 'requirements:update',
      missing: '需求不存在',
      title: 'input.title ?? titleFromBody(next body, 未命名需求); explicit empty string remains empty; omitted title is rederived instead of preserving current title',
      body: 'input.body ?? current.body ?? empty',
      status: 'input.status ?? current.status ?? open',
      duplicateCheck: false,
      order: ['validate project/id/row', 'UPDATE intake', 'persist new attachments', 'reread AppSnapshot'],
    },
    {
      id: 'feedback:update',
      missing: '反馈不存在',
      requirementId: 'undefined preserves current nonzero id or null; other falsy clears; truthy is written without legacy ownership validation',
      title: 'input.title ?? titleFromBody(next body, 未命名反馈); explicit empty remains empty',
      body: 'input.body ?? current.body ?? empty',
      status: 'input.status ?? current.status ?? open',
      duplicateCheck: false,
      order: ['validate project/id/row', 'UPDATE intake', 'persist new attachments', 'reread AppSnapshot'],
    },
    {
      id: 'intake:accept|unaccept',
      validation: 'strict requirement|feedback type, positive id, row exists in the same project',
      status: 'unchanged',
      accept: 'accepted_at and updated_at receive the same new UTC time; repeated accept refreshes the time and records previousAcceptedAt',
      unaccept: 'accepted_at=NULL and updated_at receives a new UTC time; repeated unaccept succeeds',
      eventTypes: ['requirement.accepted', 'requirement.unaccepted', 'feedback.accepted', 'feedback.unaccepted'],
      response: '{ targetType, intakeType, id, accepted_at }; IPC then returns a reread AppSnapshot',
    },
  ];
}

function deletionContract() {
  return {
    intakeDelete: {
      preconditions: ['same-project intake exists', 'project row is readable'],
      beforeTransaction: 'load attachments and links; stop operations for every existing linked plan (not rolled back if the later database transaction fails)',
      databaseBatch: [
        'requirement only: set same-project feedback.requirement_id=NULL and update timestamp',
        'delete all intake_plan_links for the intake',
        'for each linked existing plan: delete plan_tasks, plan row and matching plan scan_files',
        'delete attachment metadata for compatible owner types',
        'delete the intake row',
      ],
      databaseAtomicity: 'AppDatabase.runBatch uses one BEGIN/COMMIT and rolls back every database statement on failure',
      afterCommit: ['best-effort safe plan file deletes', 'best-effort attachment file deletes inside configured attachment root', 'intake.deleted event', 'immediate update emission', 'snapshot reread'],
      fileFailure: 'does not roll back database deletion; attachment result counts deleted/skipped/failed and plan-file failures are events',
      event: 'intake.deleted with intakeType/intakeId, first and all plan ids/files, and attachment cleanup counts',
    },
    planDelete: {
      behavior: 'stop plan operations, delete links, synchronize linked intake legacy ids to the first remaining link, delete tasks/plan/scan cache, best-effort delete plan file, retain every intake',
      databaseAtomicity: 'links, legacy sync, task/plan/scan deletion share one runBatch transaction',
      legacySyncGuard: 'only rows whose linked_plan_id equals the deleted plan are changed',
      event: 'plan.deleted with task/operation counts, linked intake ids, keepIntakes=true and optional reason',
    },
    recoveryBoundary: 'P06 strengthens post-commit file work into a persistent recoverable workflow. A database commit must never be reported as full file success when cleanup is pending or failed.',
  };
}

function attachmentContract() {
  return {
    compatibleOwnerTypes: {
      requirementWrite: ['requirement'],
      requirementReadDelete: ['requirement', 'requirements'],
      feedback: ['feedback'],
    },
    projectOwnership: 'project_id is written from the intake project; all snapshot listing and intake deletion queries include project_id',
    inputSources: ['path', 'clipboard-image', 'dataUrl', 'base64', 'dataBase64', 'bytes', 'buffer', 'data'],
    legacyPersistence: 'copy/write bytes first with exclusive create, stat and SHA-256 the final file, then INSERT one metadata row; files are processed serially in input order',
    naming: 'display/original name takes basename after slash normalization; storage name contains time, random bytes, hash prefix and a sanitized/truncated name',
    mime: 'declared type is normalized by removing parameters and lowercasing; otherwise infer a small extension allowlist; unknown is application/octet-stream',
    ordering: 'AppSnapshot attachments ORDER BY created_at DESC, id DESC; deletion inventory ORDER BY id ASC',
    legacyRisks: [
      'path input trusts an arbitrary existing local file and follows filesystem paths',
      'no count/byte/MIME/content limit in saveAttachments itself',
      'database insert failure after byte write leaves an orphan',
      'snapshot SELECT * exposes stored_path and hash',
      'autoplan-file and prompt formatting can expose local absolute paths',
      'delete uses path containment checks but not a persistent operation journal or quarantine',
    ],
    newPublicRepresentation: {
      allowedFieldsOnly: ['id', 'display_name', 'size', 'mime_type', 'download_url'],
      displayNameSource: 'legacy original_name',
      forbiddenEverywherePublic: ['owner_type', 'owner_id', 'project_id', 'original_name', 'stored_path', 'hash', 'file://', 'autoplan-file://', 'server absolute path'],
      downloadUrl: 'authenticated loopback HTTP content endpoint; never a storage path',
    },
  };
}

function snapshotContract() {
  return {
    mutationReturn: 'all successful IPC create/update/delete/accept/unaccept operations return LoopService.snapshot(projectId) after persistence; failures do not return a success snapshot',
    collectionOrder: {
      requirements: ['updated_at DESC'], feedback: ['updated_at DESC'], attachments: ['created_at DESC', 'id DESC'],
      linkedPlans: ['phase_index ASC', 'plan_id ASC'], events: ['id DESC', 'LIMIT 80'],
    },
    intakeRow: {
      base: 'SELECT intake.* plus legacy joined plan aliases, then normalize agent CLI, accepted_at, backend fields and generation-failure fields',
      acceptedAt: 'always explicit string or null',
      generationFailure: 'generate_fail_count is finite number default 0; other failure fields are explicit normalized value or null',
      linkedPlans: 'always present array; link DTO uses snake_case fields and explicit nulls',
      noLink: 'linked_plans=[] and legacy plan summary aliases are absent, not null',
      withLinkAliases: ['plan_title', 'plan_file_path', 'plan_status', 'plan_completed', 'plan_total', 'linked_plan_title', 'linked_plan_file_path', 'linked_plan_status', 'linked_plan_completed_tasks', 'linked_plan_total_tasks'],
    },
    legacyAttachmentRisk: 'current Node snapshot returns SELECT * metadata including stored_path/hash; this is evidence only and must not be copied into the new public snapshot',
    publicAttachmentRule: 'P06 HTTP/MCP/UI-compatible snapshot attachment DTO is the allowed five-field representation defined above',
  };
}

function mcpContract() {
  return {
    tools: ['list_requirements', 'create_requirement', 'list_feedback', 'create_feedback'],
    list: 'requires project ownership; optional status filter; limit defaults 100/max 200; ORDER BY updated_at DESC, id DESC',
    create: 'requires non-empty trimmed title and body, so IPC default-title behavior is not reachable through MCP; attachments max 20 and input validation is transport-only',
    response: 'camelCase intake summary/detail plus linked plan summaries; create identifies the latest intake by project and includes an openable deep link and reduced snapshot counts',
    duplicate: 'DUPLICATE_INTAKE maps to isError plus stable code/errorCode, intake type, existingId and type-specific existing id',
    legacyAttachmentRisk: 'MCP path input is accepted today; P06 permits it only for an explicitly local caller after P05 Files policy authorization and converts it to a controlled byte stream',
  };
}

function errorContract() {
  return [
    { code: 'PROJECT_NOT_FOUND', legacyMessage: '项目不存在', operations: ['create', 'update', 'delete'] },
    { code: 'RECORD_NOT_FOUND', legacyMessage: '记录不存在', operations: ['IPC id normalization'] },
    { code: 'REQUIREMENT_NOT_FOUND', legacyMessage: '需求不存在', operations: ['requirement update/delete'] },
    { code: 'FEEDBACK_NOT_FOUND', legacyMessage: '反馈不存在', operations: ['feedback update/delete'] },
    { code: 'REQUIREMENT_ASSOCIATION_NOT_FOUND', legacyMessage: '关联需求不存在', operations: ['feedback create'] },
    { code: 'REQUIREMENT_PROJECT_MISMATCH', legacyMessage: '关联需求不属于当前项目', operations: ['feedback create'] },
    { code: 'INTAKE_TYPE_INVALID', legacyMessage: '需求/反馈类型无效', operations: ['accept/unaccept strict route'] },
    { code: 'INTAKE_ID_INVALID', legacyMessage: '需求/反馈 ID 无效', operations: ['accept/unaccept'] },
    { code: 'INTAKE_PROJECT_MISMATCH_OR_NOT_FOUND', legacyMessagePattern: '<需求|反馈>不存在或不属于当前项目', operations: ['accept/unaccept'] },
    { code: 'DUPLICATE_INTAKE', legacyMessagePattern: '<需求|反馈>已存在：#<id>', operations: ['create'] },
    { code: 'ATTACHMENT_UNIQUE_NAME_EXHAUSTED', legacyMessage: '附件保存失败：无法生成唯一文件名', operations: ['attachment save'] },
  ];
}

function writeContract(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  const outputPath = path.resolve(rootDir, options.outputPath || OUTPUT_PATH);
  const expected = path.resolve(rootDir, OUTPUT_PATH);
  if (outputPath !== expected && !options.allowExternalOutput) throw new Error('write contract output must remain in docs/migration/p06');
  fs.mkdirSync(path.dirname(outputPath), { recursive: true });
  fs.writeFileSync(outputPath, stableJson(buildContract(rootDir)), 'utf8');
  return outputPath;
}

function parseArgs(argv) {
  const options = { write: false };
  for (const arg of argv) {
    if (arg === '--write') options.write = true;
    else throw new Error(`unknown argument: ${arg}`);
  }
  return options;
}

if (require.main === module) {
  try {
    const options = parseArgs(process.argv.slice(2));
    const contract = buildContract(ROOT);
    if (options.write) {
      const outputPath = writeContract();
      process.stdout.write(`${JSON.stringify({ ok: true, output: path.relative(ROOT, outputPath), sha256: sha256(stableJson(contract)) })}\n`);
    } else {
      process.stdout.write(stableJson(contract));
    }
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}

module.exports = {
  CONTRACT_VERSION,
  FORMAT_VERSION,
  OUTPUT_PATH,
  SOURCE_MARKERS,
  buildContract,
  parseArgs,
  sha256,
  stableJson,
  writeContract,
};
