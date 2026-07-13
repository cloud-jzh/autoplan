'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');

const FIXTURE_MANIFEST = path.join(__dirname, '..', '..', 'fixtures', 'migration', 'p09', 'manifest.json');
const COPY_FILE = 'scale-copy.json';
const OUTPUT_MANIFEST_FILE = 'scale-manifest.json';
const GENERATED_MARKER = '.autoplan-p09-scale-copy';
const FORBIDDEN = /(?:api[_-]?key|secret|token|password|authorization|appdata[\\/]+roaming[\\/]+autoplan|library[\\/]+application support[\\/]+autoplan|\.config[\\/]+autoplan)/i;

function readFixtureManifest(file = FIXTURE_MANIFEST) {
  const manifest = JSON.parse(fs.readFileSync(file, 'utf8'));
  if (manifest?.schema_version !== 1 || manifest?.kind !== 'p09-sanitized-scale-copy' || !safeSeed(manifest.seed) || !validScale(manifest.scale)) {
    throw stableError('scale_manifest_invalid');
  }
  return manifest;
}

function generateScaleCopy(options = {}) {
  const recipe = options.recipe || readFixtureManifest(options.manifestPath || FIXTURE_MANIFEST);
  const seed = options.seed === undefined ? recipe.seed : String(options.seed);
  const scale = normalizeScale(options.scale || recipe.scale);
  const random = seededRandom(seed);
  const rows = { projects: [], plans: [], plan_tasks: [], events: [], chat_messages: [], chat_queue: [], scripts: [], executors: [], relation_anomalies: [] };
  let planID = 1;
  let taskID = 1;
  let eventID = 1;
  let messageID = 1;
  let scriptID = 1;
  let executorID = 1;
  const baseTimestamp = '2026-07-11T04:45:56.000Z';

  for (let projectID = 1; projectID <= scale.projects; projectID += 1) {
    rows.projects.push({ id: projectID, name: `sanitized-project-${projectID}-${Math.floor(random() * 100000)}`, workspace_path: `fixture/project-${projectID}`, created_at: baseTimestamp, updated_at: baseTimestamp, state: projectID % 2 ? 'idle' : 'stopped' });
    for (let planIndex = 0; planIndex < scale.plansPerProject; planIndex += 1) {
      const currentPlanID = planID++;
      const timestamp = timestampFor(planIndex, baseTimestamp);
      rows.plans.push({
        id: currentPlanID, project_id: projectID, title: `sanitized-plan-${projectID}-${planIndex}-${'x'.repeat(180)}`,
        status: planIndex % 7 === 0 ? 'interrupted' : planIndex % 5 === 0 ? 'completed' : 'pending',
        sort_order: planIndex, created_at: timestamp, updated_at: timestamp,
      });
      for (let taskIndex = 0; taskIndex < scale.tasksPerPlan; taskIndex += 1) {
        rows.plan_tasks.push({ id: taskID++, plan_id: currentPlanID, project_id: projectID, title: `sanitized-task-${projectID}-${planIndex}-${taskIndex}`, status: taskIndex % 13 === 0 ? 'interrupted' : taskIndex % 9 === 0 ? 'completed' : 'pending', sort_order: taskIndex, updated_at: timestamp });
      }
    }
    for (let eventIndex = 0; eventIndex < scale.eventsPerProject; eventIndex += 1) {
      const timestamp = timestampFor(eventIndex % 8, baseTimestamp);
      rows.events.push({ id: eventID++, project_id: projectID, type: eventIndex % 11 === 0 ? 'task.interrupted' : 'task.updated', created_at: timestamp, sequence: eventIndex, payload_hash: digest(`event:${projectID}:${eventIndex}`) });
    }
    for (let messageIndex = 0; messageIndex < scale.messagesPerProject; messageIndex += 1) {
      const conversationID = projectID * 100 + (messageIndex % 4) + 1;
      const state = messageIndex % 17 === 0 ? 'interrupted' : messageIndex % 7 === 0 ? 'queued' : 'completed';
      rows.chat_messages.push({ id: messageID++, project_id: projectID, conversation_id: conversationID, role: messageIndex % 2 ? 'assistant' : 'user', state, created_at: timestampFor(messageIndex % 6, baseTimestamp), body: `sanitized-message-${projectID}-${messageIndex}` });
      if (state === 'queued' || state === 'interrupted') rows.chat_queue.push({ project_id: projectID, conversation_id: conversationID, message_ordinal: messageIndex, state, queue_order: messageIndex });
    }
    for (let index = 0; index < scale.scriptsPerProject; index += 1) {
      rows.scripts.push({ id: scriptID++, project_id: projectID, name: `sanitized-script-${projectID}-${index}`, trigger_mode: index % 2 ? 'schedule' : 'manual', schedule_cron: index % 2 ? '*/5 * * * *' : '', enabled: index % 3 ? 1 : 0, sort_order: index });
    }
    for (let index = 0; index < scale.executorsPerProject; index += 1) {
      rows.executors.push({ id: executorID++, project_id: projectID, label: `sanitized-executor-${projectID}-${index}`, type: index % 2 ? 'shell' : 'plugin', enabled: index % 4 ? 1 : 0, sort_order: index, action: index % 2 ? 'run' : 'reload' });
    }
    rows.relation_anomalies.push({ kind: 'orphan_task_reference', project_id: projectID, reference_id: 900000 + projectID, expected: 'blocked_by_audit' });
  }
  const copy = {
    schema_version: recipe.schema_version_target, generator_version: recipe.generator_version, seed, created_at: baseTimestamp,
    scale, rows, ordering: { events: 'project_id,created_at,sequence,id', messages: 'project_id,conversation_id,created_at,id', plans: 'project_id,sort_order,id' },
  };
  assertSanitized(copy);
  return { copy, manifest: runtimeManifest(recipe, copy) };
}

