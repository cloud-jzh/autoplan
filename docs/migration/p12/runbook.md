# P12 Runbook

1. Confirm P00 and P11 each have an accepted immutable evidence run. P12 refuses to continue without those prerequisites.
2. Keep `fixtures/migration/p12/process-fixtures` repository-owned and unchanged except for reviewed sanitized fixture updates. Its marker and manifest are required; fixture roots under user-data locations are rejected.
3. Run `npm run migration:p12:verify` from the repository root. The verifier has no Electron, database, workspace, shell, or user-data input path.
4. Review the generated `summary.json` and `evidence-manifest.json` below `docs/migration/p12/evidence/runs/<run-id>`. Each listed artifact hash must match and all command records must be accepted.
5. If the result is blocked, correct the reported prerequisite or safety condition and create a new run directory. Existing evidence directories are immutable and must not be edited.

The expected process-tree test starts only a child of the Go test binary with a minimal environment. Any leaked child or zombie, unsafe output, direct renderer process access, cross-runtime fallback after acceptance, or unredacted evidence is a verification failure.
