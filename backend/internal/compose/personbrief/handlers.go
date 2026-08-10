// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package personbrief

// The HTTP transport for the relationship brief. Wire concerns only: bind the
// path id, refuse the modes this read cannot honestly serve, and hand the
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

// Handlers shadows the generated person-brief stubs.
type Handlers struct {
	svc     *Service
	overlay OverlayMode
}

// NewHandlers binds the transport to a ready service.
func NewHandlers(svc *Service, overlay OverlayMode) Handlers {
	return Handlers{svc: svc, overlay: overlay}
}

// GetPersonBrief implements GET /people/{id}/brief.
func (h Handlers) GetPersonBrief(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	h.serve(w, r, id, false)
}

// RegeneratePersonBrief implements POST /people/{id}/brief — the explicit
// refresh behind the card's "outdated". It writes only the cached brief: no
// record field changes, and nothing is sent.
func (h Handlers) RegeneratePersonBrief(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	h.serve(w, r, id, true)
}

func (h Handlers) serve(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, force bool) {
	if !h.native(w, r) {
		return
	}
	brief, err := h.svc.Get(r.Context(), ids.From[ids.PersonKind](ids.UUID(id)), force)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, brief)
}

// native refuses the read in overlay mode.
//
// The check is repeated here rather than inherited: the 360's refusal lives in
// ITS handler, and this route reaches the composite read through the service.
// A mirror holds none of these conversations, so a brief written from it would
// describe a relationship this installation does not own.
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
			"the relationship brief is written from this system of record; while the workspace reads from the incumbent mirror, open the contact in the incumbent's own UI"))
		return false
	}
	return true
}
