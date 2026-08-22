# The AI activity rail — what the AI is doing for you, while it does it

A rep who asks the product to write a summary should see that it is being
written. Before this existed they saw nothing for however long the model took,
and then the answer appeared — which reads as a product that did nothing and
then guessed.

This page explains the rail: the one projection behind it, who reports into it,
who an occurrence belongs to, and — separately, because it is a different
question with a different owner — which kinds a reader is actually shown.

**The two questions this page keeps apart.** The server's obligation is a
COMPLETE record: every AI task this build can run reports, because a task that
reports nothing is AI work the product performed and then denied. What a reader
is SHOWN is the client's decision, and it is narrower. Conflating them is how
you get either a silent product or 300 strings nobody reads.

## The shape at a glance

```
  a writer                     the bus                the projection            the read              the rail
  ────────                     ───────                ──────────────            ────────              ────────
  ai.Router          ──┐
  agent runner       ──┼──▶  ai_task.state_changed ──▶  aiactivity      ──▶  GET /me/ai-activity ──▶  AgentRail
  attachment extract ──┘         (outbox)                handler              (one statement,          (locale
                                                          │                    two arms)                decides
                                                          ▼                                             the words)
                                                     ai_task_run
```

Nothing outside `internal/modules/aiactivity` writes `ai_task_run`, and no
statement in it invents a fact the bus did not carry. That is why the module
imports no sibling: the facts it needs arrive in the envelope, and a projection
that reached back into a source's tables would be a second reader of a truth it
is supposed to hold. (Two exceptions, both retention: `PurgeSettledBefore` ages
rows out and `CloseAbandonedRouterRuns` settles the ones whose source will never
settle them. Ageing a read model is not a domain mutation, but it IS a write.)

## Who reports — the router, a carrier, or nobody

`ai.railOwners` (`internal/modules/ai/railowner.go`) answers this for every task
in `api/ai-tasks.yaml`, and it is TOTAL over that table: a task the generator
adds and nobody answers fails the build. Three answers:

| Owner | What it means | What it can say |
|---|---|---|
| `SourceRouter` (`ai_router`) | The default. The router announces on the task's behalf, so a task is wired before its author has thought about the rail. | It learns of a call only once the call is over — plus a `running` line announced just before the call. Never `queued`. |
| A **carrier** (`agent_runner`, `attachment_extraction`) | Work that owns a durable row reports for itself. | `queued`, `running`, and — because a carrier declares a lease — a dead attempt that can be derived as `stalled`. |
| `SourceNoOccurrence` (`none`) | The work is a STEP inside somebody else's occurrence, not an occurrence of its own. | Nothing of its own; it is reported under the unit of work it serves. |

`SourceNoOccurrence` is **not** an exemption from reporting, and every use owes a
reason in `railNoOccurrenceReasons` — checked, because "reported by nobody" is
one keystroke from the silence this registry exists to end. One task uses it
today: `embeddings`, because every embedding call happens in service of a search,
an enrich or a reindex, and that is the occurrence. A reason that is really an
editorial preference ("no rep wants to see it") belongs in the CLIENT, which
decides what to draw.

Where a carrier exists it is the better reporter and **the router stays silent**,
so the two never write one occurrence between them.

## Who an occurrence belongs to

`ResolveActor` (`aiactivity/actor.go`) derives the owner FROM THE ENVELOPE, never
from the payload. An emitter chooses its payload; it cannot choose the
authenticated actor the write shape stamped, so it cannot attribute its work to
somebody else by filling in a field.

| The event's actor | Scope | `actor_user_id` |
|---|---|---|
| human | `personal` | that human |
| non-human **with** `on_behalf_of` | `personal` | the human behind it |
| non-human **without** `on_behalf_of` | `workspace` | NULL |
| human that does not parse | *refused* | — |

The last row is the interesting one: a human actor that does not parse is
REFUSED rather than quietly made workspace-scoped, because quietly widening it is
how one person's work becomes a system sweep nobody can find and nobody notices
is missing.

Stated honestly: on a worker path `OnBehalfOf` is itself derived from the job's
own args, so this is uniform rather than tamper-proof. Uniform is the benefit
worth having — one rule, one place, one failure mode.

**The consequence that decides most of the display census below:** a
workspace-scoped occurrence has a NULL `actor_user_id`, and the read filters
`actor_user_id = $1`. So background work with nobody behind it reaches nobody's
rail — not by editorial choice, but structurally.

## The read

`GET /me/ai-activity` (`aiactivity/read.go`), cookie-authenticated, `human-only`,
read-only — no audit or event row.

