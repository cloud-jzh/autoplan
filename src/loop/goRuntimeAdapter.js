'use strict';

const { EventEmitter } = require('node:events');
const { GoDataClientError } = require('../data/goDataClient');

// GoRuntimeAdapter keeps the legacy main-process call surface away from the
// sql.js database. It only forwards command families already defined by the
// Go runtime bridge; unsupported legacy table operations fail closed.
class GoRuntimeAdapter extends EventEmitter {
  constructor(goDataClient) {
    super();
    if (!goDataClient) throw new GoDataClientError('service_unavailable');
    this.goDataClient = goDataClient;
    this.lastSnapshot = null;
  }

  snapshot(projectId = null) {
    if (projectId !== null && projectId !== undefined) return this.goDataClient.snapshot(projectId) || emptySnapshot(projectId);
    return this.lastSnapshot || emptySnapshot(null);
  }

  projects() { return Array.isArray(this.snapshot(null).projects) ? this.snapshot(null).projects : []; }

  project(projectId) {
    const id = Number(projectId);
    return this.projects().find((project) => Number(project?.id) === id) || null;
  }

  defaultProjectId() { return Number(this.snapshot(null).activeProjectId || 0) || null; }
  status(projectId) { return this.project(projectId)?.state || this.snapshot(projectId).state || null; }
  setMcpStatusProvider() {}
  setTerminalMetadataProvider() {}
  startScheduler() {}
  stopScheduler() {}
  addEvent() {}
  hasRuntimeConfigInput() { return false; }
  configure() { throw new GoDataClientError('service_unavailable'); }

  async start(projectId, options) { return this.invoke(() => this.goDataClient.startLoop(projectId, options)); }
  async stop(projectId, options) {
    if (!projectId) return this.snapshot(null);
    return this.invoke(() => this.goDataClient.stopLoop(projectId, options));
  }
  async runOnce(projectId, options) { return this.invoke(() => this.goDataClient.runLoopOnce(projectId, options)); }
  async stopPlan(projectId, planId, options) { return this.invoke(() => this.goDataClient.stopPlan(projectId, planId, options)); }
  async resumePlan(projectId, planId, options) { return this.invoke(() => this.goDataClient.resumePlan(projectId, planId, options)); }
  async reExecutePlan(projectId, planId, options) { return this.invoke(() => this.goDataClient.reexecutePlan(projectId, planId, options)); }
  async recreatePlanFromIntake(projectId, planId, options) { return this.invoke(() => this.goDataClient.recreatePlan(projectId, planId, options)); }
  async validatePlan(projectId, planId, options) { return this.invoke(() => this.goDataClient.validatePlan(projectId, planId, options)); }
  async runTask(projectId, taskId, options = {}) {
    const planId = Number(options.planId || options.plan_id || 0) || this.planIdForTask(projectId, taskId);
    return this.invoke(() => this.goDataClient.runTask(projectId, planId, taskId, options));
  }
  async runTaskBatches(projectId, planId, batches, options) { return this.invoke(() => this.goDataClient.runTaskBatches(projectId, planId, batches, options)); }
  async stopTask(projectId, taskId, options = {}) {
    const planId = Number(options.planId || options.plan_id || 0) || this.planIdForTask(projectId, taskId);
    return this.invoke(() => this.goDataClient.stopTask(projectId, planId, taskId, options));
  }
  async runScriptManually(projectId, scriptId, options) { return this.invoke(() => this.goDataClient.runScript(projectId, scriptId, options)); }
  async stopScript(projectId, scriptId, options) { return this.invoke(() => this.goDataClient.stopScript(projectId, scriptId, options)); }
  async runExecutor(projectId, executorId, options) { return this.invoke(() => this.goDataClient.runExecutor(projectId, executorId, options)); }
  async stopExecutor(projectId, executorId, options) { return this.invoke(() => this.goDataClient.stopExecutor(projectId, executorId, options)); }
  async runExecutorAction(projectId, executorId, action, options) { return this.invoke(() => this.goDataClient.runExecutorAction(projectId, executorId, action, options)); }

  async invoke(operation) {
    const result = await operation();
    if (result?.snapshot && typeof result.snapshot === 'object') {
      this.lastSnapshot = result.snapshot;
      this.emit('update', result.snapshot);
    }
    return result?.snapshot || this.snapshot(null);
  }

  planIdForTask(projectId, taskId) {
    const task = (this.snapshot(projectId).tasks || []).find((item) => Number(item?.id) === Number(taskId));
    const planId = Number(task?.plan_id || task?.planId || 0);
    if (!Number.isSafeInteger(planId) || planId <= 0) throw new GoDataClientError('invalid_runtime_command');
    return planId;
  }
}

function emptySnapshot(projectId) {
  return {
    activeProjectId: projectId || null,
    activeProject: null,
    projects: [], requirements: [], feedback: [], attachments: [], plans: [], tasks: [], events: [], scripts: [], executors: [], terminals: [],
    mcp: {}, state: null, scans: [], scanSummary: {}, activeOperation: null, activeOperations: [], lastOperation: null,
  };
}

module.exports = { GoRuntimeAdapter };
