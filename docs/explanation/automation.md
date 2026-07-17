# Automation — the closed catalog & its trigger runtime

`internal/modules/automation` (`ADR-0035`) lets a workspace turn on and parameterize a **fixed** set
of "when X happens, do Y" templates. There is no rule builder, no expression language, and no
user-defined trigger or action — the vocabulary is seven triggers × seven actions, closed, and adding
to it is a code-and-test change, never data. This document is the deep reference: the catalog, the one
engine both trigger shapes run through, and the invariants that keep a firing honest.

For the one-paragraph version see [reference/modules.md](../reference/modules.md); to *add* an
automation, jump to [how-to/create-a-workflow.md](../how-to/create-a-workflow.md); for the write shape
every firing commits through, see [write-backbone.md](write-backbone.md). Read those first if you want
the short version.

## The shape at a glance

An automation is a **handler** (the code) that a workspace enables as an **instance** (a row, with
params). Two entirely different triggers reach the same firing pipeline:

```text
EVENT TRIGGER                                   CLOCK TRIGGER
domain write → outbox → relay → Redis           River periodic job (TimeScanner.Scan)
  → cg:workflows → WorkflowEngine.HandleEvent      → enumerate workspaces
        │ once per enabled instance                → read stale candidates (ActivityScan seam)
        │                                          → synthesize one workflow.Event per candidate
        └───────────────┬──────────────────────────────────┘
                        ▼
              WorkflowEngine.runOne          ← the ONE firing path (engine_run.go)
                        │
   Match ─▶ Plan ─▶ owner gate ─▶ claim (idempotency) ─▶ Apply ─▶ record outcome
                        │
              one workflow_run row per firing: applied | skipped | blocked | requires_approval | failed
```

**Why one path.** Nothing downstream of `runOne` can tell a synthesized clock pass from a real bus
delivery — so the idempotency guard and the permission gate are wired **once**, at the point both
paths meet, and neither entry can be governed differently by accident.

---

## 1. The catalog — seven triggers, seven actions

A workspace author picks one **trigger** and one or more **actions**; the pair is a catalog entry they
enable and parameterize. Both sets are closed and pinned to the spec in both directions by
`catalog_closure_test.go` — the code can neither grow past these nor silently drop one.

**Triggers** (`catalog_triggers.go`) — an *event* trigger reaches the matcher off the bus; a *clock*
trigger has no event and is swept by the time-scan:

| Trigger kind | Fires when… | Entry |
|---|---|---|
| `record_created_updated` | any record is created or updated | event (many streams; `Match` decides) |
| `field_reaches_value` | a field crosses a configured value | event (same streams, field predicate) |
| `deal_enters_leaves_stage` | a deal moves pipeline stage | event (`deal.stage_changed`) |
| `inbound_reply` | an inbound reply is captured | event (`engagement.reply`) |
| `no_activity_for_n_days` | a record has been quiet for N days | **clock** (time-scan) |
| `date_field_approaching` | a date field is within N days | **clock** (time-scan) |
| `task_overdue` | a task passes its due date | **clock** (time-scan) |

**Actions** (`catalog_actions.go`) — each carries a fixed autonomy **tier**: 🟢 auto-executes, 🟡
stages for a human approval, `dynamic` resolves 🟢/🟡 from the firing's own scale:

| Action type | Tier | What it does |
|---|---|---|
| `create_task` | 🟢 | mint a follow-up task on the record's timeline |
| `set_field` | 🟢 | update a field on the record that fired |
| `add_to_list` | 🟢 | add the record to a static list |
| `draft_email` | 🟢 | compose a draft email (records the draft; **never sends**) |
| `notify` | 🟢 | notify a user (no transport wired here → honest `skipped`, §9) |
| `assign_owner` | `dynamic` | set/reassign the owner — 🟢 single-entity, 🟡 at scale (ADR-0026 §3) |
| `request_approval` | 🟡 | stage a human approval — confirm-first by its very nature |

Adding an eighth member of either set moves three things together — a new constant, its `triggerDefs`
/ `actionDefs` registry row, and the closure test's pinned list — so a silent addition or drop can't
land. This is a deliberate rejection of a visual builder, not a deferred feature: a free
predicate/action DSL would be a second evaluator to secure and audit independently of everything else
that touches a record. The catalog evaluates **no predicate of its own** — every trigger condition
compiles through `storekit.CompilePredicate`, the same primitive `collections`' dynamic segments use.

## 2. "Automation" vs "workflow" — one word per layer

Two words show up a lot here and they are **not** synonyms — they name two layers of the same
feature, and the split is deliberate (it's baked into the spec and the public contract, so it isn't
drift to "clean up"):

