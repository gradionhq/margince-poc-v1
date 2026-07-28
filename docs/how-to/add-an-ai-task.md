# Add an AI task or invocation site

A task checklist for putting a new AI call into the product. The AI surface is
contract-first like the HTTP API: you declare it, regenerate, then implement —
and the build refuses a site the contract never declared, a shipped task whose
site nobody wrote, and a site no certification case can measure.

For *why* it works this way see
[explanation/ai-runtime.md](../explanation/ai-runtime.md). Step 6 — writing the
certification case — has its own guide:
[write-a-certification-case.md](write-a-certification-case.md). To certify a
model **binding** that already exists (a swap, a cheaper candidate), you want
[certify-an-ai-model.md](certify-an-ai-model.md) instead. For the branch,
sign-off and PR mechanics every change goes through, see
[CONTRIBUTING.md](../../CONTRIBUTING.md).

> **Everything here is free except one step.** Steps 1–7 need no model, no key
> and no network. Only step 8 (`make e2e-ai`) calls a real provider, and it bills
> **your own API key** — Margince runs no inference of its own. Nothing forces
> you to run it: a site that has never been certified is reported honestly as
> `absent`, and that is a legitimate state to open a PR in.

## Quick start — a second prompt on a task that already exists

The common case, and the cheapest way to see the whole loop. `enrich` already has
a ladder, a budget posture and a **lane** (the field on `compose.ModelPath` that
hands a task to the process running it), so all you add is one more place that
calls it.

```bash
# 1. name the new site in the contract, under the task's sites:
#      sites: [signature, letterhead]        # backend/api/ai-tasks.yaml
make gen                                   # compiles it into tasks_gen.go

# 2. write the call site in internal/compose/, then register it +
#    bind its certification case (one line, in NewTaskCensus):
#      oneShot(ai.TaskEnrich, "letterhead", letterheadCases{})
#                                            internal/compose/aitaskregistry.go

# 3. write internal/compose/certcase_letterhead.go
#    and  internal/compose/aicert/corpus/enrich/letterhead_01.yaml

make check                                 # free: every gate names what is missing
make e2e-ai-report                         # free: your site now shows as `absent`
```

**Run `make check` early and often — it is the whole feedback loop.** The gates
are written to name the file you have not written yet, in the order you need
them, so a red test is the guide continuing rather than a failure. You do not
need a working AI setup for any of it.

When you are ready to spend, `make e2e-ai TASK=enrich` certifies for real
(step 8) and `make e2e-ai-report` then shows a band instead of `absent`.

## Task, or site?

**A task is not one prompt.** A task is the routing / budget / cost unit: it owns
a fallback ladder, an execution mode and a budget posture. A **site** is one
named place in the build that actually calls the model. Today 13 shipped tasks
carry 19 sites between them — `cold_start` has four, `voice_build` three,
`rate_extract` two.

| You are adding | Do |
|---|---|
| Another prompt for the same workload — a second pass, an evaluation call, a fan-out lane | a **site**: one name in the task's `sites[]`, then steps 3–9 |
| A workload that deserves its own ladder, budget posture or cost line | a **task**: all nine steps |

**Do not reuse an existing task's name for a different workload** just because it
is convenient. Routing, budget limits, tracing and the certification record are
all tracked per task name, so two workloads sharing one name have their spend
merged in reporting — and certifying that name then proves neither of them works.

Every site declares a **kind**, which is a claim about how the model is invoked —
and it caps how much of the site one certification run can cover:

| Kind | The site… | A run can certify at most |
|---|---|---|
| `one_shot` | builds one request and reads one reply | `full_invocation` |
| `multi_turn` | answers inside a conversation the caller supplies | `single_turn` |
| `agent_loop` | reasons over a cumulative, tool-fed window | `single_turn` |

## Steps

