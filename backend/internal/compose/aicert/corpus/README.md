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

## The fence marker in a scenario

A live prompt bounds its untrusted data with a marker minted per call
(`internal/shared/kernel/promptfence`) — the sender has never seen it, so
nothing they write can close the span. A scenario file cannot mint one and
still be a fixed document, so every scenario here uses the same EXAMPLE
marker, `untrusted-0198f3a1-7c42-7e0b-9d51-2a6f4b8c1e07`, in both its
`system` and its `input`. It stands in for the value a real call generates;
nothing may depend on it, and a scenario that would pass only because the
marker is predictable is testing the wrong thing.

The nonce is the ONLY thing a scenario is allowed to differ from production
in. `TestRateExtractPromptMatchesCorpus` and `TestFxExtractPromptMatchesCorpus`
enforce that for their two lanes: they lift the marker out of the scenario,
rebuild the shipped prompt around it, and compare byte for byte — the
boundary sentence's exact wording included, since that wording is part of
what a certification run certified.

Every scenario currently under this tree carries `sanitized_by:
hand_authored/claude-fable-5`: every input, evidence snippet, and fixture
(`site_extract/fixtures/*.html`) is synthetic, invented for this corpus —
no real company, deal, or person data. `TestLoadCorpusCoversEveryTask`
(`corpus_test.go`) loads this tree and fails if a contract task
(`ai.AllTasks()`) has no scenario, so the "every task is prompt-testable"
goal stays enforced rather than a one-time claim.