| Term | Layer | Where it lives |
|---|---|---|
| **automation** | the **product** — what a user creates and sees | ADR-0035; the `/automations` API + `AutomationCatalogEntry`; the `automation` table; this module's name |
| **workflow** | the **engine** — how that automation executes | the frozen `shared/ports/workflow` seam (`Handler`, `Effect`, `ActionKind`); the `WorkflowEngine`; the `workflow_run` table + `/workflow-runs`; the `cg:workflows` dispatch group |

The rule of thumb: **product/user-facing code says _automation_; the executor seam and runtime say
_workflow_.** So a user enables an *automation*, and the system records its *workflow-runs*. Don't try
to collapse the two — `ports/workflow` is a frozen additive-only seam (ADR-0017 §1) and `workflow_run`
/ `/workflow-runs` are shipped contract surface, so renaming either would break the seam rule and the
contract gate; a genuine merge would be an upstream spec change (P3), not a local rename.

This same split is why "action" means two different types — conflating them is the easiest way to
misread the module:

- **`automation.ActionType`** (and `TriggerKind`) — the **user-facing catalog** (§1); what
  `AutomationCatalogEntry` names on the wire.
- **`workflow.ActionKind`** (`shared/ports/workflow`) — the **executor vocabulary** one layer down;
  the typed actions `ApplyActions` actually runs.

`ActionDef.Executor` maps each catalog action to its executor (many-to-one in principle). The gate
reverse-maps executor → permission via `RequiredPermissionForKind`, and
`TestRequiredPermissionForKindReverseMapIsUnambiguous` proves no two catalog actions disagree about
what an executor requires. The catalog is the productized subset; the executor set is the real
capability.

## 3. The engine — how one firing runs

A **handler** is the whole of one automation type: a `workflow.Handler` with five methods.

```go
Spec() workflow.Spec              // name, trigger (EventType xor Schedule), risk tier
Match(ctx, ev) (bool, error)      // does this event/candidate satisfy the condition?
Plan(ctx, ev) (workflow.Effect, error)            // the typed actions to apply — computes, never applies
Apply(ctx, ev, effect, token) (RunResult, error)  // run them through the seams
IdempotencyKey(ev) string         // what "the same occurrence" means (§5)
```

`WorkflowEngine.runOne` (`engine_run.go`) drives every firing through a fixed pipeline and records
**every** terminal outcome durably — a run history that showed only successes would hide exactly the
firings a human needs to see. Each non-applied outcome carries a human-readable reason on the run's
`detail` column (`rundetail.go`):

| Stage | Outcome |
|---|---|
| `Match` returns false | event trigger → `skipped`; **clock trigger → nothing recorded** (§5) |
| `Plan` declines (e.g. no recipient) | `skipped` with the reason — never a hard error (§5) |
| `Plan`/encode errors | `failed` |
| owner gate blocks (§6) | `blocked`; a *transient* resolver error propagates so it retries |
| claim conflicts (redelivery) | nothing — the first firing already won |
| `Apply` runs | `applied` · 🟡 → `requires_approval` (§8) · no transport → `skipped` · error → `failed` |

The **claim** is the idempotency guard: `runOne` inserts the `workflow_run` row `ON CONFLICT DO
NOTHING` **before** `Apply`, so an at-least-once redelivery of the same occurrence finds the row taken
and does nothing. Every write in the pipeline runs inside `database.WithWorkspaceTx` (RLS-bound), and
`workflow_run` is a FORCE-RLS tenant table — a run can never be read or written cross-workspace.

## 4. Two entry points, one path

- **Event triggers** ride the bus. A domain write lands in the outbox, the relay ships it, the
  `cg:workflows` consumer group delivers it, and `WorkflowEngine.HandleEvent` (`engine.go`)
  dispatches to every registered handler whose `Spec().Trigger.EventType` matches — once per **enabled
  instance** in the event's workspace, the instance's params riding the event into `Plan`. `cmd/worker`
  is the only consumer of `cg:workflows`.
- **Clock triggers** have no event to arrive on (AUTO-EV-7). `TimeScanner.Scan` (`timescan.go`) is a
  River-driven periodic pass against an **injected clock**: it enumerates workspaces, reads each clock
  automation's stale candidates through the `ActivityScan` seam, synthesizes a `workflow.Event` per
  candidate, and hands each to `runOne`.

## 5. The occurrence key — and the trap it avoids

`runOne` claims a `(handler, idempotency_key)` row. What "the same occurrence" means differs by trigger
shape, and getting it wrong is the subtlest bug this subsystem has to avoid:

- An **event trigger**'s key carries the bus envelope's own `ev.ID` — unique per delivery. A non-match
  is safe to record as `skipped` (`recordSkip`): that key is never needed again.
