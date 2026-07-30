// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgbrief

// The HTTP transport for the account brief. Wire concerns only: bind the
// path id, say whether this is a read or an explicit refresh, and hand the
// result to the sentinel error mapping. The service owns every gate.

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

// Handlers shadows the generated GetOrganizationBrief /
// RegenerateOrganizationBrief stubs.
type Handlers struct {
	svc     *Service
	overlay OverlayMode
}

// NewHandlers binds the transport to a ready service; compose constructs it
// once per process role.
//
// overlay is the same mode dispatch the company view asks. The brief is
// written from the 360's reads, and the 360 refuses an overlay workspace —
// but that refusal lives in ITS handler, not in the service this one calls.
// Without the same gate here, an overlay workspace would get a brief written
// from native rows while its own company page refuses to render at all.
func NewHandlers(svc *Service, overlay OverlayMode) Handlers {
	return Handlers{svc: svc, overlay: overlay}
}

// GetOrganizationBrief implements GET /organizations/{id}/brief.
func (h Handlers) GetOrganizationBrief(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	h.serve(w, r, id, false)
}

// RegenerateOrganizationBrief implements POST /organizations/{id}/brief —
// the explicit refresh behind "outdated — refresh".
func (h Handlers) RegenerateOrganizationBrief(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	h.serve(w, r, id, true)
}

// AskAboutOrganization implements POST /organizations/{id}/ask.
func (h Handlers) AskAboutOrganization(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if !h.native(w, r, "the prepared answer") {
		return
	}
	var req crmcontracts.AskAboutOrganizationJSONRequestBody
	if !httperr.Decode(w, r, &req) {
		return
	}
	answer, err := h.svc.Ask(r.Context(), ids.From[ids.OrganizationKind](ids.UUID(id)), req.Question)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, answer)
}

// native reports whether this workspace reads from this system of record,
// writing the refusal itself when it does not. Both the brief and the prepared
// questions are written from the 360's reads, and the 360 refuses an overlay
// workspace — but that refusal lives in ITS handler, so without this gate an
// overlay workspace would get generated prose about native rows while its own
// company page refuses to render at all.
func (h Handlers) native(w http.ResponseWriter, r *http.Request, subject string) bool {
	overlay, err := h.overlay(r.Context())
	if err != nil {
		// A mode-resolution failure refuses: writing from native rows because
		// the lookup broke is the silent fallback overlay exists to prevent.
		httperr.Write(w, r, err)
		return false
	}
	if overlay {
		httperr.Write(w, r, httperr.Validation("id", "unsupported_in_overlay_mode",
			subject+" is written from this system of record; while the workspace reads from "+
				"the incumbent mirror there is nothing here to write from — open the account "+
				"in the incumbent's own UI"))
		return false
	}
	return true
}

func (h Handlers) serve(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, force bool) {
	if !h.native(w, r, "the account brief") {
		return
	}
	brief, err := h.svc.Get(r.Context(), ids.From[ids.OrganizationKind](ids.UUID(id)), force)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, brief)
}
