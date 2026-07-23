// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

func toContractFxRate(r FxRateRow) crmcontracts.FxRate {
	return crmcontracts.FxRate{
		FromCurrency:  r.FromCurrency,
		ToCurrency:    r.ToCurrency,
		Rate:          r.Rate,
		EffectiveDate: openapi_types.Date{Time: r.RateDate},
	}
}

// ListFxRates returns the latest rate per currency, or (with ?from=USD) one
// pair's effective-dated history. Admin/ops-gated in the store.
func (h Handlers) ListFxRates(w http.ResponseWriter, r *http.Request, params crmcontracts.ListFxRatesParams) {
	var (
		rows []FxRateRow
		err  error
	)
	if params.From != nil && *params.From != "" {
		rows, err = h.store.FxRateHistory(r.Context(), *params.From)
	} else {
		rows, err = h.store.ListLatestFxRates(r.Context())
	}
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	out := make([]crmcontracts.FxRate, 0, len(rows))
	for _, row := range rows {
		out = append(out, toContractFxRate(row))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.FxRateListResponse{Data: out})
}

// SetFxRate appends (or same-day corrects) one effective-dated FX rate.
// Human-admin/ops only; append-forward. Effective date defaults to today.
func (h Handlers) SetFxRate(w http.ResponseWriter, r *http.Request) {
	var req crmcontracts.SetFxRateRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	effective := time.Now().UTC()
	if req.EffectiveDate != nil {
		effective = req.EffectiveDate.Time
	}
	row, err := h.store.SetFxRate(r.Context(), SetFxRateInput{
		FromCurrency:  req.FromCurrency,
		Rate:          req.Rate,
		EffectiveDate: effective,
	})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, toContractFxRate(row))
}
