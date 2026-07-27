# records/

Committed certification outcomes, one file per task×provider×model×env
combination: `<task>/<provider>_<model>_<env>.json`, written by
`WriteRecord` and read back by `LoadRecords`. Marshaling is stable — fixed field
order, a trailing newline — so an identical Record leaves a diff-free file. The
verdict, the counts and the stamps are the durable signal; the latency, token and
cost means move with network noise on every re-run, so a diff confined to those
is not an outcome change.