- A **clock trigger**'s condition is *continuously* true or false. There is no `ev.ID`; the key must be
  the **anchor** that makes it true — the last-activity timestamp, the date value
  (`anchorIdempotencyKey`, `handlers_clock.go`). The firing re-arms exactly when the anchor moves.

The trap: if a clock non-match went through `recordSkip`, it would claim the anchor key **while the
condition is still false**. Days later, when the anchor finally crosses the threshold, the real firing
tries to claim the same key, finds it taken, and **never fires** — with run history showing an
unremarkable `skipped` row. Nothing errors; a test asserting only "no second run on an unchanged
anchor" passes green against this bug. `runOne`'s guard: a clock non-match returns `nil` directly,
never touching `recordSkip` — the skip ledger and the firing claim don't share a key space for a
continuously-evaluated trigger.

## 6. Both permission gates

Two checks ask "is this automation allowed to do this?" from two moments — only one is the security
boundary:

- **Author-time ceiling** (`ceiling.go`) runs when a human creates/updates an automation: checks the
  *author's* current RBAC and rejects (422/403) an effect they plainly couldn't perform by hand. A
  fast-fail UX convenience — **not** the enforcement point.
- **Match-time owner gate** (`gate.go`) runs on *every firing*, from both entry paths, immediately
  before `Apply`. It re-resolves the automation's `owner_id`'s **live** RBAC through the
  `authz.Resolver` seam and checks it against the actually-planned effect. A firing acts on behalf of
  its owner, whose authority can be revoked or downgraded between authoring and firing; if the owner
  can no longer do it, the firing lands a durable `blocked` run with the reason — never a silent pass.
  A transient resolver failure propagates so the firing retries, never a terminal answer over a blip.

Why both: firings run as `PrincipalSystem`, which `platform/auth` short-circuits straight through —
without the match-time gate the owner's live rights would never be consulted at all. And the ceiling
still earns its keep by catching the obvious mistake where a human can see and fix it, not days later
in run history.

**A `NULL`-owner automation runs ungated.** The seeded starter templates stamp no `owner_id` — no
human authority to re-check — so the gate exits immediately for a zero `OwnerID`. A human-authored
automation always carries an owner (stamped from the acting user, never a request body), so the only
way to reach the gate with a `NULL` owner is the trusted system-seed path.

## 7. The actor & context boundary of a run

Every firing runs under a **synthesized principal context** — there is no HTTP request or logged-in
user behind it — and both entry paths build the same three-part boundary before `runOne` touches the
database:

| | Event trigger (`HandleEvent`, engine.go) | Clock trigger (`TimeScanner`, timescan.go) |
|---|---|---|
| **Tenant** | `WithWorkspaceID(env.WorkspaceID)` — from the bus envelope | `WithWorkspaceID(wsID)` — one per enumerated workspace |
| **Actor** | `PrincipalSystem`, id `"system"` | `PrincipalSystem`, id `"system:time-scan"` |
| **Correlation** | a fresh `ids.NewV7()` per event | a fresh `ids.NewV7()` per scan pass |
| **Causation** | `WithCausationEvent(env.EventID)` — the triggering event is the parent edge | none — a clock pass has no source event |

**The tenant boundary is RLS, not a filter.** The workspace on the context is bound as the
`app.workspace_id` GUC by `database.WithWorkspaceTx` (see [write-backbone.md](write-backbone.md)), and
every read and write a firing makes runs inside that transaction — so a run can only ever see and
touch its own workspace's rows, `workflow_run` included (a FORCE-RLS table). The clock scan is the one
place that reads across tenants (`enumerateWorkspaces`, a marked `rls-exempt` pool query over the
non-tenant `workspace` table); it immediately re-enters a per-workspace context before any per-record
work, so nothing downstream ever runs tenant-agnostic.

**A firing acts _as the system_, authorized _as its owner_** — two different identities, and keeping
them straight is the crux:

- **Attribution** — every domain write a firing makes is stamped with the **system** actor and
  `Source = "system"` (`systemSource`), through the normal `storekit.Audit`/`Emit` write shape. An
  automation never impersonates the human who authored it; the audit trail says the system did it.
