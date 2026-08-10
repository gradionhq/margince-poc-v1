// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package accountdraft

// The HTTP transport. Wire concerns only: bind the path id, decode the body,
// refuse an overlay workspace, and hand the result to the sentinel error
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

// Handlers shadows the generated DraftAccountEmail stub.
type Handlers struct {
	svc     *Service
	overlay OverlayMode
}

// NewHandlers binds the transport to a ready service; compose constructs it
// once per process role.
func NewHandlers(svc *Service, overlay OverlayMode) Handlers {
	return Handlers{svc: svc, overlay: overlay}
}

// DraftAccountEmail implements POST /organizations/{id}/draft-email.
func (h Handlers) DraftAccountEmail(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	overlay, err := h.overlay(r.Context())
	if err != nil {
		// A mode-resolution failure refuses: drafting from native rows because
		// the lookup broke is the silent fallback overlay exists to prevent.
		httperr.Write(w, r, err)
		return
	}
	if overlay {
		httperr.Write(w, r, httperr.Validation("id", "unsupported_in_overlay_mode",
			"a grounded draft is written from this system of record; while the workspace "+
				"reads from the incumbent mirror there is nothing here to write from"))
		return
	}
	var body crmcontracts.DraftAccountEmailJSONRequestBody
	if !httperr.Decode(w, r, &body) {
		return
	}
	req, err := requestFrom(body)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	draft, err := h.svc.Draft(r.Context(),
		ids.From[ids.OrganizationKind](ids.UUID(id)), req)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, draft)
}

// requestFrom maps the wire body, refusing an omitted person_id before it can
// become a lookup.
//
// An absent key decodes to the zero UUID with no error, so without this the
// caller would be told that a record they never named is not a contact on this
// account — a refusal about a record they cannot connect to anything they did.
func requestFrom(body crmcontracts.DraftAccountEmailJSONRequestBody) (Request, error) {
	if err := httperr.RequireBodyID("person_id", ids.UUID(body.PersonId)); err != nil {
		return Request{}, err
	}
	req := Request{PersonID: body.PersonId.String()}
	if body.DealId != nil {
		// A null deal_id is "the account in general", which is an ordinary
		// case. A present-but-zero one is a client bug, and answering "that
		// deal is not open" about the nil UUID would hide it.
		if err := httperr.RequireBodyID("deal_id", ids.UUID(*body.DealId)); err != nil {
			return Request{}, err
		}
		req.DealID = body.DealId.String()
	}
	if body.Intent != nil {
		req.Intent = *body.Intent
	}
	return req, nil
}
