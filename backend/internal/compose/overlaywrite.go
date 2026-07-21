// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The overlay-mode write-admission guard. In overlay mode a workspace's
// records are served from a read-only incumbent mirror; the overlay
// datasource Provider declares every record write unsupported_by_sor until
// branch 2 lands the write-back path. But the REST write handlers are the
// module-owned transports (people.Handlers.CreatePerson, …) — they write
// their native tables directly and never ride the Dispatcher's write
// verbs, so nothing consulted x_sor_mode on the write path. A human or a
// static-tier agent issuing a create/update/archive/advance/merge/promote
// against an overlay-mode workspace therefore committed to the EMPTY
// native tables: the write then vanished from every mirror-backed read and
// never reached the incumbent. The SPA hides these affordances in overlay,
// but that cannot bind a direct API caller. This guard is the server-side
// chokepoint that makes the refusal real for every principal.

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// The backing MCP tool verbs (agentPolicy.Tool values) of the operations
// the overlay Provider declares unsupported, plus the "tool" access class.
// Named as constants both for legibility and because these strings recur
// across the generated policy table.
const (
	toolCreateRecord   = "create_record"
	toolUpdateRecord   = "update_record"
	toolArchiveRecord  = "archive_record"
	toolDisqualifyLead = "disqualify_lead"
	toolAdvanceDeal    = "advance_deal"
	toolMergeRecords   = "merge_records"
	toolPromoteLead    = "promote_lead"
	toolLogActivity    = "log_activity"
	accessTool         = "tool"
)

// overlaySoRWriteTools are the backing MCP tool verbs of the operations the
// overlay Provider declares unsupported (its Create/Update/AdvanceDeal/
// Archive/Merge/PromoteLead seam). A mutating route whose generated policy
// names one of these writes a mirrored entity through the system-of-record
// seam, so it is refused in overlay mode. Side-service tools (draft/send
// email, book meeting, relink) and human-only governance are deliberately
// ABSENT — they are not SoR record writes and remain available in overlay.
var overlaySoRWriteTools = map[string]bool{
	toolCreateRecord:   true,
	toolUpdateRecord:   true,
	toolArchiveRecord:  true,
	toolDisqualifyLead: true,
	toolAdvanceDeal:    true,
	toolMergeRecords:   true,
	toolPromoteLead:    true,
	toolLogActivity:    true,
}

// overlayModeChecker resolves whether the request's workspace is in overlay
// mode. It is the Dispatcher's own resolver, kept as a one-method interface
// so the guard is unit-testable without the full dispatch. NOTE: the answer
// rides the Dispatcher's short TTL cache, so a non-connecting process can
// serve the pre-flip mode for up to that TTL after a mode change on another
// instance (the connecting process invalidates its own cache). Closing that
// last window needs an uncached, in-transaction mode read on the write path
// and lands with the branch-2 write-back work; this guard closes the far
// larger hole — that human/static writes were not mode-checked at all.
type overlayModeChecker interface {
	isOverlay(ctx context.Context) (bool, error)
}

// overlayWriteGuard refuses a mutating request that would write a mirrored
// entity through the SoR seam when the workspace is in overlay mode,
// answering the same unsupported_by_sor the Provider's write verbs and the
// agent-tier dispatch already give. It is keyed off the generated
// agentPolicies table (the contract's own op→tool classification), so the
// guarded set never drifts from the contract. It runs for every principal
// — the reason it is a standalone middleware rather than part of the
// agent-only gate.
func overlayWriteGuard(mode overlayModeChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !mutatingMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			pattern := chi.RouteContext(r.Context()).RoutePattern()
			pol, known := agentPolicies[r.Method+" "+pattern]
			if !known || pol.Access != accessTool || !overlaySoRWriteTools[pol.Tool] {
				next.ServeHTTP(w, r)
				return
			}
			overlay, err := mode.isOverlay(r.Context())
			if err != nil {
				httperr.Write(w, r, err)
				return
			}
			if overlay {
				httperr.Write(w, r, apperrors.ErrUnsupportedBySoR)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
