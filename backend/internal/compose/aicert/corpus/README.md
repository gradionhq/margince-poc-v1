# corpus/

Scenario files for the AI certification harness, one subdirectory per
`ai.Task` (e.g. `summarize/basic_01.yaml`), loaded by `LoadCorpus`. Nested
non-`.yaml` assets (fixture pages, etc.) are ignored by the loader, so a
task's own subdirectory may carry supporting files alongside its scenarios.

Every scenario is hand-authored (`source: hand_authored`) and names who
reviewed it for sensitive content (`sanitized_by`) — `LoadCorpus` refuses
anything else.

`site_extract` scenarios mirror the deep read's profile lane
(`compose/siteprofile.go`); `site_fact_extract` scenarios mirror its
page-parallel fact lane (`compose/sitepagefacts.go`) — the two lanes the
v3 rewrite split the old single-call deep read into. Every scenario's
`system`/`input` text is copied verbatim from what that production code
path actually sends (prompt constructor output and
`sitesnippet.go`'s `renderNumbered()`), not restated from memory, so a
prompt change there is a corpus change here too.

Every scenario currently under this tree carries `sanitized_by:
hand_authored/claude-fable-5`: every input, evidence snippet, and fixture
(`site_extract/fixtures/*.html`) is synthetic, invented for this corpus —
no real company, deal, or person data. `TestLoadCorpusCoversEveryTask`
(`corpus_test.go`) loads this tree and fails if a contract task
(`ai.AllTasks()`) has no scenario, so the "every task is prompt-testable"
goal stays enforced rather than a one-time claim.
