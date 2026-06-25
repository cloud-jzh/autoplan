const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const vm = require('node:vm');
const { saveAttachments } = require('../src/attachments');
const { AppDatabase, nowIso } = require('../src/database');
const { LoopService } = require('../src/loopService');
const { acceptPlanDraft, createPlanDraft, updatePlanDraft } = require('../src/planDrafts');

async function main() {
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-smoke-'));
  const dbPath = path.join(tempRoot, 'data', 'autoplan.sqlite');
  const workspace = path.join(tempRoot, 'workspace');
  const otherWorkspace = path.join(tempRoot, 'other-workspace');

  try {
    const db = new AppDatabase(dbPath);
    await db.init();
    const defaultState = db.get('SELECT project_id FROM project_states ORDER BY project_id ASC LIMIT 1');
    db.run('UPDATE project_states SET running = 1, phase = ?, updated_at = ? WHERE project_id = ?', [
      'execute-task',
      nowIso(),
      defaultState.project_id,
    ]);
    const loop = new LoopService(db);
    const projectId = loop.defaultProjectId();
    const otherProjectId = insertProject(db, loop, 'Other Project', otherWorkspace);

    assert.ok(projectId, '启动后应存在默认项目');
    assert.equal(loop.snapshot(projectId).state.running, 0, '重启后不应继承旧的 running 状态');
    assert.equal(loop.snapshot(projectId).state.phase, 'stopped', '重启后执行中阶段应复位为 stopped');
    db.run('UPDATE project_states SET running = 1, phase = ?, updated_at = ? WHERE project_id = ?', [
      'execute-task',
      nowIso(),
      projectId,
    ]);
    assert.equal(loop.snapshot(projectId).state.running, 0, '快照不应盲信 SQLite 残留 running 状态');
    assert.equal(loop.snapshot(projectId).state.phase, 'stopped', '空闲时快照不应显示残留执行阶段');
    assert.equal(loop.snapshot().activeProjectId, null, '未选择项目时快照应停留在项目列表');
    assert.ok(loop.snapshot().projects.length >= 2, '项目列表应包含默认项目和新增项目');

    const requirementId = insertRequirement(db, projectId);
    insertFeedback(db, projectId);
    insertRequirement(db, otherProjectId);

    const imageFile = path.join(tempRoot, 'smoke.png');
    fs.writeFileSync(
      imageFile,
      Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAFgwJ/lZl0WQAAAABJRU5ErkJggg==', 'base64'),
    );
    const attachments = saveAttachments(db, path.join(tempRoot, 'attachments'), 'requirement', requirementId, [
      { path: imageFile, name: 'smoke.png', type: 'image/png' },
    ], projectId);

    const draftId = createPlanDraft(db, {
      projectId,
      sourceType: 'requirement',
      sourceId: requirementId,
      body: '目标：\n普通文本验收\n允许拖拽图片附件',
      attachments,
    });
    updatePlanDraft(db, draftId, `${loop.snapshot(projectId).planDrafts[0].markdown}\n<!-- smoke adjusted -->\n`);

    let snapshot = loop.snapshot(projectId);
    assert.equal(snapshot.requirements.length, 1, '当前项目只应显示自己的需求');
    assert.equal(loop.snapshot(otherProjectId).requirements.length, 1, '另一个项目应保留独立需求');
    assert.equal(snapshot.feedback.length, 1, '反馈模块应按项目读取列表');
    assert.match(snapshot.requirements[0].body, /普通文本验收/, '需求正文应保留普通文本内容');
    assert.equal(snapshot.attachments.length, 1, '附件应写入 SQLite 并绑定项目');
    assert.equal(snapshot.attachments[0].project_id, projectId, '附件应记录 project_id');
    assert.equal(snapshot.attachments[0].mime_type, 'image/png', '图片附件应保留 MIME 类型');
    assert.ok(fs.existsSync(snapshot.attachments[0].stored_path), '附件文件应复制到持久化目录');
    assert.equal(snapshot.planDrafts.length, 1, '发送后应生成计划草稿');
    assert.match(snapshot.planDrafts[0].markdown, /smoke\.png.+file:\/\//, '计划草稿应通过链接引用图片附件');

    loop.configure(projectId, {
      workspacePath: workspace,
      intervalSeconds: 5,
      validationCommand: 'node -e "process.exit(0)"',
    });
    snapshot = loop.snapshot(projectId);
    assert.equal(snapshot.state.workspace_path, workspace, '项目应能保存工作区路径');
    assert.equal(snapshot.state.interval_seconds, 5, '项目应能保存循环间隔');

    loop.ensureWorkspaceDirs(workspace);
    const issueFile = path.join(workspace, 'docs', 'issues', 'smoke.md');
    fs.writeFileSync(issueFile, '# Smoke 需求\n\n- [ ] 支持普通文本输入和附件\n', 'utf8');
    const issueScan = loop.scanDirectory(path.join(workspace, 'docs', 'issues'), workspace, ['.md']);
    loop.saveScan(projectId, 'issue', issueScan);
    assert.equal(issueScan.files.length, 1, '扫描模块应发现 docs/issues 文件');
    assert.equal(loop.snapshot(projectId).scans.length, 1, '扫描记录应按项目写入 SQLite');
    assert.equal(loop.snapshot(otherProjectId).scans.length, 0, '其它项目不应看到当前项目扫描记录');

    const planId = acceptPlanDraft(db, loop, workspace, draftId);
    snapshot = loop.snapshot(projectId);
    assert.equal(snapshot.planDrafts[0].status, 'accepted', '确认后计划草稿应标记 accepted');
    assert.equal(snapshot.plans.length, 1, '确认后应加入当前项目任务系统');
    assert.equal(snapshot.plans[0].project_id, projectId, 'plan 应绑定项目');
    assert.equal(snapshot.tasks.length, 5, '任务模块应同步草稿中的 checkbox 为任务列表');
    assert.ok(
      snapshot.tasks.every((task) => hasTaskDurationShape(task)),
      '任务快照应包含符合前端类型预期的耗时字段',
    );
    assert.equal(snapshot.plans[0].total_tasks, 5, 'plan 应记录总任务数');
    assert.ok(
      snapshot.tasks.every((task) => task.raw_line.includes('scope:')),
      '入库任务应保留固定格式 scope 注释',
    );
    assert.ok(
      snapshot.tasks.every((task) => task.scope === 'unknown'),
      '无法判断 scope 的默认草稿任务应标记为 unknown',
    );
    assert.deepEqual(
      loop.parallelTaskBatch([
        { task_key: 'P001', title: 'A', raw_line: '- [ ] P001: A <!-- scope: lib/a.dart -->' },
        { task_key: 'P002', title: 'B', raw_line: '- [ ] P002: B <!-- scope: lib/b.dart -->' },
        { task_key: 'P003', title: 'C', raw_line: '- [ ] P003: C <!-- scope: lib/a.dart -->' },
      ]).map((task) => task.task_key),
      ['P001', 'P002'],
      '互不重叠 scope 的任务应允许进入同一并发批次',
    );
    assert.deepEqual(
      loop.parallelTaskBatch([
        { task_key: 'P001', title: 'A', raw_line: '- [ ] P001: A <!-- scope: unknown -->' },
        { task_key: 'P002', title: 'B', raw_line: '- [ ] P002: B <!-- scope: lib/b.dart -->' },
      ]).map((task) => task.task_key),
      ['P001'],
      'unknown scope 任务应保持串行',
    );
    assert.deepEqual(
      loop.parallelTaskBatch([
        { task_key: 'P009', title: '补充自动化回归测试', raw_line: '- [ ] P009: 补充自动化回归测试 <!-- scope: test/a.dart -->' },
        { task_key: 'P010', title: '执行回归验证', raw_line: '- [ ] P010: 执行回归验证 <!-- scope: test/b.dart -->' },
      ]).map((task) => task.task_key),
      ['P009'],
      '测试/验证类任务应保持串行',
    );

    const plan = db.get('SELECT * FROM plans WHERE id = ?', [planId]);
    const planFile = path.join(workspace, plan.file_path);
    const executableTask = db.get('SELECT * FROM plan_tasks WHERE plan_id = ? AND task_key = ?', [planId, 'P001']);
    const fakeLogFile = path.join(workspace, 'docs', 'progress', 'logs', 'fake-execute.log');
    fs.writeFileSync(fakeLogFile, 'fake ok', 'utf8');
    const originalRunCodex = loop.runCodex.bind(loop);
    const originalPlanText = fs.readFileSync(planFile, 'utf8');
    await assertPlanReadRegression(db, loop, {
      projectId,
      otherProjectId,
      otherWorkspace,
      planId,
      plan,
      planFile,
      originalPlanText,
    });
    loop.runCodex = async (_workspace, prompt) => {
      assert.match(prompt, /只执行指定任务 P001/, '执行 prompt 应锁定指定任务');
      assert.match(prompt, /不要修改 plan 文件/, '执行 prompt 应禁止 Codex 写 plan');
      await sleep(20);
      const runningTask = db.get('SELECT * FROM plan_tasks WHERE id = ?', [executableTask.id]);
      assert.equal(runningTask.status, 'running', '任务开始后应写入 running 状态');
      assertIsoString(runningTask.started_at, '任务开始后应写入 started_at');
      assert.equal(runningTask.finished_at, null, '任务运行中不应写入 finished_at');
      assert.equal(runningTask.duration_ms, 0, '首次运行中累计耗时应保持 0');
      const runningSnapshotTask = taskByKey(loop.snapshot(projectId), 'P001');
      assertTaskDurationShape(runningSnapshotTask, '运行中任务快照');
      assert.equal(typeof runningSnapshotTask.run_duration_ms, 'number', '运行中任务快照应包含实时耗时');
      fs.writeFileSync(planFile, originalPlanText.replace('P002', 'P999'), 'utf8');
      return { exitCode: 0, logFile: fakeLogFile, lastFile: path.join(workspace, 'fake-last.txt') };
    };
    const executeResult = await loop.executeTask(workspace, plan, executableTask);
    loop.runCodex = originalRunCodex;
    assert.equal(executeResult.exitCode, 0, '模拟执行应成功');
    loop.completeTask(workspace, plan, executableTask, executeResult);
    const guardedPlanText = fs.readFileSync(planFile, 'utf8');
    assert.match(guardedPlanText, /- \[x\] P001:/, '系统应在任务成功后勾选 checkbox');
    assert.match(guardedPlanText, /P001 AutoPlan 完成/, '系统应在进度区写入完成记录');
    assert.doesNotMatch(guardedPlanText, /P999/, 'Codex 对 plan 的写入应被恢复');
    const completedTask = db.get('SELECT * FROM plan_tasks WHERE id = ?', [executableTask.id]);
    assert.equal(
      completedTask.status,
      'completed',
      '系统应更新任务入库状态',
    );
    assertIsoString(completedTask.started_at, '任务完成后应保留 started_at');
    assertIsoString(completedTask.finished_at, '任务完成后应写入 finished_at');
    assert.ok(completedTask.duration_ms > 0, '任务完成后应写入正数 duration_ms');
    const completedSnapshotTask = taskByKey(loop.snapshot(projectId), 'P001');
    assertTaskDurationShape(completedSnapshotTask, '已完成任务快照');
    assert.ok(completedSnapshotTask.duration_ms > 0, '已完成任务快照应包含累计耗时');
    const completedTaskEvents = taskEventsByKey(loop.snapshot(projectId), 'P001');
    assertTaskEventOrder(completedTaskEvents, ['task.succeeded', 'task.started'], '成功任务事件');
    assertTaskEventMeta(completedTaskEvents[0], executableTask, 'completed', '成功结束任务事件');
    assert.equal(completedTaskEvents[0].meta.log, fakeLogFile, '成功结束事件 meta 应包含日志路径');
    assert.equal(completedTaskEvents[0].meta.exitCode, 0, '成功结束事件 meta 应包含退出码');
    assertTaskEventMeta(completedTaskEvents[1], executableTask, 'running', '开始任务事件');
    assert.equal(
      db.get('SELECT completed_tasks FROM plans WHERE id = ?', [planId]).completed_tasks,
      1,
      '系统应更新 plan 完成计数',
    );

    const retryTask = db.get('SELECT * FROM plan_tasks WHERE plan_id = ? AND task_key = ?', [planId, 'P002']);
    loop.runCodex = async (_workspace, prompt) => {
      assert.match(prompt, /只执行指定任务 P002/, '重试任务首次执行 prompt 应锁定指定任务');
      await sleep(20);
      const runningRetryTask = db.get('SELECT * FROM plan_tasks WHERE id = ?', [retryTask.id]);
      assert.equal(runningRetryTask.status, 'running', '失败前任务应处于 running 状态');
      assertIsoString(runningRetryTask.started_at, '失败前任务应写入 started_at');
      assert.equal(runningRetryTask.finished_at, null, '失败前任务运行中不应写入 finished_at');
      assertTaskDurationShape(taskByKey(loop.snapshot(projectId), 'P002'), '失败前任务快照');
      return { exitCode: 1, logFile: fakeLogFile, lastFile: path.join(workspace, 'fake-failed.txt') };
    };
    const failedResult = await loop.executeTask(workspace, plan, retryTask);
    loop.runCodex = originalRunCodex;
    assert.equal(failedResult.exitCode, 1, '模拟失败应返回非 0 exitCode');
    const failedTask = db.get('SELECT * FROM plan_tasks WHERE id = ?', [retryTask.id]);
    assert.equal(failedTask.status, 'pending', '任务失败后应回退为 pending');
    assertIsoString(failedTask.started_at, '任务失败后应保留 started_at');
    assertIsoString(failedTask.finished_at, '任务失败后应写入 finished_at');
    assert.ok(failedTask.duration_ms > 0, '任务失败后应累计正数 duration_ms');
    const failedTaskEvents = taskEventsByKey(loop.snapshot(projectId), 'P002');
    assertTaskEventOrder(failedTaskEvents, ['task.failed', 'task.started'], '失败任务事件');
    assertTaskEventMeta(failedTaskEvents[0], retryTask, 'failed', '失败任务事件');
    assert.equal(failedTaskEvents[0].meta.log, fakeLogFile, '失败事件 meta 应包含日志路径');
    assert.equal(failedTaskEvents[0].meta.exitCode, 1, '失败事件 meta 应包含退出码');
    assertTaskEventMeta(failedTaskEvents[1], retryTask, 'running', '失败前开始任务事件');

    const firstAttemptDurationMs = failedTask.duration_ms;
    const retryAttemptTask = db.get('SELECT * FROM plan_tasks WHERE id = ?', [retryTask.id]);
    loop.runCodex = async (_workspace, prompt) => {
      assert.match(prompt, /只执行指定任务 P002/, '重试任务再次执行 prompt 应锁定指定任务');
      await sleep(20);
      const runningRetryTask = db.get('SELECT * FROM plan_tasks WHERE id = ?', [retryTask.id]);
      assert.equal(runningRetryTask.status, 'running', '重试时任务应重新进入 running 状态');
      assertIsoString(runningRetryTask.started_at, '重试时任务应刷新 started_at');
      assert.equal(runningRetryTask.finished_at, null, '重试运行中应清空 finished_at');
      assert.equal(runningRetryTask.duration_ms, firstAttemptDurationMs, '重试运行中不应丢失历史累计耗时');
      return { exitCode: 0, logFile: fakeLogFile, lastFile: path.join(workspace, 'fake-retry.txt') };
    };
    const retryResult = await loop.executeTask(workspace, plan, retryAttemptTask);
    loop.runCodex = originalRunCodex;
    assert.equal(retryResult.exitCode, 0, '模拟重试应成功');
    loop.completeTask(workspace, plan, retryAttemptTask, retryResult);
    const retriedTask = db.get('SELECT * FROM plan_tasks WHERE id = ?', [retryTask.id]);
    assert.equal(retriedTask.status, 'completed', '重试成功后任务应完成');
    assertIsoString(retriedTask.finished_at, '重试成功后应写入 finished_at');
    assert.ok(retriedTask.duration_ms > firstAttemptDurationMs, '重试成功后应继续累加 duration_ms');
    assertTaskDurationShape(taskByKey(loop.snapshot(projectId), 'P002'), '重试完成任务快照');

    const stoppableTask = db.get('SELECT * FROM plan_tasks WHERE plan_id = ? AND task_key = ?', [planId, 'P003']);
    loop.startTaskRun(stoppableTask.id, nowIso());
    await sleep(20);
    loop.stopTask(projectId, stoppableTask.id);
    const stoppedTask = db.get('SELECT * FROM plan_tasks WHERE id = ?', [stoppableTask.id]);
    assert.equal(stoppedTask.status, 'pending', '手动停止后任务应回到 pending 以便重试');
    assertIsoString(stoppedTask.finished_at, '手动停止后应写入 finished_at');
    assert.ok(stoppedTask.duration_ms > 0, '手动停止后应累计耗时');
    const stoppedTaskEvents = taskEventsByKey(loop.snapshot(projectId), 'P003');
    assertTaskEventOrder(stoppedTaskEvents, ['task.stop.requested'], '手动停止任务事件');
    assertTaskEventMeta(stoppedTaskEvents[0], stoppableTask, 'stopping', '手动停止任务事件');

    const stoppedDurationMs = stoppedTask.duration_ms;
    const retryStoppedTask = db.get('SELECT * FROM plan_tasks WHERE id = ?', [stoppableTask.id]);
    loop.runCodex = async (_workspace, prompt) => {
      assert.match(prompt, /只执行指定任务 P003/, '停止后重试任务 prompt 应锁定指定任务');
      await sleep(20);
      const runningStoppedRetryTask = db.get('SELECT * FROM plan_tasks WHERE id = ?', [stoppableTask.id]);
      assert.equal(runningStoppedRetryTask.status, 'running', '停止后重试应重新进入 running 状态');
      assert.equal(runningStoppedRetryTask.duration_ms, stoppedDurationMs, '停止后重试不应丢失停止前耗时');
      return { exitCode: 0, logFile: fakeLogFile, lastFile: path.join(workspace, 'fake-stopped-retry.txt') };
    };
    const stoppedRetryResult = await loop.executeTask(workspace, plan, retryStoppedTask);
    loop.runCodex = originalRunCodex;
    assert.equal(stoppedRetryResult.exitCode, 0, '手动停止后的重试应成功');
    loop.completeTask(workspace, plan, retryStoppedTask, stoppedRetryResult);
    const stoppedRetriedTask = db.get('SELECT * FROM plan_tasks WHERE id = ?', [stoppableTask.id]);
    assert.equal(stoppedRetriedTask.status, 'completed', '手动停止后任务应可重试完成');
    assert.ok(stoppedRetriedTask.duration_ms > stoppedDurationMs, '手动停止后重试应继续累加耗时');
    assertTaskDurationShape(taskByKey(loop.snapshot(projectId), 'P003'), '停止后重试完成任务快照');

    fs.writeFileSync(planFile, fs.readFileSync(planFile, 'utf8').replaceAll('- [ ]', '- [x]'), 'utf8');
    loop.syncPlanTasks(planId, planFile);
    await loop.validatePlan(workspace, db.get('SELECT * FROM plans WHERE id = ?', [planId]));
    snapshot = loop.snapshot(projectId);
    assert.equal(snapshot.plans[0].status, 'completed', '验收通过后 plan 应标记 completed');
    assert.equal(snapshot.plans[0].validation_passed, 1, '验收通过后 validation_passed 应为 1');
    assertWorkspaceSearchRegression(snapshot);

    await assertCodexSessionReuseSmoke(db, loop, projectId, workspace);

    const multiWorkspaceA = path.join(tempRoot, 'multi-a');
    const multiWorkspaceB = path.join(tempRoot, 'multi-b');
    const multiProjectA = insertProject(db, loop, 'Multi Project A', multiWorkspaceA);
    const multiProjectB = insertProject(db, loop, 'Multi Project B', multiWorkspaceB);
    const sameWorkspaceProject = insertProject(db, loop, 'Same Workspace Project', multiWorkspaceB);
    loop.configure(multiProjectA, { workspacePath: multiWorkspaceA, intervalSeconds: 60, validationCommand: '' });
    loop.configure(multiProjectB, { workspacePath: multiWorkspaceB, intervalSeconds: 60, validationCommand: '' });
    loop.configure(sameWorkspaceProject, { workspacePath: multiWorkspaceB, intervalSeconds: 60, validationCommand: '' });

    loop.start(multiProjectA);
    loop.start(multiProjectB);
    assert.equal(loop.snapshot(multiProjectA).state.running, 1, '项目 A 循环应保持运行中');
    assert.equal(loop.snapshot(multiProjectB).state.running, 1, '项目 B 循环应保持运行中');
    assert.ok(
      loop.snapshot().projects.filter((project) => project.running).length >= 2,
      '项目列表应能显示多个运行中项目',
    );
    assert.throws(
      () => loop.start(sameWorkspaceProject),
      /工作区正在被项目/,
      '同一工作区不应被两个项目循环同时占用',
    );
    loop.stop(multiProjectA);
    assert.equal(loop.snapshot(multiProjectA).state.running, 0, '停止项目 A 后 A 应停止');
    assert.equal(loop.snapshot(multiProjectB).state.running, 1, '停止项目 A 不应影响项目 B');
    loop.stop(multiProjectB);
    assert.equal(loop.snapshot(multiProjectB).state.running, 0, '项目 B 应可独立停止');

    console.log('smoke ok: projects, scoped snapshots, attachments, draft plan, plan reader, search, task acceptance, task events, scan, validation, duration stats, codex session reuse, multi-loop');
  } finally {
    fs.rmSync(tempRoot, { recursive: true, force: true });
  }
}

function assertWorkspaceSearchRegression(snapshot) {
  const { searchWorkspaceSnapshot } = loadRendererTsModule(
    path.join(__dirname, '..', 'src', 'renderer', 'utils', 'search.ts'),
  );

  assertSearchHit(
    searchWorkspaceSnapshot(snapshot, '普通文本需求'),
    'requirement',
    'title',
    /普通文本需求/,
    '搜索应支持需求标题命中',
  );
  assertSearchHit(
    searchWorkspaceSnapshot(snapshot, '重点内容'),
    'feedback',
    'body',
    /重点内容/,
    '搜索应支持反馈正文命中',
  );
  assertSearchHit(
    searchWorkspaceSnapshot(snapshot, 'P002'),
    'task',
    'taskKey',
    /P002/,
    '搜索应支持任务 key 命中',
  );
  assertSearchHit(
    searchWorkspaceSnapshot(snapshot, 'fake-execute.log'),
    'event',
    'eventMeta',
    /fake-execute\.log/,
    '搜索应支持事件元信息命中',
  );

  const emptySearch = searchWorkspaceSnapshot(snapshot, '没有任何匹配的搜索词');
  assert.equal(emptySearch.total, 0, '搜索无结果时 total 应为 0');
  assert.ok(emptySearch.groups.every((group) => group.count === 0), '搜索无结果时各分组计数应为 0');
}

function assertSearchHit(searchState, source, field, valuePattern, label) {
  assert.ok(searchState.total > 0, `${label}：应返回搜索结果`);
  const result = searchState.results.find(
    (item) => item.source === source && item.matches.some((match) => match.field === field),
  );
  assert.ok(result, `${label}：应包含 ${source}/${field} 命中`);
  const match = result.matches.find((item) => item.field === field);
  assert.match(match.value, valuePattern, `${label}：命中值应包含关键字`);
  assert.match(match.snippet, valuePattern, `${label}：摘要片段应包含关键字`);
  assert.ok(
    searchState.groups.some((group) => group.source === source && group.count > 0),
    `${label}：来源分组应统计命中数量`,
  );
}

async function assertPlanReadRegression(
  db,
  loop,
  { projectId, otherProjectId, otherWorkspace, planId, plan, planFile, originalPlanText },
) {
  const readPlan = loadMainPlanReadHandler(db, loop);

  const existingRead = await readPlan(null, { projectId, planId });
  assert.equal(existingRead.ok, true, '读取存在的 Plan 文件应成功');
  assert.equal(existingRead.id, planId, '读取存在的 Plan 应返回 plan id');
  assert.equal(existingRead.project_id, projectId, '读取存在的 Plan 应返回项目 id');
  assert.equal(existingRead.file_path, plan.file_path, '读取存在的 Plan 应返回相对路径');
  assert.equal(existingRead.markdown, originalPlanText, '读取存在的 Plan 应返回完整 Markdown');
  assert.equal(existingRead.error, null, '读取存在的 Plan 不应返回错误');

  const unknownRead = await readPlan(null, { projectId, planId: planId + 99999 });
  assert.equal(unknownRead.ok, false, '读取不存在的 Plan 应失败');
  assert.equal(unknownRead.markdown, '', '读取不存在的 Plan 不应返回正文');
  assert.equal(unknownRead.error, '计划不存在', '读取不存在的 Plan 应提示计划不存在');

  const otherPlanRel = path.join('docs', 'plan', 'other-project-readable.md');
  const otherPlanFile = path.join(otherWorkspace, otherPlanRel);
  fs.mkdirSync(path.dirname(otherPlanFile), { recursive: true });
  fs.writeFileSync(otherPlanFile, '# Other Project Plan\n\n- [ ] O001: 仅属于其它项目\n', 'utf8');
  const otherPlanId = insertPlan(db, otherProjectId, otherPlanRel, 'smoke-plan-read-other');

  const crossProjectRead = await readPlan(null, { projectId, planId: otherPlanId });
  assert.equal(crossProjectRead.ok, false, '当前项目不应读取其它项目 Plan');
  assert.equal(crossProjectRead.markdown, '', '跨项目读取不应泄漏 Markdown 正文');
  assert.equal(crossProjectRead.error, '计划不存在', '跨项目读取应按当前项目隔离为计划不存在');

  const reverseCrossProjectRead = await readPlan(null, { projectId: otherProjectId, planId });
  assert.equal(reverseCrossProjectRead.ok, false, '其它项目不应读取当前项目 Plan');
  assert.equal(reverseCrossProjectRead.markdown, '', '反向跨项目读取不应泄漏 Markdown 正文');
  assert.equal(reverseCrossProjectRead.error, '计划不存在', '反向跨项目读取应按项目隔离为计划不存在');

  const missingPlanId = insertPlan(
    db,
    otherProjectId,
    path.join('docs', 'plan', 'missing-plan.md'),
    'smoke-plan-read-missing',
  );
  const missingRead = await readPlan(null, { projectId: otherProjectId, planId: missingPlanId });
  assert.equal(missingRead.ok, false, '读取缺失的 Plan 文件应失败');
  assert.equal(missingRead.markdown, '', '读取缺失的 Plan 文件不应返回正文');
  assert.equal(missingRead.error, '计划文件不存在', '读取缺失的 Plan 文件应提示文件不存在');

  const outsidePlanId = insertPlan(
    db,
    otherProjectId,
    path.join('..', 'outside-plan.md'),
    'smoke-plan-read-outside',
  );
  const outsideRead = await readPlan(null, { projectId: otherProjectId, planId: outsidePlanId });
  assert.equal(outsideRead.ok, false, '读取越界 Plan 路径应失败');
  assert.equal(outsideRead.markdown, '', '读取越界 Plan 路径不应返回正文');
  assert.equal(outsideRead.error, '计划文件路径超出项目工作区', '读取越界 Plan 路径应提示越界');

  assert.equal(fs.readFileSync(planFile, 'utf8'), originalPlanText, 'Plan 阅读 smoke 不应修改正式 Plan 文件');
}

function loadMainPlanReadHandler(db, loop) {
  const mainPath = path.join(__dirname, '..', 'src', 'main.js');
  const handlers = new Map();
  const module = { exports: {} };
  const source = `${fs.readFileSync(mainPath, 'utf8')}\nmodule.exports.__setSmokeState = (state) => { db = state.db; loop = state.loop; };\nmodule.exports.__smokeIpcHandlers = __smokeIpcHandlers;\n`;
  const fakeElectron = {
    app: {
      getPath: () => path.join(os.tmpdir(), 'autoplan-smoke-user-data'),
      on: () => {},
      quit: () => {},
      whenReady: () => ({ then: () => undefined }),
    },
    BrowserWindow: function SmokeBrowserWindow() {
      throw new Error('smoke 不应创建 Electron 窗口');
    },
    ipcMain: {
      handle: (channel, handler) => handlers.set(channel, handler),
    },
    Menu: {
      setApplicationMenu: () => {},
    },
  };
  const localRequire = (request) => {
    if (request === 'electron') return fakeElectron;
    if (request.startsWith('./')) return require(path.join(path.dirname(mainPath), request));
    return require(request);
  };

  vm.runInNewContext(
    source,
    {
      require: localRequire,
      module,
      exports: module.exports,
      __dirname: path.dirname(mainPath),
      __filename: mainPath,
      __smokeIpcHandlers: handlers,
      Buffer,
      clearTimeout,
      console,
      process,
      setTimeout,
    },
    { filename: mainPath },
  );
  module.exports.__setSmokeState({ db, loop });
  const handler = handlers.get('plans:read');
  assert.equal(typeof handler, 'function', '主进程应注册 plans:read IPC handler');
  return handler;
}

function loadRendererTsModule(modulePath, cache = new Map()) {
  const absolutePath = path.resolve(modulePath);
  const cachedModule = cache.get(absolutePath);
  if (cachedModule) return cachedModule.exports;

  const ts = require('typescript');
  const module = { exports: {} };
  cache.set(absolutePath, module);

  const source = fs.readFileSync(absolutePath, 'utf8');
  const transpiled = ts.transpileModule(source, {
    fileName: absolutePath,
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      target: ts.ScriptTarget.ES2022,
      jsx: ts.JsxEmit.ReactJSX,
      esModuleInterop: true,
    },
    reportDiagnostics: true,
  });
  const diagnostics = (transpiled.diagnostics || []).filter(
    (diagnostic) => diagnostic.category === ts.DiagnosticCategory.Error,
  );
  assert.deepEqual(
    diagnostics.map((diagnostic) => ts.flattenDiagnosticMessageText(diagnostic.messageText, '\n')),
    [],
    `${absolutePath} 应能被 TypeScript 转译`,
  );

  const rendererRoot = path.join(__dirname, '..', 'src', 'renderer');
  const localRequire = (request) => {
    if (request.startsWith('.') || path.isAbsolute(request)) {
      return loadRendererTsModule(resolveRendererModule(path.dirname(absolutePath), request, rendererRoot), cache);
    }
    return require(request);
  };

  const script = new vm.Script(transpiled.outputText, { filename: absolutePath });
  script.runInNewContext({
    require: localRequire,
    module,
    exports: module.exports,
    __dirname: path.dirname(absolutePath),
    __filename: absolutePath,
    console,
  });
  return module.exports;
}

