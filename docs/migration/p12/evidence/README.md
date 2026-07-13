# P12 Evidence Directory

`runs/` is created by the P12 verifier. Every run directory contains a sanitized `summary.json`, sanitized command stdout/stderr logs, and an `evidence-manifest.json` with content hashes. Evidence must not contain command lines, raw environment values, tokens, credentials, user-data locations, external workspace locations, raw process output, or real PIDs.

Run directories are immutable. A failed or blocked run is retained as evidence; a corrective verification creates a new directory.