**The person is taken from the bound principal and is NOT a parameter, and that
is the whole of the authorization.** A store method that accepted a user id would
let any in-process caller ask for somebody else's feed, and the only thing
standing between that and a leak would be every caller remembering to pass its
own. Here there is nothing to remember: another person's feed cannot be
expressed. No RBAC object gates it, because there is no wider set to withhold.

Four properties worth knowing:

- **One statement, not two.** The transaction is READ COMMITTED, so two
  statements would take two snapshots and an occurrence that settled between them
  would appear in both — the rail saying "reading your document" and "I've read
  your document" about one reading at once. One statement is one snapshot, so the
  window does not exist to be closed.
- **Two arms, each with its own ordering, bound and partial index.** `live` is
  `queued`/`running` ordered by `queued_at`; `settled` is bounded by
  `finished_at`, because "what the AI finished for me today" is a question about
  when it finished — keyed on its start, a run that began 23:50 and ended 00:10
  would fall out of `settled` AND have already left `live`, vanishing entirely.
- **`stalled` is derived at read time and never stored.** A live occurrence past
  the lease its own source declared is reported stalled, unconditionally, in SQL,
  against the DATABASE clock. Nothing writes it, so nothing can forget to — which
  is what stops a worker that died mid-run from being displayed as working
  forever.
- **`recent` is bounded** — since local midnight, at most 10. An unbounded
  per-person history is a per-person activity ledger, which this installation
  deliberately does not keep. `summary` and `degrade_reason` are capped on the
  way to the wire (2000 / 500), because a model's whole output — possibly
  inflated by a prompt injection — otherwise ships to every open tab on every
  poll.

`degrade_reason` is server-authored prose in the SOURCE's own words, never a
provider's or a parser's message: those carry vendor text and can echo credential
material, and this field reaches an ordinary rep. `MarkFailed` takes a typed
`runner.FailureReason` so a raw error **fails to compile**.

**`kinds` is how a client that draws part of the record asks for its part**, and
it is applied BEFORE the bounds. Every task reports, so a caller that renders six
kinds and is served the newest ten of twenty-three can be handed ten it draws
nothing for — the bound would fall on rows the reader never sees and the rail
would go blank while its work was reported correctly. An empty list is a 422, and
so is an unknown name; both would otherwise come back as an empty feed, which is
the TRUE answer for an AI at rest.

The SPA polls every **3s while something is live, 30s at rest**, and refetches
explicitly on tab return — focus refetching is disabled app-wide, and the cached
body is exactly the one that is wrong, because the run it shows as live is the
run that finished while the tab was away.

## What a reader is actually shown

`frontend/src/app/ai-activity-lines.ts` holds `ACTIVITY_LINE`: for each of the
contract's 23 kinds, either a `(state → message key)` table or a written reason
there is none. It is typed `Record`, not `Partial<Record>`, so **a new kind fails
the build** until somebody either writes its copy in every locale or says, in
code, why it is not shown. The reason lives in the source rather than in a review
comment for the same reason: the next author reads the file, not the PR.

Copy is by LITERAL key, never `t(\`agent.activity.${kind}.${state}\`)` — the
orphan guard in `i18n.test.ts` counts a key as rendered when it starts with a
template stem, so an interpolated key would vouch for the whole namespace forever
and a retired kind's copy would sit in three catalogs with nothing to flag it.

**Six kinds are narrated**, in en/de/vi, total over all six states:

| Kind | Reported by | The line a rep sees |
|---|---|---|
| `morning_brief` | carrier (`agent_runner`) | the scheduled brief |
| `overnight_at_risk_sweep` | carrier (`agent_runner`) | the scheduled sweep |
| `document_extract` | carrier (`attachment_extraction`) | reading a document you attached |
| `summarize` | router | "I'm writing your summary." |
| `draft_reply` | router | "I'm drafting your reply." |
| `offer_draft` | router | "I'm drafting your offer." |

For the three router-owned ones, `queued` copy exists and **is unreachable**: the
router announces a call it is ABOUT to serve, never one waiting, and no carrier
owns these tasks. The key exists because the state axis is total and the compiler
requires it — not because a producer is missing.

**Seventeen are not narrated**, and each reason is a different kind of fact:

