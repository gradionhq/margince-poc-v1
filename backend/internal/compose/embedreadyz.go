// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"log/slog"
)

// embedStateUnknown is readyzEmbedState's answer whenever there is no
// embed lane to report on (engine nil or unbound) or the marker read
// itself failed — three distinct causes, one line, matching the /readyz
// doc: Readyz's body never distinguishes them.
const embedStateUnknown = "unknown"

// readyzEmbedState is the non-fatal embed-store visibility line on /readyz:
// "active" when the populated identity matches the configured one,
// "needs_reindex" when the binding changed under it, "reembedding" while a
// reindex runs, and embedStateUnknown for every no-lane/unreadable cause.
// It NEVER gates readiness — the embed store keeps serving retrieval under a
// stale or unreadable binding — so a marker-read failure only downgrades
// this one line.
func (s Server) readyzEmbedState() func(context.Context) string {
	engine := s.embedReindexHandlers.engine
	return func(ctx context.Context) string {
		if engine == nil {
			return embedStateUnknown
		}
		if engine.currentIdentity() == "" {
			// Unbound embed lane (--ai-fake, or any routing config that
			// never declared an embeddings model) — brain.go's
			// seedEmbedBinding never plants the marker for this shape, so
			// reading it here would only ever error. Report the same
			// embedStateUnknown an engine-nil role reports, without the
			// read (and without its error log) at all.
			return embedStateUnknown
		}
		populated, status, _, err := engine.store.PopulatedIdentity(ctx)
		if err != nil {
			// The marker read failing mid-request never gates readiness (the
			// embed store still serves N+1 reads correctly under a stale or
			// unreadable binding) — it only ever downgrades this one
			// visibility line, so the failure is logged here rather than
			// surfaced to the probe body.
			slog.ErrorContext(ctx, "readyz: reading embed binding marker failed", "err", err)
			return embedStateUnknown
		}
		if status == reembeddingStatus {
			return "reembedding"
		}
		if populated == engine.currentIdentity() {
			return "active"
		}
		return "needs_reindex"
	}
}
