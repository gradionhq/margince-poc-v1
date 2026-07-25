// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// Handlers is the overlay module's transport surface, wired by the
// composition layer (crm.yaml /overlay/*). It is deliberately
// zero-value-constructible: a Handlers{} with svc unset keeps every
// operation an explicit 501 (Server embeds it unconditionally, and a
// role that never wires the vault-backed service must not nil-deref),
// the same posture as every other declared-but-unimplemented surface.

import (
	"net/http"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
)

// Handlers is the overlay module's transport surface (crm.yaml
// /overlay/*): the incumbent connection lifecycle, mirror sync health,
// budget, reconcile, and the read-mode→overlay flip. svc backs every
// verb except the flip pair (branch 2), which stays an explicit 501
// until it lands regardless of whether svc is set — a partially-wired
// Handlers never silently succeeds on an op it doesn't yet serve.
type Handlers struct {
	svc *Service
}

// NewHandlers constructs Handlers over svc.
func NewHandlers(svc *Service) Handlers {
	return Handlers{svc: svc}
}

// GetOverlayConnection returns the workspace's overlay incumbent
// connection.
func (h Handlers) GetOverlayConnection(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		httperr.NotImplemented(w, r, "getOverlayConnection")
		return
	}
	conn, err := h.svc.Get(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, connectionToWire(conn))
}

// ConnectOverlay connects the workspace's overlay incumbent.
func (h Handlers) ConnectOverlay(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		httperr.NotImplemented(w, r, "connectOverlay")
		return
	}
	var req crmcontracts.OverlayConnectRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	// privateAppToken is a required field (crm.yaml): reject its absence as
	// a 422 here rather than letting an empty credential reach Connect and
	// surface as an internal error.
	if req.PrivateAppToken == "" {
		httperr.Write(w, r, httperr.Validation("privateAppToken", "required", "a private-app token is required to connect an incumbent"))
		return
	}
	conn, err := h.svc.Connect(r.Context(), ConnectInput{
		Incumbent: string(req.Incumbent),
		Region:    req.Region,
		Token:     req.PrivateAppToken,
	})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, connectionToWire(conn))
}

// connectionToWire maps the domain Connection onto the contract's
// OverlayConnection shape — the credential never rides either side of
// this mapping.
func connectionToWire(c Connection) crmcontracts.OverlayConnection {
	return crmcontracts.OverlayConnection{
		Incumbent:   crmcontracts.OverlayConnectionIncumbent(c.Incumbent),
		Region:      c.Region,
		Status:      crmcontracts.OverlayConnectionStatus(c.Status),
		ConnectedAt: c.ConnectedAt,
		Scopes:      c.Scopes,
	}
}

// DisconnectOverlay disconnects the overlay incumbent and tears down the
// mirror (design.md §4.9: revoke + purge + tombstone; see teardown.go
// for the audit-scrub scoping), synchronously — branch 1 has no async
// job runner for this yet, so the 202 answers "teardown complete"
// rather than "queued".
func (h Handlers) DisconnectOverlay(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		httperr.NotImplemented(w, r, "disconnectOverlay")
		return
	}
	if err := h.svc.Disconnect(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// GetOverlaySyncStatus returns per-object mirror sync freshness.
func (h Handlers) GetOverlaySyncStatus(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		httperr.NotImplemented(w, r, "getOverlaySyncStatus")
		return
	}
	objects, err := h.svc.SyncStatus(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, syncStatusToWire(objects))
}

// wireSyncObject is a type ALIAS (not a defined type) for the generated
// OverlaySyncStatus.Objects element shape — an alias, not a distinct
// type, so a []wireSyncObject is structurally the exact
// []struct{...} api_gen.go declares and assigns straight into
// crmcontracts.OverlaySyncStatus.Objects with no per-field copy.
type wireSyncObject = struct {
	BackfillComplete *bool                                       `json:"backfillComplete,omitempty"` //nolint:tagliatelle // must match the generated OverlaySyncStatus.Objects element shape verbatim (crm.yaml's own camelCase)
	LastSyncedAt     *time.Time                                  `json:"lastSyncedAt,omitempty"`     //nolint:tagliatelle // see above
	Object           *string                                     `json:"object,omitempty"`
	State            *crmcontracts.OverlaySyncStatusObjectsState `json:"state,omitempty"`
}

