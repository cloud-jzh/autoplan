'use strict';

const { describe, it } = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const initSqlJs = require('sql.js');
const {
  AppDatabase,
  DatabaseCompatibilityError,
  DATABASE_COMPATIBILITY_CODES,
} = require('./database');

describe('AppDatabase Go schema compatibility boundary', () => {
  it('rejects schema v1 before migration or persistence and preserves database bytes', async () => {
    const fixture = await createLegacyFixture();
    try {
      fixture.database.db.run(`
        CREATE TABLE schema_migrations (
          version INTEGER PRIMARY KEY,
          name TEXT NOT NULL,
          checksum TEXT NOT NULL,
          applied_at TEXT NOT NULL
        )
      `);
      fixture.database.db.run('PRAGMA user_version = 1');
      fs.writeFileSync(fixture.target, Buffer.from(fixture.database.db.export()));
      fixture.database.close();

      const before = sha256File(fixture.target);
      const incompatible = new AppDatabase(fixture.target);
      await assert.rejects(
        incompatible.init(),
        (error) => error instanceof DatabaseCompatibilityError &&
          error.code === DATABASE_COMPATIBILITY_CODES.GO_SCHEMA &&
          error.message === DATABASE_COMPATIBILITY_CODES.GO_SCHEMA &&
          !error.message.includes(fixture.root),
      );
      assert.equal(sha256File(fixture.target), before);
      assert.equal(fs.existsSync(`${fixture.target}.autoplan-owner.lock`), false);
    } finally {
      fixture.cleanup();
    }
  });

  it('rejects future and partial ownership markers with one stable safe error', async () => {
    for (const scenario of [
      { name: 'future', version: 2, ledger: false },
      { name: 'partial-ledger', version: 0, ledger: true },
      { name: 'version-without-ledger', version: 1, ledger: false },
    ]) {
      const root = fs.mkdtempSync(path.join(os.tmpdir(), `autoplan-compat-${scenario.name}-`));
      const target = path.join(root, 'database.sqlite.copy');
      try {
        await writeOwnedSchema(target, scenario.version, scenario.ledger);
        const before = sha256File(target);
        await assert.rejects(
          new AppDatabase(target).init(),
          (error) => error instanceof DatabaseCompatibilityError &&
            error.code === DATABASE_COMPATIBILITY_CODES.UNSUPPORTED_SCHEMA &&
            !error.message.includes(root),
        );
        assert.equal(sha256File(target), before, scenario.name);
      } finally {
        fs.rmSync(root, { recursive: true, force: true });
      }
    }
  });
});

describe('AppDatabase database owner protocol', () => {
  it('rejects a second writer and treats metadata without an OS owner as stale', async () => {
    const fixture = await createLegacyFixture();
    try {
      const metadataPath = `${fs.realpathSync.native(fixture.target)}.autoplan-owner.lock`;
      const metadata = fs.readFileSync(metadataPath, 'utf8');
      const record = JSON.parse(metadata);
      assert.deepEqual(Object.keys(record).sort(), [
        'database_id', 'owner_digest', 'pid_digest', 'ports', 'version',
      ]);
      assert.equal(metadata.includes(fixture.target), false);
      assert.equal(metadata.includes(fixture.root), false);

      const before = sha256File(fixture.target);
      await assert.rejects(
        new AppDatabase(fixture.target).init(),
        (error) => error instanceof DatabaseCompatibilityError &&
          error.code === DATABASE_COMPATIBILITY_CODES.OWNER_LOCKED &&
          !error.message.includes(fixture.root),
      );
      assert.equal(sha256File(fixture.target), before);

      fixture.database.close();
      fs.writeFileSync(metadataPath, metadata, { encoding: 'utf8', mode: 0o600 });
      const replacement = new AppDatabase(fixture.target);
      await replacement.init();
      assert.notEqual(JSON.parse(fs.readFileSync(metadataPath, 'utf8')).owner_digest, record.owner_digest);
      replacement.close();
      assert.equal(fs.existsSync(metadataPath), false);
    } finally {
      fixture.cleanup();
    }
  });
});

async function createLegacyFixture() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-database-compat-'));
  const target = path.join(root, 'database.sqlite.copy');
  const database = new AppDatabase(target);
  await database.init();
  return {
    root,
    target,
    database,
    cleanup() {
      try {
        database.close();
      } catch {
        // Best-effort cleanup for a synthetic fixture only.
      }
      fs.rmSync(root, { recursive: true, force: true });
    },
  };
}

async function writeOwnedSchema(target, version, ledger) {
  const SQL = await initSqlJs({
    locateFile: (file) => path.join(__dirname, '..', 'node_modules', 'sql.js', 'dist', file),
  });
  const database = new SQL.Database();
  database.run('CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)');
  if (ledger) {
    database.run(`
      CREATE TABLE schema_migrations (
        version INTEGER PRIMARY KEY,
        name TEXT NOT NULL,
        checksum TEXT NOT NULL,
        applied_at TEXT NOT NULL
      )
    `);
  }
  database.run(`PRAGMA user_version = ${version}`);
  fs.writeFileSync(target, Buffer.from(database.export()));
  database.close();
}

function sha256File(target) {
  return crypto.createHash('sha256').update(fs.readFileSync(target)).digest('hex');
}
