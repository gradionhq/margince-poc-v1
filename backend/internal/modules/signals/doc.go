// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package signals is the warm-room signal spine (B-E08.1–.4, features/07
// §9, data-model §12.5): the company-level, consent-gated `signal`
// substrate, the inspectable signal→organization resolver, the warm/cold
// join over our OWN contact graph, and the warm-intro path proposal.
//
// The trust boundary, encoded rather than promised (P12): a
// signal is attributable at COMPANY level only — the resolver never
// creates a person row, sets resolved_person_id only under a recorded
// consent grant, and drops what it cannot attribute to an organization.
// The warm room proposes; the rep sends — every proposed outbound rides
// the 🟡 confirm-first send tool, never this module.
//
// Tables owned: signal, signal_resolution.
package signals
