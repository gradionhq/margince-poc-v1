// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package people owns the person, organization and lead aggregates —
// creation, dedupe, keyset listing, optimistic updates, archive, the
// two-record merge (features/01 §1.3) and lead promotion (§6.4) — as
// store + contract mapping + transport handlers + the people slice of
// the datasource provider, flat per ADR-0054 §3.
//
// Tables owned: person, person_email, person_phone, person_consent,
// organization, organization_domain, relationship, partner, lead,
// lead_score_history, lead_manual_signal, lead_source,
// lead_disqualify_reason, person_signature_enrich_state,
// person_provider_claim.
// lead_source and lead_disqualify_reason are the two administered lead
// vocabularies: the pick lists behind "where did this come from" and "why
// was it dropped", and the weighting the scorer reads.
// lead_score_history is the retained series behind "Explain This Score"
// (ADR-0105): the breakdown is written with the score and read back
// verbatim, because a decomposition recomputed at read time explains a
// number the record no longer carries. lead_manual_signal is what a rep
// knows and capture cannot fetch.
// person_provider_claim is what a licensed data provider asserted about
// one of our people (ADR-0101): the domain owns the VALUES because it
// decides what a claim means and how it renders, while integrations owns
// the run that bought it.
// Merge and promotion additionally relink rows in deal, activity_link,
// list_member, taggable, consent_event and provider_run inside their
// single transaction — the ratified cross-aggregate ownership call of the
// primary aggregate; nothing else in this module WRITES a sibling table.
// The provider_run relink is the merge's alone: it decides which of two
// colliding LIVE runs keeps the one-live-run index, which is knowable
// only where both records' run states are visible at once.
// The person list READS two of collections' — tag and taggable — for the
// contract's `tag` filter: a tagged person is a link row whichever module
// writes it, and the alternative is a declared filter answered by nobody.
//
// Imports shared + platform + the generated contract only; never a
// sibling module. Every write rides storekit's audit+outbox shape and
// every entry point is gated by platform/auth.
package people
