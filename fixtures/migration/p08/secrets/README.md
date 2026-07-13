# P08 synthetic secret fixture

This directory contains metadata only. It intentionally contains no SQLite
database, credential-shaped value, provider locator, user-data path, or source
environment assignment. A test harness must construct its database in a
dedicated temporary directory and use a non-production placeholder value only
inside that temporary database.

The fixture manifest is an inventory contract, not migration evidence and not
an authorization to access a local application database.
