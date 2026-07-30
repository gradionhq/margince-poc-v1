// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The HTTP transport for the company record page. Wire concerns only:
// bind the path id, refuse the modes this read cannot honestly serve, and
// hand the result to the sentinel error mapping. The service owns the
// transaction and every gate.

import (
	"context"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// OverlayMode answers whether the calling workspace reads from an
// incumbent mirror instead of this system of record. The composition
// layer injects the one Dispatcher every other overlay-aware read uses,
// so a mode flip is observed here at the same moment it is observed there.
type OverlayMode func(ctx context.Context) (bool, error)

// Handlers shadows the generated GetOrganization360 /
// AcknowledgeOrganizationView stubs.
type Handlers struct {
	svc     *Service
	overlay OverlayMode
}

// NewHandlers binds the transport to a ready service.
func NewHandlers(svc *Service, overlay OverlayMode) Handlers {
	return Handlers{svc: svc, overlay: overlay}
}

// GetOrganization360 implements GET /organizations/{id}/360.
func (h Handlers) GetOrganization360(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if !h.nativeOnly(w, r) {
		return
	}
	view, err := h.svc.Assemble(r.Context(), ids.From[ids.OrganizationKind](ids.UUID(id)))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, view)
}

// GetOrganizationGraph implements GET /organizations/{id}/graph.
func (h Handlers) GetOrganizationGraph(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if !h.nativeOnly(w, r) {
		return
	}
	graph, err := h.svc.Graph(r.Context(), ids.From[ids.OrganizationKind](ids.UUID(id)))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, graph)
}

// AcknowledgeOrganizationView implements POST /organizations/{id}/view-ack.
func (h Handlers) AcknowledgeOrganizationView(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if !h.nativeOnly(w, r) {
		return
	}
	ack, err := h.svc.Acknowledge(r.Context(), ids.From[ids.OrganizationKind](ids.UUID(id)))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, ack)
}

// DismissOrganizationSuggestion implements
// POST /organizations/{id}/suggestions/dismiss.
func (h Handlers) DismissOrganizationSuggestion(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if !h.nativeOnly(w, r) {
		return
	}
	var req crmcontracts.DismissOrganizationSuggestionJSONRequestBody
	if !httperr.Decode(w, r, &req) {
		return
	}
	if err := h.svc.DismissSuggestion(r.Context(),
		ids.From[ids.OrganizationKind](ids.UUID(id)), req.Fingerprint); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// nativeOnly refuses an overlay-mode workspace. The mirror holds the
// incumbent's records, not our relationship edges, tags, approvals or
// visit marks, so there is no honest 360 to assemble from it — the same
// refusal entity-scoped activity reads already give, rather than a page
// that quietly omits most of itself. A mode-resolution failure refuses
// too: serving native data because the lookup broke is the silent
// fallback the overlay module exists to prevent.
func (h Handlers) nativeOnly(w http.ResponseWriter, r *http.Request) bool {
	overlay, err := h.overlay(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return false
	}
	if overlay {
		httperr.Write(w, r, httperr.Validation("id", "unsupported_in_overlay_mode",
			"the company view is assembled from this system of record; while the workspace reads from the incumbent mirror, open the account in the incumbent's own UI"))
		return false
	}
	return true
}
