// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// claimHandlers is the one HTTP door for claiming a customer record. The
// write lands in the module that owns the table — people for person,
// organization and lead; deals for deal — so the route dispatches on the
// record type rather than either module reaching into the other's table.
type claimHandlers struct {
	people *people.Store
	deals  *deals.Store
}

func (h claimHandlers) ClaimRecord(w http.ResponseWriter, r *http.Request, recordType string, id crmcontracts.Id, _ crmcontracts.ClaimRecordParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	out := crmcontracts.RecordClaim{RecordType: crmcontracts.RecordClaimRecordType(recordType), RecordId: id}
	if recordType == "deal" {
		claim, err := h.deals.ClaimDeal(ctx, ids.From[ids.DealKind](ids.UUID(id)), ifVersion)
		if err != nil {
			httperr.Write(w, r, err)
			return
		}
		version := crmcontracts.RowVersion(claim.Version)
		out.OwnerId, out.Version = openapi_types.UUID(claim.OwnerID), &version
		httperr.WriteJSON(w, http.StatusOK, out)
		return
	}
	claim, err := h.people.ClaimRecord(ctx, recordType, ids.UUID(id), ifVersion)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	version := crmcontracts.RowVersion(claim.Version)
	out.OwnerId, out.Version = openapi_types.UUID(claim.OwnerID), &version
	httperr.WriteJSON(w, http.StatusOK, out)
}
