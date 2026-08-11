// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package persondraft

// The HTTP transport. Wire concerns only: bind the path id, decode the optional
// body, refuse an overlay workspace, and hand the result to the sentinel error
// mapping. The service owns every gate that matters.

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

// Handlers shadows the generated DraftPersonEmail stub.
type Handlers struct {
	svc     *Service
	overlay OverlayMode
}

// NewHandlers binds the transport to a ready service; compose constructs it
// once per process role.
func NewHandlers(svc *Service, overlay OverlayMode) Handlers {
	return Handlers{svc: svc, overlay: overlay}
}

// DraftPersonEmail implements POST /people/{id}/draft-email.
//
// The body is decoded BEFORE the overlay mode is resolved: a caller who
// mistyped `intent` must be told which field is wrong, not that their workspace
// is in the wrong mode.
func (h Handlers) DraftPersonEmail(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	req, ok := decodeRequest(w, r)
	if !ok {
		return
	}
	if !h.native(w, r) {
		return
	}
	draft, err := h.svc.Draft(r.Context(), ids.From[ids.PersonKind](ids.UUID(id)), req)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, draft)
}

// decodeRequest reads the optional steering. An absent body is the ordinary
// case — "write to this person" with nothing further to say — so it is not a
// missing field.
func decodeRequest(w http.ResponseWriter, r *http.Request) (Request, bool) {
	if r.ContentLength == 0 {
		return Request{}, true
	}
	var body crmcontracts.DraftPersonEmailJSONRequestBody
	if !httperr.Decode(w, r, &body) {
		return Request{}, false
	}
	if body.Intent == nil {
		return Request{}, true
	}
	return Request{Intent: *body.Intent}, true
}

// native refuses the draft in overlay mode.
//
// A grounded draft is written from this system of record; a mirror holds none
// of these conversations, so there would be nothing here to write from.
func (h Handlers) native(w http.ResponseWriter, r *http.Request) bool {
	if h.overlay == nil {
		return true
	}
	overlay, err := h.overlay(r.Context())
	if err != nil {
		// A mode-resolution failure refuses: drafting from native rows because
		// the lookup broke is the silent fallback overlay exists to prevent.
		httperr.Write(w, r, err)
		return false
	}
	if overlay {
		httperr.Write(w, r, httperr.Validation("id", "unsupported_in_overlay_mode",
			"a grounded draft is written from this system of record; while the workspace "+
				"reads from the incumbent mirror there is nothing here to write from"))
		return false
	}
	return true
}
