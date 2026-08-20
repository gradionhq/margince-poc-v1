// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package commissions

// The commission HTTP surface.

import (
	"errors"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Handlers is this module's transport.
type Handlers struct {
	store *Store
}

// NewHandlers builds the commission handler set.
func NewHandlers(db *database.DB) Handlers {
	return Handlers{store: NewStore(db)}
}

// Store exposes the ledger writer to the composition layer, which injects the
// won-deal edge that accrues into it.
func (h Handlers) Store() *Store { return h.store }

// pathID converts a commission path parameter into its typed id.
func pathID(id crmcontracts.Id) ids.CommissionEntryID {
	return ids.CommissionEntryID{UUID: ids.UUID(id)}
}

// ListCommissionEntries serves the ledger page.
func (h Handlers) ListCommissionEntries(w http.ResponseWriter, r *http.Request, params crmcontracts.ListCommissionEntriesParams) {
	in := ListInput{Cursor: params.Cursor, Limit: params.Limit}
	if params.PartnerOrgId != nil {
		partner := ids.From[ids.OrganizationKind](ids.UUID(*params.PartnerOrgId))
		in.PartnerOrgID = &partner
	}
	if params.DealId != nil {
		deal := ids.From[ids.DealKind](ids.UUID(*params.DealId))
		in.DealID = &deal
	}
	if params.Status != nil {
		status := string(*params.Status)
		in.Status = &status
	}

	page, err := h.store.List(r.Context(), in)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, page)
}

// GetCommissionSummary serves the open-liability figure.
func (h Handlers) GetCommissionSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := h.store.Summary(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, summary)
}

// GetCommissionEntry serves one ledger row.
func (h Handlers) GetCommissionEntry(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	entry, err := h.store.GetCommissionEntry(r.Context(), pathID(id))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, entry)
}

// DecideCommissionEntry approves, pays, or voids one entry.
func (h Handlers) DecideCommissionEntry(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.DecideCommissionEntryParams) {
	var body crmcontracts.DecideCommissionRequest
	if !httperr.Decode(w, r, &body) {
		return
	}
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	entry, err := h.store.Decide(r.Context(), pathID(id), DecideInput{
		Decision:  string(body.Decision),
		Reason:    body.Reason,
		IfVersion: ifVersion,
	})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, entry)
}

// writeStoreErr maps this module's typed refusals onto the wire, falling
// through to the shared sentinel registry.
func writeStoreErr(w http.ResponseWriter, r *http.Request, err error) {
	var fault interface {
		FieldFault() (field, code, message string)
	}
	if errors.As(err, &fault) {
		field, code, message := fault.FieldFault()
		httperr.Write(w, r, httperr.Validation(field, code, message))
		return
	}
	httperr.Write(w, r, err)
}