- **Authorization** — the effect is nonetheless gated against the **owner's** live RBAC at match time
  (§6), so a robot can never carry an action its owner could not perform by hand. The owner is the
  authorization *subject* (read from the automation instance's `owner_id`), not the recorded actor.

So "on behalf of the owner" is an *authorization* statement, not a provenance one: the owner bounds
what may happen; the system principal is who it is attributed to. The actor and `captured_by` are
server-derived from this synthesized principal — **never** from an event payload or request body (P5).
A NULL-owner (system-seeded) automation and a human-authored one therefore differ only by the
instance's `owner_id`, never by anything a caller can set.

## 8. The 🟡 staging path

A handler whose `Plan` emits a 🟡 executor kind (`request_approval`, `send_email`, `advance_deal`
to won/lost), or `assign_owner` resolved at-scale, does **not** run the effect: `ApplyActions` stages
it through the `Approvals` seam, creating a **real** approval row, and `runOne` parks the run in
`requires_approval` with the approval id on its `detail` column. The staged effect lands later through
the approvals redemption path. If the approval is **rejected**, the engine's own `approval.decided`
consumer flips the parked run to a terminal `blocked` (`engine_blocked.go`), matching structurally
on `detail->>'approval_id'` so a wording change can't break the link. This closes the dead end where a
🟡 firing would otherwise park forever with no approval behind it.

## 9. Current limitations

Documented honestly, not apologetically:

- **`renewal_reminder`'s candidate enumeration is deferred.** The handler is fully implemented and
  registered (`Match`/`Plan`/`IdempotencyKey` proven by unit tests over hand-built payloads), but
  `TimeScanner` has no candidate source wired for it. Sourcing "which records have this custom
  renewal-date field's value inside a date range" needs a typed range query over an arbitrary `cf_*`
  column, which no existing cross-module read seam reaches (`fieldcatalog.Reader` is metadata-only;
  `collections.SegmentEngine` is closed to a static field vocabulary; `datasource.SystemOfRecordProvider`
  is frozen at V1). It is inert until that seam is built — never a fabricated read.
- **`notify` ships with no wired transport.** The `Notifier` seam exists and `stage_change_notify`
  drives it, but `compose/workflows.go` wires it `nil`. A firing that would have delivered records an
  honest `skipped` run (`"no notification transport configured"`) instead of a silent no-op or a fake
  success.
- **AUTO-AC-6's reminder reaches the owner through row-scope, not a personal task queue.** The
  no-activity/check-in/renewal reminders create a task owned by the entity's owner; that owner sees it
  via RBAC row-scope (`own`/`team`/`all`), not a dedicated assignee-based "my tasks" queue (a separate,
  planned epic).

## Rules of thumb

- **The catalog is code, not config.** A new trigger/action is a constant + a registry row + the
  closure test's pinned list, moved together — never a data row.
- **`Plan` computes, `Apply` executes.** Keep `Match`/`Plan` pure (no I/O): a pre-`Apply` failure is
  recorded terminal, so a transient error there would strand the effect.
- **`Key == Spec().Name`** is the only join between a catalog entry and the handler that runs it. A
  mismatch is a silent no-op the orphan-key fitness test exists to catch.
- **A clock handler keys on its anchor, an event handler on `ev.ID`.** Never key a clock trigger on a
  per-pass id, or it refires every tick.
- **Every firing is gated against the owner's *live* RBAC** — never a snapshot, never the author's.
- **A cross-module read/write is a seam** (`seams.go`) wired in compose — never a sibling import.

## Where the code lives

| | |
|---|---|
| The registry (7×7 catalog + closure test) | `internal/modules/automation/catalog_triggers.go`, `catalog_actions.go`, `catalog_closure_test.go` |
| The instantiable catalog (seeded + authorable-only) + store | `internal/modules/automation/automations_catalog.go`, `automations.go` |
| The engine + per-firing lifecycle | `internal/modules/automation/engine.go`, `engine_run.go`, `engine_blocked.go` |
| The shipped handlers (event / clock) | `internal/modules/automation/handlers_event.go`, `handlers_clock.go` (+ `people`'s `assign_lead_owner`, `lead_score_recompute`) |
| The action executors | `internal/modules/automation/handlers_actions.go` |
| The clock entry point | `internal/modules/automation/timescan.go` |
| The two permission gates | `internal/modules/automation/ceiling.go`, `gate.go` |
| The cross-module seams (adapters wired in compose) | `internal/modules/automation/seams.go` |
| The scaffold generator (write-once) | `backend/tools/gen-workflow` |
| Cross-module wiring (executors, resolver, River job) | `internal/compose/workflows.go`, `timescan.go`, `jobs.go` |

The single source of truth for **which workflows exist and where they're registered** is
`compose/workflows.go` — it lists `StarterWorkflows(...)`, `people.LeadRoutingWorkflow(...)`, and the
system handlers in one place. Two handlers live in `people` (not `automation`) on purpose: their engine
is people's own lead SQL, and a module never imports a sibling, so compose registers them as the
cross-module edge.

## Where to go next

- Creating or wiring a new automation: [how-to/create-a-workflow.md](../how-to/create-a-workflow.md).
- What every module owns, including `automation`'s tables and HTTP surface: [reference/modules.md](../reference/modules.md).
- The write shape every firing commits through and who else consumes `cg:workflows`: [write-backbone.md](write-backbone.md).
- How `compose` wires the seams, the `authz.Resolver`, and the River time-scan job: [composition-layer.md](composition-layer.md).
