# Add an AI task — and get it certified

A task checklist for adding a new AI workload, or a new **invocation site** on an
existing one. Like the HTTP API, the AI surface is contract-first: you declare the
task, regenerate, then implement — and the build refuses a site the contract never
declared, a shipped task whose site nobody wrote, and a site no certification case
can measure. For *why* it works this way see
[explanation/ai-runtime.md](../explanation/ai-runtime.md); to certify a
**binding** that already exists (a model swap), see
[certify-an-ai-model.md](certify-an-ai-model.md).

## First decide: a task, or a site?

**A task is not one prompt.** A task is the routing/budget/cost unit — the thing
that owns a fallback ladder, an execution mode and a budget posture. A **site** is
one named place in the build that actually calls the model. `cold_start` ships
four sites, `voice_build` three, `rate_extract` two; 13 shipped tasks carry 19
sites between them.

- **Same workload, another prompt** (a second pass, an evaluation call, a fan-out
  lane) → add a **site**: one name in the existing task's `sites[]`, then steps
  2–8. The task's ladder, budget posture and lane are already there.
- **A different workload** — one that deserves its own ladder, its own budget
  posture, or its own cost line → add a **task**, all nine steps.

Reusing an existing task label for an unrelated workload is the mistake to avoid:
routing, budget deferral, tracing and the certification record are all *per task*,
so a borrowed label silently merges two workloads' spend and certifies neither.

## Steps

1. **Declare the task** in `backend/api/ai-tasks.yaml` — the contract is the
   authority, so this is where the work starts, never in code:

   ```yaml
   tasks:
     meeting_notes:                       # illustrative — not a shipped task
       ladder: [cheap_cloud, premium]     # ordered capability tiers, not models
       execution_mode: interactive        # interactive | background
       on_budget_exhausted: degrade       # closed pairing: interactive↔degrade,
       status: shipped                    #                 background↔queue
       sites: [summarise]                 # bare name = kind one_shot
       company_context: none              # or {scopes: [...], token_budget: N}
       # cost_unit: per_message           # ONLY if the estimator prices it
       doc: "one line on what this task is for"
   ```

   The fields that decide what the build demands of you:

   - **`status`** — `shipped` obliges every site in `sites[]` to exist, be
     registered, own a certification case and own at least one corpus scenario.
     `planned` forbids all four. Declare `planned` while the site is unwritten and
     flip it in the commit that lands the site; a task that ships uncertified and a
     task that certifies a prompt nobody calls are the two lies this field exists
     to refuse.
   - **`sites`** — a bare name defaults to kind `one_shot`; a mapping declares
     another: `{name: chat, kind: multi_turn}` or `kind: agent_loop`. The kind is
     a claim about how the model is invoked, and it caps how much of the site a
     run can certify (step 6).
   - **`company_context`** — `none`, or scopes + `token_budget` (+ `conditional`
     for "only when the caller asks"). Not optional: an absent policy is a build
     error, never a runtime default.
   - **`no_payload: true`** — content from this task must never reach
     `ai_call_payload`, whatever the deployment's capture posture says. A parsed
     field, because a data-protection control must not be load-bearing prose.
   - **`cost_unit`** — only for a task the backfill estimator prices, and only a
     name `internal/compose/costestimate` implements (today `per_message`,
     `per_person`, and `per_entity` for the embed workload). Naming a rule that
     does not exist, or implementing one nothing names, both fail
     (`TestEveryContractCostUnitHasARule` and its reverse). Omit the field for an
     unpriced task.

2. **Regenerate** — `make gen`. `tools/gen-aitasks` compiles the contract into
   `internal/modules/ai/tasks_gen.go` (the `ai.TaskX` constant, `ai.SitesFor`,
   `ai.Status`, `ai.CompanyContextFor`, `ai.CostUnitFor`) and rewrites
   `config/ai-routing.schema.json`. Never hand-edit either — `make drift` fails a
   hand edit or a missed `make gen`, and both files are committed **with** the
   contract in one commit.

