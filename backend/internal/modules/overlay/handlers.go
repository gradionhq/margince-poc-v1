// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// Handlers is the overlay module's transport surface, wired by the
// composition layer (crm.yaml /overlay/*). It is deliberately
// zero-value-constructible: a Handlers{} with svc/reconciler unset keeps
// every operation an explicit 501 (Server embeds it unconditionally, and
// a role that never wires the vault-backed service must not nil-deref),
// the same posture as every other declared-but-unimplemented surface.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Reconciler runs an out-of-band mirror reconciliation sweep for ctx's
// workspace, right now — the seam ReconcileOverlay drives. Sweeping
// needs a LIVE incumbent adapter built from this workspace's own
// vaulted credential (keyvault.Vault.Get keyed by the incumbent_
// connection row) plus the concrete adapter type (overlay/hubspot), and
// this package can import neither (the vault provider selection stays
// out of this module per NewOverlayHandlers' own doc; overlay/hubspot
// imports THIS package, so the reverse import would cycle) — so compose
// implements this interface over the real pieces (compose/overlay.go)
// and injects it here, the same "seam interface here, concrete wiring in
// compose" shape ai.NewHandlers/agents.NewHandlers already use for their
// own cross-module dependencies.
type Reconciler interface {
	Reconcile(ctx context.Context) error
}

// Handlers is the overlay module's transport surface (crm.yaml
// /overlay/*): the incumbent connection lifecycle, mirror sync health,
// budget, and the read-mode→overlay flip. svc backs the connection-
// lifecycle ops plus sync-status/budget; reconciler backs
// ReconcileOverlay; the flip pair (branch 2) stays an explicit 501 until
// it lands, regardless of whether svc/reconciler are set — a
// partially-wired Handlers never silently succeeds on an op it doesn't
// yet serve.
type Handlers struct {
	svc        *Service
	reconciler Reconciler
}

// NewHandlers constructs Handlers over svc.
func NewHandlers(svc *Service) Handlers {
	return Handlers{svc: svc}
}

// WithReconciler wires Reconciler onto Handlers — ReconcileOverlay stays
// its declared 501 until this is called (the same "declared or absent"
// posture WithKeyvault documents for the connection lifecycle).
func (h Handlers) WithReconciler(r Reconciler) Handlers {
	h.reconciler = r
	return h
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

// reconcileTimeout bounds ReconcileOverlay's synchronous, in-request sweep
// (see the handler's own doc below on why it runs inline at all). Without
// a cap, a slow or rate-limited HubSpot page walk would hang the request
// thread indefinitely; 60s is generous enough for a normal per-object-class
// page walk to land while still failing the request rather than the
// process. True async (River-enqueue the sweep, answer immediately) is the
// tracked follow-up once cmd/api gets its own enqueue seam into
// cmd/worker's job substrate — this is the honest bounded-synchronous
// branch-1 stopgap, not the final shape.
const reconcileTimeout = 60 * time.Second

// ReconcileOverlay triggers an out-of-band mirror reconciliation sweep
// for the calling workspace, right now. Gated by
// auth.Require("overlay_connection", ActionUpdate): reconcile is a
// mutating, budget-spending live HubSpot sweep — the same admin/ops-only
// posture Connect/Disconnect already carry (identity/internal/policy),
// so a read-only or non-admin seat is refused before the sweep ever
// starts, never after it has already spent budget.
//
// Branch 1 has no seam that lets cmd/api enqueue a River job cmd/worker's
// own periodic runner would pick up (River's job substrate is wired only
// into cmd/worker — see compose/jobs.go's NewJobRunner) — the same gap
// the Disconnect handler already names and answers the same way: this
// runs the sweep SYNCHRONOUSLY, in-request, reusing the identical
// overlay.Reconcile sweep the periodic poller drives (compose/overlay.go's
// Reconciler wiring), bounded by reconcileTimeout so it cannot hang the
// request thread forever, then answers 202. The 202 here means "the sweep
// for this window already ran (or was cut off by the timeout)," not
// "queued for later" — an honest divergence from crm.yaml's "Sweep
// queued" prose, the same divergence Disconnect's own doc comment already
// accepts for this branch.
func (h Handlers) ReconcileOverlay(w http.ResponseWriter, r *http.Request) {
	if h.reconciler == nil {
		httperr.NotImplemented(w, r, "reconcileOverlay")
		return
	}
	ctx := r.Context()
	if err := auth.Require(ctx, overlayConnectionObject, principal.ActionUpdate); err != nil {
		httperr.Write(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()
	if err := h.reconciler.Reconcile(ctx); err != nil {
		// Any context cancellation — our own reconcileTimeout (DeadlineExceeded)
		// OR the caller abandoning the request (Canceled) — is a "cut off, retry"
		// availability outcome, not an internal error: answer 503, never a
		// misleading 500. Per-object-class progress already landed is retained,
		// so a retry continues rather than restarts.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			httperr.Write(w, r, &httperr.DetailedError{
				Status: http.StatusServiceUnavailable,
				Code:   "overlay_reconcile_timeout",
				Detail: fmt.Sprintf("the mirror reconciliation sweep was cut off before finishing (timeout %s or the request was canceled); per-object-class progress already landed is retained, retry to continue", reconcileTimeout),
			})
			return
		}
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
// window. Consumed/Limit ride as float32 per the generated schema (an OpenAPI
// integer-as-number artifact), never fractional in practice. A source that
// spent nothing this window is a map miss (0).
func budgetToWire(b overlaybudget.Budget) crmcontracts.OverlayBudget {
	window := b.Window
	consumed := float32(b.Consumed)
	limit := float32(b.Limit)
	band := crmcontracts.OverlayBudgetBand(b.Band)
	headroom := b.Headroom

	forceFresh := float32(b.Breakdown[overlaybudget.SourceForceFresh])
	poller := float32(b.Breakdown[overlaybudget.SourcePoller])
	capture := float32(b.Breakdown[overlaybudget.SourceCapture])

	searchWindow := b.SearchWindow
	searchConsumed := float32(b.SearchConsumed)
	searchLimit := float32(b.SearchLimit)
	searchBand := crmcontracts.OverlayBudgetSearchBand(b.SearchBand)

	return crmcontracts.OverlayBudget{
		Window:   &window,
		Consumed: &consumed,
		Limit:    &limit,
		Band:     &band,
		Headroom: &headroom,
		Sources: &struct {
			Capture    *float32 `json:"capture,omitempty"`
			ForceFresh *float32 `json:"force_fresh,omitempty"`
			Poller     *float32 `json:"poller,omitempty"`
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
