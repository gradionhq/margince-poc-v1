# ADR-0011 — Consent is recorded per purpose, with proof, and data has a retention deadline

**Status:** Active
**Decided:** 2026-06-10

## The decision

Consent is stored for each person and each purpose separately, not as one flag
on the person. Every grant and every withdrawal writes an append-only proof row
that keeps the exact policy wording shown, the policy version, the source, and
the lawful basis. An outbound action for a marketing purpose is refused unless
an active granted record exists for that purpose; unknown counts as no consent.
Retention policies age data on a schedule with an action ladder — archive, then
anonymize, then erase — and a legal hold suspends every destructive action on
the records it covers.

## Why

The controller must be able to demonstrate consent, and a bare state flag with
a timestamp cannot do that. Consent is also purpose-specific: agreeing to a
newsletter is not agreeing to profiling, and one flag cannot tell those apart.
Without a retention engine, data accrues forever and the storage-limitation
duty is never met.

## What it binds in this repository

- `backend/migrations/core/0010_consent_retention.up.sql` creates
  `consent_purpose`, `person_consent`, `consent_event` and `retention_policy`,
  and adds the `legal_hold` column to `person`, `organization`, `deal` and
  `lead`.
- `backend/migrations/core/0031_retention.up.sql` creates
  `erasure_suppression`, which is what keeps an erased subject from being re-
  created by a later capture.
- `backend/migrations/core/0024_consent_verbs.up.sql` and
  `0034_consent_doi.up.sql` carry the audit verbs and the double-opt-in
  machinery.
- `backend/internal/modules/consent/` owns the enforcement: `gate.go` and
  `guard.go` refuse a send without a grant, `doi.go` handles double opt-in,
  `state.go` and `store.go` hold the per-purpose state.
- `backend/internal/modules/privacy/retention.go`, `retentionpolicystore.go`
  and `retentionactions.go` run the scheduled retention pass.
- Enforcement is proven by
  `TestConsentGateRefusesAChannelRecipientWithoutAGrant` and
  `TestConsentGateRefusesAChannelRecipientAfterWithdrawal` in
  `backend/internal/modules/consent/channelconsent_integration_test.go`.

## History

Adopted from the retired specification, decided 2026-06-10. Rewritten in plain
language 2026-08-19. Amended in part 2026-08-10 by ADR-0098: default-deny now
applies to marketing purposes only, while one-to-one business correspondence
and transactional mail rest on contract and legitimate interest with the
qualifying event recorded. The proof ledger and the retention engine are
unchanged by that amendment.
