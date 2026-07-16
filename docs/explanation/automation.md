# Automation: the closed catalog and its trigger runtime

`internal/modules/automation` (`ADR-0035`) lets a workspace enable and parameterize a fixed set of
"when X happens, do Y" templates — no builder, no rule language, no user-defined trigger or action.
This page is the *why* behind that shape: the closure, the two vocabularies it straddles, the one path
both trigger kinds converge on, the subtlest bug this module had to avoid, and the two permission
checks that look redundant until you see why neither alone is enough. For what each trigger/action
*is*, see [reference/modules.md](../reference/modules.md); for the write shape every firing commits
through, see [write-backbone.md](write-backbone.md).

## A closed catalog, not a builder

The catalog is exactly **seven trigger kinds × seven action types** (`catalog_triggers.go`,
`catalog_actions.go`), and the closure is asserted in both directions against the spec's pinned
vocabulary (`catalog_closure_test.go`): the code can't silently grow past the seven, and it can't
silently drop below them either. Adding an eighth member of either set is a code-and-test change —
a new `TriggerKind`/`ActionType` constant, a registry entry, and the closure test's pinned list all
move together — **never a data row**.

This is a deliberate rejection, not a deferred feature. An earlier design considered a visual
rule builder (arbitrary predicates over arbitrary fields, freely composed). It was turned down: a
free predicate/action DSL is a second evaluator the codebase would then have to secure, audit, and
reason about independently of everything else that already touches a record — the filter engine,
the RBAC gate, the write shape. The catalog instead evaluates **no predicate of its own**: every
trigger's condition compiles through `storekit.CompilePredicate`, the same platform primitive that
already backs `collections`' dynamic list segments. A closed catalog with one shared filter engine
underneath is a narrower attack surface than a builder with its own, and it is the only thing this
codebase's row-scope and RBAC machinery is proven to reason about correctly.

## Two vocabularies, one layer apart

Two distinct types both call themselves "actions," and conflating them is the easiest way to
misread this module:

- **`automation.ActionType`** (and `automation.TriggerKind`) is the **user-facing catalog** — the
  seven things a workspace author picks from when they enable an automation. This is what
  `AutomationCatalogEntry` on the wire names.
- **`workflow.ActionKind`** (`shared/ports/workflow`) is the **executor vocabulary**, one layer
  down — the typed actions `ApplyActions` (`workflows.go`) actually knows how to run
  (`ActionCreateTask`, `ActionUpdateRecord`, `ActionAssignOwner`, `ActionAddToList`,
  `ActionDraftEmail`, `ActionNotify`, `ActionEmitFlowEvent`, plus a couple no starter uses yet).

`ActionDef.Executor` (`catalog_actions.go`) is the registry's ruling on which executor backs each
catalog action — the mapping is many-to-one in principle (nothing stops two catalog actions from
sharing an executor) and is proven unambiguous in the reverse direction:
`RequiredPermissionForKind` walks the map back from an executor kind to its permission, and
`TestRequiredPermissionForKindReverseMapIsUnambiguous` proves no two catalog actions disagree about
what an executor requires. The catalog is the productized subset; the executor set is the actual
capability. A catalog entry is a template over the registry, never the other way around.

## Two entries, one path

An automation can fire off two genuinely different triggers, and both converge on the identical
firing logic:

- **Event triggers** ride the bus: a domain write lands in the outbox, the relay ships it to Redis,
  `cg:workflows` delivers it, and `WorkflowEngine.HandleEvent` (`workflows.go`) dispatches to every
  registered handler whose `Spec().Trigger.EventType` matches, once per enabled automation instance.
- **Clock triggers** have no event to arrive on at all (`no_activity_for_n_days`,
  `date_field_approaching`, `task_overdue` — AUTO-EV-7). `TimeScanner.Scan` (`timescan.go`) is a
  River-driven periodic pass: it enumerates workspaces, reads each clock automation's stale
  candidates through the `ActivityScan` seam, and synthesizes a `workflow.Event` per candidate.

Both hand off to the **same** `WorkflowEngine.runOne` (`workflows_run.go`): `Match → Plan → claim →
Apply`, one claimed-and-recorded run row per firing. Nothing downstream of `runOne` can tell a
synthesized clock pass from a bus delivery — which is exactly the point. The occurrence-key
idempotency guard and the match-time owner gate (both below) apply to both entries automatically,
because they're wired once, at the one place both paths reach.

## The occurrence key, and the trap it exists to avoid

