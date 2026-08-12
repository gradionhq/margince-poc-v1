// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package integrations owns the licensed-data-provider platform (ADR-0101/A152):
// the connection registry, the metered run ledger, the per-pool credit
// reservations, and the adapters that talk to the vendors.
//
// Tables owned: provider_connection, provider_connection_budget, provider_run,
// provider_run_reservation.
//
// person_provider_claim is NOT owned here. What a purchased value MEANS, and
// how it renders beside a person's own data, is the domain's judgment —
// modules/people owns that table and writes it through the WriteClaims
// callback below.
//
// # Why this module holds no domain knowledge
//
// The platform decides whether a run may happen, what it may cost and whether
// it succeeded. It never decides what the answer means. That split is what
// lets provider #2 arrive as a descriptor and an adapter rather than as a
// second copy of the budget machinery.
//
// # The seam, and why half of it lives here
//
// shared/ports/provider holds the two pinned interfaces (Adapter facing the
// vendor, RunService facing the domain). It is Tier-0 and stdlib-only, so it
// cannot name a transaction.
//
// The callbacks that must run INSIDE a caller's transaction therefore live
// here as func types, and compose supplies them from modules/people:
// WriteClaims, FenceSubject, DuplicateCluster, SubjectIdentifiers. This is the
// same shape as capture.EnqueueBackfill and privacy.EdgeInvalidator — the
// module that needs the callback declares it, and the composition layer is the
// only place that knows both sides exist.
//
// # Money is the invariant
//
// Every rule here that looks fussy is protecting a charge the customer did not
// authorize:
//
//   - The reservation is taken for the whole worst case before submitting, so
//     a cascade can never fire against a budget that has since run out.
//   - inflight_at is written before egress and cleared only by proof the call
//     did not land, so a crashed worker resolves to submission_unknown rather
//     than silently retrying a charge.
//   - The execution epoch is re-read immediately before the call and again
//     before any terminal write, so a disconnected connection cannot produce
//     stored data.
//   - The live-run index means a duplicate trigger returns the run in flight
//     instead of buying the same answer twice.
package integrations