| Reason | Kinds | Why |
|---|---|---|
| Watched by the asker | `growth_fit`, `cold_start` | The work lands on the surface that asked and changes it on arrival. `growth_fit` renders the band it returns on the panel that asked. `cold_start` runs behind TWO product surfaces and both need naming (it declares four invocation *sites* in `aitaskregistry.go`, which is a different count and not the one that matters here): onboarding, whose screen is deliberately RAILLESS (`onboarding` is a member of `RAIL_LESS_SCREENS` in `nav.ts`, which `shell.tsx` reads to drop the chrome), and the organization page's Enrich card — `cmd/api/modelwiring.go` wires `WithScrape` with the cold-start brain — where a rail does exist and the card itself renders the proposal. |
| System sweep | `brief_ranking`, `capture_classify`, `capture_counterparty_verdict`, `rate_extract`, `signal_extract`, `transcript_propose`, `voice_build` | Background workspace work that belongs to nobody in particular, so it has no personal line to draw. |
| Watched where it runs | `site_extract`, `site_fact_extract`, `site_triage` | Attribution here is a fact about the READ, not the task: a human-requested read carries that person as `on_behalf_of` and IS personal to them; a domain-triage or auto-enrich read names no human and is workspace-scoped. Neither wants a line. The human's read is already drawn live where it was started (the organization page polls the read's own status), and the system's read belongs to nobody. A grain problem seals it: the occurrence key is correlation+task and a read's correlation is its `site_read` row id, so one read files one occurrence per lane it runs — and only a domain-triage read reaches all three (`site_triage` fires solely for `isDomainTriageRequest`). |
| Reaches nobody, and would not be worth showing | `enrich` | Both halves matter. **Reachability:** its one production site is the signature-enrichment pass, which runs under a system principal with no `on_behalf_of` — so every occurrence is workspace-scoped with a NULL `actor_user_id`, and the personal feed selects on `actor_user_id`. **Worth:** it could not be per-person even if it were reachable. The pass mints ONE correlation id for the whole run (`capture_enrich`, up to 100 candidates in series) and the occurrence key is correlation+task, so every candidate collapses into one row — a per-person subject would make that row flap rather than narrate anybody. What a reader wants from it is what it FOUND, which is durable and already drawn as evidence-or-omit provenance on the person record. |
| An operator's measurement | `cert_judge` | The certification lane grading this build's own answers — not a rep's work. |
| Declared, not built | `deal_health`, `nl_search`, `transcript` | Named in `api/ai-tasks.yaml`; no site runs them, so nothing reports them yet. |

`enrich` is the one worth reading twice, because it looks visible and is not: the
ticker's own `enrich` key names DIFFERENT work — a provider run on a person
(`personprovider.tsx`), and the organization page's Enrich card
(`organizations.tsx`), which POSTs `/organizations/{id}/enrich` and therefore
runs `cold_start`, not this task. The deep read rides its own `site-read` ticker
key, not this one.

### Two surfaces, one action, no double narration

The taskbar ticker narrates **this tab's own react-query cache**; the rail
narrates **the server's feed**. Three ticker entries describe work the rail now
covers, and they do not collide: the bar renders the ticker line OR the rail line
as one `if/else` on a single span (`agentrail.tsx`), so a reader never sees one
action twice.

One collision was real and was fixed at the key rather than the table: removing
the ticker's `email` entry to end a two-vocabulary clash also silenced email
SENDS, because four mutations shared that key — two drafts and two sends. The key
is now split (`email-draft` for the rail, `email` for the ticker), so each action
is narrated exactly once by whichever surface actually knows about it.

## The gates that hold it

All of these live in the ROOT package, because the root is the only place that
can see the task contract, the wire contract, the read's own bounds and the
emitters at once.

| Obligation | Held by |
|---|---|
| Every task names the source that reports it, and every `SourceNoOccurrence` owes a defensible reason | `TestEveryAITaskNamesTheSourceThatReportsIt` (`backend/aitaskrailcensus_test.go`) |
| The registry names no task the contract dropped | `TestTheRailRegistryNamesNoTaskTheContractDropped` |
| A carrier's source literal is one something really emits | `TestEveryCarrierOverrideNamesASourceThatIsReallyEmitted` |
| Every kind something produces is one the contract enum can express | `TestEveryKindSomethingProducesIsOneTheContractCanExpress` (`backend/aiactivitycatalogparity_test.go`) — its failure prints an `align:` line naming the file, the schema and exactly what to add |
| Every contract kind has something that produces it | `TestEveryContractKindHasSomethingThatProducesIt` |
| The read's text caps are the ones the contract publishes | `TestTheReadsTextCapsAreTheOnesTheContractPublishes` |
| Every spec name can be a message-key segment | `TestEverySpecNameCanBeAMessageKeySegment` |
| Every contract kind is displayed or carries a written reason | the TypeScript `Record` type — a **compile error**, not a test |
| Every displayed kind has copy in en/de/vi, for all six states | the `LineSet` type + the i18n catalogs |

