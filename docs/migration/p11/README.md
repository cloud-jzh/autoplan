# P11 Runtime Migration Verification

P11 verifies the Go-owned Runtime migration without reading Electron userData,
starting a real Agent CLI, or updating the frozen Node golden. The matrix is
defined by `fixtures/migration/p11/state-machine-cases.json`; it is compared
with the P001 sanitized Node golden before runner tests are admitted.

The five independent migration gates are `go_loop_actions`,
`go_plan_actions`, `go_task_actions`, `go_acceptance_retry_actions`, and
`go_agent_cli_runtime`. An accepted Operation retains its selected owner; a
transport failure never changes that owner or retries through Node/sql.js.
The renderer transport contract is evaluated through a synchronous fake test
harness after TypeScript transpilation; it reads source only and never starts
Electron.

`npm run migration:p11:verify -- --fixture-root <authorized-copy>` performs
the gated verification. The copy must be outside real userData and contain
the authorization marker and manifest described in the runbook. Output is
written once beneath `docs/migration/p11/evidence/runs/`.

The verifier stops on the first failed command. It does not update fixtures,
does not hide a failure with skip/only, and preserves unrelated worktree
changes, including `.claude/`. Its immutable evidence includes the final
sanitized `git status`, fixture hashes, Operation/event matrix hashes, and the
process-tree cleanup assertion result. Read-only worktree status is recorded
even for a blocked or failed run; it never restarts a runner step.