`runOne` claims a `(handler, idempotency_key)` row before doing anything else
(`workflow_run`'s unique constraint, `ON CONFLICT DO NOTHING`) — whoever inserts first wins, so
an at-least-once redelivery of the same occurrence is a no-op. What "the same occurrence" means
differs by trigger shape, and getting this wrong is the single subtlest bug this subsystem has to
avoid:

- An **event trigger**'s key carries the bus envelope's own `ev.ID` — a fresh, unique value per
  delivery. A non-match (the event arrived, but the automation's own condition declined) is safe to
  record as a `skipped` run (`recordSkip`), because that key will never be needed again: no future
  delivery of the *same* event exists to reuse it.
- A **clock trigger**'s condition is *continuously* true or false — "no activity for 7 days" stays
  false for six days and nine hours, then flips true and stays true until something resets the
  clock. There is no `ev.ID` to key on. The key has to be the **anchor** that makes the condition
  true: the last genuine activity timestamp, the date value, the due date. The firing re-arms
  exactly when that anchor moves, and stays quiet while it doesn't.

The trap: if a clock non-match were recorded through the same `recordSkip` path an event non-match
uses, it would claim the anchor key on `ON CONFLICT DO NOTHING` — while the condition is still
false. Days later, when the anchor finally crosses the threshold and the condition turns true, the
real firing tries to claim the *same* key and finds it already taken. `claimRun` reports
`claimed=false`, `runOne` returns cleanly, and the automation **never fires** — with the run
history showing a `skipped` row that looks entirely unremarkable. Nothing errors. Nothing looks
broken. A test asserting only "no second run on an unchanged anchor" would pass green against this
bug, because it can't distinguish "correctly ran once" from "never ran at all."

`runOne`'s guard against this is explicit and reads as the invariant, not a patch: a clock trigger's
non-match returns `nil` directly, never touching `recordSkip` at all (`workflows_run.go`). A coarse
pre-filter's rejects are pre-filter noise, not a user-meaningful skip, and recording them would only
accrete a row per rejected candidate per pass anyway. The skip ledger and the firing claim
deliberately do not share a key space for a continuously-evaluated trigger.

## Both permission gates

Two checks look at the same question — "is this automation allowed to do this?" — from two
different moments, and only one of them is the actual security boundary:

- **The author-time ceiling** (`ceiling.go`, `requireAuthorCeiling`) runs when a human creates or
  updates an automation: it checks the *author's* current RBAC against the action's required
  permission and rejects (422/403) if they plainly couldn't perform the effect by hand today. This
  is a fast-fail UX convenience — it stops an obviously-wrong authoring attempt before it's saved.
  It is **not** the enforcement point.
- **The match-time owner gate** (`gate.go`, `checkOwnerPermission`) runs on *every single firing*,
  from both entry paths, immediately before `Apply`. It re-resolves the automation's `owner_id`'s
  **live** RBAC through the `authz.Resolver` seam and checks it against the actually-planned effect
  — not a permission snapshot taken at authoring time, and not the author's permission either. A
  firing acts on behalf of its owner, and an owner's authority can be revoked, demoted, or the
  account archived at any point between authoring and firing. If the owner can no longer do it, the
  firing is refused and lands as a durable `blocked` run carrying the reason
  (`"owner's authority no longer permits update on deal"`, for instance) — never a silent pass and
  never a swallowed failure.

Why both are needed, not just the second one: firings run as `PrincipalSystem`, and
`platform/auth`'s admission short-circuits `PrincipalSystem` calls straight through. Without the
match-time gate, the owner's actual rights would never be consulted at runtime at all — the author
ceiling alone would be pure theater once the automation is saved. And why not just the match-time
gate alone: it only runs once a human has already authored something to fire, so the ceiling still
earns its keep by rejecting the obviously-wrong case at the point a human can see and fix it,
instead of only ever surfacing as a `blocked` row in run history days later.

**A `NULL`-owner automation runs ungated.** The six seeded starter templates
(`SeedStarterAutomationsTx`) stamp no `owner_id` — there is no human authority behind a system-seeded
default to re-check, so the gate exits immediately for a zero `OwnerID`. Every human-authored
automation always carries an owner (`Create` stamps the acting user), so the only way to reach the
gate with a `NULL` owner is the trusted system-seed path, never a request-supplied value.

## The scaffold generator, and why there's no manifest

`backend/tools/gen-workflow` (`make gen-workflow NAME=<snake_case_name>`) scaffolds a new
`workflow.Handler`: a compiling, registering handler stub plus its test stub, both carrying the
BUSL SPDX header. It is deliberately **write-once** — it refuses outright if either target file
already exists, rather than silently clobbering hand-written logic the next time someone runs it.
That property is what separates "seeded your starting point" from "owns your code" once you start
filling in `Match`/`Plan`.

The generator stops there on purpose. It does not touch `Catalog()` (`automations_catalog.go`) or
`StarterWorkflows()` (`workflows_starter.go`) — **there is no generated manifest** wiring a
scaffolded handler into the running registry. Once you've filled in the handler, you add a
`Catalog()` entry and register the handler in `StarterWorkflows()` by hand, the same way every
existing starter got there. This is the honest as-built state, not a gap in an otherwise
manifest-driven design: nothing else in this module generates a registration list either — the
closed `Registry` (the catalog + the handler slice) *is* the compile-time glue, and hand-registering
a short, human-reviewed list is exactly the same shape the rest of the repo already uses for its
other hand-wired composition points (see [composition-layer.md](composition-layer.md)). A generated
manifest is a real pattern this codebase uses elsewhere for larger, mechanically-derivable surfaces
(the contract-generated `stubs_gen.go`, for instance) — it just isn't the shape a seven-member,
rarely-changing catalog needed here.

## Current limitations

Documented honestly, not apologetically:

- **`renewal_reminder`'s candidate enumeration is deferred.** The handler is fully implemented and
  registered — `Match`/`Plan`/`IdempotencyKey` are all correct against whatever anchor a real
  enumeration would eventually carry, proven by unit tests over hand-built payloads — but
  `TimeScanner` has no candidate source wired for it (`timescan.go`'s `activityScanHandlers` map
  carries no entry for it). Sourcing "which records have this custom renewal-date field's value
  inside a date range" needs a typed range query over an arbitrary `cf_*` column, and none of the
  existing cross-module read seams reach that: `fieldcatalog.Reader` answers catalog metadata only,
  never a row's value; the one facility that runs typed range predicates over a record table
  (`collections.SegmentEngine`) is deliberately closed to a static, compile-time field vocabulary
  per resource, excluding `cf_*` columns by design; and `datasource.SystemOfRecordProvider` is
  frozen at V1. Wiring this candidate source is real customfields/collections engineering, not a
  thin adapter over something that already exists — it is inert (never surfaces a candidate) until
  that seam is built, in every workspace, whether or not a renewal-date field is configured.
- **`notify` ships with no wired transport.** This repo has no notification table and no channel
  adapter — the `Notifier` seam (`seams.go`) exists and the `stage_change_notify` starter drives it,
  but `compose/workflows.go` wires it `nil`. A firing that matches and would have delivered records
  an honest `skipped` run with the reason `"no notification transport configured"`
  (`ErrNoNotificationTransport`) instead of silently discarding the outcome or fabricating a success.
  The template stays seeded and instantiable regardless — a workspace with no transport configured
  simply never sees a delivered notification, which is a different, more honest failure than a
  template that doesn't exist.
- **AUTO-AC-6's reminder reaches the owner through row-scope, not a personal task queue.** The
  no-activity/check-in/renewal reminders create a task whose owner is the entity's owner; that
  owner sees it because RBAC row-scope makes the row visible to them (`own`/`team`/`all`), not
  because a dedicated assignee-based "my tasks" queue surfaces it specifically. A personal task
  queue with its own assignment semantics is a separate, planned epic, not a gap in this PR's own
  scope.

## Where the code lives

| | |
|---|---|
| The registry (7×7 catalog + closure test) | `internal/modules/automation/catalog_triggers.go`, `catalog_actions.go`, `catalog_closure_test.go` |
| The instantiable catalog (seeded + authorable-only) | `internal/modules/automation/automations_catalog.go` |
| The engine + per-firing lifecycle | `internal/modules/automation/workflows.go`, `workflows_run.go` |
| The clock entry point | `internal/modules/automation/timescan.go` |
| The two permission gates | `internal/modules/automation/ceiling.go`, `gate.go` |
| The cross-module seams (adapters wired in compose) | `internal/modules/automation/seams.go` |
| The scaffold generator | `backend/tools/gen-workflow` |
| Cross-module wiring (executors, the resolver, the River job) | `internal/compose/workflows.go`, `internal/compose/timescan.go`, `internal/compose/jobs.go` |

## Where to go next

- Adding a new trigger or action: [how-to/add-an-automation-trigger-or-action.md](../how-to/add-an-automation-trigger-or-action.md).
- What every module owns, including `automation`'s tables and HTTP surface: [reference/modules.md](../reference/modules.md).
- The write shape every firing commits through (`workflow_run` + `audit_log` + `event_outbox` in one
  transaction) and who else consumes `cg:workflows`: [write-backbone.md](write-backbone.md).
- How `compose` wires the executor seams, the `authz.Resolver`, and the River time-scan job:
  [composition-layer.md](composition-layer.md).
