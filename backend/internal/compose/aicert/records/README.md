# records/

Committed certification outcomes, one file per task×provider×model×env
combination: `<task>/<provider>_<model>_<env>.json`, written by
`WriteRecord` and read back by `LoadRecords`. A re-run that reaches the same
verdict leaves the file byte-for-byte unchanged; only a real outcome change
touches it, so a diff here is always a certification result worth reviewing.