3. **Give the task a lane, and wire it into a process role.** Add the field to
   `compose.ModelPath` (`internal/compose/brain.go`), named for the task it
   serves, and hand it to the role that runs the workload (`cmd/api`,
   `cmd/worker`). Two gates read this: `TestEveryModelLaneIsNamedForAShippedTask`
   / `…IsWiredToTheTaskItIsNamedFor` (`internal/compose/modellanes_test.go`) hold
   the lane's name and binding to the contract, and
   `TestEveryCensusedSiteRidesALaneAProcessRoleWires`
   (`backend/aitaskwiring_test.go`) fails when a censused site's task owns no lane
   or no `cmd/` role passes it anywhere. A task with no lane needs an entry in
   that test's `laneWiringExemptions` **with its reason** (`cert_judge` is the one
   today: the certification runner builds it on its own pinned binding).

4. **Write the site.** The request builder and the reply validator live with the
   code that owns the workload — a module, or `internal/compose` when the prompt
   needs more than one module's data. Every call goes through the Router
   (`ai.Router`, reached via the lane); there is no second path, and
   `TestNoModelClientOutsideTheGate` fails a model client built anywhere else.
   Keep the builder and the validator **reachable** — the certification case must
   call the same two functions production does, so an unexported helper in the
   same package is fine, a copy is not.

5. **Register the site and bind its case** — one line in `compose.NewTaskCensus`
   (`internal/compose/aitaskregistry.go`):

   ```go
   oneShot(ai.TaskMeetingNotes, "summarise", meetingNotesCases{})
   ```

   (`oneShot` / `multiTurn` / `agentLoop` are the three helpers there; use the one
   matching the kind the contract declares.)

   The list is written out rather than derived from the contract on purpose: a
   loop would compare the contract to itself. `Registry.Validate` reports every
   mismatch at once — a site the contract never declared, a shipped task's missing
   site, a site registered against a `planned` task, a kind that disagrees with the
   contract, a case bound twice, and a case whose own `Site()` disagrees with the
   line it sits on.

6. **Write the certification case** — `internal/compose/certcase_<site>.go`,
   implementing `aitasks.CaseFactory`:

   | Method | What it owes |
   |---|---|
   | `Site()` | the same task/variant/kind the census line claims |
   | `Prepare(fixture, expected)` | parse the fixture into the shape **production** is handed, refuse an expectation this site's validator could never satisfy, and return a `PreparedCase` closed over both |
   | `Run(ctx, completer)` | issue the production request — the real builder — and return every request in the `Trace` |
   | `Evaluate(trace)` | apply the production validator, then compare against the expectation, and report one of `accepted` / `wrong_answer` / `invalid` / `abstained` |
   | `CertifiedScope()` *(optional)* | narrow the claim when a run covers less than the site does |

   Three rules carry most of the value:

   - **Call production, don't re-create it.** A case that rebuilds the request or
     re-implements the validator measures a copy, and a copy stays green through
     the change that breaks the original.
   - **Refuse an unreachable expectation at `Prepare`.** A label outside the
     closed set, a count that cannot match, a fixture longer than the read
     truncates — naming it here costs a parse; finding it after a paid run costs
     money and a wrong band.
   - **Pick the outcome word deliberately.** `invalid` is the validator refusing a
     reply; `abstained` is a well-formed reply that grounds nothing *and* whose
     site treats that as completed work. Collapsing them makes a model fabricating
     past a gate indistinguishable from one declining to fabricate. Where an empty
     result *is* the failure (cold-start field extraction shows the human an
     unreadable-source message), report `invalid`.

   **Scope** defaults from the site's kind — `one_shot` → `full_invocation`,
   otherwise `single_turn` — and a case may only ever *narrow* it
   (`TestOnlyTheCasesThatMeasureLessNarrowWhatTheyCertify`). Declare
   `ScopeSingleCall` when the site re-asks a below-floor item, retries an
   unreadable answer, or fans out over pages and folds the replies: the answer
   production serves is then assembled from calls the run never made. A narrowing
   also needs its entry in that test's `narrowedSites` map, with the reason.

