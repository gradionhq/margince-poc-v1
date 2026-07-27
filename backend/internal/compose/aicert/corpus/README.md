# corpus/

Scenario files for the AI certification harness, one subdirectory per
`ai.Task` (e.g. `site_extract/empty_page.yaml`), loaded by `LoadCorpus`.

A scenario holds the DATA production is given, never the prompt production
sends. `site:` names which registered invocation site is under certification,
`fixture:` carries what that site is handed, and the site's own case builds the
request and applies the shipped validator. A scenario that carried a prompt
would certify a copy of one, and a copy stays green through the change that
breaks the original — including the per-call fence marker, which the product
mints and no scenario ever spells.

Every scenario is hand-authored (`source: hand_authored`) and names who
reviewed it for sensitive content (`sanitized_by`) — `LoadCorpus` refuses
anything else. Every fixture under this tree is synthetic, invented for this
corpus: no real company, deal, or person data.

## What a scenario asserts

`expect.outcome` is one of the four things a certified reply can be
(`internal/compose/aitasks`): `accepted`, `wrong_answer`, `invalid`, or
`abstained`. A run passes when the site's own validator reports the outcome the
scenario named — nothing privileges `accepted`, which is what lets a scenario
whose right answer is silence exist at all.

`expect.answer` is written in the SITE's vocabulary, not a common one, because
what separates a right answer from a wrong one differs per site. Read the site's
own `Prepare` before authoring one. `site_extract/profile` accepts two
spellings: a bare field-to-value map when the whole claim is what a crawl
grounds, and a `grounded`/`not_grounded` mapping when the scenario also needs to
say what must NOT be grounded — the claim a page stating nothing is entirely
made of.

## The gates this tree passes

- `TestLoadCorpusCoversEveryShippedSite` (`corpus_test.go`) runs both ways off
  the task contract: a shipped SITE with no scenario is a prompt that ships
  uncertified, and a `planned` task carrying one scores a prompt that does not
  ship. The unit is the site, not the task — `cold_start` has four — so one
  scenario can never stand for a task's other prompts.
- `TestEveryCorpusScenarioPreparesAgainstItsSite` (`corpusprepare_test.go`)
  fails if a scenario's fixture is not the shape its site takes, or its
  expectation is one that site's validator could never satisfy.
- `TestEachAbstentionScenarioCatchesTheFabricationItTargets`
  (`corpusabstention_test.go`) runs each abstention scenario against both the
  answer it calls correct and the fabrication it exists to refuse, because a
  scenario that passes whatever the model does is worse than no scenario.
