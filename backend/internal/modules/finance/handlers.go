// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package finance

// The HTTP transport for the finance mirror. Wire concerns only: bind the path
// id and hand the result to the sentinel error mapping. The store owns every
// gate.

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Handlers shadows the generated finance stubs.
type Handlers struct {
	store *Store
}

// NewHandlers binds the transport to the pool the mirror is read through.
func NewHandlers(pool *pgxpool.Pool) Handlers {
	return Handlers{store: NewStore(pool)}
}

// GetOrganizationFinanceSummary implements
// GET /organizations/{id}/finance-summary.
func (h Handlers) GetOrganizationFinanceSummary(
	w http.ResponseWriter, r *http.Request, id crmcontracts.Id,
) {
	summary, err := h.store.SummaryFor(r.Context(),
		ids.From[ids.OrganizationKind](ids.UUID(id)))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, summary)
}
