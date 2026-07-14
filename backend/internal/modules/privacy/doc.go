// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package privacy owns the GDPR engines (ADR-0011): right-to-erasure
// (Art. 17), subject-access assembly (Art. 15) and the nightly
// retention evaluator. The DSR case queue (data_subject_request rows +
// their HTTP surface) stays in consent; this module is the machinery a
// fulfilled request executes — the composition root injects the Eraser
// into consent's DSR handlers, so the two never become an import edge.
//
// Tables owned: erasure_suppression. Everything else this module
// touches it deliberately does NOT own: erasure and retention write
// person, lead, activity, deal, embedding and raw_capture rows because
// a data-subject obligation must reach every store that holds the
// subject, in ONE transaction per record (the sanctioned
// single-transaction exception to the module-boundary rule) — routing
// each purge through its owning
// module's API would trade the atomicity that IS the guarantee for
// boundary hygiene. Those writes are ratified per table in the
// table-ownership fitness test (backend/tableownership_test.go).
//
// Imports shared + platform only; never a sibling module. Retention
// floors come from the compiled-in jurisdiction packs via
// shared/ports/jurisdiction.
package privacy
