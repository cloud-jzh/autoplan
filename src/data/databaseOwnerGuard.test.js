'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { describe, it } = require('node:test');
const assert = require('node:assert/strict');
const {
  DATABASE_OWNER_ERROR_CODES,
  DatabaseOwnerGuardError,
  createDatabaseOwnerGuard,
} = require('./databaseOwnerGuard');

describe('database owner guard', () => {
  it('rejects missing, conflicting, and non-loopback owner selection', () => {
    assert.throws(
      () => createDatabaseOwnerGuard({ env: {} }),
      (error) => error instanceof DatabaseOwnerGuardError && error.code === DATABASE_OWNER_ERROR_CODES.CONFIG_INVALID,
    );
    assert.throws(
      () => createDatabaseOwnerGuard({ env: { AUTOPLAN_DATABASE_OWNER: 'node', AUTOPLAN_GO_API_URL: 'http://127.0.0.1:4444' } }),
      (error) => error instanceof DatabaseOwnerGuardError && error.code === DATABASE_OWNER_ERROR_CODES.CONFIG_INVALID,
    );
    assert.throws(
      () => createDatabaseOwnerGuard({ env: { AUTOPLAN_DATABASE_OWNER: 'go', AUTOPLAN_GO_API_URL: 'https://example.test:4444' } }),
      (error) => error instanceof DatabaseOwnerGuardError && error.code === DATABASE_OWNER_ERROR_CODES.CONFIG_INVALID,
    );
  });

  it('rejects every Node/sql.js entry before a Go-owned database can be opened', () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-owner-'));
    try {
      const target = path.join(root, 'autoplan.sqlite');
      const guard = createDatabaseOwnerGuard({
        env: { AUTOPLAN_DATABASE_OWNER: 'go', AUTOPLAN_GO_API_URL: 'http://127.0.0.1:4444' },
      });
      assert.throws(
        () => guard.assertSqlJsAllowed(target),
        (error) => error instanceof DatabaseOwnerGuardError && error.code === DATABASE_OWNER_ERROR_CODES.NODE_SQL_FORBIDDEN,
      );
      assert.equal(fs.existsSync(target), false);
      assert.equal(fs.existsSync(`${target}.mirror`), false);
      assert.equal(fs.existsSync(`${target}.bak`), false);
    } finally {
      fs.rmSync(root, { recursive: true, force: true });
    }
  });

  it('rejects a Go schema marker before sql.js initialization', () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-schema-'));
    try {
      const target = path.join(root, 'autoplan.sqlite');
      const header = Buffer.alloc(100);
      header.write('SQLite format 3\u0000', 0, 'ascii');
      header.writeUInt32BE(1, 60);
      fs.writeFileSync(target, header);
      const guard = createDatabaseOwnerGuard({ env: { AUTOPLAN_DATABASE_OWNER: 'node' } });
      assert.throws(
        () => guard.assertSqlJsAllowed(target),
        (error) => error instanceof DatabaseOwnerGuardError && error.code === DATABASE_OWNER_ERROR_CODES.GO_SCHEMA,
      );
    } finally {
      fs.rmSync(root, { recursive: true, force: true });
    }
  });
});
