# Overlay augmentation — HubSpot as the system of record

An enterprise already living in HubSpot will not rip it out to try Margince's AI. **Overlay mode**
sells the AI layer *on top of* the CRM they keep: HubSpot stays canonical, Margince runs a governed,
cached mirror alongside it. This page is the mental model — the two SoR modes, the two seams, why the
mirror is a cache and not a copy of authority, how it stays in sync, how it fails closed, and what
happens on teardown.

The full design (decisions, spike findings, review trail) lives in the working design doc at
`.tmp/hubspot-overlay/design.md` (an internal build artifact, not tracked in git); this page is the
durable summary. Cross-references: [write-backbone.md](write-backbone.md) (the audit+outbox shape overlay
*ingest* deliberately does not use), [authorization.md](authorization.md) (the RBAC gate overlay's
connection lifecycle rides), [contract-first.md](contract-first.md) (the frozen contract this seam
binds to), [composition-layer.md](composition-layer.md) (where the per-workspace dispatch lives).

**Status: branch 1 (read + continuous sync) only.** Write-back, the visibility SLO/2×SLO floor, and
the overlay→native flip are later branches — see [What branch 1 does not do](#what-branch-1-does-not-do)
below. Don't read this page as a description of a finished write-capable overlay.

## Two system-of-record modes, one codebase

Every workspace picks its SoR mode once, at connect time:

| `workspace.x_sor_mode` | Who is canonical | Served by |
|---|---|---|
| `native` (default) | Margince's own tables (`person`, `deal`, …) | `compose.Provider` |
| `overlay` | the connected incumbent (HubSpot today) | `overlay.Provider` |

`x_sor_mode = overlay` requires `x_incumbent` to name one (`hubspot`/`salesforce`/`dynamics` — only
`hubspot` is implemented); the inverse is a database CHECK constraint
(`x_overlay_iff_incumbent`), not an application convention. Nothing above the seam — the AI layers, the
MCP tool surface, capture, the React UI — knows which mode a workspace is in; they all call the same
`datasource.SystemOfRecordProvider` port, and a request-scoped **dispatcher** resolves the workspace's
mode and routes to the right implementation per call. That dispatch is itself new work (branch 1's
hardest architectural risk, per the design doc's D1): `compose.Provider` was a boot-time singleton
before overlay existed; routing per-request by `x_sor_mode` is what makes both modes coexist in one
running binary.

## The life of an overlay, end to end

The sections below each explain one mechanism in depth; this is the chronological spine that ties them
together — what actually happens, in order, from connect to disconnect:

1. **Connect.** An admin calls `POST /v1/overlay/connection` with a HubSpot private-app token and
   region. In one transaction the token is sealed into the vault, the `incumbent_connection` row lands
   (with the standard audit + outbox shape), and `workspace.x_sor_mode` flips to `overlay`. At most one
   connection can be active. See [Connection lifecycle](#connection-lifecycle-and-who-may-touch-it).
2. **Seed who-sees-what.** Connecting pulls HubSpot's **owners directory** (`owner id → email`) and
   matches each owner's email against the installation's existing users, writing one `mirror_user_map`
   row per match — a *match against existing users, never an import that creates them*. No match, no
   row: a just-connected overlay is visible to exactly the users HubSpot actually owns records
   through, and to nobody else. See [Fail-closed visibility](#fail-closed-visibility).
3. **Backfill.** The first reconcile sweep after connect — the background poller's next tick, or an
   on-demand `POST /v1/overlay/reconcile` — pages every object class into the mirror,
   cursor-checkpointed so a crash or timeout resumes rather than restarts, and marks
   `backfillComplete` truthfully in `GET /v1/overlay/sync-status`. See
   [One ingest path](#one-ingest-path-backfill-and-poll-converge).
4. **Continuous sync.** Every later sweep rides the per-class modified-timestamp watermark: changed
   records are re-fetched and land through the same guarded ingest upsert, incumbent-wins
   (`mirror.conflict` on divergence). Each sweep also re-runs the owners-directory match (catching
   users and owners added since) and re-validates existing email-sourced mappings against owners'
   *current* emails, dropping any that no longer match — fail-closed in both directions.
5. **Reads.** Nothing above the seam changes: the same `/v1` endpoints, search, and MCP tools serve
   overlay records through the dispatcher, filtered by the visibility deny-join and labeled honestly
   (`authoritative: false`, `trust_tier: external`, a real `last_synced_at`, `source: overlay`). A
   list/search dial the mirror cannot answer (owner/tag/stage filters, sorts, search cursors) is
   refused with a 422 naming the parameter — never silently ignored. One contract caveat: an
   overlay deal's required `pipeline_id`/`stage_id` UUID slots are the zero UUID (no native
   pipeline/stage rows exist in overlay mode; the incumbent's own keys ride `raw`) — making these
   nullable for overlay reads is an open contract question to reconcile upstream. Force-fresh
   read-through spends a metered budget and degrades to the mirror under pressure. See
   [Force-fresh reads](#force-fresh-reads-the-budget-meter-and-shed-degrade).
6. **Disconnect.** `DELETE /v1/overlay/connection` purges the mirror, associations, visibility
   projections, and user map in one transaction, writes tombstones, and flips back to `native` — the
   connection's own audit history is retained, but no HubSpot-derived record data remains queryable.
   See [Teardown](#teardown-purge-and-tombstone).

## The frozen seam, and the inner seam behind it

The single most important constraint on this build: **`datasource.SystemOfRecordProvider`'s method set
is frozen** (a fitness test, `TestSystemOfRecordProviderV1MethodSetIsFrozen`, fails a PR that changes
it). Overlay mode is built entirely as a *second implementation* of that interface — never a change to
it:

```
AI layers · MCP tools · capture · React UI
        │  (call ONLY the port — arch-lint forbids an incumbent-SDK import above the seam)
        ▼
datasource.SystemOfRecordProvider        ← FROZEN seam (13 methods)
        │
   dispatcher resolves workspace.x_sor_mode:
        ├── native   → compose.Provider     (people/deals/activities/… tables)
        └── overlay  → overlay.Provider     (the HubSpot-backed adapter)
```

A verb the overlay adapter cannot serve returns `apperrors.ErrUnsupportedBySoR` rather than a partial or
faked answer — the caller always gets an honest "not supported here," never silence.

**A second, inner seam exists so more incumbents slot in additively.** `overlay.Provider` doesn't talk
HubSpot directly; it depends on `incumbent.Incumbent`, the shared substrate's own seam for "any incumbent
CRM":

```
overlay.Provider            ← binds the frozen outer seam; incumbent-agnostic
      │  depends only on ↓
incumbent.Incumbent         ← inner seam (ours): Backfill · ParseEvent · DriftRead · Push ·
      │                        DiscoverSchema · MapObject/MapAssoc · VisibilityProjection · Capabilities
      ▼
hubspot.Adapter              ← the first (and only) real implementation
fake.Adapter                 ← the mock the acceptance-gate tests drive
```

This split earns its place on real need, not on "the test mock counts as a second caller": the
substrate genuinely has to cross the wire through *some* boundary, and the spec commits to Salesforce
and Dynamics adapters as concrete future callers of the same inner seam — a new incumbent is meant to
cost one new subpackage + one dispatch `case`, with the substrate (mirror engine, visibility, budget
meter, teardown) untouched.

## The mirror is a cache, not a second authority

`overlay_mirror` holds one row per HubSpot object the workspace can see — projected fields, a
`sync_state` (`fresh`/`pending_sync`/`stale`), and `last_synced_at`. It is a **derived read-model**, and
every read through it says so honestly:

- The contract's `Freshness`/`Authoritative` fields are never faked. A mirror-served read answers
  `Authoritative: false` plus the real `last_synced_at` — never a false "this is live."
- The trust tier crosses the frozen seam without widening it: `SearchResult.TrustTier` is stamped
  `external`/`unverified` (**T2**, "external, unverified" in the taxonomy) at the contract-assembly
  layer, derived from `workspace.x_sor_mode = overlay` plus the row's own provenance — not carried as a
  new field on the frozen port types. That taint is meant to ride into anything derived from an overlay
  read (embeddings, context-graph nodes, tool output), though the derivative paths themselves
  (embeddings/context-graph) depend on substrate this branch does not build — see below.
- `RunReport` has no HubSpot analogue and returns `ErrUnsupportedBySoR`, declared in the capability
  manifest rather than silently missing.

## One ingest path: backfill and poll converge

Every sync trigger lands on **one** ingest statement keyed on the record's own
`hs_lastmodifieddate` clock — deduping across multiple clocks is unsound, so there is exactly one
staleness authority however a record arrives. Branch 1 ships two triggers, both pull:

- **Backfill** pages the list endpoint's id-keyset cursor (`after=<id>`), checkpointed and resumable, on
  the general (cheaper, uncapped) rate bucket. It runs as the first step of each reconcile sweep — the
  first sweep after connect does the initial load; once its cursor converges it is a cheap no-op and
  later sweeps skip straight to the incremental pass. For dev/demo against a large portal,
  `MARGINCE_OVERLAY_BACKFILL_LIMIT` bounds the initial load per object class (encoded into the
  checkpoint cursor, so it is stateless and restart-safe); incremental sweeps are never capped.
- **Incremental poll** (a River leader-elected periodic job, always on) sweeps the record's own
  modified-timestamp watermark within the search rate budget — branch 1's continuous-sync
  mechanism.

A webhook-as-signal push lane (HubSpot telling us *something changed*, answered by a coalesced
record-clock re-fetch through this same ingest) is **deferred to branch 1b** — see
[the deferral note](#connection-lifecycle-and-who-may-touch-it) under connection lifecycle for
what must land before it can be mounted.

Whichever trigger fires, the same upsert statement lands the row:
`INSERT … ON CONFLICT … DO UPDATE … WHERE excluded.updated_at > overlay_mirror.updated_at` — the
staleness check lives **in the SQL**, so two concurrent triggers serialize on the row lock rather than
racing an application-level read-compare-write.

**This ingest is a derived-cache refresh, not a system-of-record mutation.** It intentionally does not
carry the [`storekit.Audit`+`Emit` write shape](write-backbone.md) — there is no per-row audit entry or
domain event for "the mirror refreshed a field," because that would flood the audit log with what is,
by design, a health/freshness concern, not a business event. What genuinely does emit through the
outbox: `mirror.conflict` (an incumbent value won over a diverged mirror row) and
`mirror.budget_degraded` (a force-fresh read fell back to the mirror under budget pressure) — real
events, registered in the [event catalog](write-backbone.md#the-event-catalog-internalsharedkerneleventscataloggo)
like any other.

## Fail-closed visibility

Overlay reads are per-user visibility-gated, not just per-workspace. `mirror_visibility` is a
deny-projection joined on **every** overlay read: `can_see = false`, or **no row at all**, means the row
is not returned — never "visible until proven otherwise." Three cases the design pins explicitly:

- **Owner projection.** `mirror_user_map` maps a Margince user to a HubSpot owner id (email-matched, or
  an admin `manual` override for reassignment/ambiguity); visibility is computed from
  `hubspot_owner_id` through that map, inline in the same transaction the ingest upsert runs in — not a
  trailing "after backfill completes" pass, which would hide the whole portal during backfill.
- **The map seeds itself, as a match — never an import.** On connect, and again on every reconcile
  sweep, the HubSpot owners directory is matched against the installation's existing users by
  normalized email; each match writes an email-sourced `mirror_user_map` row (re-verified against the
  owner's current email before it lands). No Margince account is ever created from HubSpot data, and
  the sweep also re-validates existing email-sourced mappings — an owner whose email changes drops to
  fail-closed until re-matched or manually re-mapped. `manual` rows are the human escape hatch for
  users the email match cannot reach.
- **Unmapped user ⇒ zero rows.** A Margince user with no incumbent mapping (or one whose email matched
  zero or more than one incumbent user) sees nothing from the overlay, by design — a guessed match is
  never an acceptable trade for convenience.
- **Null-owner records** get an explicit, test-guarded rule rather than an accidental default — an
  unowned HubSpot record is neither silently workspace-visible nor silently hidden from everyone; the
  branch pins one behavior and tests it.

**Honest residual (stated, not hidden):** access revoked in HubSpot mid-window is served until the next
refresh — this branch's visibility is a snapshot model, not an instantaneous mirror of HubSpot's own
permission changes. The tighter floor (a freshness SLO per sensitivity tier, a 2×SLO fail-closed cutoff,
a budget-reserved refresh slice that degrades to *over-hiding* rather than leaking) is deferred — see
below.

## Force-fresh reads, the budget meter, and shed-degrade

The frozen seam's `Freshness` verb is the one place branch 1 does a **live** HubSpot read-through,
bypassing the mirror, and is allowed to answer `Authoritative: true` for an overlay-mode workspace — the
only place it can honestly do so, since every other overlay read serves the mirror. Because that read
spends the same shared HubSpot rate quota the poller spends from, it ships with a matching degrade:
past a configured shed threshold, force-fresh falls back to the mirror (spending zero quota) and emits
`mirror.budget_degraded` rather than either blocking or silently pretending the live read happened.

`GET /overlay/budget` (the OVB meter's read surface) reports the window, consumption, limit, and a
`ok`/`warn`/`shed` band for the **API process's own meter**. The periodic reconcile poller normally runs
in a separate worker process with its own in-memory meter, so its consumption — often the largest single
consumer of the workspace's HubSpot quota — is **not** reflected on this surface until the counter is
shared across processes (a follow-up: the per-process meter is a documented branch-1 limitation, not
shared accounting).

**Current wiring limitation, stated plainly:** the compose-wired `overlay.Provider` behind the api's
per-workspace dispatch currently constructs its `FreshnessReader` with no live incumbent client bound
(`overlay.NewFreshnessReader(nil, …)`) — the per-workspace credential→adapter wiring that would let a
force-fresh read actually reach HubSpot is not yet connected on that path. A `nil` incumbent is the same
honest degrade path as a shed band: the read falls back to the mirror rather than failing or faking
authority. So today, force-fresh reads reached through the compose-assembled dispatcher answer from the
mirror unconditionally; the meter, the shed-band logic, and the `mirror.budget_degraded` event are all
implemented and unit-tested against a real incumbent, they are just not yet reachable end-to-end from an
HTTP request.

## Connection lifecycle, and who may touch it

Connecting or disconnecting a workspace's incumbent binding is destructive, workspace-wide config — it
purges the mirror and flips `x_sor_mode` for every user in the workspace — so it follows the same
posture as quota configuration: **create/update/delete are admin/ops-only**; every role may read the
connection status (a rep can see whether overlay mode is live, the same as reading a quota's
attainment). `POST /overlay/reconcile` (a manual reconciliation sweep) is object-RBAC-gated the same
way, as `ActionUpdate` on `overlay_connection`. See
[how-to/connect-a-hubspot-overlay.md](../how-to/connect-a-hubspot-overlay.md) for the operator recipe,
and [authorization.md](authorization.md) for how object RBAC is enforced at the store/service entry
point regardless of transport.

The private-app token is sealed into `keyvault.Vault` at connect time; `incumbent_connection` stores
only the opaque `credential_ref`, never the token itself — it is never echoed back on any read.

**Continuous sync is poller-only in branch 1; webhook-as-signal is deferred to branch 1b.** The
always-on reconcile poller is the sync mechanism, end-to-end. The webhook-as-signal push lane — an
HMAC-verified HubSpot receipt treated purely as an invalidation signal, coalesced per record and
drained by one batched record-clock fetch through the same ingest — is branch 1b's latency
optimization on top, and it carries a hard precondition: HubSpot's v3 signature authenticates the
request but does not bind it to a tenant, so branch 1b must add a portal-id→workspace binding to
the verification basis (a portal id on `incumbent_connection` and a receiver that resolves the
workspace from the authenticated payload's portal id, never from a caller-supplied header) before
any receiving route is mounted.

## Teardown: purge and tombstone

Disconnecting an overlay connection (`DELETE /overlay/connection`) revokes the connection and, in one
transaction, purges everything branch 1 actually mirrored: `overlay_mirror`, `overlay_association`,
`mirror_visibility`, and `mirror_user_map` — plus writing a tombstone per purged record and flipping the
workspace back to `native` mode. A crash mid-teardown can't leave the workspace half in overlay mode
with its mirror already gone (or vice versa), because it's one transaction.

Because HubSpot stays canonical while read-only, a single-record GDPR erasure of a mirrored row would
otherwise be **re-hydrated by the next poller sweep**. So overlay erasure is a suppression-tombstone
checked **in the same SQL statement** as every ingest upsert (`WHERE NOT EXISTS (SELECT … FROM
overlay_tombstone …)`), never a check-then-act window a sweep could race. An explicit admin lift path
exists for re-consent or mis-erasure — the tombstone is a hold, not a one-way door. The connection's own
audit trail (who connected/disconnected, when) is retained untouched — disconnecting is itself a
governed action, not an erasure of its own record.

**Two purge-scope items the design doc names are not yet live, and the code says so plainly.** Neither
is a silent gap — both are the honest, currently-vacuous state of a branch that is read-only by design:

- **PII-scrub of retained augmentation is vacuous today, not implemented-and-working.** The design doc's
  concern — that audit rows, approvals, activities, or saved agent output might embed *copies* of
  incumbent PII that would need scrubbing or reclassifying on teardown — is real in principle, but
  branch 1 has no code path that copies a mirrored field into a mutable domain row (no note-taking or
  write-back surface exists yet) and `audit_log` itself is immutable by construction regardless. So
  there is currently nothing *to* scrub; this closes when a later branch (write-back, or a note-taking
  surface) starts copying mirrored content into a row of its own — the same pattern `privacy.Eraser`
  already uses for redacting mutable domain rows on Article 17 erasure.
- **FTS/embeddings/context-graph derivatives are not purged today because they don't exist yet.**
  Branch 1 builds no full-text index and no embedding/context-graph node over mirror content, so there
  is nothing there for teardown to purge. Once that retrieval substrate lands, teardown will need to
  purge those derivatives too — tracked as a dependency, not shipped today.

## What branch 1 does not do

Stated explicitly so this page is not read as describing more than was built:

- **Write-back** (editing a record in Margince and pushing it to HubSpot) is the entire next branch —
  every write verb on the overlay provider (`Create`, `Update`, `AdvanceDeal`, `Archive`, `Merge`,
  `PromoteLead`) returns `ErrUnsupportedBySoR` today, by design, not by omission.
- **The visibility SLO / 2×SLO fail-closed floor / budget-reserved refresh slice** is a compliance-floor
  guarantee reserved for the next visibility increment — branch 1 ships the visibility *contract* (the
  deny-join, fail-closed-on-absence, the inline owner projection) but not the freshness-tiered
  refresh cadence on top of it.
- **Overlay→native flip** — the `/overlay/flip*` operations exist in the contract as parseable stubs
  (they answer the not-implemented sentinel) but do no migration work yet.
- **OAuth (public-app) connect** — branch 1 is private-app-token only; there is no refresh-token
  rotation to demonstrate on a static token.

## Where the code lives

| | |
|---|---|
| The outer (frozen) and inner seams | `internal/shared/ports/datasource/`, `internal/modules/overlay/incumbent.go` |
| `overlay.Provider`, the mirror store, ingest, reconcile, teardown, budget meter | `internal/modules/overlay/{provider,mirrorstore,backfill,reconcile,teardown,budgetmeter,freshness}.go` |
| Visibility deny-join + owner projection | `internal/modules/overlay/visibility.go` |
| The HubSpot adapter (the first `incumbent.Incumbent` implementation) | `internal/modules/overlay/hubspot/` |
| Connection lifecycle + RBAC policy | `internal/modules/overlay/connection.go`, `internal/modules/identity/internal/policy/policy.go` |
| Compose wiring (dispatcher, meter, handlers) | `internal/compose/overlay.go`, `internal/compose/server.go` |
| Migrations (fork-owned) | `backend/migrations/custom/20260716120000_overlay.up.sql` and kin |