// syncStatusToWire maps the domain []ObjectSyncStatus onto the contract's
// OverlaySyncStatus shape. No object classes (e.g. backfill has not yet
// landed a single row) answers Objects left nil — an honest "nothing to
// report yet," never a fabricated empty-but-present list.
func syncStatusToWire(objects []ObjectSyncStatus) crmcontracts.OverlaySyncStatus {
	if len(objects) == 0 {
		return crmcontracts.OverlaySyncStatus{}
	}
	wire := make([]wireSyncObject, len(objects))
	for i, o := range objects {
		object, lastSyncedAt, complete := o.Object, o.LastSyncedAt, o.BackfillComplete
		state := crmcontracts.OverlaySyncStatusObjectsState(o.State)
		wire[i] = wireSyncObject{BackfillComplete: &complete, LastSyncedAt: &lastSyncedAt, Object: &object, State: &state}
	}
	return crmcontracts.OverlaySyncStatus{Objects: &wire}
}

// ReconcileOverlay asks for an out-of-band mirror reconciliation sweep. The
// sweep runs in the worker, which owns the vault-backed incumbent adapter and
// the job substrate; this handler records the request by making the workspace
// due now, so the 202 means exactly what the contract says — queued.
func (h Handlers) ReconcileOverlay(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		httperr.NotImplemented(w, r, "reconcileOverlay")
		return
	}
	if err := h.svc.RequestSweep(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// GetOverlayBudget returns the incumbent API budget window's consumption
// and degradation band.
func (h Handlers) GetOverlayBudget(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		httperr.NotImplemented(w, r, "getOverlayBudget")
		return
	}
	budget, err := h.svc.Budget(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, budgetToWire(budget))
}

// budgetToWire maps the domain Budget onto the contract's OverlayBudget shape
// (overlay-budget.md "The budget read (wire shape)", OVB-AC-1/AC-5): the REST
// window total/cap/band, the per-source breakdown (summing to consumed), the
// honest headroom (the meter's `~unknown` sentinel is carried through verbatim
// as a string — never a fabricated number, OVB-AC-1), and the per-second Search
// window. Counts ride as int64 (exact — a float would round independently and
// could break the per-source sum above 2^24). A source that spent nothing this
// window is a map miss (0).
func budgetToWire(b overlaybudget.Budget) crmcontracts.OverlayBudget {
	window := b.Window
	consumed := int64(b.Consumed)
	limit := int64(b.Limit)
	band := crmcontracts.OverlayBudgetBand(b.Band)
	headroom := b.Headroom

	forceFresh := int64(b.Breakdown[overlaybudget.SourceForceFresh])
	poller := int64(b.Breakdown[overlaybudget.SourcePoller])
	capture := int64(b.Breakdown[overlaybudget.SourceCapture])

	searchWindow := b.SearchWindow
	searchConsumed := int64(b.SearchConsumed)
	searchLimit := int64(b.SearchLimit)
	searchBand := crmcontracts.OverlayBudgetBand(b.SearchBand)

	return crmcontracts.OverlayBudget{
		Window:   &window,
		Consumed: &consumed,
		Limit:    &limit,
		Band:     &band,
		Headroom: &headroom,
		Sources: &struct {
			Capture    *int64 `json:"capture,omitempty"`
			ForceFresh *int64 `json:"force_fresh,omitempty"`
			Poller     *int64 `json:"poller,omitempty"`
		}{Capture: &capture, ForceFresh: &forceFresh, Poller: &poller},
		Search: &crmcontracts.OverlayBudgetSearch{
			Window:   &searchWindow,
			Consumed: &searchConsumed,
			Limit:    &searchLimit,
			Band:     &searchBand,
		},
	}
}

// PreflightOverlayFlip dry-runs the read-mode→overlay flip's readiness
// checks without executing it.
func (h Handlers) PreflightOverlayFlip(w http.ResponseWriter, r *http.Request) {
	httperr.NotImplemented(w, r, "preflightOverlayFlip")
}

// ExecuteOverlayFlip executes the read-mode→overlay flip, queuing the
// migration.
func (h Handlers) ExecuteOverlayFlip(w http.ResponseWriter, r *http.Request) {
	httperr.NotImplemented(w, r, "executeOverlayFlip")
}