The census gate is deliberately derived rather than listed: the router announces
for every task the registry leaves to it, and that set grows the moment somebody
declares a task — so a hand-kept list would be one edit behind the contract
forever. That is the exact shape of the defect that left seventeen shipped tasks
reporting nothing at all.

## Known limits

- **Five tasks report settled-only** ([#2272]). `transcript_propose`,
  `site_extract`, `site_fact_extract`, `site_triage` and `voice_build` each
  already have a durable, attributable row that COULD say `queued`/`running`, but
  none is registered as a carrier. For long work, settled-only is worse than
  silence: a rep clicks "read this site", sees nothing for forty seconds, then it
  appears already finished.
- **One site read files one occurrence per lane it runs** ([#2272]). Its
  correlation id is the `site_read` row id, so the router's
  `correlation_id + task` key produces a separate line per task under one read.
  Only a domain-triage read reaches all three — `site_triage` fires solely for
  `isDomainTriageRequest`, so an ordinary human-requested read does not run it.
  Harmless while the rail narrates none of them; it becomes visible the moment
  anybody writes the copy.
- **A multi-call unit reopens its occurrence once per call** ([#2276]).
  `CompleteStructured` walks the ladder three times, and the lease is announced
  once and cannot be renewed.
- **The projection refuses a write into a live attempt.** `applyStateChangeSQL`
  guards with strict `>` on `(attempt, rank)`, so an equal-tuple event updates
  nothing. A lease REFRESH and per-step progress TICKS are both impossible
  without changing the projection.
- **The contract has no word for two real states.** Site read's `deferred`
  ("waiting on budget, retry at `next_attempt_at`" — not `queued`, since nothing
  will pick it up, and not settled) and `cancelled` (terminal, but `done` and
  `failed` would both lie). Either the enum grows or somebody rules on the
  mapping. Two things already fit exactly: `site_read.stopped_reason` is a closed
  vocabulary that drops straight into `degrade_reason`, and `partial` →
  `degraded`.
- **`subject_type` / `subject_id` are carried and stored but never read.** The
  event envelope has both, `ai_task_run` has both columns, and exactly one
  emitter populates them (`document_extract` → `attachment`). Nothing selects
  them, and the wire contract does not expose them. **This is not a to-do.** A
  subject-scoped read was designed and declined: it would replace an
  authorization that holds by construction — another person's feed cannot be
  expressed — with one that holds because a gate ran, and `auth.EnsureVisible`
  alone is not that gate (it checks no object grant, and for an identity table
  its clause is empty, so it returns success without a query). The one populated
  subject, `attachment`, is not row-scoped at all; its authority is inherited
  from a polymorphic parent. And a single `LIMIT` over a widened predicate lets
  one population evict the other, which is the opposite of what a subject arm is
  for. If it is ever built it copies `ai/feedback.go`, which already spells the
  gate correctly.
- **Per-person `enrich` narration was considered and declined**, not deferred.
  The reason is in the census entry beside the code: the pass mints one
  correlation id for all its candidates, so they share a single occurrence that
  no per-person subject could describe.

[#2272]: https://github.com/gradionhq/margince-poc-v1/issues/2272
[#2276]: https://github.com/gradionhq/margince-poc-v1/issues/2276

## Reference

| Concern | Where |
|---|---|
| The projection + the read | `internal/modules/aiactivity` (owns `ai_task_run`, imports no sibling) |
| Who reports each task | `internal/modules/ai/railowner.go` — `railOwners`, `RailOwner`, `RouterReports` |
| The router's announce/settle pair | `internal/modules/ai/railstart.go`, `railemit.go` |
| The carriers | `internal/modules/agents/runner/activity.go`, `internal/modules/activities/extractionactivity.go` |
| Attribution | `aiactivity/actor.go` — `ResolveActor` |
| The event | `ai_task.state_changed` (`shared/kernel/events/catalog.go`; payload generated into `internalevents_gen.go`) |
| The wire | `AiActivity` / `AiActivityKind` / `AiActivityItem` + `GET /me/ai-activity` (`backend/api/crm.yaml`) |
| What is drawn, and what is not | `frontend/src/app/ai-activity-lines.ts` |
| The rail component + poll | `frontend/src/app/agentrail.tsx`, `ai-activity.ts` |
| The census gate | `backend/aiactivitycatalogparity_test.go` |

**Related:** [ai-runtime.md](ai-runtime.md) (the task contract and the Router
gate) · [write-backbone.md](write-backbone.md) (the outbox the facts ride) ·
[job-fleet.md](job-fleet.md) (the scheduled work two carriers report) ·
[frontend-architecture.md](frontend-architecture.md) ·
[how-to/add-an-ai-task.md](../how-to/add-an-ai-task.md).