7. **Add at least one corpus scenario** —
   `internal/compose/aicert/corpus/<task>/<name>.yaml`:

   ```yaml
   name: meeting_request_from_reply
   task: capture_classify
   site: classify                     # which registered site is under certification
   source: hand_authored              # LoadCorpus refuses anything else
   sanitized_by: hand_authored/<who>  # who reviewed it for sensitive content
   fixture:                           # the DATA production is given — never a prompt
     - subject: 'Re: pricing walkthrough'
       body: |
         Could we grab 30 minutes Thursday afternoon?
   expect:
     outcome: accepted                # accepted | wrong_answer | invalid | abstained
     answer: [meeting]                # in the SITE's vocabulary — read its Prepare
     rubric: >
       What the grader is told to score, and why it matters to the product.
     bands: {certified_min: 70, degraded_min: 50, floor: 40}
   ```

   - **A scenario holds the input, not the prompt.** The site's own case builds
     the request; a scenario carrying a prompt certifies a copy — and could not
     spell the per-call fence marker the product mints anyway.
   - **`expect.answer` has no common shape.** A bare token, a list, a map, a
     `{min,max}` band — each site owns its vocabulary. Read that site's `Prepare`
     before authoring one.
   - **A rubric may only ask for what the site's reply envelope can carry.** Four
     rubrics were once found grading fields their schema had no room for; a rubric
     that asks for the impossible can only mark a correct reply down.
   - **Fixtures are synthetic.** No real company, deal or person data under this
     tree.

8. **Verify** — `make check`. The chain, and what each omission looks like:

   | Missing | Fails as |
   |---|---|
   | the contract edited but not regenerated | `make drift` — a generated file differs |
   | a shipped task's site not registered | `task X is shipped but its site "y" is not registered` |
   | a site registered the contract never declared | `…the contract declares no such site (add it to sites[]…)` |
   | a site registered on a `planned` task | `task X is planned but site "y" is registered` |
   | a registered site with no certification case | `TestTaskCensusBindsACaseToEverySite` |
   | a case claiming more than its kind allows | `…claims more than its kind's "…" — a case may only narrow` |
   | a shipped site with no scenario | `shipped sites with no corpus scenario: […] — each is a prompt that ships uncertified` |
   | a `planned` task carrying a scenario or a record | `…a task nobody built cannot be certified` |
   | a fixture that is not the shape its site takes | `TestEveryCorpusScenarioPreparesAgainstItsSite` |
   | an abstention scenario that passes whatever the model does | `TestEachAbstentionScenarioCatchesTheFabricationItTargets` |
   | a task with no lane, or a lane no role wires | `TestEveryCensusedSiteRidesALaneAProcessRoleWires` |
   | a new `.go` file with no SPDX header | `TestEveryHandWrittenGoFileCarriesTheLicenseHeader` |

   Every one of these names the next file to touch, so follow the red test rather
   than a checklist.

9. **Certify it** — the paid lane, once the gates are green:

   ```
   make e2e-ai TASK=meeting_notes    # real provider calls, BYOK budget
   make e2e-ai-report                # free: band, scope, binding, counts
   ```

   Commit the record under `internal/compose/aicert/records/<task>/`. Until you
   run it the report reads **`absent`** for your site — honest, and the state to
   land in if the binding you would certify is not the one that ships. Full
   walkthrough, including benchmarking a candidate model and reading the payload
   trace: [certify-an-ai-model.md](certify-an-ai-model.md).

## Notes

- **A record is a claim about one (provider, model, env) binding**, not about the
  prompt in the abstract. Changing the prompt, the request builder, or the
  grader's own prompt re-stamps the version and marks the record **stale** — the
  report renders stale and absent distinctly, because staleness is a lie and
  absence is honest. Re-certify rather than hand-editing a record.
- **The certification lane never gates a merge.** It is paid and BYOK-gated, so
  gating on it would mean a green paid run before any prompt edit — including a
  security fix — could land. It reports readiness; the deterministic gates in
  step 8 are what block.
- **Renaming a task or a site** is a contract change (step 1) followed by the
  census line, the case's `Site()`, the corpus `site:` field, the record
  directory, and any `laneWiringExemptions` / `narrowedSites` entry keyed by the
  old name. The exemption maps are keyed by the contract's own string so a rename
  fails toward "this site owns no lane" rather than passing in silence.
- **Retiring a site** means deleting its scenarios and its record too. A record
  left behind asserts a band for a prompt that no longer ships.
