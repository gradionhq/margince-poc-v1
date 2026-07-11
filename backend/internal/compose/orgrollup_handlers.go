// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The HTTP transport for GET /organizations/{id}/hierarchy-rollup
// (RD-T04): binds the query default, calls the gated aggregate read in
// orgrollupread.go, and maps its result (and its one typed failure) onto
// the generated wire shape. No aggregation logic lives here — this file
// is pure edge + shape translation.

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// orgRollupHandlers shadows the generated GetOrganizationHierarchyRollup
// stub over the gated read.
type orgRollupHandlers struct {
	pool *pgxpool.Pool
}

// GetOrganizationHierarchyRollup implements (GET
// /organizations/{id}/hierarchy-rollup). An absent scope defaults to the
// contract's "tree"; anything else reaches OrgHierarchyRollup verbatim,
// which is the read's own refusal point for an out-of-vocabulary value.
func (h orgRollupHandlers) GetOrganizationHierarchyRollup(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, params crmcontracts.GetOrganizationHierarchyRollupParams) {
	scope := orgRollupScopeTree
	if params.Scope != nil {
		scope = string(*params.Scope)
	}

	result, err := OrgHierarchyRollup(r.Context(), h.pool, ids.UUID(id), scope)
	if err != nil {
		var fxErr *FXRateUnavailableError
		if errors.As(err, &fxErr) {
			asOf := fxErr.AsOf.Format(time.DateOnly)
			httperr.Write(w, r, &httperr.DetailedError{
				Status: http.StatusUnprocessableEntity,
				Code:   "fx_rate_unavailable",
				Detail: fmt.Sprintf("no stored FX rate for %s as of %s", fxErr.Currency, asOf),
				Details: map[string]any{
					"currency": fxErr.Currency, //nolint:goconst // a details-map key, coincidentally the same text as a SQL column/report-field name elsewhere in this package — not the same concept
					"as_of":    asOf,
				},
			})
			return
		}
		httperr.Write(w, r, err)
		return
	}

	httperr.WriteJSON(w, http.StatusOK, orgRollupToWire(result))
}

// orgRollupToWire renders the computed result onto the contract's
// OrganizationHierarchyRollup: both money figures carry the read's
// resolved base currency, and restricted_excluded is always a real
// (possibly empty) array — OrgRollupResult never leaves it nil.
func orgRollupToWire(result OrgRollupResult) crmcontracts.OrganizationHierarchyRollup {
	// The generated schema left restricted_excluded's item shape unnamed
	// (see api_gen.go), so its field spelling — Id, not ID — is dictated
	// by the wire contract this literal must match, not by house style.
	restricted := make([]struct {
		DisplayName string             `json:"display_name"`
		Id          openapi_types.UUID `json:"id"` //nolint:staticcheck // matches the generated OrganizationHierarchyRollup.RestrictedExcluded item shape
	}, len(result.RestrictedExcluded))
	for i, n := range result.RestrictedExcluded {
		restricted[i] = struct {
			DisplayName string             `json:"display_name"`
			Id          openapi_types.UUID `json:"id"` //nolint:staticcheck // matches the generated OrganizationHierarchyRollup.RestrictedExcluded item shape
		}{DisplayName: n.DisplayName, Id: openapi_types.UUID(n.ID)}
	}

	return crmcontracts.OrganizationHierarchyRollup{
		RootId:                 openapi_types.UUID(result.RootID),
		Scope:                  crmcontracts.OrganizationHierarchyRollupScope(result.Scope),
		WeightedPipeline:       orgRollupMoney(result.WeightedPipelineMinor, result.BaseCurrency),
		ClosedWon:              orgRollupMoney(result.ClosedWonMinor, result.BaseCurrency),
		ActivityCount30d:       result.ActivityCount30d,
		AggregatedAccountCount: result.AggregatedAccountCount,
		RestrictedExcluded:     restricted,
		ComputedAt:             result.ComputedAt,
	}
}

// orgRollupMoney renders one rollup figure as the wire Money shape — both
// fields always present, since every rollup measure is a real computed
// total in the workspace base currency, never a client-nullable input.
func orgRollupMoney(minor int64, currency string) crmcontracts.Money {
	amount := minor
	cur := currency
	return crmcontracts.Money{AmountMinor: &amount, Currency: &cur}
}
