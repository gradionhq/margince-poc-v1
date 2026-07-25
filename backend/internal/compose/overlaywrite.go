// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The overlay-mode write-admission guard. In overlay mode a workspace's
// records are served from an incumbent mirror. Write-back (branch 2) routes
// canonical writes to the incumbent, but ONLY through the SoR seam — the
// datasource Provider's Create/Update/Archive verbs the Dispatcher and the
// agent tool registry call. The REST record-write handlers are the
// module-owned transports (people.Handlers.CreatePerson, …): they write
// their native tables DIRECTLY and never ride the SoR seam, so nothing on
// that path consults x_sor_mode or reaches the incumbent.
//
// A native REST write against an overlay-mode workspace is only a problem
// when BOTH of these hold: the record type is one the mirror actually holds
// (overlayMirroredTypes — a tag, offer, product, list, saved view, custom
// field, relationship, or webhook subscription is never mirrored, so its
// native table is the live one even in overlay mode), and the overlay
// provider cannot itself serve that verb for the type
// (overlay.SupportsWrite) — a verb it CAN serve is let through to reach a
// write shadow rather than being refused outright. Refusing beyond that
// blocks working capability for no reason; letting a write through when
// neither condition holds risks a commit to an empty native table that
// vanishes from every mirror-backed read and never reaches the incumbent.
// The SPA hides the affected affordances in overlay, but that cannot bind a
// direct API caller — this guard is the server-side chokepoint, run for
// every principal.

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// The backing MCP tool verbs (agentPolicy.Tool values) of the record-write
// operations this guard reasons about, plus the "tool" access class. Named
// as constants both for legibility and because these strings recur across
// the generated policy table.
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

// overlayWriteVerbs maps the generated policy's tool verb onto the SoR seam's
// write verb. A record-write tool with no entry here (advance_deal,
// merge_records, promote_lead, disqualify_lead) is one the overlay provider
// declares unsupported outright (provider_writes.go), so it is always refused
// for a mirrored type.
var overlayWriteVerbs = map[string]overlay.WriteVerb{
	toolCreateRecord:  overlay.WriteCreate,
	toolUpdateRecord:  overlay.WriteUpdate,
	toolArchiveRecord: overlay.WriteArchive,
	toolLogActivity:   overlay.WriteCreate,
}

// overlayRecordWriteTools are the tool verbs that write a record at all — the
// set whose RecordType must then be checked against the mirror. Side-service
// tools (draft/send email, book meeting, relink) and human-only governance
// are deliberately ABSENT — they are not SoR record writes and remain
// available in overlay regardless of record type.
var overlayRecordWriteTools = map[string]bool{
	toolCreateRecord: true, toolUpdateRecord: true, toolArchiveRecord: true,
	toolLogActivity: true, toolAdvanceDeal: true, toolMergeRecords: true,
	toolPromoteLead: true, toolDisqualifyLead: true,
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

// overlayWriteGuard refuses a mutating REST request whose native module
// handler would write a mirrored entity's native table DIRECTLY (bypassing
// the SoR seam) when the workspace is in overlay mode AND the overlay
// provider cannot itself serve that write — those native tables are empty in
// overlay, so an unserviceable write would vanish and never reach the
// incumbent. A write the provider CAN serve (overlay.SupportsWrite) is let
// through to reach a write shadow instead of being refused; a native-only
// record type (never in overlayMirroredTypes) is let through unconditionally,
// since its native table is the live one in overlay mode too. The guarded set
// is keyed off the generated agentPolicies table (the contract's own
// op→tool classification), so it never drifts from the contract. It runs for
// every principal — the reason it is a standalone middleware rather than part
// of the agent-only gate.
func overlayWriteGuard(mode overlayModeChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !mutatingMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			pattern := chi.RouteContext(r.Context()).RoutePattern()
			pol, known := agentPolicies[r.Method+" "+pattern]
			// The verb says the op writes a record; the record type says whether
			// the mirror is the system of record for it (a tag or an offer writes
			// a native table the overlay never shadows, and that table is the
			// live one in overlay mode too); and the provider says whether the
			// seam can serve that write at all. Only a write the provider serves
			// is allowed through — to its shadow, never to the native handler.
			if !known || pol.Access != accessTool ||
				!overlayRecordWriteTools[pol.Tool] || !overlayMirroredTypes[pol.RecordType] {
				next.ServeHTTP(w, r)
				return
			}
			verb, isSeamVerb := overlayWriteVerbs[pol.Tool]
			if isSeamVerb && overlay.SupportsWrite(verb, datasource.EntityType(pol.RecordType)) {
				next.ServeHTTP(w, r)
				return
			}
			inOverlay, err := mode.isOverlay(r.Context())
			if err != nil {
				httperr.Write(w, r, err)
				return
			}
			if inOverlay {
				httperr.Write(w, r, apperrors.ErrUnsupportedBySoR)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
