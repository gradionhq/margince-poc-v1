// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The capture exclusion surface: list the rules that bind the caller's
// connections, add one, lift one. Thin transport — the capture store owns the
// gate (admin/ops for a workspace rule, the user themselves for their own) and
// the audited write.

import (
	"encoding/json"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type captureExclusionHandlers struct {
	store *capture.ExclusionStore
}

func (h captureExclusionHandlers) ListCaptureExclusions(w http.ResponseWriter, r *http.Request) {
	rules, err := h.store.List(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	out := crmcontracts.CaptureExclusionListResponse{Data: make([]crmcontracts.CaptureExclusion, 0, len(rules))}
	for _, rule := range rules {
		out.Data = append(out.Data, toContractExclusion(rule))
	}
	httperr.WriteJSON(w, http.StatusOK, out)
}

func (h captureExclusionHandlers) CreateCaptureExclusion(w http.ResponseWriter, r *http.Request) {
	var req crmcontracts.CreateCaptureExclusionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.Write(w, r, httperr.Validation("body", "invalid_json", "request body is not valid JSON"))
		return
	}
	rule, err := h.store.Add(r.Context(), string(req.Scope), string(req.Kind), req.Value)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, toContractExclusion(rule))
}

func (h captureExclusionHandlers) DeleteCaptureExclusion(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if err := h.store.Remove(r.Context(), ids.UUID(id)); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toContractExclusion(rule capture.Exclusion) crmcontracts.CaptureExclusion {
	return crmcontracts.CaptureExclusion{
		Id:        openapi_types.UUID(rule.ID),
		Scope:     crmcontracts.CaptureExclusionScope(rule.Scope),
		Kind:      crmcontracts.CaptureExclusionKind(rule.Kind),
		Value:     rule.Value,
		CreatedAt: rule.CreatedAt,
	}
}
