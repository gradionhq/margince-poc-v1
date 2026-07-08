# Worklist — spec drift reconciliation (assessed 2026-07-08)

Source: the spec repo's 2026-07-05→07-07 gap-audit (founder rulings A73–A100,
Batch C/C0/D/E contract build-out) landed **after** this repo's last contract
sync. The handoff ledger (`../margince/specs/HANDOFF-to-code-session-2026-07-06.md`)
only covers the Niraj/A99 items (already built — do not re-schedule). Everything
below is the delta *outside* that ledger, classified by what it means for this
repo. Check items off as they land; keep STATUS.md pointing here until empty.

Verified 2026-07-08: no deferred-from-V1 feature (A81/A83/A89/A94/A95/A96) was
ever built here — no throwaway work.

## 0 — Land the in-flight state first (avoid rebasing everything below over it)

- [ ] PR #21 (`feat/lars-strojny-feedback`): force-push + DCO already done, all
      checks green except SonarCloud (pending). Update-branch (it is BEHIND main
      after PR #22), wait for green, merge.
- [ ] Close PR #20 after #21 merges (its branch is fully contained in #21; the
      merge empties it).
- [ ] Post the issue #16 reply (draft: `scratchpad/issue16-reply-FINAL.md`,
      weave in the PR #21 link — outward-facing, needs Lars's approval).

## 1 — Contract re-sync (its own PR, before the fix work)

- [ ] Pull the spec's `crm.yaml` deltas for **implemented** surfaces into
      `backend/api/crm.yaml`, `make gen`, fix compile. Preserve the recorded
      build deviations (`x-agent-access` on automations + public booking,
      ADR-0055 fail-closed lint).
- [ ] Note: most Batch C/D additions sit in the spec crm.yaml as a normative
      **comment registry** (~lines 3110–3430), not parseable OpenAPI — sync the
      inline ops now; registry ops arrive with their tickets.

## 2 — Already built, spec changed → fix/update

Structural (largest first):

- [ ] **Capture connections reshape** (EP05 §B, spec 1965421): built
      `connector_connection` (`0023_capture.up.sql`) is workspace-level and
      stores `auth bytea`/`cursor bytea` in the row; spec's `capture_connection`
      is per-**user** with a vault `credential_ref` ("NEVER in this row"),
      `sync_cursor`, `watch_expires_at`. Migration + `modules/capture` +
      connect/disconnect surface.
- [ ] **Cold-start request/response rework** (E01, spec 293108a — breaking):
      `ColdStartRequest` becomes `oneOf url|text|self_description` (exactly one,
      422 otherwise); response gains required `source_kind`, nullable
      `source_url`, `evidence_offset`; 422 gains `details.populated_fields`.
      Handler: `compose/coldstart.go`.
- [ ] **Frontend locale default en-GB** (A100, spec e22debb): B-EP09.16 carries
      the realign note; `frontend/src/i18n/index.tsx` + `i18n.test.ts` +
      `frontend/src/format/format.ts` hard-code de-DE.

Migrations + store logic (additive):

- [ ] **`audit_log.action` CHECK — ONE migration** (rule 1): add `demote`,
      `import`, `import_undo` (new) + `disqualify`, `anonymize`, `send_email`
      (pre-existing spec verbs the built CHECK in `0047_signals.up.sql` lacks).
      Reverse drift: built verb `resolve` is absent from the spec CHECK → feedback/.
- [ ] **`brief_item` snooze** (A77): add `'snoozed'` state + `snoozed_until` +
      its CHECK to `0045_brief_read_model`; `compose/briefs` state machine +
      re-surface at `now() ≥ snoozed_until`. ⚠ Verb-shape conflict: build has
      `act|dismiss` endpoints, spec registry wants single
      `POST /brief/items/{id}/state` (`acted|dismissed|snoozed`) — reconcile
      with the spec session before building.
- [ ] **`app_user` lockout + invite** (EP03 §E / A97): status CHECK gains
      `'invited'`; new `failed_login_count`, `locked_until`; login path counts
      failures, refuses while locked (formulas §27, knobs RC-17), resets on
      success.
- [ ] **Lead-scoped consent** (E12.20): `person_consent`/`consent_event` —
      `person_id` nullable, add `lead_id` FK + XOR-ish CHECK + second UNIQUE;
      promotion re-points consent to the person, proof preserved
      (`modules/consent` + `modules/people` promote).
- [ ] **`partner` override pair** (A68/ADR-0053): `partner_fit_score_computed`
      + `partner_fit_override_reason` — mirror the built lead pattern
      (`0046_lead_score_override`) + recompute suppression.
- [ ] **`deal_stage_history.win_probability_at_change`** (E03.11): column +
      stamp in `modules/deals/deal_advance.go`.
- [ ] **`offer_line_item.proposal_state`** (E03.21a): `staged|accepted`
      (default accepted); staged AI-drafted lines excluded from server totals
      until human accept.
