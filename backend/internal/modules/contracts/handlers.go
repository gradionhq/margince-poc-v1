// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package contracts

// The contract HTTP surface.

import (
	"errors"
	"net/http"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Handlers is this module's transport.
type Handlers struct {
	store *Store
}

// NewHandlers builds the contract handler set.
func NewHandlers(db *database.DB) Handlers {
	return Handlers{store: NewStore(db)}
}

// pathID converts a contract path parameter into its typed id.
func pathID(id crmcontracts.Id) ids.ContractID {
	return ids.ContractID{UUID: ids.UUID(id)}
}

// writeStoreErr maps this module's typed refusals onto the wire, falling
// through to the shared sentinel registry.
func writeStoreErr(w http.ResponseWriter, r *http.Request, err error) {
	var transition *InvalidStatusTransitionError
	if errors.As(err, &transition) {
		httperr.Write(w, r, httperr.Validation("status", "invalid_status_transition", transition.Error()))
		return
	}
	var check *ContractCheckError
	if errors.As(err, &check) {
		field := check.Field
		if field == "" {
			field = "contract"
		}
		httperr.Write(w, r, httperr.Validation(field, "contract_terms_contradict", check.Reason))
		return
	}
	httperr.Write(w, r, err)
}

// ListOrganizationContracts serves one account's agreements.
func (h Handlers) ListOrganizationContracts(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, params crmcontracts.ListOrganizationContractsParams) {
	in := ListContractsInput{
		OrganizationID: ids.OrganizationID{UUID: ids.UUID(id)},
		Cursor:         params.Cursor,
		Limit:          params.Limit,
	}
	if params.Status != nil {
		status := string(*params.Status)
		in.Status = &status
	}
	if params.UnderContractOnly != nil {
		in.UnderContractOnly = *params.UnderContractOnly
	}

	page, err := h.store.ListOrganizationContracts(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, page)
}

// CreateContract records an agreement.
func (h Handlers) CreateContract(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateContractParams) {
	var req crmcontracts.CreateContractRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in, err := createInput(req)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	contract, err := h.store.CreateContract(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/contracts/"+contract.Id.String())
	httperr.WriteJSON(w, http.StatusCreated, contract)
}

// GetContract serves one agreement.
func (h Handlers) GetContract(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	contract, err := h.store.GetContract(r.Context(), pathID(id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, contract)
}

// UpdateContract patches an agreement's recorded terms.
func (h Handlers) UpdateContract(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateContractParams) {
	var req map[string]any
	if !httperr.Decode(w, r, &req) {
		return
	}
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	contract, err := h.store.UpdateContract(r.Context(), pathID(id), req, ifVersion)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, contract)
}

// ArchiveContract soft-deletes an agreement.
func (h Handlers) ArchiveContract(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if err := h.store.ArchiveContract(r.Context(), pathID(id)); err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ChangeContractStatus asserts a new status.
func (h Handlers) ChangeContractStatus(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.ChangeContractStatusParams) {
	var req crmcontracts.ChangeContractStatusRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	contract, err := h.store.ChangeStatus(r.Context(), pathID(id), string(req.Status), ifVersion)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, contract)
}

// CancelContract records notice and when it takes effect.
func (h Handlers) CancelContract(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.CancelContractParams) {
	var req crmcontracts.CancelContractRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	contract, err := h.store.Cancel(r.Context(), pathID(id),
		req.CancellationNoticeOn.Time, req.CancellationEffectiveOn.Time, ifVersion)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, contract)
}

// RenewContract creates the successor and supersedes this agreement.
func (h Handlers) RenewContract(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.RenewContractParams) {
	var req crmcontracts.RenewContractRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	successor, err := h.store.Renew(r.Context(), pathID(id), renewInput(req), ifVersion)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/contracts/"+successor.Id.String())
	httperr.WriteJSON(w, http.StatusCreated, successor)
}

// createInput maps the wire body onto the store's input. Source is stamped
// here rather than taken from the body: how a record arrived is the server's
// observation, not the caller's claim.
func createInput(req crmcontracts.CreateContractRequest) (CreateContractInput, error) {
	// An absent organization_id decodes to the zero UUID with no error, which
	// would reach the lookup and answer "no such organization" for a company the
	// caller never named. Refuse it by name instead.
	if err := httperr.RequireBodyID("organization_id", ids.UUID(req.OrganizationId)); err != nil {
		return CreateContractInput{}, err
	}
	in := CreateContractInput{
		OrganizationID: ids.OrganizationID{UUID: ids.UUID(req.OrganizationId)},
		ContractNumber: req.ContractNumber,
		Title:          req.Title,
		ValueMinor:     req.ValueMinor,
		Currency:       req.Currency,
		ValueBasis:     BasisTotal,
		Source:         "manual",
	}
	if req.ValueBasis != nil {
		in.ValueBasis = string(*req.ValueBasis)
	}
	if req.DealId != nil {
		in.DealID = &ids.DealID{UUID: ids.UUID(*req.DealId)}
	}
	if req.ProjectId != nil {
		in.ProjectID = &ids.ProjectID{UUID: ids.UUID(*req.ProjectId)}
	}
	if req.AutoRenew != nil {
		in.AutoRenew = *req.AutoRenew
	}
	in.NoticePeriodDays = req.NoticePeriodDays
	in.StartsOn = timePtr(req.StartsOn)
	in.EndsOn = timePtr(req.EndsOn)
	in.RenewalOn = timePtr(req.RenewalOn)
	in.SignedOn = timePtr(req.SignedOn)
	return in, nil
}

// renewInput maps a renewal body onto a create. The counterparty is absent
// here on purpose — the store takes it from the predecessor, so a renewal can
// never quietly move an agreement to a different company.
func renewInput(req crmcontracts.RenewContractRequest) CreateContractInput {
	in := CreateContractInput{
		ContractNumber: req.ContractNumber,
		Title:          req.Title,
		ValueMinor:     req.ValueMinor,
		Currency:       req.Currency,
		ValueBasis:     string(req.ValueBasis),
		Source:         "renewal",
	}
	if req.AutoRenew != nil {
		in.AutoRenew = *req.AutoRenew
	}
	in.NoticePeriodDays = req.NoticePeriodDays
	in.StartsOn = timePtr(req.StartsOn)
	in.EndsOn = timePtr(req.EndsOn)
	in.RenewalOn = timePtr(req.RenewalOn)
	in.SignedOn = timePtr(req.SignedOn)
	return in
}

// timePtr converts a wire date into the store's time value.
func timePtr(d *openapi_types.Date) *time.Time {
	if d == nil {
		return nil
	}
	t := d.Time
	return &t
}
