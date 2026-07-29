// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgbrief

// The HTTP transport for the account brief. Wire concerns only: bind the
// path id, say whether this is a read or an explicit refresh, and hand the
// result to the sentinel error mapping. The service owns every gate.

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Handlers shadows the generated GetOrganizationBrief /
// RegenerateOrganizationBrief stubs.
type Handlers struct {
	svc *Service
}

// NewHandlers binds the transport to a ready service; compose
// constructs it once per process role.
func NewHandlers(svc *Service) Handlers { return Handlers{svc: svc} }

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
	brief, err := h.svc.Get(r.Context(), ids.From[ids.OrganizationKind](ids.UUID(id)), force)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, brief)
}
