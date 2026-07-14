// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package people owns the person, organization and lead aggregates —
// creation, dedupe, keyset listing, optimistic updates, archive, the
// two-record merge (features/01 §1.3) and lead promotion (§6.4) — as
// store + contract mapping + transport handlers + the people slice of
// the datasource provider, flat per ADR-0054 §3.
//
// Tables owned: person, person_email, person_phone, person_consent,
// organization, organization_domain, relationship, partner, lead.
// Merge and promotion additionally relink rows in deal, activity_link,
// list_member, taggable and consent_event inside their single
// transaction — the ratified cross-aggregate ownership call of the
// primary aggregate; nothing else in this module touches sibling tables.
//
// Imports shared + platform + the generated contract only; never a
// sibling module. Every write rides storekit's audit+outbox shape and
// every entry point is gated by platform/auth.
package people
