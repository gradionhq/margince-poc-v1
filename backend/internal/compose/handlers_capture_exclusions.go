// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The RC-2 personal-mail exclusion CRUD (capture.md CAP-WIRE-2):
// list/create/delete over a human's own bounded rule set. Human-only by
// contract (x-agent-access), so an agent never widens or narrows a human's
// personal-mail boundary; the store enforces the same at the principal.
// Thin transport — validate the bounded (kind, value), delegate to the
// capture store, map to the wire shape.

import (
	"encoding/json"
	"net/http"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type captureExclusionHandlers struct {
	store *capture.Exclusions
}

func (h captureExclusionHandlers) ListCaptureExclusions(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "ListCaptureExclusions")
		return
	}
	rules, err := h.store.List(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	resp := crmcontracts.CaptureExclusionRuleListResponse{
		Data: make([]crmcontracts.CaptureExclusionRule, 0, len(rules)),
	}
	for _, rule := range rules {
		resp.Data = append(resp.Data, toContractExclusion(rule))
	}
	httperr.WriteJSON(w, http.StatusOK, resp)
}

func (h captureExclusionHandlers) CreateCaptureExclusion(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "CreateCaptureExclusion")
		return
	}
	var req crmcontracts.CreateCaptureExclusionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.Write(w, r, httperr.Validation("body", "invalid_json", "request body is not valid JSON"))
		return
	}
	if !req.Kind.Valid() {
		httperr.Write(w, r, httperr.Validation("kind", "invalid_kind", "kind must be one of sender_domain, recipient_domain, label"))
		return
	}
	if strings.TrimSpace(req.Value) == "" {
		httperr.Write(w, r, httperr.Validation("value", "required", "value is required"))
		return
	}
	rule, err := h.store.Create(r.Context(), string(req.Kind), req.Value)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, toContractExclusion(rule))
}

func (h captureExclusionHandlers) DeleteCaptureExclusion(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "DeleteCaptureExclusion")
		return
	}
	if err := h.store.Delete(r.Context(), ids.UUID(id)); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// toContractExclusion maps a stored rule onto the wire shape.
func toContractExclusion(rule capture.ExclusionRule) crmcontracts.CaptureExclusionRule {
	c := crmcontracts.CaptureExclusionRule{
		Id:    openapi_types.UUID(rule.ID),
		Kind:  crmcontracts.CaptureExclusionRuleKind(rule.Kind),
		Value: rule.Value,
	}
	if !rule.CreatedAt.IsZero() {
		created := rule.CreatedAt
		c.CreatedAt = &created
	}
	return c
}
