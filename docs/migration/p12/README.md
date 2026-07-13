# P12 Process Runtime Evidence

P12 records the safety evidence for migrating Script and Executor execution from legacy desktop paths to the shared Go application services. The checked-in fixtures are sanitized contract metadata only; they must never be replaced with Electron user data, a repository database, an external workspace, real commands, environment values, credentials, raw output, or live process identifiers.

The test set covers hostile request fields, controlled argument construction, process-tree termination using the Go test binary only, restart recovery without process adoption, bounded output redaction, REST/MCP request closure, and independent Script/Executor transport ownership gates.

Run the staged verification only after P00 and P11 have accepted immutable evidence runs:

```text
npm run migration:p12:verify
```

The verifier requires the explicitly authorized `fixtures/migration/p12/process-fixtures` root, uses a temporary isolated home/config/cache, stores only sanitized logs, and writes one immutable evidence directory below `docs/migration/p12/evidence/runs`.