- [ ] **`lead.linkedin_url`** (E12.10/.11): normalized column + exact-match
      dedupe key. Person-side conflict — see feedback bucket (built
      `person_social` relation vs spec's jsonb + promoted column).
- [ ] **Automation runs** (A72/ADR-0035 Am.1): add `blocked` status + failure
      `detail` to the run record; firing path records `failed/blocked/skipped`,
      not only successes (B-E15.3a); new `GET /automations/{id}/runs` +
      `POST /automations/{id}/preview` (dry-run blast radius). ⚠ Naming:
      built `workflow_run` (`0029`) vs spec `automation_run` vocabulary
      (`fired|skipped|queued_for_approval|failed|blocked`) — needs a decision.
- [ ] **`audit_log.batch_id`** (B-E11.23): FK to `bulk_operation` — sequence
      AFTER `bulk_operation` exists (bucket 4); touches the `storekit.Audit`
      signature, i.e. every module store call shape.

Behavioral gaps on built flows:

- [ ] **Identity revocation events** (events §5.6a, B-EP03.10):
      `user.deactivated`, `role.changed`, `passport.revoked` — flows exist,
      events not emitted; catalog has no identity-family stream yet (check
      events.md §4.1 for routing); agents-side consumer kills in-flight
      sessions within one bus cycle.
- [ ] **`POST /bookings` consent** field (`CaptureConsent`) wired into the
      booking write (`modules/activities/scheduling.go`).
- [ ] **Activity links gain `lead` target** — DB CHECK + handler enums
      (`relink`, `ActivityLink`, `CreateActivityRequest`) + lead-scoring reads.
- [ ] **`GET /deals` filters** `partner_org_id` + `partner_sourced`
      (`appendDealFilters`).
- [ ] **`Lead`/`CreateLeadRequest`**: `linkedin_url` + nullable `full_name` on
      create.
- [ ] **Voice corpus text-only** (B-E07.5a RE-OPENED, ADR-0058): drop
      `.docx`/`.pdf` accepted formats and DELETE the byte-size word-count
      estimate in `modules/ai` voicesources.go — real counts only.
- [ ] **Automations screen re-opened** (B-EP09.15, M→L): primary-nav promotion,
      single-automation recipe editor (When→If→Then), live dry-run preview,
      honest run history incl. errored/blocked/skipped. Frontend-lane work
      (`frontend/src/screens/automations.tsx` is now "partial").
- [ ] **Audit/field-history rendering under field-masks** (E11.7): if any
      user-rendered audit read surface exists, apply the viewer's mask
      projection to before/after (null-with-`_masked`); otherwise pin as a
      constraint on the future surface.

## 3 — Conflicts needing a decision (route: decisions/ or spec session)

- [ ] `UpdateLeadRequest.score_override_reason` clearing: spec = `null` clears
      (nullable, minLength 1); build = empty string clears, null rejected.
      Opposite wire gestures — founder/spec call.
- [ ] `person_social` child table (built, Strojny 0051) vs spec's jsonb +
      promoted `person.linkedin_url` — spec text predates the built shape.
- [ ] Brief item verb shape (see snooze item above).
- [ ] `workflow_run` vs `automation_run` naming + status vocabulary.
- [ ] Blobstore: spec claims B-E04.1 "shipped PR #62"; no `ports/blobstore`
      exists in this tree — one side is wrong, verify against PR #62.

## 4 — Reverse drift → file as feedback/ notes (spec absorbs, no build work)

Build-only surfaces the spec never absorbed (~49 ops): offers cluster (14) +
products (5), signals (8), voice profiles (8), saved views (5 — built at
`/views`, spec registry says `/saved-views`), brief verbs (4), preference
center (3, RFC 8058 — absent even as a comment), exports, report derivation,
IMAP connect, company enrich; schemas `close_date_provisional` +
INV-CLOSE-PAST, `ReportResult.derivation_url`; audit verb `resolve`; the
`x-agent-access` deviations (upstream candidates, recorded in-file).

## 5 — Net-new V1 work (sizing reference, schedule via the backlog)

~60+ registry operations + new tables. Big blocks: E08 deal-room public API;
EP03 identity §E (MFA, sessions, IdP, reset/verify/invite, `auth_token`,
`mfa_credential`, `idp_config`); **EP09.23 notification engine** (~L, the
previously-unowned features/05 surface — B-E16 reminders now deliver through
it); E04 AI-moment endpoints (`deal_inference`, dossier/KPI/qualification,
`POST /ai-feedback`); E14 Dispact interop (`integration_connection`,
`conversation-links`, new sentinel `integration_not_configured` — the
sanctioned apperrors §0 extension); A93 importer (`import_run`,
`import_mapping`, undo); A91 users/teams CRUD + `scope=mine|team|all` +
360 `include=` embeds; partner lifecycle endpoints; OAuth2 hosted auth server
(A2, decisions/0012 records the lag); `bulk_operation`; `lead_manual_signal` +
score history (Explain-This-Score); `workspace_email_domain`; FX populator
(EP02.18a); prebuilt report keys `quota-attainment`/`coverage-gaps`;
`lead.sla_breached` + `lead.demoted` events + `POST /leads/{id}/demote`.

Net-new tickets: 9 (after the 3 EP01 leaves the handoff closes); ~25 still-V1
tickets gained expanded acceptance criteria (mostly contract surfaces, not new
scope).