1. **Get the declaration into the contract.** `backend/api/ai-tasks.yaml` is a
   **mirror**: the normative AI task contract is maintained in a specification
   repository the maintainers own, and this file must match it verbatim. So the
   declaration is agreed there first, and lands here as a copy.

   What that means for you depends on where you are:

   - **Maintainer, or working with spec access** — land the declaration in the
     specification repo, then copy it down into `ai-tasks.yaml` unchanged.
   - **Outside contributor** — **open an issue** proposing the task or site, with
     the YAML block below filled in and a sentence on what calls it. A maintainer
     lands it upstream; your PR then carries the mirrored copy and everything from
     step 2 on. Do not skip ahead and hand-edit `ai-tasks.yaml` on its own: a
     mirror with no upstream declaration behind it cannot be merged, however green
     the build is.

   > **No gate here can catch the wrong order.** Every check in this build
   > compares the code against *this repo's copy* of the contract, so a task
   > declared here first passes all of them and is still out of order. The build
   > cannot warn you about a step it cannot see — which is exactly why it is
   > written down rather than left to a test.

   The declaration itself — the shape to put in that issue, and to mirror here:

   ```yaml
   tasks:
     meeting_notes:                       # illustrative — not a shipped task
       ladder: [cheap_cloud, premium]     # ordered capability tiers, not models
       execution_mode: interactive        # interactive | background
       on_budget_exhausted: degrade       # pairs with execution_mode, always:
       status: shipped                    #   interactive↔degrade, background↔queue
       # a bare name is a site of kind one_shot; write
       # {name: chat, kind: multi_turn} for any other kind
       sites: [summarise]
       company_context: none              # or {scopes: [...], token_budget: N}
       # no_payload: true                 # content that must never be captured
       # cost_unit: per_message           # only if the estimator prices it
       doc: "one line on what this task is for"
   ```

   - **`status`** — `shipped` obliges every name in `sites[]` to exist, be
     registered, own a certification case and own a corpus scenario. `planned`
     forbids all four. Declare `planned` while the site is unwritten and flip it
     in the commit that lands it: a task that ships uncertified and a task that
     certifies a prompt nobody calls are the two lies this field refuses.
   - **`company_context`** — `none`, or scopes + `token_budget` (+ `conditional`
     for "only when the caller asks"). Not optional: an absent policy is a
     **build** error, never a runtime default.
   - **`no_payload: true`** — content from this task must never reach
     `ai_call_payload`, whatever the deployment's capture posture says. A parsed
     field, because a data-protection control must not be load-bearing prose.
   - **`cost_unit`** — only for a task the pre-flight estimator prices, and only
     a name `internal/compose/costestimate` implements (today `per_message`,
     `per_person`). Naming a rule that does not exist — or implementing one
     nothing names — fails the build. Omit it for an unpriced task.

2. **Regenerate** — `make gen`. `tools/gen-aitasks` compiles the contract into
   `internal/modules/ai/tasks_gen.go` (your `ai.TaskX` constant, `ai.SitesFor`,
   `ai.Status`, `ai.CompanyContextFor`) and rewrites
   `config/ai-routing.schema.json`. Never hand-edit either; commit both **with**
   the contract in one commit, or the drift gate fails.

3. **Give the task a lane, and wire it into a process role** *(new task only)*.
   A **lane** is how a task reaches a running process, and it takes three edits,
   not one — the first two both in `internal/compose/brain.go`:

   1. **Declare it** — a field on `compose.ModelPath`, named for the task it
      serves.
   2. **Bind it** — in `modelPathForRouter`, or the field stays nil and the task
      never reaches the Router: `MeetingNotes: brain(ai.TaskMeetingNotes),`.
   3. **Hand it to a role** — an `Option` in the compose package (the
      `WithColdStart` / `WithOfferDraft` pattern), passed by the process that
      runs the workload: `cmd/api` for an `interactive` task, `cmd/worker` for a
      `background` one.

   Two gates read this. `TestEveryModelLaneIsWiredToTheTaskItIsNamedFor` names
   the missing bind for you; `TestEveryCensusedSiteRidesALaneAProcessRoleWires`
   fails when no `cmd/` role passes the lane anywhere — without it a site can be
   registered, scored and recorded while no binary ever reaches it.

4. **Write the call site** in `internal/compose/`. That is not a style
   preference: the import DAG lets only `compose` and `cmd` depend on the `ai`
   module, so a request builder inside `internal/modules/<name>/` fails
   `arch-lint` before any test runs. Every call goes through the Router via the
   lane, and `TestNoModelClientOutsideTheGate` fails a model client built
   anywhere else. Keep the builder and the validator **reachable** — the
   certification case must call the same two functions, and a copy is not one.

