// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"errors"
	"net/http"
	"strings"
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
	// Distinguish parameter PRESENCE from a non-blank value: a blank or
	// whitespace-only `?provider=` is treated as absent, so it can't slip
	// through as "one half of the pair" or query an empty key.
	provider, modelID := "", ""
	if params.Provider != nil {
		provider = strings.TrimSpace(*params.Provider)
	}
	if params.ModelId != nil {
		modelID = strings.TrimSpace(*params.ModelId)
	}
	hasProvider, hasModel := provider != "", modelID != ""
	if hasProvider != hasModel {
		// One half of the history key without the other is ambiguous — reject
		// it (naming the MISSING half) rather than silently returning the whole
		// latest-price sheet, which would leak more cost data than requested.
		missing := "provider"
		if hasProvider {
			missing = "model_id"
		}
		httperr.Write(w, r, httperr.Validation(missing, "rate_history_pair",
			"provider and model_id must be supplied together to fetch a model's history"))
		return
	}
	var (
		rows []ModelRateRow
		err  error
	)
	if hasProvider {
		rows, err = h.rates.ModelRateHistory(r.Context(), provider, modelID)
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
	// Human-only at the handler too, not only via the agent gate (which skips
	// GETs): the POST is x-agent-access: human-only, so an AGENT principal is
	// refused here the same way the GET is — belt-and-suspenders enforcement.
	if err := auth.RequireHuman(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	var req crmcontracts.SetAiModelRateRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	// Leave EffectiveDate zero when omitted — the store derives "today" from its
	// in-transaction clock sample, so an omitted-date write that waits for the
	// pool across UTC midnight isn't rejected as past against a stale now().
	var effective time.Time
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
