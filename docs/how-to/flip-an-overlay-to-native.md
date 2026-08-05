# Flip an overlay to native

Cut the installation over from `overlay` mode — where a HubSpot portal is the system of record and
Margince serves a mirror — to `native`, where the imported records are first-party and the incumbent
is retired. For the mental model (the cutover's place in the sequence overlay live → parallel run →
flip → verify → retire, and why the seal exists), read the explanation page's [cutover lifecycle
section](../explanation/overlay-augmentation.md#the-overlaynative-cutover-lifecycle-adr-0071--ova-ac-6)
first. This page is the recipe that section is not.

> **This is one-way, estate-wide, and there is no rollback.** `POST /overlay/flip` imports the whole
> frozen mirror into the native tables and sets `workspace.x_sor_mode = 'native'` in one audited
> transaction; a second call answers `409` (`the workspace is not in overlay mode, nothing to flip`).
> No native→overlay path exists. Recovery means *rebuilding* from the pre-flip export bundle — see
> [7. If it goes wrong](#7-if-it-goes-wrong) — so do not start step 2 until step 1's bundle is on
> disk.

Connecting an overlay in the first place is [connect-a-hubspot-overlay.md](connect-a-hubspot-overlay.md);
exercising the whole lifecycle against an isolated HubSpot test account (the safe place to rehearse
this page) is [test-overlay-locally.md](test-overlay-locally.md).

> **Single-organization installation.** One installation serves one organization and the server
> resolves it itself, so the `curl`s below carry only the session cookie — no tenant header.

## 1. Who may run it

Two gates, both on `preflightOverlayFlip` **and** `executeOverlayFlip`, and both checked inside the
runner rather than only at the route (`internal/compose/flip.go`):

- **`overlay_connection` UPDATE.** That grant is admin/ops-only — every other role holds read on the
  object (a rep may see *whether* overlay mode is live) and gets `403` here. The flip also writes
  through `import_run`, which is admin/ops-only on every verb, read included.
- **Human-only.** `auth.RequireHuman` refuses an agent (Passport) principal outright, whatever its
  scopes and whatever the granting human's RBAC. The contract says the same thing twice more:
  `security: [cookieAuth]` and `x-agent-access: human-only`, which the generated agent gate enforces
  per route. `GET /overlay/export` carries the identical pair.

Neither flip operation carries an `x-mcp-tool` block, so it is not in the governed tool surface at
all: an agent cannot call it, and it cannot be *staged* as a 🟡 confirm-first approval either. That
is deliberate. The typed confirmation phrase in step 3 **is** the human-intent control; routing it
through the approvals queue would let an agent supply the phrase in the staged arguments and reduce a
one-way, estate-wide cutover to one click on an approval card — the same click that approves a single
record edit. A cutover that cannot be undone must be typed by the person who owns the consequence.

There is no SPA screen for the flip today (Settings → Integrations covers connect/disconnect, sync
status, budget and the user map). This is a terminal operation, run against `/v1` with an admin or
ops session cookie.

## 2. Before you flip

Three preconditions, all re-checked by both the preflight and the execute (`flipVerdict`, one
function, so the two paths cannot drift).

**Converge the sync.** Drive a sweep and watch every object class reach `backfillComplete: true` and
`state: "fresh"`; readiness is *a sweep has succeeded* **and** *no mirror row is stale* **and** *every
mirrored class's backfill genuinely converged* (not truncated by a backfill cap):

```sh
curl -sS -X POST http://localhost:8080/v1/overlay/reconcile -b cookies.txt
curl -sS http://localhost:8080/v1/overlay/sync-status -b cookies.txt | jq '.objects'
```

**Drain pending writes.** Any mirror row still in `pending_sync` — a local write-back not yet
confirmed against the incumbent — blocks the flip. The same `sync-status` read shows it.

**Download the pre-flip export, and keep it.**

```sh
curl -sS -o margince-preflip.zip http://localhost:8080/v1/overlay/export -b cookies.txt
```

This is the `margince-export/1` bundle: a CSV per object plus `data.json`, a files manifest, and a
`manifest.json` whose `canonical_data_resides_in` names the incumbent. In overlay mode it carries the
mirror snapshot under **the caller's** mirror-visibility deny-join — so run it as an operator whose
map sees the estate, or the bundle you keep is narrower than the estate you flip.

How the recency gate works, exactly:

- The **cutoff** is the later of the mirror's freshest row (`max(overlay_mirror.last_synced_at)`) and
  the last successful sweep (`overlay_sync_state.last_success_at`).
- The gate then asks whether an `audit_log` row with `entity_type='workspace'` and `action='export'`
  exists at or after that cutoff. That audit row is written by the bundle writer **only once the ZIP
  is complete**, so an aborted download cannot satisfy it.
- A **zero cutoff** — no mirrored row has a watermark *and* no sweep has ever succeeded — proves
  nothing, and the gate says so: it returns `export_missing` rather than accepting any export ever
  written, including one taken before the incumbent was connected. A zero cutoff is not "everything is
  fine", it is "there is no instant an export could be newer than".

## 3. Preflight

```sh
curl -sS -X POST http://localhost:8080/v1/overlay/flip:preflight -b cookies.txt | jq
```

`200`, with `{ready, blocking[], unresolved_conflicts[]}` always present, plus `snapshot` and `parity`
when green and `emergency` when the incumbent is unreachable:

```json
{
  "ready": true,
  "blocking": [],
  "unresolved_conflicts": [],
  "snapshot": { "id": "snap-2026-08-05T09:14:22Z-0198…", "frozen_at": "2026-08-05T09:14:22Z" },
  "parity": [
    { "object": "organization", "mirror_count": 412, "will_create": 412, "will_update": 0 },
    { "object": "person", "mirror_count": 3180, "will_create": 3176, "will_update": 0,
      "skipped": [ { "external_id": "701", "reason": "duplicate_email" } ] }
  ]
}
```

`parity` is the migration engine's zero-write dry-run over the sealed snapshot, in import order
(organization → person → lead → deal → activity). It writes no CRM row; every row it cannot carry is
listed with a reason rather than dropped.

### Blocking reasons

| Reason | What it means | How to clear it |
| --- | --- | --- |
| `incumbent_unreachable` | `incumbent_connection.status` is not `active` (`revoked` or `error`) — no live read can pass, so force-fresh cannot be proven | Restore the connection (reconnect with a valid token). If the portal is genuinely gone, the emergency cutover in step 5 is the path — this is the only reason that offers it |
| `force_fresh_incomplete` | No sweep has succeeded yet, or a mirror row is `stale`, or a mirrored class's backfill has not genuinely converged | `POST /overlay/reconcile`, then poll `sync-status` until every class is `backfillComplete: true` / `fresh` |
| `pending_sync_draining` | Mirror rows still carry un-drained local write-backs | Let the sweep confirm them against the incumbent, then re-run the preflight |
| `export_missing` | No completed export bundle post-dates the cutoff (see step 2) | `GET /overlay/export` and let the download finish |

`unresolved_conflicts` is in the contract's blocking enum but **nothing produces it in this build**:
branch-1 reconciliation settles incumbent-wins at ingest and persists no conflict queue, so the array
is always empty and the reason is never emitted. Do not write tooling that waits for it.

### A green preflight SEALS the mirror

This is the part that surprises people. On a fully green verdict *whose parity dry-run also
succeeded*, the preflight writes `flip_snapshot_id` + `mirror_frozen_at` onto `overlay_sync_state` and
leaves them there. While the seal holds:

- every **fenced mirror write** refuses — sweep ingest, the webhook re-fetch, write-back's pending
  mark, the manual user-map writes, and on-demand `POST /overlay/reconcile`. Write-back and the
  user-map writes answer `409`: *the mirror is frozen for the overlay→native flip; it is released when
  the flip completes, or when a preflight finds the workspace not ready*. `POST /overlay/reconcile` is
  the exception: its handler folds the fence's disconnect signal but not the freeze signal, so a sweep
  request against a frozen mirror surfaces as a `500 internal` rather than that `409`. Treat a `500`
  there as "the mirror is sealed", and expect this to become the same `409` once the fold is shared;
- the background sweep **skips the workspace entirely**, so staleness grows on purpose —
  `sync-status` marks it with `frozenForFlip: true` so a paused mirror is not mistaken for an idle one;
- **reads stay open.** The workspace keeps working off its mirror.

The seal is all-or-nothing, and it unseals on every other exit: any blocking reason, a failed parity
dry-run, an error mid-preflight, or a request the caller abandons mid-flight (the unseal deliberately
runs on a cancellation-free context, so hanging up cannot latch the freeze). A refused execute on the
`fresh_sync` path unseals too. **A completed green preflight, however, keeps its seal on purpose** —
it names the one frozen state the flip will import — and there is no "cancel" endpoint. Once you are
green, the ways out are: run the flip (step 4); run it and be refused because a gate has since gone
red, which unseals; or disconnect, which purges the mirror outright. Do not run a green preflight
until you intend to flip.

Both operations take a workspace advisory claim for their duration, so a second concurrent preflight
or flip answers `409` (`another flip is already running for this workspace`) rather than queueing.

## 4. The flip

```sh
curl -sS -X POST http://localhost:8080/v1/overlay/flip -b cookies.txt \
  -H 'content-type: application/json' \
  -d '{"confirmation_phrase":"FLIP TO SOR","mode":"fresh_sync"}'
```

The phrase is compared for **exact equality** with `FLIP TO SOR` — uppercase, one space between each
word, no trailing punctuation. Anything else (including an absent body, which decodes to the zero
request) answers `422` with field `confirmation_phrase`, code `confirmation_phrase_mismatch`. An
unknown `mode` answers `422` / `invalid_mode`. `mode` may be omitted; it defaults to `fresh_sync`.

The response is **`202`** — but the import is *synchronous behind it*: the request returns when the
migration run has finished, not when it has been queued. On a large estate this is a long request.
Do not kill the client (see step 8 if you do).

```json
{ "run_id": "0198c3…", "mode": "fresh_sync", "records_imported": 3982 }
```

`records_imported` counts creates plus updates only — skipped and unchanged rows are excluded, and on
a *resumed* run it counts this attempt's leg alone (the merged total lives in the run record, step 8).

What runs behind the 202: every gate re-validated (`409 overlay_flip_blocked`, naming each
unsatisfied reason, and unsealing so a refusal leaves a healthy overlay) → the freeze re-asserted
idempotently → a checkpointed `import_run` record → the frozen estate imported through the owning
module stores, so every record lands with its ordinary audit + outbox write shape → association edges
detangled into FKs and typed relationship rows → `x_sor_mode = 'native'`, `x_incumbent = NULL` in one
audited transaction. The `incumbent_connection` row deliberately survives, still `active` and no
longer authoritative; retiring it is step 6.

The workspace needs a default pipeline with an open stage before you flip — deals are born on an open
stage and then advanced — or the run fails with *the workspace has no default pipeline with an open
stage; seed the workspace before flipping*.

### Disclosed skips

Every row the importer cannot carry is named in the run report with one of these reasons. There are
no others.

| Reason | Which rows | Why |
| --- | --- | --- |
| `empty_payload` | any class | The mirror row carries no fields at all — a payload-less system entry. Creating it would land a nameless native row |
| `duplicate_email` | person | The contact's email already belongs to a native person. That is a merge candidate, and the flip never auto-merges |
| `natural_key_already_taken` | lead, activity | The store replayed an existing row under the flip's namespaced `(source_system, source_id)` key instead of creating one. It is not adopted into the identity map — a one-shot disclosure beats silent convergence |

Association edges carry their own two reasons: `endpoint_not_imported` (one of the edge's endpoints
was skipped, so there is nothing to link) and `unmodelled_edge_shape` (the native model has no target
for that shape — e.g. an activity→lead edge, which `activity_link` cannot carry).

Separately, the report carries **disclosures** — lossy-but-applied decisions, not skips: a record
whose incumbent owner has no `mirror_user_map` entry (or which names no owner at all) is imported
under the **flip operator** rather than left ownerless, because an ownerless native row is
workspace-shared at every tier while the mirror row it came from was hidden from every seat; and a
deal whose incumbent stage identity does not resolve lands on the default pipeline's first open stage.
Read them: they are the difference between "3,982 records imported" and knowing who can now see what.

## 5. Emergency cutover

`mode: "emergency"` is the last-known-mirror path (ADR-0071). It exists for one situation: **the
incumbent is gone** and you need your estate anyway.

```sh
curl -sS -X POST http://localhost:8080/v1/overlay/flip -b cookies.txt \
  -H 'content-type: application/json' \
  -d '{"confirmation_phrase":"FLIP TO SOR","mode":"emergency"}'
```

| Situation | Answer |
| --- | --- |
| Connection `revoked`/`error`, mirror non-empty, pre-flip export present | Permitted. Cuts over from the frozen mirror |
| Connection `active` | **Refused** `409` — *the incumbent is reachable — run the fresh-sync flip; the emergency cutover is only for a lost incumbent*. Never a silent substitute, in either direction |
| Mirror empty | **Refused** `409` — *no mirror snapshot exists to cut over from* |
| No pre-flip export past the cutoff | **Refused** `409 overlay_flip_blocked` naming `export_missing`. Reversibility-as-reconstruction has to stay real even on the lossy path, and a frozen mirror can still be exported |

Note what it does *not* require: force-fresh convergence and drained `pending_sync` writes are waived —
they are unprovable against an unreachable incumbent. That waiver is exactly the loss, and the 202
discloses it rather than implying it:

```json
{
  "run_id": "0198c3…", "mode": "emergency", "records_imported": 3510,
  "emergency_disclosure": {
    "last_synced_at": "2026-08-04T22:10:03Z",
    "staleness_seconds": 39619,
    "unverifiable_parity_notice": "Cut over from the last-known mirror snapshot: record parity cannot be re-verified against the incumbent, which is unreachable. Data changed in the incumbent after the last sync is not included."
  }
}
```

If the mirror never synced at all, `last_synced_at` is `null` and `staleness_seconds` is absent —
never a fabricated zero. The preflight shows the same block ahead of time whenever
`incumbent_unreachable` is in `blocking`, with `available` true only when there are mirror rows to cut
over from.

## 6. After the flip: verify native, then retire the connection

**Verify.** `GET /v1/me` reports `system_of_record.mode` — it must read `native`. The overlay
lifecycle endpoints now answer `404 mode_not_overlay` (`/overlay/sync-status`, `/overlay/reconcile`,
both flip operations), which is itself confirmation. `GET /overlay/connection` still answers `200`,
with the connection `active` but no longer authoritative.

```sh
curl -sS http://localhost:8080/v1/me -b cookies.txt | jq '.system_of_record.mode'   # "native"
curl -sS 'http://localhost:8080/v1/people?limit=5' -b cookies.txt | jq '.data[].full_name'
```

Spot-check ownership and the timeline before retiring anything — a record imported under the flip
operator (the disclosures in step 4) is visible to a different set of seats than its mirror row was.

**Retire.**

```sh
curl -sS -X DELETE http://localhost:8080/v1/overlay/connection -b cookies.txt   # 202
```

One transaction: the connection flips to `revoked` and every incumbent-derived table is tombstoned
and purged — `overlay_mirror`, `overlay_association`, `mirror_visibility`, `mirror_user_map`, the
auto-map blocks, the backfill cursors, the reconcile watermarks, `overlay_sync_state` (which is also
where the flip seal lived) and the write ledger. The sealed credential is deleted from the vault after
the commit.

What it **keeps**: every record the flip imported (those are native rows now, not mirror rows), the
audit spine — including the connection's own lifecycle rows (`entity_type = incumbent_connection`) and
the freeze/unfreeze records — the `import_run` row with its report, and the pre-flip bundle on your
disk. Disconnecting is a governed action, not an erasure of its own record.

**A disconnect while an import is actually running is refused** `409` (*a flip is in progress for this
workspace; let it finish (or fail) before disconnecting*). Liveness is two conditions together: the
flip's advisory lock held by a live session **and** a `mirror`-connector run recorded as `running` —
so a merely sealed-but-idle workspace, or a run left at `running` by a cancelled request, does not
block you. Disconnect must stay the escape hatch that revokes the credential and purges mirrored PII;
it must never become a latch.

## 7. If it goes wrong

Be plain about this: **the flip does not roll back.** `CompleteFlip` is exactly-once, there is no
native→overlay operation anywhere in the contract, and the mirror is purged the moment you disconnect.
Reversibility means **reconstruction**: the pre-flip bundle's mirror snapshot is re-imported into a
clean instance through the same migration engine and the same native writers the flip used, with zero
incumbent calls. It rebuilds an estate; it does not restore the incumbent as system of record.

Two things to know before you rely on it:

- **Only a pre-flip overlay bundle rebuilds anything.** The parser refuses a bundle whose
  `manifest.json` carries no `canonical_data_resides_in` — that field is written only in overlay mode,
  so an export taken *after* the flip has no mirror members and cannot reconstruct. It also refuses a
  bundle whose `format` is not `margince-export/1`. The bundle's own `mirror_user_map` is what maps
  owners back to users, filtered to the app_users that exist in the target; a record whose owner did
  not travel is imported ownerless with a disclosure rather than failing the rebuild.
- **There is no HTTP surface for it today.** `reconstructFromBundle` is package-internal in
  `internal/compose/flipbundle.go`, reachable only through `compose.ReconstructForTest`, which the
  OVA-AC-6(d) lane exercises. The `/import/*` wire that would expose it is an unminted contract
  extension. So reconstruction today is an operator-run path against the code, not a self-serve
  endpoint — plan for that before you cut over, not after.

## 8. Resuming a crashed flip

The import is checkpointed, so a killed request is resumable rather than lost. The run record
(`import_run`) carries `connector = 'mirror'`, `source_ref` = the sealed snapshot id, `source =
'overlay:flip:<mode>'`, and a `checkpoint` that advances after **every** landed row.

**To resume, issue the identical `POST /overlay/flip` again.** The runner finds the latest `mirror`
run for the *same sealed snapshot* and either resumes it (status `failed`) or re-enters it (status
`running`, left behind by a crashed request) — never from zero, never past the checkpoint. A run for a
different snapshot is not touched; a fresh one is created instead. The mirror stays frozen between
attempts on purpose: the estate must not drift under a positional cursor.

Two mechanics that make the retry safe:

- **Identity repair runs first, unconditionally.** A record lands in two steps that are not one
  transaction (the store creates the native row, then the engine maps the external id to it), so a
  crash between them leaves a record the resume cannot recognize. Before the loop can duplicate
  anything, the repair adopts **live** native rows whose provenance carries the reserved import prefix
  — a namespace no client-facing create can write — and binds them into the identity map. Archived or
  merged-away rows are deliberately left unadopted.
- **A closed deal is the one class the repair must finish, not merely recognize.** Deals are born open
  and then advanced; an adopted deal whose close never ran is re-advanced on this attempt (idempotent —
  an already-terminal deal needs nothing) rather than reported as converged while parked open.

**Reading the merged report.** There is no REST surface for `import_run`, so read it from the
database (`make psql`). Each attempt reports only its own leg, and the store folds it into what the
run already recorded, so the stored report is the whole cutover across every attempt:

```sql
SELECT status, checkpoint, error,
       report->'imported'    AS imported,
       jsonb_pretty(report->'objects')              AS objects,
       jsonb_pretty(report->'associations_skipped') AS edges_skipped
FROM import_run
WHERE connector = 'mirror'
ORDER BY created_at DESC LIMIT 1;
```

Per object you get `created`, `updated`, `unchanged` (rows a replayed page found already landed),
`skipped[]` with the reasons from step 4, and `disclosures[]`. Counts add across attempts and cannot
double-count, because the checkpoint guarantees no row is walked twice; `mirror_count` is the source's
size, not a tally.

## Where the code lives

| | |
|---|---|
| Flip orchestration: the readiness verdict, execute, modes, emergency disclosure | `backend/internal/compose/{flip,flipverdict}.go` |
| The typed phrase, the mutual-exclusion claim, the disconnect liveness probe | `backend/internal/compose/{flip,flipclaim}.go` |
| Frozen-mirror source, native writers, owners, stages, deals, associations, crash repair | `backend/internal/compose/{flipsource,flipwriters,flipowners,flipstages,flipdeals,flipassoc,flipreconcile}.go` |
| Preflight primitives: readiness checks, seal/unseal, `CompleteFlip`, estate reads | `backend/internal/modules/overlay/{flipstate,flipreads}.go` |
| The freeze fence every mirror write passes | `backend/internal/modules/overlay/disconnectfence.go` |
| Teardown: what a disconnect purges and tombstones | `backend/internal/modules/overlay/teardown.go` |
| Migration engine: run records, checkpointed loop, dry-run, unreachable guard | `backend/internal/modules/migration/{engine,run,guard}.go` |
| The export bundle (the pre-flip artifact) and its transport | `backend/internal/compose/{export,exportbundle,exporttransport}.go` |
| Reconstruction from a pre-flip bundle | `backend/internal/compose/flipbundle.go` |
| The contract: `/overlay/flip:preflight`, `/overlay/flip`, `/overlay/export`, `/overlay/connection` | `backend/api/crm.yaml` |
| RBAC posture for `overlay_connection` and `import_run` | `backend/internal/modules/identity/internal/policy/policy.go` |
