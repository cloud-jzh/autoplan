# P13B MCP verification and rollback runbook

Status: `default-off / evidence-required`.

P13B is independent from P13A. A Chat result is not MCP evidence, and a P13B failure blocks only new Go MCP transport admission. It never enables a legacy Node MCP fallback, takes over an in-flight operation, starts a second listener, or changes the database writer.

## Verification boundary

Run only the repository-authorized synthetic fixture:

```text
npm.cmd run migration:p13b:verify
```

The verifier creates isolated temporary HOME, application-data, and Go-cache directories. It removes sensitive environment variables and forces both P13 feature flags off while it validates prerequisites. It rejects real user-data locations, ownership sidecars, symlinks, active database sidecars, credentials, absolute external paths, and unsafe command output.

Before any MCP contract command, the verifier independently requires accepted P00 and P10-P12 evidence, the authorized P13B fixture, no filtered P13B tests, and a shared-adapter architecture. The architecture check requires one frozen catalog, one HTTP/stdio server boundary, and the P008 factory to be wired from the dependency container. A missing factory is a `blocked` result, not permission to enable the flag.

## Required consistency matrix

The P13B test and fixture suite records a normalized comparison for each frozen tool across UI adapter, REST, MCP HTTP, and MCP stdio. Business DTOs, stable error codes, operation and audit effects, and persisted post-state must match. Only documented request identity, transport metadata, and timestamp fields may differ.

The matrix includes successful calls, duplicate idempotency keys, idempotency conflicts, concurrent duplicates, unavailable dependencies, invalid state, unauthorized callers, cross-project IDs, stale resources, file-policy and realpath escapes, and rate limits. Rejected calls must have no mutation side effect. HTTP requires loopback authority, one approved Origin, bootstrap session, and bearer credential; stdio writes protocol frames only to stdout and bounded diagnostics only to stderr.

## Enablement and rollback

Do not set `go_mcp_api` merely because a command was started. Enablement requires a completed P13B summary with every command accepted, stable source hashes, no remaining risk, and a separately approved deployment action.

If listener startup, connection handling, or a transport contract fails, leave the flag off and stop only the new MCP admission path. Preserve the originating runtime for active operations, retain evidence, close only the listener owned by that server, and do not start Node MCP or a second Go listener. Rollback does not transfer database ownership or replay a mutation.

## Evidence

Each real verification attempt creates an immutable directory under `docs/migration/p13/evidence/runs/`. It contains sanitized stdout and stderr, exact command identities and exit codes, source hashes before and after execution, fixture hashes, artifact hashes, worktree state, platform facts, rollback state, and remaining risks. A blocked or unrun verifier is never evidence of success.
