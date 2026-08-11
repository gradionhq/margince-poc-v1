// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package personresearch

// The HTTP transport for the research surface. Wire concerns only: bind the
// path id, refuse the modes this surface cannot honestly serve, and hand the
// result to the sentinel error mapping.

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

// Handlers shadows the generated research stubs.
type Handlers struct {
	svc     *Service
	overlay OverlayMode
}

// NewHandlers binds the transport to a ready service.
func NewHandlers(svc *Service, overlay OverlayMode) Handlers {
	return Handlers{svc: svc, overlay: overlay}
}

// RunPersonResearch implements POST /people/{id}/research.
func (h Handlers) RunPersonResearch(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if !h.native(w, r) {
		return
	}
	run, err := h.svc.Run(r.Context(), ids.From[ids.PersonKind](ids.UUID(id)))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, run)
}

// SavePersonResearch implements POST /people/{id}/research/save.
func (h Handlers) SavePersonResearch(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if !h.native(w, r) {
		return
	}
	var body crmcontracts.SavePersonResearchRequest
	if !httperr.Decode(w, r, &body) {
		return
	}
	saved, err := h.svc.Save(r.Context(), ids.From[ids.PersonKind](ids.UUID(id)), body)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, struct {
		Saved int `json:"saved"`
	}{Saved: saved})
}

// native refuses in overlay mode: a mirror holds none of these records, so a
// claim saved against it would name a person this installation does not own.
func (h Handlers) native(w http.ResponseWriter, r *http.Request) bool {
	if h.overlay == nil {
		return true
	}
	overlay, err := h.overlay(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return false
	}
	if overlay {
		httperr.Write(w, r, httperr.Validation("id", "unsupported_in_overlay_mode",
			"research is staged against this system of record; while the workspace reads from the incumbent mirror, open the contact in the incumbent's own UI"))
		return false
	}
	return true
}
