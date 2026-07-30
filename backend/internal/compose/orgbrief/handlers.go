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

func (h Handlers) serve(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, force bool) {
	overlay, err := h.overlay(r.Context())
	if err != nil {
		// A mode-resolution failure refuses: writing a brief from native rows
		// because the lookup broke is the silent fallback overlay exists to
		// prevent.
		httperr.Write(w, r, err)
		return
	}
	if overlay {
		httperr.Write(w, r, httperr.Validation("id", "unsupported_in_overlay_mode",
			"the account brief is written from this system of record; while the workspace reads from the incumbent mirror, there is no brief to write"))
		return
	}
	brief, err := h.svc.Get(r.Context(), ids.From[ids.OrganizationKind](ids.UUID(id)), force)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, brief)
}