function resolveRendererModule(fromDir, request, rendererRoot) {
  const basePath = path.resolve(fromDir, request);
  const candidates = [
    basePath,
    `${basePath}.ts`,
    `${basePath}.tsx`,
    `${basePath}.js`,
    `${basePath}.jsx`,
    path.join(basePath, 'index.ts'),
    path.join(basePath, 'index.tsx'),
    path.join(basePath, 'index.js'),
  ];
  const resolvedPath = candidates.find((candidate) => fs.existsSync(candidate) && fs.statSync(candidate).isFile());
  assert.ok(resolvedPath, `应能解析前端模块 ${request}`);

  const relativePath = path.relative(rendererRoot, resolvedPath);
  assert.ok(
    relativePath && !relativePath.startsWith('..') && !path.isAbsolute(relativePath),
    `前端 smoke 模块应限制在 renderer 目录内：${request}`,
  );
  return resolvedPath;
}

function insertProject(db, loop, name, workspacePath) {
  const now = nowIso();
  const id = db.insert(
    `INSERT INTO projects (name, workspace_path, description, created_at, updated_at)
     VALUES (?, ?, ?, ?, ?)`,
    [name, workspacePath, '', now, now],
  );
  loop.ensureProjectState(id);
  return id;
}

function insertPlan(db, projectId, filePath, issueHash) {
  const now = nowIso();
  return db.insert(
    `INSERT INTO plans (project_id, issue_hash, file_path, hash, status, total_tasks, completed_tasks, validation_passed, created_at, updated_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
    [projectId, issueHash, filePath, `${issueHash}-hash`, 'running', 0, 0, 0, now, now],
  );
}

function insertRequirement(db, projectId) {
  const now = nowIso();
  return db.insert(
    `INSERT INTO requirements (project_id, title, body, status, created_at, updated_at)
     VALUES (?, ?, ?, ?, ?, ?)`,
    [projectId, '普通文本需求', '目标：\n普通文本验收\n允许拖拽图片附件', 'open', now, now],
  );
}

function insertFeedback(db, projectId) {
  const now = nowIso();
  db.insert(
    `INSERT INTO feedback (project_id, requirement_id, title, body, status, created_at, updated_at)
     VALUES (?, ?, ?, ?, ?, ?, ?)`,
    [projectId, null, '普通文本反馈', '列表展示\n重点内容\n可附加图片', 'open', now, now],
  );
}

async function assertCodexSessionReuseSmoke(db, loop, projectId, workspace) {
  const contextPlanRel = path.join('docs', 'plan', 'context-reuse-smoke.md');
  const contextPlanFile = path.join(workspace, contextPlanRel);
  fs.mkdirSync(path.dirname(contextPlanFile), { recursive: true });
  fs.writeFileSync(
    contextPlanFile,
    [
      '# Context reuse smoke',
      '',
      '- [ ] C001: 失败后复用上下文 <!-- scope: smoke/c001.js -->',
      '- [ ] C002: 并发任务 A <!-- scope: smoke/c002.js -->',
      '- [ ] C003: 并发任务 B <!-- scope: smoke/c003.js -->',
      '- [ ] C004: 同步保留上下文 <!-- scope: smoke/c004.js -->',
      '- [ ] C005: 同步改名旧任务 <!-- scope: smoke/c005.js -->',
      '',
    ].join('\n'),
    'utf8',
  );

  const now = nowIso();
  const contextPlanId = db.insert(
    `INSERT INTO plans (project_id, issue_hash, file_path, hash, status, total_tasks, completed_tasks, validation_passed, created_at, updated_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
    [projectId, 'smoke-context-reuse', contextPlanRel, '', 'running', 0, 0, 0, now, now],
  );
  loop.syncPlanTasks(contextPlanId, contextPlanFile);
  const contextPlan = db.get('SELECT * FROM plans WHERE id = ?', [contextPlanId]);
  const contextTask = (taskKey) => db.get('SELECT * FROM plan_tasks WHERE plan_id = ? AND task_key = ?', [contextPlanId, taskKey]);

  const logDir = path.join(workspace, 'docs', 'progress', 'logs');
  fs.mkdirSync(logDir, { recursive: true });
  const fakeLogFile = path.join(logDir, 'context-reuse-smoke.log');
  fs.writeFileSync(fakeLogFile, 'context reuse smoke', 'utf8');

  const retrySessionId = '11111111-1111-4111-8111-111111111111';
  const parallelSessionA = '22222222-2222-4222-8222-222222222222';
  const parallelSessionB = '33333333-3333-4333-8333-333333333333';
  const syncKeptSessionId = '44444444-4444-4444-8444-444444444444';
  const syncRenamedOldSessionId = '55555555-5555-4555-8555-555555555555';
  const originalRunCodex = loop.runCodex.bind(loop);

  try {
    const retryTask = contextTask('C001');
    loop.runCodex = async (_workspace, prompt, label, operation = {}) => {
      assert.equal(_workspace, workspace, '会话复用 smoke 应使用当前工作区');
      assert.match(prompt, /只执行指定任务 C001/, '首次失败执行 prompt 应锁定 C001');
      assert.equal(label, 'execute-C001', '首次失败执行 label 应包含任务 key');
      assert.equal(operation.planId, contextPlanId, '首次失败执行 operation 应绑定 plan');
      assert.equal(operation.taskId, retryTask.id, '首次失败执行 operation 应绑定 task');
      assert.equal(operation.codexSessionId, undefined, '首次执行不应传入已有 session id');
      await sleep(5);
      return {
        exitCode: 1,
        logFile: fakeLogFile,
        lastFile: path.join(logDir, 'context-c001-failed.txt'),
        codexSessionId: retrySessionId,
        codexSessionMode: 'new',
      };
    };
    const failedResult = await loop.executeTask(workspace, contextPlan, retryTask);
    assert.equal(failedResult.exitCode, 1, '会话复用 smoke 首次执行应模拟失败');
    const failedTask = contextTask('C001');
    assert.equal(failedTask.status, 'pending', '失败任务应回到 pending 以便重试');
    assert.equal(failedTask.codex_session_id, retrySessionId, '失败后应保存首次捕获的 session id');

    loop.runCodex = async (_workspace, prompt, label, operation = {}) => {
      assert.equal(_workspace, workspace, '重试执行应使用当前工作区');
      assert.match(prompt, /只执行指定任务 C001/, '重试执行 prompt 应锁定 C001');
      assert.equal(label, 'execute-C001', '重试执行 label 应包含任务 key');
      assert.equal(operation.planId, contextPlanId, '重试执行 operation 应绑定 plan');
      assert.equal(operation.taskId, retryTask.id, '重试执行 operation 应绑定 task');
      assert.equal(operation.codexSessionId, retrySessionId, '失败后再次执行应传入同一个 session id');
      await sleep(5);
      return {
        exitCode: 0,
        logFile: fakeLogFile,
        lastFile: path.join(logDir, 'context-c001-retry.txt'),
        codexSessionId: retrySessionId,
        codexSessionMode: 'resume',
      };
    };
    const retryResult = await loop.executeTask(workspace, contextPlan, failedTask);
    assert.equal(retryResult.exitCode, 0, '会话复用 smoke 重试应成功');
    loop.completeTask(workspace, contextPlan, failedTask, retryResult);
    const retriedTask = contextTask('C001');
    assert.equal(retriedTask.status, 'completed', '重试成功后任务应完成');
    assert.equal(retriedTask.codex_session_id, retrySessionId, '重试成功后应保留同一个 session id');

    const parallelTasks = [contextTask('C002'), contextTask('C003')];
    const parallelSessions = new Map([
      [parallelTasks[0].id, parallelSessionA],
      [parallelTasks[1].id, parallelSessionB],
    ]);
    const seenParallelTaskIds = new Set();
    loop.runCodex = async (_workspace, prompt, label, operation = {}) => {
      const currentTask = parallelTasks.find((task) => task.id === operation.taskId);
      assert.ok(currentTask, '并发执行 operation 应绑定当前任务');
      assert.match(prompt, new RegExp(`只执行指定任务 ${currentTask.task_key}`), '并发执行 prompt 应锁定当前任务');
      assert.equal(label, `execute-${currentTask.task_key}`, '并发执行 label 应包含当前任务 key');
      assert.equal(operation.planId, contextPlanId, '并发执行 operation 应绑定 plan');
      assert.equal(operation.parallel, true, '并发执行 operation 应标记 parallel');
      assert.equal(operation.codexSessionId, undefined, '同一 plan 的不同任务首次执行不应复用其它 session id');
      seenParallelTaskIds.add(operation.taskId);
      await sleep(currentTask.task_key === 'C002' ? 10 : 1);
      return {
        exitCode: 0,
        logFile: fakeLogFile,
        lastFile: path.join(logDir, `context-${currentTask.task_key}.txt`),
        codexSessionId: parallelSessions.get(currentTask.id),
        codexSessionMode: 'new',
      };
    };
    const parallelResults = await loop.executeTaskBatch(workspace, contextPlan, parallelTasks);
    assert.deepEqual(
      parallelResults.map(({ result }) => result.exitCode),
      [0, 0],
      '并发会话 smoke 应全部执行成功',
    );
    assert.equal(seenParallelTaskIds.size, 2, '并发执行应覆盖两个独立任务');
    const parallelTaskA = contextTask('C002');
    const parallelTaskB = contextTask('C003');
    assert.equal(parallelTaskA.codex_session_id, parallelSessionA, '并发任务 A 应保存自己的 session id');
    assert.equal(parallelTaskB.codex_session_id, parallelSessionB, '并发任务 B 应保存自己的 session id');
    assert.notEqual(parallelTaskA.codex_session_id, parallelTaskB.codex_session_id, '并发任务 session id 应彼此独立');
    assert.notEqual(parallelTaskA.codex_session_id, retrySessionId, '并发任务 A 不应复用重试任务 session id');
    assert.notEqual(parallelTaskB.codex_session_id, retrySessionId, '并发任务 B 不应复用重试任务 session id');

    const syncKeptTask = contextTask('C004');
    const syncRenamedTask = contextTask('C005');
    loop.updateTaskCodexSession(syncKeptTask.id, syncKeptSessionId);
    loop.updateTaskCodexSession(syncRenamedTask.id, syncRenamedOldSessionId);
    fs.writeFileSync(
      contextPlanFile,
      [
        '# Context reuse smoke',
        '',
        '- [x] C001: 失败后复用上下文 <!-- scope: smoke/c001.js -->',
        '- [x] C002: 并发任务 A <!-- scope: smoke/c002.js -->',
        '- [x] C003: 并发任务 B <!-- scope: smoke/c003.js -->',
        '- [ ] C004: 同步保留上下文已更新 <!-- scope: smoke/c004-updated.js -->',
        '- [ ] C105: 同步改名新任务 <!-- scope: smoke/c105.js -->',
        '- [ ] C006: 同步新增任务 <!-- scope: smoke/c006.js -->',
        '',
      ].join('\n'),
      'utf8',
    );
    loop.syncPlanTasks(contextPlanId, contextPlanFile);
    const keptAfterSync = contextTask('C004');
    const renamedAfterSync = contextTask('C105');
    const addedAfterSync = contextTask('C006');
    assert.equal(keptAfterSync.id, syncKeptTask.id, 'sync 后同一 task_key 应保留原任务记录');
    assert.equal(keptAfterSync.codex_session_id, syncKeptSessionId, 'sync 后同一 task_key 应保留上下文');
    assert.match(keptAfterSync.raw_line, /c004-updated\.js/, 'sync 后同一 task_key 应更新 raw line');
    assert.equal(contextTask('C005'), null, '改名后的旧任务 key 应被移除');
    assert.ok(renamedAfterSync, '改名后的新任务 key 应入库');
    assert.equal(renamedAfterSync.codex_session_id, null, '改名任务不应继承旧 session id');
    assert.ok(addedAfterSync, '新增任务应入库');
    assert.equal(addedAfterSync.codex_session_id, null, '新增任务不应继承任何 session id');
  } finally {
    loop.runCodex = originalRunCodex;
  }
}

function sleep(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function taskByKey(snapshot, taskKey) {
  return snapshot.tasks.find((task) => task.task_key === taskKey);
}

function taskEventsByKey(snapshot, taskKey) {
  return snapshot.events.filter((event) => event.meta?.taskKey === taskKey);
}

function assertTaskEventOrder(events, expectedTypes, label) {
  assert.deepEqual(
    events.slice(0, expectedTypes.length).map((event) => event.type),
    expectedTypes,
    `${label} 应按最新事件在前返回`,
  );
  for (let index = 1; index < expectedTypes.length; index += 1) {
    assert.ok(events[index - 1].id > events[index].id, `${label} 应按事件 id 倒序排列`);
  }
}

function assertTaskEventMeta(event, task, status, label) {
  assert.ok(event, `${label} 应存在`);
  assert.ok(event.meta && typeof event.meta === 'object', `${label} 应包含结构化 meta`);
  assert.equal(event.meta.taskId, task.id, `${label} meta 应包含 taskId`);
  assert.equal(event.meta.taskKey, task.task_key, `${label} meta 应包含 taskKey`);
  assert.equal(event.meta.taskTitle, task.title, `${label} meta 应包含 taskTitle`);
  assert.equal(event.meta.planId, task.plan_id, `${label} meta 应包含 planId`);
  assert.equal(event.meta.status, status, `${label} meta 应包含任务状态`);
  if (event.meta.startedAt) assertIsoString(event.meta.startedAt, `${label} meta startedAt 应为 ISO 时间`);
  if (event.meta.finishedAt) assertIsoString(event.meta.finishedAt, `${label} meta finishedAt 应为 ISO 时间`);
}

function hasTaskDurationShape(task) {
  if (!task) return false;
  const hasStartedAt = task.started_at === null || typeof task.started_at === 'string';
  const hasFinishedAt = task.finished_at === null || typeof task.finished_at === 'string';
  const hasDurationMs = typeof task.duration_ms === 'number' && Number.isFinite(task.duration_ms);
  const hasRunDurationMs =
    !Object.prototype.hasOwnProperty.call(task, 'run_duration_ms') ||
    (typeof task.run_duration_ms === 'number' && Number.isFinite(task.run_duration_ms));
  return hasStartedAt && hasFinishedAt && hasDurationMs && hasRunDurationMs;
}

function assertTaskDurationShape(task, label) {
  assert.ok(task, `${label} 应存在`);
  assert.ok(hasTaskDurationShape(task), `${label} 应包含符合前端类型预期的耗时字段`);
}

function assertIsoString(value, message) {
  assert.equal(typeof value, 'string', message);
  assert.ok(Number.isFinite(Date.parse(value)), message);
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
