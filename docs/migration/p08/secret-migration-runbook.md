# P08 Secret Migration Runbook

## Scope and isolation

Run P08 only against a caller-prepared SQLite copy below an explicit temporary
or synthetic-fixture root. Do not point any P08 command at an Electron
`userData` location, an application default database, a live database, a
symlink, or a database with WAL, SHM, journal, or owner-lock sidecars. Close
Node/sql.js before any Go command opens the authorized copy. Node/sql.js must
not reopen a copy after Go has written it.

`prepare-secret-copy.js` accepts an already isolated file ending in
`.sqlite.copy`; its output is a second authorized copy plus a small
authorization manifest. It does not discover source locations. Keep the root
private and pass every path explicitly to the Go command.

## Prepare and preflight

1. Create a caller-owned temporary or synthetic-fixture root and place the
   isolated source copy below it. Verify that no writer or sidecar exists.
2. Run the copy-preparation script with `--source`, `--allow-root`,
   `--output-root`, and `--sanitized-copy`. Retain its hash-only output.
3. Use the prepared database, authorization manifest, immutable-backup
   directory, secret-store root, and key root as explicit arguments to
   `autoplan-migrate-secrets preflight`.
4. Run `dry-run` before any clear. Its report may contain only source kind,
   table, column, count, action, and hashes. It must not contain values,
   partial values, owner identifiers, environment text, provider references,
   or absolute paths.

## Immutable backup retain point

`migrate` creates and verifies an immutable backup before opening a write
transaction. Retain the backup manifest, artifact hashes, preflight hash, and
sanitized reports together. The retain point is complete only when the
database artifact hash equals the preflight source hash and the manifest has
been re-verified. Do not delete or overwrite this set during the migration or
restore drill.

## Migration and rollback boundary

Run `migrate` only with the separate `--confirm-clear` approval. For each
record it writes the provider secret, checks availability without reading the
value, then atomically inserts `secret_refs` and clears the legacy field. If a
write transaction does not commit, the newly created provider reference is
compensated and the legacy plaintext copy remains unchanged.

Before the first formal Go migration write, rollback is simply discarding the
prepared copy and secret-store root. After that boundary, restoring the
immutable backup reintroduces legacy plaintext into a new drill target. Any
production irreversible-clear decision requires a separate approval after the
backup, migration verification, restore drill, and sensitive-output scan have
all been retained. This runbook does not authorize that decision.

## Restore drill

1. Select a fresh, non-existent target below the same explicit allowed root;
   never use the authorized source, backup artifact, or a pre-existing file.
2. Run `restore` with the immutable manifest and the new `--restore-target`.
   The command copies with exclusive creation and never overwrites the drill
   source.
3. Verify the drill report: manifest hash, SQLite integrity, schema version,
   table row counts, foreign-key relationships, snapshot compatibility, and
   availability of any active secret references present in the drill copy.
4. Preserve the drill report and scan it with `scan-sensitive-output.js` using
   an explicit root and explicit evidence file list. The scanner emits only
   counts and a classification.

## Evidence hygiene

Persist only authorization manifests, backup manifests, hashes, and sanitized
aggregate reports. Never place plaintext, final-four fragments, environment
assignments, credential references, provider data, database paths, or command
bodies in fixtures, logs, manifests, tickets, or evidence files. The
`fixtures/migration/p08/secrets` directory is metadata-only and cannot be used
as a real credential source.