5. **Register the site and bind its case** — one line in the **census**
   (`compose.NewTaskCensus`, `internal/compose/aitaskregistry.go`): the list of
   every invocation site this build ships, checked against the contract on every
   boot and in every test run.

   ```go
   oneShot(ai.TaskMeetingNotes, "summarise", meetingNotesCases{})
   ```

   `oneShot` / `multiTurn` / `agentLoop` are the three helpers; use the one
   matching the kind the contract declares. The list is written out rather than
   derived from the contract on purpose — a loop would compare the contract to
   itself, and pass however little this build implements.

6. **Write the certification case and its scenario** —
   `internal/compose/certcase_<site>.go` plus at least one fixture under
   `internal/compose/aicert/corpus/<task>/`. This is the substantial half:
   [write-a-certification-case.md](write-a-certification-case.md).

7. **Verify** — `make check`. What each omission looks like:

   | Missing | Fails as |
   |---|---|
   | contract edited but not regenerated | `make drift` — a generated file differs |
   | a shipped task's site not registered | `task X is shipped but its site "y" is not registered` |
   | a site the contract never declared | `…the contract declares no such site (add it to sites[]…)` |
   | a site registered on a `planned` task | `task X is planned but site "y" is registered` |
   | a registered site with no case | `TestTaskCensusBindsACaseToEverySite` |
   | a case claiming more than its kind allows | `…claims more than its kind's "…" — a case may only narrow` |
   | a shipped site with no scenario | `shipped sites with no corpus scenario: […] — each is a prompt that ships uncertified` |
   | a `planned` task carrying a corpus scenario | `planned tasks carry corpus scenarios: […] — a task nobody built cannot be certified` |
   | a `planned` task carrying a certification record | `planned tasks carry certification records: […] — the record claims a band for a prompt that does not ship` |
   | a fixture that is not the shape its site takes | `TestEveryCorpusScenarioPreparesAgainstItsSite` |
   | a task with no lane, or a lane no role wires | `TestEveryCensusedSiteRidesALaneAProcessRoleWires` |
   | a new `.go` file with no SPDX header | `TestEveryHandWrittenGoFileCarriesTheLicenseHeader` |

8. **Certify it** — the one step that costs money, once the gates are green.
   It needs two things this guide has not asked for until now: a provider key in
   your environment (`GEMINI_API_KEY`, `ANTHROPIC_API_KEY`, …) and a
   `config/ai-routing.yaml` binding your task's tiers to a real model. Both are
   set up in
   [certify-an-ai-model.md § Prerequisites](certify-an-ai-model.md#prerequisites)
   — read that first, or the run fails on a missing key.

   ```
   make e2e-ai TASK=<your task>      # real calls, billed to YOUR api key
   make e2e-ai-report                # free: band, scope, binding, counts
   ```

   `TASK=` takes the name you declared in step 1. A name with no scenarios behind
   it — a typo, or a task nobody has written a corpus for — stops the run with
   `task "…" has no scenarios under …` **before** anything is sent, so a mistake
   here costs a message rather than a call.

   Commit the record under `internal/compose/aicert/records/<task>/`.

9. **Ship it** — contract, generated files, site, census line, case, scenario and
   record in the PR ([CONTRIBUTING.md](../../CONTRIBUTING.md) has the branch,
   sign-off and gate rules). Skipping step 8 is allowed: the report then reads
   **`absent`** for your site, which is honest, and the paid lane never gates a
   merge.

## Notes

- **A record is a claim about one (provider, model, env) binding**, not about the
  prompt in the abstract. Editing the prompt, the request builder or the grader
  re-stamps the version and marks the record **stale** — re-certify rather than
  hand-editing a record.
- **Renaming** a task or site starts upstream like any other contract change,
  then lands here as the mirrored declaration, the census line, the case's
  `Site()`, the corpus `site:` field, the record directory, and any exemption
  entry keyed by the old name.
- **Retiring a site** means deleting its scenarios and its record too. A record
  left behind asserts a band for a prompt that no longer ships.
