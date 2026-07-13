'use strict';

const fs = require('node:fs');

function normalizeLegacyProject(value) {
  if (!value || typeof value !== 'object') return null;
  const id = Number(value.id);
  const name = String(value.name || '').trim();
  const workspacePath = String(value.workspace_path || '').trim();
  const description = String(value.description || '').trim();
  if (!Number.isSafeInteger(id) || id <= 0 || !name || name.length > 200 ||
      workspacePath.length > 4096 || description.length > 4000) return null;
  return Object.freeze({ id, name, workspace_path: workspacePath, description });
}

async function readLegacyProjects(databasePath, initSqlJs, sqlJsOptions) {
  if (!databasePath || !fs.existsSync(databasePath) || typeof initSqlJs !== 'function') return [];
  const SQL = await initSqlJs(sqlJsOptions);
  const database = new SQL.Database(fs.readFileSync(databasePath));
  try {
    const result = database.exec('SELECT id, name, workspace_path, description FROM projects ORDER BY id ASC');
    const rows = result?.[0]?.values || [];
    return rows.map(([id, name, workspace_path, description]) =>
      normalizeLegacyProject({ id, name, workspace_path, description })).filter(Boolean);
  } catch {
    return [];
  } finally {
    database.close();
  }
}

async function restoreLegacyProjects({ legacyProjects, existingProjects, createProject }) {
  if (!Array.isArray(legacyProjects) || !Array.isArray(existingProjects) || typeof createProject !== 'function') return 0;
  const existing = new Set(existingProjects.map((value) => {
    const project = normalizeLegacyProject(value);
    return project ? projectKey(project) : '';
  }).filter(Boolean));
  let restored = 0;
  for (const candidate of legacyProjects) {
    const project = normalizeLegacyProject(candidate);
    if (!project || existing.has(projectKey(project))) continue;
    await createProject(project);
    existing.add(projectKey(project));
    restored += 1;
  }
  return restored;
}

function projectKey(project) {
  return `${project.name}\u0000${project.workspace_path}`;
}

module.exports = { normalizeLegacyProject, readLegacyProjects, restoreLegacyProjects };
