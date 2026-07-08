# Worklist — spec drift reconciliation (assessed 2026-07-08)

Source: the spec repo's 2026-07-05→07-07 gap-audit (founder rulings A73–A100,
Batch C/C0/D/E) landed **after** this repo's last contract sync. The handoff
ledger (`../margince/specs/HANDOFF-to-code-session-2026-07-06.md`) only covers
the Niraj/A99 items. Everything below was the delta *outside* that ledger.

**State as of 2026-07-08 PM: §0–§3 executed.** PR #23 (squash `487d625`)
landed the contract re-sync + every built-surface fix; the feedback notes
were processed into the foundation (spec commit `b59644e` — eight founder
rulings + built-surface reconciliation + three new tickets) and the local
`feedback/` notes deleted. What remains open is listed at the bottom.

## 0 — In-flight state ✅
- [x] PR #21 merged; PR #23 merged (`487d625`).
- [ ] Close PR #20 as superseded (fully contained in the merged #21; its
      diff is empty — needs a human click, the agent lacks permission).
- [ ] Post the issue #16 reply (draft: `scratchpad/issue16-reply-FINAL.md`,
      weave in the PR #21 link — outward-facing, needs Lars to post).

## 1 — Contract re-sync ✅ (PR #23)
- [x] Spec crm.yaml deltas for implemented surfaces synced, regenerated,
      recorded build deviations preserved.

## 2 — Already built, spec changed → fixed ✅ (PR #23)
- [x] Cold-start oneOf(url|text|self_description) + source_kind evidence.
- [x] Frontend locale default en-GB (A100).
- [x] Migrations 0053–0061: audit verbs (one widening), brief snooze,
      lockout+invited, lead-scoped consent (composite tenant FKs),
      partner-fit override pair, stage-history win-probability, staged
      offer lines, lead.linkedin_url, workflow_run 'blocked'.
- [x] Identity: §27 lockout, revocation events on `gw:events:crm:identity`,
      structural agent-kill proof.
- [x] People/consent: lead-scoped consent + promotion carry-through,
      bookings consent, null-clears override, linkedin dedupe probe.
- [x] Deals: partner filters, win-prob snapshot, staged totals (incl.
      revision copy), partner-fit override seam + 422 mapping.
- [x] Compose: brief snooze verb + re-surface; agents: honest run
      recording (blocked + reasons), GET runs + POST preview; voice
      text-only.
- [x] Activity lead link — was already built (mig 0038), no work.

## 3 — Conflicts → RULED 2026-07-08 (spec commit b59644e)
- [x] score_override_reason: null clears (spec semantics adopted in #23).
- [x] person_social relation ratified; spec person DDL restated.
- [x] Brief verbs: per-action act/dismiss/snooze win.
- [x] workflow_run naming + status vocabulary wins (wire enum = presentation).
- [x] Blobstore "shipped PR #62" claim corrected in four spec places.
      ⚠ DECISIONS.md A66 still carries the claim (locked file) — erratum
      pending a founder call.

## 4 — Reverse drift ✅ absorbed spec-side (b59644e)
- [x] Registry annotations for all poc-v1-shipped surfaces (offers,
      products, signals, voice, views, brief, preference center, exports,
      derivation, imap, enrich), close_date_provisional, 'resolve' verb,
      x-agent-access deviation note, §4.1 identity-stream amendment.

## Remaining open (tracked, not this worklist's execution)
- **Capture-connection reshape** (EP05 §B): per-user + vault
      `credential_ref` — structural, own PR arc.
- **audit_log.batch_id** — ships with `bulk_operation` (B-E11.21/.23);
      touches the storekit.Audit signature.
- **Automations screen rework** (B-EP09.15, A72, M→L) — frontend lane.
- **B-EP06.29** (new, founder-ruled): full create audit images so
      create-typed fields are human-owned from birth.
- **B-E11.37** [Backlog]: bounce/complaint/manual suppression store.
- **B-EP07.22** [Backlog]: undecidable approval targets
      (list/tag/relationship/partner).
- §5 net-new V1 work: scheduled via the foundation backlog (ticket book),
      not here.