function writeScaleCopy(options = {}) {
  const outputDirectory = validateOutputDirectory(options.outputDirectory);
  const generated = generateScaleCopy(options);
  fs.mkdirSync(outputDirectory, { mode: 0o700 });
  let committed = false;
  try {
    fs.writeFileSync(path.join(outputDirectory, GENERATED_MARKER), 'p09-scale-v1\n', { encoding: 'utf8', mode: 0o600, flag: 'wx' });
    writeExclusiveJSON(path.join(outputDirectory, COPY_FILE), generated.copy);
    writeExclusiveJSON(path.join(outputDirectory, OUTPUT_MANIFEST_FILE), generated.manifest);
    committed = true;
    return { outputDirectory, manifest: generated.manifest };
  } finally {
    if (!committed) fs.rmSync(outputDirectory, { recursive: true, force: true });
  }
}

function cleanupScaleCopy(outputDirectory) {
  const directory = validateExistingOutputDirectory(outputDirectory);
  const marker = path.join(directory, GENERATED_MARKER);
  const copy = path.join(directory, COPY_FILE);
  const manifest = path.join(directory, OUTPUT_MANIFEST_FILE);
  if (!fs.existsSync(marker) || !fs.existsSync(copy) || !fs.existsSync(manifest)) throw stableError('scale_cleanup_rejected');
  fs.rmSync(directory, { recursive: true, force: false });
}

function runtimeManifest(recipe, copy) {
  const serialized = JSON.stringify(copy);
  const rowCounts = Object.fromEntries(Object.entries(copy.rows).map(([name, rows]) => [name, rows.length]));
  const tableSHA256 = Object.fromEntries(Object.entries(copy.rows).map(([name, rows]) => [name, digest(JSON.stringify(rows))]));
  return {
    schema_version: 1, kind: 'p09-generated-scale-copy', generator_version: recipe.generator_version, seed: copy.seed,
    schema_version_target: copy.schema_version, scale: copy.scale, row_counts: rowCounts, table_sha256: tableSHA256,
    sha256: digest(serialized), cleanup: recipe.cleanup, redaction: recipe.redaction,
  };
}

function normalizeScale(value) {
  if (!validScale(value)) throw stableError('scale_invalid');
  return {
    projects: Number(value.projects), plansPerProject: Number(value.plans_per_project ?? value.plansPerProject), tasksPerPlan: Number(value.tasks_per_plan ?? value.tasksPerPlan),
    eventsPerProject: Number(value.events_per_project ?? value.eventsPerProject), messagesPerProject: Number(value.messages_per_project ?? value.messagesPerProject),
    scriptsPerProject: Number(value.scripts_per_project ?? value.scriptsPerProject), executorsPerProject: Number(value.executors_per_project ?? value.executorsPerProject),
  };
}

