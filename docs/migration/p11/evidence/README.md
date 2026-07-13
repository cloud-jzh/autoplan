# P11 Evidence

Each verification creates one immutable directory under `runs/`. It contains
sanitized stdout/stderr per command, `summary.json`, and an
`evidence-manifest.json` with hashes for every artifact. Evidence records real
commands, timestamps, exit codes, source hashes, fixture hashes, and declared
process cleanup results; it never records prompt bodies, environment values,
session credentials, real userData paths, or raw CLI output.

Blocked runs are evidence too. They show the prerequisite failure and must not
claim that a runner, fixture database, or external process was exercised.
