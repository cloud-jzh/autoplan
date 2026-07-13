# P11 Verification Runbook

1. Create a sanitized, explicit database copy or temporary fixture directory.
   Do not point at Electron userData or a live daemon directory.
2. Add `.autoplan-p11-authorized-copy` and `p11-fixture-manifest.json` to that
   directory. The manifest must be JSON with:

   ```json
   {"schema_version":1,"kind":"p11-authorized-fixture","authorized_copy":true}
   ```

3. Confirm there are no owner-lock, WAL, SHM, or journal files in the fixture.
4. Run `npm run migration:p11:verify -- --fixture-root <absolute-authorized-copy>`.

The verifier first validates P00's known red signature, accepted P10 evidence,
the Go-only database-owner boundary, fixture authorization, and the refusal of
real userData. A failed prerequisite creates a blocked evidence run and does
not start process-runner tests or write to the fixture.

When admitted, each command uses a temporary HOME, config, cache, and work
root. Captured output is sanitized and hashed. Review `summary.json` and
`evidence-manifest.json`; a completed run requires every runtime test command
to exit zero. The static check may retain only P00's accepted, exact known-red
signature; any other nonzero result is rejected. Stable source hashes, no
sensitive-output finding, and cleanup with zero declared leaked children or
zombies are also required.

Do not edit a completed run, regenerate the Node golden during verification,
or delete unrelated changes to make `git status` appear clean.
