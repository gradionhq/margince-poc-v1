// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package nextaction

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Handlers serves the recommendation.
type Handlers struct {
	svc *Service
}

// NewHandlers binds the transport to the service.
func NewHandlers(svc *Service) Handlers {
	return Handlers{svc: svc}
}

// GetDealNextBestAction computes and returns; it performs nothing.
func (h Handlers) GetDealNextBestAction(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	out, err := h.svc.Get(r.Context(), ids.From[ids.DealKind](ids.UUID(id)))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, out)
}
