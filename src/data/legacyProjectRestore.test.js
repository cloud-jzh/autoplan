'use strict';

const assert = require('node:assert/strict');
const { describe, it } = require('node:test');
const { normalizeLegacyProject, restoreLegacyProjects } = require('./legacyProjectRestore');

describe('legacy project restoration', () => {
  it('restores only projects absent from the Go-owned store', async () => {
    const created = [];
    const restored = await restoreLegacyProjects({
      legacyProjects: [
        { id: 2, name: 'Existing', workspace_path: 'D:/existing', description: '' },
        { id: 3, name: 'Restore me', workspace_path: 'D:/restore', description: 'legacy' },
      ],
      existingProjects: [{ id: 20, name: 'Existing', workspace_path: 'D:/existing', description: '' }],
      createProject: async (project) => created.push(project),
    });
    assert.equal(restored, 1);
    assert.deepEqual(created, [{ id: 3, name: 'Restore me', workspace_path: 'D:/restore', description: 'legacy' }]);
  });

  it('rejects malformed legacy rows before any write', () => {
    assert.equal(normalizeLegacyProject({ id: 0, name: 'bad', workspace_path: '', description: '' }), null);
    assert.equal(normalizeLegacyProject({ id: 1, name: '', workspace_path: '', description: '' }), null);
  });
});
