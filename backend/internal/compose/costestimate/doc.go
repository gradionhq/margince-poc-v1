// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package costestimate is the AI cost pre-flight for the backfill preview
// (ADR-0068, phase 2/2 of the cost hand-off): it composes the ai, capture, and
// activities reads — served ai_call totals, current model-rate bindings,
// capture_backfill yields, and labeled-message counts — into a priced projection
// of what a backfill window would spend. The monetary computation itself lives
// in ai.PriceCall (the ai module owns price-on-read); this is the cross-module
// projection/composition layer that PRICES the preview by feeding those reads
// through it — the modules it reads never import one another. Cost here is
// transparency, never a gate (ADR-0020, NEVER-4): an unpriced estimate is
// suppressed, never fabricated.
package costestimate
