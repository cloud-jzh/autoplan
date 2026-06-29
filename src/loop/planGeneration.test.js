const { describe, it } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('path');
const { recoverPlanFromStdout, generatePlanForIntake } = require('./planGeneration');

/**
 * planGeneration 模块单元测试（node:test 风格，对齐 acceptance.test.js）。
 * 覆盖 recoverPlanFromStdout 兜底落盘逻辑和短正文上下文注入。
 */

describe('recoverPlanFromStdout', () => {
  it('stdout 含 ## 任务拆解 → 正确切取并写入 planFile，返回 true', () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-test-'));
    try {
      const planFile = path.join(dir, 'test-plan.md');
      const stdout = [
        '好的，我来帮你生成开发计划。',
        '',
        '## 任务拆解',
        '',
        '- [ ] P001: 任务一 <!-- scope: src/foo.js -->',
        '- [ ] P002: 任务二 <!-- scope: src/bar.js -->',
      ].join('\n');

      const result = recoverPlanFromStdout(planFile, stdout);

      assert.equal(result, true);
      assert.ok(fs.existsSync(planFile));
      const content = fs.readFileSync(planFile, 'utf-8');
      assert.ok(content.startsWith('## 任务拆解'), '应从 ## 标题开始，去掉前面对话式寒暄');
      assert.ok(!content.includes('好的，我来帮你生成开发计划。'), '不应包含寒暄文本');
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  });

  it('stdout 仅寒暄文本（无任何 ## 标题）→ 返回 false，不写入文件', () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-test-'));
    try {
      const planFile = path.join(dir, 'test-plan.md');
      const stdout = '好的，请告诉我你希望我生成什么样的计划？需要更多信息才能帮你。';

      const result = recoverPlanFromStdout(planFile, stdout);

      assert.equal(result, false);
      assert.ok(!fs.existsSync(planFile), '不应写入文件');
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  });

  it('stdout 为空字符串 → 返回 false', () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-test-'));
    try {
      const planFile = path.join(dir, 'test-plan.md');

      const result = recoverPlanFromStdout(planFile, '');

      assert.equal(result, false);
      assert.ok(!fs.existsSync(planFile));
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  });
});

describe('generatePlanForIntake 短正文上下文注入', () => {
  /**
   * 构造最小 mock service，捕获 runCodex 收到的 prompt 后立即返回失败，
   * 避免进入完整生成流程。适用于 prompt 内容断言。
   */
  function createCaptureService() {
    const captured = { prompt: null };
    const svc = {
      setPhase() {},
      status() {
        return {};
      },
      intakeAttachmentPrompt() {
        return '';
      },
      async runCodex(_workspace, prompt) {
        captured.prompt = prompt;
        return { exitCode: 1, output: '', logFile: '/tmp/mock.log', errorMessage: 'mock' };
      },
      addEvent() {},
      async runHookScripts() {},
      db: {
        all() {
          return [];
        },
        run() {},
      },
    };
    return { svc, captured };
  }

  function makeHelpers(workspace) {
    return {
      timestampForPath: () => '20260629-120000',
      readSnippet: (filePath, maxLen) => {
        if (!fs.existsSync(filePath)) return '';
        return fs.readFileSync(filePath, 'utf-8').slice(0, maxLen);
      },
      normalizeRelative: (_ws, p) => p,
      hashFile: () => 'abc123',
      hashText: () => 'abc123',
    };
  }

  it('body 长度 < 20 时 prompt 含上下文标注和 README 摘要', async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-test-ctx-'));
    try {
      // 构造 workspace fixture：放一个 README.md
      fs.writeFileSync(path.join(dir, 'README.md'), '# 测试项目\n这是一个测试项目。\n', 'utf-8');
      // 放几个目录/文件以验证目录概览
      fs.mkdirSync(path.join(dir, 'src'));
      fs.mkdirSync(path.join(dir, 'docs'));
      fs.writeFileSync(path.join(dir, 'package.json'), '{}', 'utf-8');

      const { svc, captured } = createCaptureService();
      const helpers = makeHelpers(dir);
      const intake = { __type: 'requirement', id: 1, body: '优化' }; // 2 个字 < 20

      await generatePlanForIntake(svc, helpers, 'p1', dir, intake);

      assert.ok(captured.prompt, 'prompt 应被捕获');
      assert.ok(
        captured.prompt.includes('以下是项目自动收集的上下文，供你判断需求涉及范围：'),
        '短正文时 prompt 应包含上下文标注',
      );
      assert.ok(
        captured.prompt.includes('## 项目 README 摘要：'),
        '应包含 README 摘要章节',
      );
      assert.ok(
        captured.prompt.includes('# 测试项目'),
        '应包含 README 内容',
      );
      assert.ok(
        captured.prompt.includes('## 项目根目录概览：'),
        '应包含根目录概览',
      );
      assert.ok(captured.prompt.includes('src/'), '目录列表应包含 src/');
      assert.ok(captured.prompt.includes('docs/'), '目录列表应包含 docs/');
      assert.ok(captured.prompt.includes('package.json'), '目录列表应包含文件');
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  });

  it('body 长度 ≥ 20 时 prompt 不含上下文标注', async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-test-ctx-'));
    try {
      fs.writeFileSync(path.join(dir, 'README.md'), '# 不应出现\n', 'utf-8');

      const { svc, captured } = createCaptureService();
      const helpers = makeHelpers(dir);
      const intake = { __type: 'feedback', id: 2, body: '这是一段足够长的需求描述，超过二十个字符以触发跳过上下文注入逻辑。' };

      await generatePlanForIntake(svc, helpers, 'p1', dir, intake);

      assert.ok(captured.prompt, 'prompt 应被捕获');
      assert.ok(
        !captured.prompt.includes('以下是项目自动收集的上下文'),
        '长正文时 prompt 不应包含上下文标注',
      );
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  });

  it('README.md 不存在时不报错，上下文标注仍出现但不含 README 摘要', async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-test-ctx-'));
    try {
      // 不创建 README.md

      const { svc, captured } = createCaptureService();
      const helpers = makeHelpers(dir);
      const intake = { __type: 'requirement', id: 3, body: '修 bug' };

      await generatePlanForIntake(svc, helpers, 'p1', dir, intake);

      assert.ok(captured.prompt.includes('以下是项目自动收集的上下文'), '上下文标注应出现');
      assert.ok(!captured.prompt.includes('## 项目 README 摘要：'), 'README 不存在时不应有摘要');
      assert.ok(captured.prompt.includes('## 项目根目录概览：'), '目录概览仍应出现');
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  });
});
