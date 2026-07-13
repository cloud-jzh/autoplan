const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { join } = require('node:path');
const { describe, it } = require('node:test');
const vm = require('node:vm');
const ts = require('typescript');

function source(...parts) {
  return readFileSync(join(process.cwd(), ...parts), 'utf8');
}

function tupleKeys(text, name) {
  const match = text.match(new RegExp(`export const ${name} = \\[([\\s\\S]*?)\\] as const`));
  assert.ok(match, `missing ${name}`);
  return [...match[1].matchAll(/'([^']+)'/g)].map((item) => item[1]);
}

const operationKeys = tupleKeys(source('src/renderer/lib/api/client.ts'), 'AUTOPLAN_CLIENT_OPERATION_KEYS');
const eventKeys = tupleKeys(source('src/renderer/lib/api/events.ts'), 'AUTOPLAN_CLIENT_EVENT_KEYS');

function transpiledModule(file, runtimeImports = {}) {
  const compiled = ts.transpileModule(source(...file.split('/')), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
    fileName: file,
  }).outputText;
  const module = { exports: {} };
  const wrapper = vm.runInThisContext(`(function (require, module, exports) { ${compiled}\n})`);
  wrapper((id) => {
    if (Object.prototype.hasOwnProperty.call(runtimeImports, id)) return runtimeImports[id];
    throw new Error(`unexpected runtime import: ${id}`);
  }, module, module.exports);
  return module.exports;
}

function clients(api, fetchImpl) {
  const events = transpiledModule('src/renderer/lib/api/events.ts');
  const { IpcAutoplanClient } = transpiledModule('src/renderer/lib/api/ipcClient.ts', {
    './client': { AUTOPLAN_CLIENT_OPERATION_KEYS: operationKeys },
  });
  const { HttpAutoplanClient } = transpiledModule('src/renderer/lib/api/httpClient.ts', {
    './client': { AUTOPLAN_CLIENT_OPERATION_KEYS: operationKeys },
    './events': { ...events, AUTOPLAN_CLIENT_EVENT_KEYS: eventKeys },
  });
  const ipc = new IpcAutoplanClient(api);
  const http = new HttpAutoplanClient({
    baseUrl: 'http://127.0.0.1:43123', sessionCredential: 'A'.repeat(43), timeoutMs: 1_000,
    fetchImpl, idempotencyKeyFactory: () => 'renderer:intake-contract', delegate: ipc,
  });
  return { ipc, http };
}

function project() {
  return {
    id: 7, name: 'Synthetic project', workspace_path: '<fixture-workspace>/p7', description: '',
    created_at: '2026-01-02T03:04:05.000Z', updated_at: '2026-01-02T03:04:08.000Z',
  };
}

function snapshot(requirement, feedback) {
  const active = project();
  return {
    activeProjectId: 7, activeProject: active, projects: [active], mcp: {},
    state: { project_id: 7, workspace_path: active.workspace_path, version: 1 },
    requirements: [requirement], feedback: [feedback], attachments: [], plans: [], tasks: [], events: [], scans: [],
    scanSummary: {}, scripts: [], executors: [], terminals: [], activeOperation: null,
    activeOperations: [], lastOperation: null,
  };
}

function dto(type, id, requirementId = null) {
  return {
    id, project_id: 7, intake_type: type, requirement_id: requirementId,
    title: `${type} title`, body: `${type} body`, status: 'open', accepted_at: null, linked_plan_id: 51,
    linked_plans: [{ link_id: 61, plan_id: 51, phase_index: 1, phase_title: 'Implement' }],
    created_at: '2026-01-02T03:04:05.000Z', updated_at: '2026-01-02T03:04:08.000Z',
    agent_cli_provider: null, agent_cli_command: '', codex_reasoning_effort: null,
    plan_generation_strategy: null, plan_generation_provider: null, plan_generation_command: '',
    plan_generation_model: '', plan_generation_codex_reasoning_effort: null,
    plan_generation_claude_base_url: '', plan_generation_claude_model: '', plan_generation_claude_config_id: 0,
    plan_generation_has_claude_auth_token: false, generate_fail_count: 0, last_generate_fail_at: null,
    last_generate_error: null, last_generate_agent_cli_provider: null, last_generate_codex_reasoning_effort: null,
  };
}

