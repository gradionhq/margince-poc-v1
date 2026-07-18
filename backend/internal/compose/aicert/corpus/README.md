# corpus/

Scenario files for the AI certification harness, one subdirectory per
`ai.Task` (e.g. `summarize/basic_01.yaml`), loaded by `LoadCorpus`. Nested
non-`.yaml` assets (fixture pages, etc.) are ignored by the loader, so a
task's own subdirectory may carry supporting files alongside its scenarios.

Every scenario is hand-authored (`source: hand_authored`) and names who
reviewed it for sensitive content (`sanitized_by`) — `LoadCorpus` refuses
anything else.
