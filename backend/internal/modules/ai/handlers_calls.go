// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// WithPayloadCaptureFlag reports the same deployment posture that controls
// model-path capture, distinguishing disabled capture from an older call.
func (h Handlers) WithPayloadCaptureFlag(enabled bool) Handlers {
	h.capturePayloads = enabled
	return h
}

// ListAiCalls implements (GET /ai/calls).
func (h Handlers) ListAiCalls(
	w http.ResponseWriter,
	r *http.Request,
	params crmcontracts.ListAiCallsParams,
) {
	page, err := h.calls.ListCalls(r.Context(), params.Cursor, params.Limit, params.Task)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	data := make([]crmcontracts.AiCallSummary, 0, len(page.Items))
	for _, item := range page.Items {
		data = append(data, wireAiCallSummary(item))
	}
	response := crmcontracts.AiCallListResponse{
		Data:                  data,
		Page:                  crmcontracts.PageInfo{HasMore: page.HasMore},
		PayloadCaptureEnabled: h.capturePayloads,
	}
	if page.NextCursor != "" {
		response.Page.NextCursor = &page.NextCursor
	}
	httperr.WriteJSON(w, http.StatusOK, response)
}

// GetAiCall implements (GET /ai/calls/{id}).
func (h Handlers) GetAiCall(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	detail, err := h.calls.GetCall(r.Context(), ids.UUID(id))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireAiCall(detail))
}

func wireAiCallSummary(summary CallSummary) crmcontracts.AiCallSummary {
	return crmcontracts.AiCallSummary{
		Id: openapi_types.UUID(summary.ID), OccurredAt: summary.OccurredAt,
		Task: summary.Task, Tier: summary.Tier, Provider: summary.Provider,
		ModelId: summary.ModelID, ServedModel: summary.ServedModel,
		CallsAttempted: summary.Attempt, TokensIn: int(summary.TokensIn),
		TokensOut: int(summary.TokensOut), ReasoningTokens: int(summary.ReasoningTokens),
		CachedTokens: int(summary.CachedTokens), LatencyMs: int(summary.LatencyMS),
		CacheHit: summary.CacheHit, Degraded: summary.Degraded,
		ErrorSentinel: summary.ErrorSentinel, HasPayload: summary.HasPayload,
	}
}

func wireAiCall(detail CallDetail) crmcontracts.AiCall {
	summary := wireAiCallSummary(detail.CallSummary)
	out := crmcontracts.AiCall{
		Id: summary.Id, OccurredAt: summary.OccurredAt, Task: summary.Task,
		Tier: summary.Tier, Provider: summary.Provider, ModelId: summary.ModelId,
		ServedModel: summary.ServedModel, CallsAttempted: summary.CallsAttempted,
		TokensIn: summary.TokensIn, TokensOut: summary.TokensOut,
		ReasoningTokens: summary.ReasoningTokens, CachedTokens: summary.CachedTokens,
		LatencyMs: summary.LatencyMs, CacheHit: summary.CacheHit,
		Degraded: summary.Degraded, ErrorSentinel: summary.ErrorSentinel,
		HasPayload: summary.HasPayload, ServedIdentitySource: detail.ServedIdentitySource,
		ConfigHash: detail.ConfigHash, ContextScopes: detail.ContextScopes,
		ContextFingerprint: detail.ContextFingerprint,
		Attempts:           make([]crmcontracts.AiCallAttempt, 0, len(detail.Attempts)),
		PayloadCaptured:    detail.Payload != nil,
	}
	if detail.CorrelationID != nil {
		value := openapi_types.UUID(*detail.CorrelationID)
		out.CorrelationId = &value
	}
	if detail.AgentRunID != nil {
		value := openapi_types.UUID(*detail.AgentRunID)
		out.AgentRunId = &value
	}
	for _, attempt := range detail.Attempts {
		out.Attempts = append(out.Attempts, crmcontracts.AiCallAttempt{
			Attempt: attempt.Attempt, IsTerminal: attempt.IsTerminal,
			AttemptReason: attempt.AttemptReason, ErrorSentinel: attempt.ErrorSentinel,
			TokensIn: int(attempt.TokensIn), TokensOut: int(attempt.TokensOut),
			LatencyMs: int(attempt.LatencyMS), OccurredAt: attempt.OccurredAt,
		})
	}
	if detail.Payload != nil {
		out.Payload = &struct {
			Request  interface{} `json:"request"`
			Response interface{} `json:"response"`
		}{Request: detail.Payload.Request, Response: detail.Payload.Response}
	}
	return out
}
