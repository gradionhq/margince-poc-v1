# ADR-0074 — An AI task declares its invocation sites, and certification reports readiness rather than blocking a merge

**Status:** Active — the declaration, the site registry and the readiness
report are built. The total cost-unit mapping is **not built**; see below.

**Decided:** 2026-07-27

## The decision

`backend/api/ai-tasks.yaml` declares each AI task, not just its model
routing. A declaration says whether the task ships, names its model-invocation
sites, says how each site calls the model, states whether the task's content
may ever be captured, and states its company-context policy. A task marked
`planned` has no site, no scenario and no certification record, so an
unimplemented task can never look certified.

A task is not one prompt. `cold_start` has four sites, `draft_reply` has
three, `rate_extract` has two. The build registers every shipped site and
checks the set against the contract, so a prompt that ships without being
named fails the build.

Answer schemas stay in Go code and are deliberately not declared in the
contract. Two sites build their JSON schema per call, and four tasks send
none at all, so a declared schema could not describe them honestly.

Certification never gates a merge. It calls real models over the network and
costs money, so requiring a green record to merge would block a security fix
to a prompt behind somebody's provider bill.

## Why

Facts about an AI task used to live in six unrelated places with three
different completeness guarantees. The payload-capture prohibition was held by
matching a phrase inside a documentation string. The cost-unit rule was a
partial Go map with nothing checking it. Nothing at all recorded whether a task
was implemented, so four unimplemented tasks carried certification records that
a coverage test had manufactured.

A sentence in a comment must not be the only thing enforcing a data-protection
control. Making the prohibition a parsed field is what turns it into something
the build can check.

## What it binds in this repository

- `backend/api/ai-tasks.yaml` declares 22 tasks: 19 `shipped` and 3 `planned`
  (`nl_search`, `transcript`, `deal_health`).
- The 19 shipped tasks declare 29 named sites between them, compiled into
  `backend/internal/modules/ai/tasks_gen.go` as `taskSites` and read through
  `ai.SitesFor`.
- `no_payload` is a parsed field. `ai.NoPayload` reads it, and
  `backend/internal/modules/ai/payloadcapture.go` refuses to capture a payload
  for a task pinned true regardless of the deployment's capture setting.
- `backend/tools/gen-aitasks` compiles the contract into `tasks_gen.go` behind
  the drift gate; a hand edit to the generated file fails `make check`.
- `backend/internal/compose/aicert/corpus_test.go` fails when a shipped site
  has no corpus scenario, when a planned task carries a scenario, and when a
  planned task carries a certification record.
- `backend/internal/compose/aicert/corpus/` holds 19 task directories;
  `backend/internal/compose/aicert/records/` holds 42 certification records
  across 14 task directories.
- `make e2e-ai` runs the paid, opt-in certification lane; `make e2e-ai-report`
  prints each shipped site's band and whether its record is current, stale or
  absent. Neither runs inside `make check`.

## What is only partly built

The contract names a `cost_unit` rule for only three tasks — `per_message`,
`per_person` and the embeddings section's `per_entity`. The decision asked for
the mapping to be total so the build could prove no priced task is missing a
rule. `ai.CostUnitFor` returns an empty string for every other task, which
reads as "unpriced" and cannot be told apart from "somebody forgot". Making
the mapping total is still owed, and the design for what a task with no
billable unit should declare is not final.

## History

Adopted from the retired specification, decided 2026-07-27. Rewritten in plain
language 2026-08-19. The source is marked proposed; the declaration fields, the
site registry and the readiness report have all shipped, so this record is
active.

The source lists four planned tasks — `nl_search`, `summarize`, `transcript`
and `deal_health`. `summarize` has since been built and now ships with a
corpus scenario; the other three are still planned.

The source flags `agent_loop` as the open risk, unsure whether a tool-fed
message window could be certified at all. It can, and does.
