# 0013 — Overnight extension slices: scheduling, cold-start, GDPR arm, jurisdiction floor

Date: 2026-07-05 (overnight session). Build decisions for the slices past the
original five blocks; spec defects found on the way are in `feedback/09`.

## Meeting scheduling (S12a)

- **`activity.host_user_id`** (migration 0030, meeting-only by CHECK, composite
  workspace FK): the contract's scheduling host has no home in data-model.md
  (`assignee_id` is task-only) — filed as feedback/09; the column is the
  additive build-side reading.
- Free/busy derives from the CRM's own meeting record (no calendar connector in
  V1), assumes 1h meetings (no end column — same feedback), proposes
  business-hour slots (09–17 UTC, Mon–Fri).
- **Row-scope posture:** the availability busy-read rides `ActivityScopeClause`
  (free/busy must not out-see the timeline); booking another host's calendar
  needs an unbounded row scope until the spec names a calendar-delegation
  grant (feedback/09).

## Cold-start read-back (A2 close-out)

- Lives in `compose` (`coldstart.go`): it joins the fetcher seam, the routed
  model path (`TaskColdStart` lane) and approvals staging — three modules that
  never import each other.
- **The staged approval row IS the proposal** (ADR-0036 posture): proposal_id =
  approval id; kind `coldstart` (and transport-gate `enrich`) map to
  `organization.update` for decidability.
- The no-guess gate is enforced server-side (verbatim-substring evidence check
  against the fetched text); zero surviving fields → 422 `coldstart_unreadable`.
- The egress fetcher is SSRF-guarded at dial time (public addresses only,
  reserved ranges blocklisted, 5-hop redirect cap, 1 MiB body cap).
- `compose.New` grew functional options; the api role must DECLARE a model
  path (`--ai-routing`/`--ai-fake`) or the operation stays an explicit 501.

## GDPR wider arm (G2+G3)

- **Retention evaluator** in compose (`retention.go`), worker-ticked
  (`--retention-interval`, default 24h): closed selector map per
  (object_type, category) — unknown scopes skip LOUDLY; one audited tx per
  record; `legal_hold` never auto-acted (activities transitively via linked
  records). Audit action vocabulary is closed (0012), so retention names
  itself in evidence (`retention_action`).
- **Erasure** (`compose.Eraser`) is the one Art. 17 path shared by the DSR
  surface and retention's `erase`: anonymize-in-place (deleting the row would
  cascade into shared business records), purge raw capture by LIKE-escaped
  identifier match, drop the subject's AND their linked activities'
  embeddings, hash identifiers onto `erasure_suppression` (0031), PII-free
  tombstone (`action=erase`). Fulfilling an erasure DSR EXECUTES the erasure.
- **SAR** (`compose.AssembleSAR`): person.delete grant AND unbounded row scope
  (the export deliberately crosses row scope), audited `action=export`. No
  HTTP endpoint by design (admin-mediated V1).
- Suppression hashing + LIKE escaping live ONCE in `storekit`
  (`SuppressionHash`/`EscapeLike`); capture's lead upsert consults the list.

## Capture dedupe (S9)

A captured lead whose trimmed email collides with a live lead from another
source stages a 🟡 `merge_records` proposal (approvals injected as capture's
`MergeStager` seam) and answers the existing ref — never a second row, never
an auto-merge. Same-natural-key replays stay the idempotent path.

## Formulas (F1)

`deals.IsStalled` (§8) stamps every deal read and backs the `stalled` list
filter — the Go predicate and the SQL clause are two deliberate spellings held
together by the fixed-clock table test. `people.ScoreLead` (§3) reproduces the
spec's worked example (51) and seeds a new lead's score with the fit component;
behavioral recompute waits for the engagement-event substrate.

## Seat budget (A1) + structured output (EP06.25)

`compose.NewSeatBudget`: full seats × 6M × 2, counted under the target
workspace's GUC, flooring at one seat. `ai.CompleteStructured` runs the §5.2
policy (retry-with-feedback → escalate a rung → honest error), every attempt
metered; the cold-start extraction rides it for schema validity only (a retry
cannot conjure evidence).

## DE jurisdiction pack (J1)

`internal/modules/de` registers GoBD classes (6/8/10y) on the Tier-0 seam;
compiled into api+worker by blank import. The retention engine treats the
strictest correspondence class as a FLOOR: destructive actions skip email
activities younger than it; archive is exempt because archiving retains.
