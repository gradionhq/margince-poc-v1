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
[certify-an-ai-model.md](certify-an-ai-model.md) instead.

## Quick start — a second prompt on a task that already exists

The common case. `enrich` already has a ladder, a budget posture and a lane; you
are adding one more place that calls it.

```bash
# 1. name the site in the contract, under the task's sites:
#      sites: [signature, letterhead]        # backend/api/ai-tasks.yaml
make gen                                   # compiles it into tasks_gen.go

# 2. write the call site, then register it + bind its case (one line each):
#      oneShot(ai.TaskEnrich, "letterhead", letterheadCases{})
#                                            internal/compose/aitaskregistry.go

# 3. write internal/compose/certcase_letterhead.go
#    and  internal/compose/aicert/corpus/enrich/letterhead_01.yaml

make check                                 # every gate names the next thing to do
make e2e-ai TASK=enrich                    # paid: certify it for real
make e2e-ai-report                         # free: read the band
```

If you get lost, run `make check` and follow the red test — the gates are written
to name the file you have not written yet, in the order you need them.

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

Borrowing an existing task's label for an unrelated workload is the mistake worth
naming: routing, budget deferral, tracing and the certification record are all
*per task*, so a borrowed label merges two workloads' spend and certifies neither.

Every site declares a **kind**, which is a claim about how the model is invoked —
and it caps how much of the site one certification run can cover:

| Kind | The site… | A run can certify at most |
|---|---|---|
| `one_shot` | builds one request and reads one reply | `full_invocation` |
| `multi_turn` | answers inside a conversation the caller supplies | `single_turn` |
| `agent_loop` | reasons over a cumulative, tool-fed window | `single_turn` |

## Steps

1. **Declare it** in `backend/api/ai-tasks.yaml`. The contract is the authority,
   so this is where the work starts — never in code:

   ```yaml
   tasks:
     meeting_notes:                       # illustrative — not a shipped task
       ladder: [cheap_cloud, premium]     # ordered capability tiers, not models
       execution_mode: interactive        # interactive | background
       on_budget_exhausted: degrade       # closed pairing: interactive↔degrade,
       status: shipped                    #                 background↔queue
       sites: [summarise]                 # bare name = kind one_shot;
       company_context: none              #   {name: chat, kind: multi_turn} otherwise
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
   Add the field to `compose.ModelPath` (`internal/compose/brain.go`), named for
   the task it serves, and hand it to the role that runs the workload
   (`cmd/api`, `cmd/worker`). A censused site whose task owns no lane, or whose
   lane no `cmd/` role passes anywhere, fails
   `TestEveryCensusedSiteRidesALaneAProcessRoleWires` — a site can otherwise be
   registered, scored and recorded while no binary ever reaches it.

4. **Write the call site.** The request builder and the reply validator live with
   the code that owns the workload — a module, or `internal/compose` when the
   prompt needs more than one module's data. Every call goes through the Router
   via the lane; `TestNoModelClientOutsideTheGate` fails a model client built
   anywhere else. Keep the builder and the validator **reachable**: the
   certification case must call the same two functions, and a copy is not one.

5. **Register the site and bind its case** — one line in `compose.NewTaskCensus`
   (`internal/compose/aitaskregistry.go`):

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
   | a `planned` task carrying a scenario or a record | `…a task nobody built cannot be certified` |
   | a fixture that is not the shape its site takes | `TestEveryCorpusScenarioPreparesAgainstItsSite` |
   | a task with no lane, or a lane no role wires | `TestEveryCensusedSiteRidesALaneAProcessRoleWires` |
   | a new `.go` file with no SPDX header | `TestEveryHandWrittenGoFileCarriesTheLicenseHeader` |

8. **Certify it** — the paid lane, once the gates are green:

   ```
   make e2e-ai TASK=meeting_notes    # real provider calls, spends BYOK budget
   make e2e-ai-report                # free: band, scope, binding, counts
   ```

   Commit the record under `internal/compose/aicert/records/<task>/`.

9. **Ship it** — contract, generated files, site, census line, case, scenario and
   record in the PR. Until step 8 runs, the readiness report reads **`absent`**
   for your site, which is honest and a legitimate state to merge in: the lane is
   paid, so it reports readiness and never gates the merge.

## Notes

- **A record is a claim about one (provider, model, env) binding**, not about the
  prompt in the abstract. Editing the prompt, the request builder or the grader
  re-stamps the version and marks the record **stale** — re-certify rather than
  hand-editing a record.
- **Renaming** a task or site is a contract change first, then the census line,
  the case's `Site()`, the corpus `site:` field, the record directory, and any
  exemption entry keyed by the old name.
- **Retiring a site** means deleting its scenarios and its record too. A record
  left behind asserts a band for a prompt that no longer ships.