function response(body, status = 200) {
  const text = JSON.stringify(body);
  return {
    status, ok: status >= 200 && status < 300, body: null,
    headers: new Headers({
      'Content-Type': 'application/json; charset=utf-8', 'Content-Length': String(Buffer.byteLength(text)),
      'X-Request-ID': body.request_id,
    }),
    text: async () => text,
  };
}

function success(data) {
  return response({ data, request_id: 'req_intake_contract' });
}

function apiFixture(contractSnapshot) {
  const calls = [];
  const api = {};
  for (const key of operationKeys) {
    if (key === 'mcpToolNames') api[key] = [];
    else if (key === 'fileAccess') api[key] = { get: async () => ({}), save: async () => ({}) };
    else if (key === 'snapshot') api[key] = async () => contractSnapshot;
    else if (['createRequirement', 'updateRequirement', 'deleteRequirement', 'createFeedback', 'updateFeedback',
      'deleteFeedback', 'acceptIntake', 'unacceptIntake'].includes(key)) {
      api[key] = async (input) => {
        calls.push({ key, input });
        return contractSnapshot;
      };
    } else api[key] = async () => ({ key });
  }
  for (const key of eventKeys) api[key] = () => () => {};
  return { api, calls };
}

describe('Intake IPC/HTTP transport contract', () => {
  it('keeps CRUD and acceptance AppSnapshot compatibility while HTTP bypasses the IPC mutation path', async () => {
    const requirement = dto('requirement', 30);
    const feedback = dto('feedback', 31, 30);
    const contractSnapshot = snapshot(requirement, feedback);
    const fixture = apiFixture(contractSnapshot);
    const captures = [];
    const fetchImpl = async (target, init) => {
      const url = new URL(target);
      captures.push({ path: `${url.pathname}${url.search}`, method: init.method, body: init.body });
      if (url.pathname.endsWith('/requirements') && init.method === 'GET') {
        return response({
          data: [requirement], pagination: { page: 1, page_size: 50, total: 1, next_page: null },
          request_id: 'req_intake_contract',
        });
      }
      if (url.pathname.endsWith('/feedback') && init.method === 'GET') {
        return response({
          data: [feedback], pagination: { page: 1, page_size: 50, total: 1, next_page: null },
          request_id: 'req_intake_contract',
        });
      }
      if (url.pathname.endsWith('/requirements/30') && init.method === 'GET') return success(requirement);
      if (url.pathname.endsWith('/feedback/31') && init.method === 'GET') return success(feedback);
      if (url.pathname.endsWith('/plan-links') && init.method === 'GET') return success(requirement.linked_plans);
      if (url.pathname.endsWith('/plan-links') && init.method === 'PUT') return success({ snapshot: contractSnapshot });
      if (url.pathname.endsWith('/accept') || ['POST', 'PATCH', 'DELETE'].includes(init.method)) {
        return success({ snapshot: contractSnapshot });
      }
      throw new Error(`unexpected HTTP contract path: ${url.pathname}`);
    };
    const { ipc, http } = clients(fixture.api, fetchImpl);
    const requirementInput = { projectId: 7, body: 'requirement body', attachments: [] };
    const feedbackInput = { projectId: 7, body: 'feedback body', attachments: [], requirementId: 30 };

    for (const [key, input] of [
      ['createRequirement', requirementInput],
      ['updateRequirement', { projectId: 7, id: 30, status: 'completed' }],
      ['deleteRequirement', { projectId: 7, id: 30 }],
      ['createFeedback', feedbackInput],
      ['updateFeedback', { projectId: 7, id: 31, requirementId: null }],
      ['deleteFeedback', { projectId: 7, id: 31 }],
      ['acceptIntake', { projectId: 7, type: 'requirement', id: 30 }],
      ['unacceptIntake', { projectId: 7, type: 'feedback', id: 31 }],
    ]) {
      assert.deepStrictEqual(await http[key](input), await ipc[key](input));
    }

    assert.deepStrictEqual((await http.listRequirements({ projectId: 7 })).data, [requirement]);
    assert.deepStrictEqual((await http.listFeedback({ projectId: 7 })).data, [feedback]);
    assert.deepStrictEqual(await http.getRequirement(7, 30), requirement);
    assert.deepStrictEqual(await http.getFeedback(7, 31), feedback);
    assert.deepStrictEqual(await http.listRequirementPlanLinks(7, 30), requirement.linked_plans);
    await http.replaceFeedbackPlanLinks(7, 31, [{ planId: 51, phaseIndex: 1, phaseTitle: 'Implement' }]);

    assert.equal(fixture.calls.length, 8, 'only the explicit IPC comparison side may write IPC');
    assert.deepStrictEqual(captures.slice(0, 8).map(({ path, method }) => ({ path, method })), [
      { path: '/api/v1/projects/7/requirements', method: 'POST' },
      { path: '/api/v1/projects/7/requirements/30', method: 'PATCH' },
      { path: '/api/v1/projects/7/requirements/30', method: 'DELETE' },
      { path: '/api/v1/projects/7/feedback', method: 'POST' },
      { path: '/api/v1/projects/7/feedback/31', method: 'PATCH' },
      { path: '/api/v1/projects/7/feedback/31', method: 'DELETE' },
      { path: '/api/v1/projects/7/requirements/30/accept', method: 'POST' },
      { path: '/api/v1/projects/7/feedback/31/accept', method: 'DELETE' },
    ]);
    assert.ok(captures.slice(0, 8).every((capture) => typeof capture.body === 'string'));
    assert.doesNotMatch(captures.slice(0, 8).map((capture) => capture.body).join('\n'), /projectId|attachments/);
  });

  it('uses only controlled attachment URLs and multipart browser bytes', async () => {
    const requirement = dto('requirement', 30);
    const feedback = dto('feedback', 31, 30);
    const contractSnapshot = snapshot(requirement, feedback);
    const fixture = apiFixture(contractSnapshot);
    let captured;
    const { http } = clients(fixture.api, async (target, init) => {
      captured = { target, init };
      return response({
        data: {
          attachment: {
            id: 91, display_name: 'diagram.png', size: 4, mime_type: 'image/png',
            download_url: '/api/v1/attachments/91/content',
          }, state: 'completed', recovery_required: false,
        }, request_id: 'req_intake_contract',
      }, 201);
    });
    const result = await http.uploadIntakeAttachment('requirement', {
      projectId: 7, id: 30,
      attachments: [{
        id: 'browser-blob', source: 'clipboard-image', name: 'diagram.png', size: 4,
        type: 'image/png', previewUrl: 'blob:preview', blob: new Blob([Uint8Array.from([1, 2, 3, 4])]),
      }],
    }, 30, 0);

    assert.equal(captured.target, 'http://127.0.0.1:43123/api/v1/projects/7/requirements/30/attachments');
    assert.ok(captured.init.body instanceof FormData);
    assert.ok(captured.init.body.get('file') instanceof Blob);
    assert.equal(captured.init.headers['Content-Type'], undefined);
    assert.deepStrictEqual(Object.keys(result.attachment).sort(), [
      'display_name', 'download_url', 'id', 'mime_type', 'size',
    ]);
    assert.equal(result.attachment.download_url,
      'http://127.0.0.1:43123/api/v1/attachments/91/content?project_id=7');
    assert.equal(http.getAttachmentDownloadUrl(7, 91),
      'http://127.0.0.1:43123/api/v1/attachments/91/content?project_id=7');
    assert.doesNotMatch(JSON.stringify(result.attachment), /stored_path|original_name|hash|autoplan-file|file:\/\//);
    const shared = source('src/renderer/components/shared.tsx');
    const httpSource = source('src/renderer/lib/api/httpClient.ts');
    assert.match(shared, /controlledAttachmentUrl\(attachment\.download_url, attachment\.id\)/);
    assert.doesNotMatch(httpSource, /pathToFileURL|autoplan-file|File\.path/);
  });
});