function validScale(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const fields = [['projects', 1, 12], ['plans_per_project', 1, 80], ['tasks_per_plan', 1, 120], ['events_per_project', 1, 5000], ['messages_per_project', 1, 3000], ['scripts_per_project', 1, 60], ['executors_per_project', 1, 60]];
  return fields.every(([key, minimum, maximum]) => {
    const alternate = key.replace(/_([a-z])/g, (_match, letter) => letter.toUpperCase());
    const number = Number(value[key] ?? value[alternate]);
    return Number.isInteger(number) && number >= minimum && number <= maximum;
  });
}

function validateOutputDirectory(value) {
  const directory = path.resolve(String(value || ''));
  if (!path.isAbsolute(directory) || !looksLikeFixturePath(directory) || containsForbiddenPath(directory)) throw stableError('scale_output_rejected');
  if (fs.existsSync(directory)) throw stableError('scale_output_exists');
  const parent = path.dirname(directory);
  const parentInfo = fs.lstatSync(parent, { throwIfNoEntry: false });
  if (!parentInfo?.isDirectory() || parentInfo.isSymbolicLink()) throw stableError('scale_output_rejected');
  return directory;
}

function validateExistingOutputDirectory(value) {
  const directory = path.resolve(String(value || ''));
  if (!path.isAbsolute(directory) || !looksLikeFixturePath(directory) || containsForbiddenPath(directory)) throw stableError('scale_cleanup_rejected');
  const info = fs.lstatSync(directory, { throwIfNoEntry: false });
  if (!info?.isDirectory() || info.isSymbolicLink()) throw stableError('scale_cleanup_rejected');
  return directory;
}

function writeExclusiveJSON(file, value) { fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`, { encoding: 'utf8', mode: 0o600, flag: 'wx' }); }
function timestampFor(index, base) {
  if (index % 3 === 0) return base;
  const day = String(11 - (index % 6)).padStart(2, '0');
  return `2026-07-${day}T04:${String(45 + (index % 10)).padStart(2, '0')}:56.000Z`;
}
function digest(value) { return crypto.createHash('sha256').update(value).digest('hex'); }
function safeSeed(value) { return typeof value === 'string' && /^[A-Za-z0-9._-]{1,128}$/.test(value); }
function seededRandom(seed) { let state = crypto.createHash('sha256').update(seed).digest().readUInt32LE(0); return () => { state = (state + 0x6D2B79F5) >>> 0; let value = state; value = Math.imul(value ^ (value >>> 15), value | 1); value ^= value + Math.imul(value ^ (value >>> 7), value | 61); return ((value ^ (value >>> 14)) >>> 0) / 4294967296; }; }
function assertSanitized(value) { const source = JSON.stringify(value); if (FORBIDDEN.test(source) || /(?:[A-Za-z]:\\|\/Users\/|\/home\/)/.test(source)) throw stableError('scale_sensitive_content'); }
function looksLikeFixturePath(value) { return path.resolve(value).split(path.sep).some((part) => /(?:fixture|sanitized|temp|tmp|drill)/i.test(part)); }
function containsForbiddenPath(value) { return /(?:appdata[\\/]+roaming[\\/]+autoplan|library[\\/]+application support[\\/]+autoplan|\.config[\\/]+autoplan)/i.test(String(value)); }
function stableError(code) { const error = new Error(code); error.code = code; return error; }

function parseArgs(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === '--output-dir' || argument === '--seed') { values[argument] = argv[index + 1] || ''; index += 1; }
    else if (argument === '--cleanup') values.cleanup = true;
    else throw stableError('scale_arguments_invalid');
  }
  if (!values['--output-dir']) throw stableError('scale_arguments_invalid');
  return { outputDirectory: values['--output-dir'], seed: values['--seed'], cleanup: values.cleanup === true };
}

if (require.main === module) {
  try {
    const options = parseArgs(process.argv.slice(2));
    if (options.cleanup) { cleanupScaleCopy(options.outputDirectory); process.stdout.write('{"status":"cleaned"}\n'); }
    else { const result = writeScaleCopy(options); process.stdout.write(`${JSON.stringify({ status: 'generated', sha256: result.manifest.sha256 })}\n`); }
  } catch (error) {
    process.stdout.write(`${JSON.stringify({ status: 'blocked', code: error?.code || 'scale_failed' })}\n`);
    process.exitCode = 2;
  }
}

module.exports = { cleanupScaleCopy, generateScaleCopy, readFixtureManifest, runtimeManifest, validateOutputDirectory, writeScaleCopy };
