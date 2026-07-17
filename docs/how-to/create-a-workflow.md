# Create an automation workflow

A task checklist for adding a new automation — a "when X happens, do Y" template a workspace can
enable — to the closed catalog (`internal/modules/automation`). Like the API, automation is
code-and-test, never data: you scaffold a handler, fill in its `Match`/`Plan`, register it, and let
the closure tests prove the wiring. For *why* it works this way — the closure, the one firing path,
and the two permission gates every firing passes through — see
[explanation/automation.md](../explanation/automation.md).

This recipe covers the common case: **adding a new starter workflow handler** over the existing
seven-trigger × seven-action vocabulary. Adding an eighth *trigger kind* or *action type* to the
vocabulary itself is a larger, spec-level change — extend
`catalog_triggers.go`/`catalog_actions.go` and `catalog_closure_test.go`'s pinned lists together, and
expect to touch the match-time gate's permission table (`catalog_actions.go`'s `actionDefs`) too
(step 6 covers the permission side).

## Steps

1. **Scaffold the handler**: `make gen-workflow NAME=<snake_case_name>` (from `backend/`, or
   `go run ./tools/gen-workflow <snake_case_name>` directly). This writes
   `internal/modules/automation/handlers_<name>.go` and its test stub — a handler that compiles,
   registers, and declares a placeholder trigger + tier, both carrying the BUSL SPDX header. It is
   **write-once**: it refuses if either file already exists, so re-running it never clobbers your
   edits.

2. **Fill in `Match` and `Plan`** in the scaffolded handler:
   - `Match` decides whether this firing's event/candidate satisfies the automation's condition.
     Read the automation's own params off `ev.Params` (see any existing starter's own knob reader,
     e.g. `noActivityDays` in `handlers_clock.go`, for the one-reader-per-knob pattern
     that keeps a coarse pre-filter and the precise recheck from drifting apart).
   - `Plan` builds the `workflow.Effect` — one or more typed `workflow.Action` values from the
     **executor** vocabulary (`ports/workflow.ActionKind`), not the catalog's `automation.ActionType`
     — see `ApplyActions`' switch in `engine.go` for the full closed set.
   - Set `Spec().Trigger` to **either** `EventType` (a bus event, e.g. `"deal.stage_changed"`) **or**
     `Schedule` (a non-empty marker string — see `noActivityScheduleMarker`'s doc for why it documents
     intent only and is never parsed as a cron expression) — never both;
     `RegisterWorkflow` panics on a handler declaring neither, or both.
   - Set `Spec().Tier` to the risk tier the *action* actually carries (`mcp.TierGreen` for an
     auto-executing effect, `mcp.TierYellow` for one that must stage for approval).

3. **Add a `Catalog()` entry** in `automations_catalog.go` whose `Key` equals the handler's
   `Spec().Name` **character for character**. This is the load-bearing invariant of the whole
   catalog: `Key == Spec().Name` is the only thing that connects a catalog row a workspace enables to
   the handler that actually runs it — there is no other join.

   **The orphan-key trap:** a catalog entry whose `Key` has no matching registered handler is not an
   error. It reports `Active` in the UI, accepts params, and validates fine — and then **never
   fires and never logs anything**, because `HandleEvent` and the time-scan both dispatch by looking
   up `instances[h.Spec().Name]` for each *registered handler*, not by walking catalog entries
   looking for a handler. A typo in either name produces the worst kind of silent no-op: nothing
   about the automation looks broken until someone notices it never ran.

   Set `Seeded: true` only if this becomes one of the six starter templates every fresh workspace
   enrolls (step 5) — most new catalog entries are **authorable-only** (`Seeded: false`): fully
   instantiable through the API, never auto-enrolled.

4. **Register the handler** in `StarterWorkflows()` (`handlers_event.go`). `compose/workflows.go`
   already ranges over that slice when it builds the engine, so nothing else needs to change to wire
   a new starter into the running binary. (A handler whose engine needs a *sibling module's* store —
   the way `assign_lead_owner`'s routing logic lives in `people`, not `automation` — is registered
   directly from that module's own compose wiring instead; see `compose/workflows.go`'s own doc for
   why `route_lead` and `assign_lead_owner` are deliberately two different handlers under two
   different names rather than one handler with two meanings.)

5. **Run the closure tests** (`go test ./internal/modules/automation/...`, part of `make check`):
   - `catalog_closure_test.go` proves every catalog action has a definition with a resolvable
     permission shape, and (for the vocabulary itself) that the trigger/action sets match the pinned
     spec lists in both directions.
   - `seed_test.go`'s `TestEveryCatalogKeyResolvesToARegisteredHandler` is the orphan-key trap made
     mechanical: it derives the registered-handler-name set from `StarterWorkflows()` (plus the
     externally-registered `assign_lead_owner`) and fails if any catalog key has no match. If you add
     a starter without registering it, or register a handler under a name that doesn't match its
     catalog key, this test catches it before a human ever notices the silent no-op.
   - If your new entry is one of the seeded six, `TestExactlySixSeededTemplatesWithPinnedNames` and
     `TestNonSeededCatalogEntriesStayOutOfTheSeed` both need updating to keep the pinned set in sync.

6. **Declare the permission tier** the action requires (`catalog_actions.go`'s `actionDefs`, if
   you're adding a genuinely new action type rather than reusing an existing one) — this is what the
   author-time ceiling (`ceiling.go`) checks at creation time and what the match-time gate (`gate.go`)
   re-checks against the *owner's* live RBAC on every firing. Get the `PermissionShape` right:
   `PermissionPinned` if the action always touches one fixed entity type (e.g. `create_task` always
   creates an `activity`); `PermissionTargetScoped` if the real entity type comes from whatever the
   trigger fired on (e.g. `assign_owner`/`set_field`, which route to
   `provider.Update{Ref: action.Target}`). Getting this wrong either gates the wrong object or leaves
   an action ungated — `assertPermissionIsExactlyOneShape` (`catalog_closure_test.go`) proves every
   action is exactly one of the two, never neither.

7. **Seed it, if it's a starter** — only if `Seeded: true`, confirm `SeedStarterAutomationsTx`
   (`automations.go`) enrolls it and that its default (nil) params pass its own `Validate`
   (`TestSeededEntriesDefaultParamsPassTheirOwnValidate` proves this for every seeded entry).

8. **Verify**: `make check` (the catalog closure tests, the orphan-key trap, `arch-lint`, the license
   header on your new files) and `make test-integration` if your handler reads through a new
   cross-module seam (add one in `seams.go` first if it needs a store this module doesn't already
   have access to — see [explanation/automation.md](../explanation/automation.md#two-vocabularies-one-layer-apart)
   for why a seam, never a direct import of a sibling module, is the only legal path).

## Notes

- **A new cross-module read or write needs its own seam**, declared in `seams.go` with only
  `ids`/`json`/stdlib types, and its real implementation wired as a compose adapter
  (`internal/compose/workflows.go`) — the same "module never imports a sibling" rule every other
  module in this repo follows.
- **There is no generated manifest.** `gen-workflow` scaffolds the handler and its test only; it
  never touches `Catalog()` or `StarterWorkflows()`. Steps 3 and 4 above are deliberate, reviewed
  hand-edits — the generator prints them as its own "next steps" output rather than taking them for
  you, so a catalog key never gets wired to the wrong handler by an automated pass.
