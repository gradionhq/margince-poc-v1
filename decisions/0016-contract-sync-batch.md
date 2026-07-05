# 0016 — The feedback-04–15 contract sync batch: 11 new operations + follow-ons

**Date:** 2026-07-05 · **Status:** accepted

## Context

The spec resolved this repo's feedback 04–15 (spec commits `667c355`,
`76a84b4`), defining everything that had been contract-blocked:
`/automations*`, `/public/booking/{host_slug}` (+ `CaptureConsent`),
`GET /audit-log`, `GET /passports`, `issueDoubleOptIn`. This batch synced
`backend/api/crm.yaml` slice-by-slice (each slice gate-green: the compose
`ServerInterface` assertion breaks the build the moment an op enters the
contract, so sync and implementation land together), built the forced
implementations, the EP09 frontend remainder, and three pickup blocks
(coldstart ACCEPT executor, lead-score behavioral recompute, the
cold_start golden dataset). The judgement calls, one place:

## Decisions

1. **OAuth AS paths stay out of the build contract.** The spec's crm.yaml
   now carries `/oauth/*` + discovery alongside `/passports` (both
   sanctioned). The build serves OAuth mounted directly in compose (A2, since
   the overnight build) but does NOT list it in `backend/api/crm.yaml`: the
   oapi-codegen pipeline is JSON-request/response-shaped, and redirect/form
   endpoints would need drift-gate exceptions for zero functional gain.
   Revisit if the contract tooling grows non-JSON support.
2. **Tighten-only agent-access annotations added during sync.** The spec
   leaves `createAutomation`/`updateAutomation`/`deleteAutomation` and
   `bookPublicMeeting` un-annotated; gen-agentpolicy is fail-closed on
   un-annotated mutating ops. All four carry `x-agent-access: human-only`
   here (an agent configuring standing automations is self-granted
   authority; the anonymous booking page is for the booker — agents book
   via `/bookings`). Tighten-only under the ADR-0055 rule.
3. **`listPassports` returns `agent_id`/`last_used_at` as absent** — no
   storage exists, both are contract-optional, and stamping `last_used_at`
   would put a write on every agent request. Revisit if Settings needs it.
4. **The audit-log read lives in `modules/privacy`** (its first transport
   surface): reading the workspace's whole trail is compliance, gated like
   SAR — unbounded AND human (the agent gate fronts only mutating routes,
   so the human-only check binds at the store).
5. **DOI issuance**: supersede-by-expiry (a fresh token expires prior
   unredeemed ones — no extra state, `consumeDOIToken` already refuses
   expired); `deliver` is recorded on the audit row but delivery itself is
   the deployment's mail seam (the BookMeeting-invite stance); the
   plaintext exists once, in the 201 — never in audit or outbox payloads.
   Audit-only (events.md defines no `consent.doi_*` type; waived in
   writeshape with rationale).
6. **Automations**: instance table 0035 (`status` wire enum ↔ `enabled`
   boolean; soft archive — the audit vocabulary has no `delete` verb);
   catalog is in-code and closed (key == workflow handler name); the
   engine fires one run per ENABLED instance, params ride the event, the
   run-claim key is instance-scoped; the `automation` RBAC object mirrors
   `pipeline` (admin/ops configure, others read; 0035 backfills seeded
   roles). **Bootstrap seeds the two starters ENABLED** — the
   created-paused rule governs the API path; a system-seeded floor ("no
   lead sits unseen") that arrived paused would silently not exist.
   `automation_run` is NOT built (run records stay fast-follow;
   `workflow_run` remains the run/idempotency record). Audit-only (no
   `automation.*` event type).
7. **Public booking**: `booking_page` (0036) is the second sanctioned
   non-RLS `workspace_id` table — it IS the slug→tenant resolver, read
   before any GUC exists, holding slug/workspace/host/revocation only
   (ratified with rationale in both RLS fitness gates). Slug minted at
   bootstrap for the admin (no mint endpoint in the contract). The
   anonymous edge runs as a system principal `system:public_booking`
   confined to two endpoints whose responses are free/busy and
   {start,end}; throttled per-IP + per-slug by the clock-injected
   in-process limiter (`identity/internal/ratelimit` moved verbatim to
   `platform/ratelimit` so compose can share it — multi-replica moves the
   keys to Redis without changing callers). A 409 after person+consent
   stands (the subject DID submit form + consent — capture semantics).
   Idempotency claims share the system principal id; a cross-booker
   replay needs a guessed 255-char client key — accepted. **Consent
   hijack closed**: `RecordInput.NeverOverrideExisting` — the anonymous
   surface never flips a decision on record (above all a withdrawal),
   silently, because a loud refusal would be a consent-state oracle.
   Meeting provenance is `source=public_booking`, never `manual`.
8. **Coldstart ACCEPT executor**: approvals gained compose-injected
   per-kind `ApprovedEffect`s, run after the decision commits
   (approve-only); the effect redeems first (single-use redemption is the
   exactly-once claim) then writes in one governed tx under
   `agent:coldstart` on behalf of the decider. Empty columns filled,
   human-set values never overwritten; the seven non-column fields live
   in `organization_profile_field` (0037) pending a spec home
   (feedback/16). An effect failure leaves an approved-unredeemed row and
   surfaces to the decider — nothing is un-decided.
9. **Lead-score recompute**: `activity_link` gained the lead arm (0038;
   data-model omission filed as feedback/17); the workflow engine gained
   SYSTEM handlers (always-on invariant executors outside the pausable
   catalog) and people registers the §3 recompute on
   `activity.captured`/`activity.updated`. No score-override surface
   exists in the contract yet (feedback/17) — the recompute overwrites
   live leads' scores unconditionally.
10. **Evals**: only `cold_start` has a live pipeline, so only it gets a
    corpus (106 recorded-fixture cases, deterministic generator
    `tools/gen-evals`); the gate runs in the plain test lane — `make
    check` IS the hard gate until hosted CI exists. Corpora for unbuilt
    tasks land with those slices.

## Consequences

Every `crm.yaml` operation is implemented again (the 501 fallback stays
empty); EP09 is fully closed including B-EP09.15; the spec's feedback
04–15 resolution is consumed end to end. Open spec gaps: feedback/16
(coldstart profile-field home), feedback/17 (activity_link lead arm +
score override surface).
