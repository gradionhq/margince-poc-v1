// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// The HTTP transport for the company dossier. Wire concerns only: bind the path
// id, say whether this is a read or an explicit refresh, and hand the result to
// the sentinel error mapping. The service owns every other gate.

import (
	"context"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// OverlayMode answers whether the calling workspace reads from an incumbent
// mirror instead of this system of record.
type OverlayMode func(ctx context.Context) (bool, error)

// Handlers shadows the generated dossier and growth-fit stubs.
type Handlers struct {
	svc       *Service
	growthFit *GrowthFitService
	overlay   OverlayMode
}

// NewHandlers binds the transport to ready services; compose constructs it once
// per process role.
func NewHandlers(svc *Service, growthFit *GrowthFitService, overlay OverlayMode) Handlers {
	return Handlers{svc: svc, growthFit: growthFit, overlay: overlay}
}

// GetOrganizationDossier implements GET /organizations/{id}/dossier.
func (h Handlers) GetOrganizationDossier(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	h.serve(w, r, id, false)
}

// RefreshOrganizationDossier implements POST /organizations/{id}/dossier — the
// explicit rebuild, past a fingerprint that still matches.
func (h Handlers) RefreshOrganizationDossier(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	h.serve(w, r, id, true)
}

// GetOrganizationGrowthFit implements GET /organizations/{id}/growth-fit.
func (h Handlers) GetOrganizationGrowthFit(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	h.serveGrowthFit(w, r, id, false)
}

// RefreshOrganizationGrowthFit implements POST /organizations/{id}/growth-fit —
// the caller's own re-assessment, past a fingerprint that still matches.
func (h Handlers) RefreshOrganizationGrowthFit(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	h.serveGrowthFit(w, r, id, true)
}

func (h Handlers) serve(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, force bool) {
	if !h.native(w, r) {
		return
	}
	dossier, err := h.svc.Get(r.Context(), ids.From[ids.OrganizationKind](ids.UUID(id)), force)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, dossier)
}

// serveGrowthFit refuses in overlay mode for the dossier's reason and one of
// its own: the fit is computed from native facts AND from this workspace's own
// confirmed offering, and a mirror holds neither.
func (h Handlers) serveGrowthFit(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, force bool) {
	if !h.native(w, r) {
		return
	}
	fit, err := h.growthFit.Get(r.Context(), ids.From[ids.OrganizationKind](ids.UUID(id)), force)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, fit)
}

// native reports whether this workspace reads from this system of record,
// writing the refusal itself when it does not (ADR-0088, DOSS-AC-15).
//
// The dossier is assembled from NATIVE facts — profile fields and extracted
// facts this system holds. A workspace reading from an incumbent mirror has
// none of them, so the honest answer is that the surface is unavailable in this
// mode and why, rather than a confident dossier about a company whose records
// live somewhere else.
func (h Handlers) native(w http.ResponseWriter, r *http.Request) bool {
	if h.overlay == nil {
		// An unwired mode check is a deployment defect on a surface whose whole
		// premise is which system of record it reads. It refuses rather than
		// assuming native, which is the silent fallback overlay exists to stop.
		httperr.Write(w, r, httperr.Validation("id", "unsupported_in_overlay_mode",
			"the dossier cannot confirm which system of record this workspace reads from"))
		return false
	}
	overlay, err := h.overlay(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return false
	}
	if overlay {
		httperr.Write(w, r, httperr.Validation("id", "unsupported_in_overlay_mode",
			"the dossier is assembled from facts held in this system of record; while the "+
				"workspace reads from the incumbent mirror there is nothing here to assemble "+
				"from — open the account in the incumbent's own UI"))
		return false
	}
	return true
}
