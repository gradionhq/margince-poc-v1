// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package deals owns the deal aggregate and its pipeline scaffolding —
// creation (born open, never onto a terminal stage), keyset listing,
// optimistic updates, stage advancement with the won/lost semantics and
// FX freezing (formulas-and-rules), archive, and the per-workspace
// default-pipeline seed injected into identity's bootstrap at the
// composition root — as store + contract mapping + transport handlers +
// the deals slice of the datasource provider, flat per ADR-0054 §3.
//
// Tables owned: deal, deal_stage_history, pipeline, stage, fx_rate,
// product, offer, offer_line_item (the E03.16-.20 offer engine: rate-card
// products, versioned deal-bound offers with derived money totals).
//
// Imports shared + platform + the generated contract only; never a
// sibling module. Every write rides storekit's audit+outbox shape and
// every entry point is gated by platform/auth.
package deals
