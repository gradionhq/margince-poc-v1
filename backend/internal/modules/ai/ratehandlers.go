// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"errors"
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

func toContractModelRate(r ModelRateRow) crmcontracts.AiModelRate {
	return crmcontracts.AiModelRate{
		Provider:          r.Provider,
		ModelId:           r.ModelID,
		InputPerMtok:      r.InputUsd,
		OutputPerMtok:     r.OutputUsd,
		CacheReadPerMtok:  r.CacheReadUsd,
		CacheWritePerMtok: r.CacheWriteUsd,
		EffectiveDate:     openapi_types.Date{Time: r.EffectiveDate},
	}
}

// writeRateErr maps the ai rate store's typed validation error to a 422,
// falling through to the sentinel registry otherwise.
func writeRateErr(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *RateValidationError
	if errors.As(err, &invalid) {
		httperr.Write(w, r, httperr.Validation(invalid.Field, invalid.Code, invalid.Message))
		return
	}
	httperr.Write(w, r, err)
}

// ListAiModelRates returns the latest price per model, or (with both
// provider and model_id) one model's effective-dated history. Admin/ops-gated.
func (h Handlers) ListAiModelRates(w http.ResponseWriter, r *http.Request, params crmcontracts.ListAiModelRatesParams) {
	if err := auth.RequireHuman(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	var (
		rows []ModelRateRow
		err  error
	)
	if params.Provider != nil && *params.Provider != "" && params.ModelId != nil && *params.ModelId != "" {
		rows, err = h.rates.ModelRateHistory(r.Context(), *params.Provider, *params.ModelId)
	} else {
		rows, err = h.rates.ListLatestModelRates(r.Context())
	}
	if err != nil {
		writeRateErr(w, r, err)
		return
	}
	out := make([]crmcontracts.AiModelRate, 0, len(rows))
	for _, row := range rows {
		out = append(out, toContractModelRate(row))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.AiModelRateListResponse{Data: out})
}

// SetAiModelRate appends (or same-day corrects) one effective-dated model
// price. Human-admin/ops only; append-forward. Effective date defaults to today.
func (h Handlers) SetAiModelRate(w http.ResponseWriter, r *http.Request) {
	var req crmcontracts.SetAiModelRateRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	effective := time.Now().UTC()
	if req.EffectiveDate != nil {
		effective = req.EffectiveDate.Time
	}
	row, err := h.rates.SetModelRate(r.Context(), SetModelRateInput{
		Provider:      req.Provider,
		ModelID:       req.ModelId,
		InputUsd:      req.InputPerMtok,
		OutputUsd:     req.OutputPerMtok,
		CacheReadUsd:  req.CacheReadPerMtok,
		CacheWriteUsd: req.CacheWritePerMtok,
		EffectiveDate: effective,
	})
	if err != nil {
		writeRateErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, toContractModelRate(row))
}
