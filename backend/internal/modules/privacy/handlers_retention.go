// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The retention-authoring transport (GCS-WIRE-1..5): the six operations that
// turn the storage-limitation ladder from something we ship into something an
// admin owns.
//
// Every scope arriving here is resolved through ParseRetentionScope before it
// reaches the store, so the ONE place that decides what is authorable is the
// evaluator's own selector table.

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ListRetentionPolicies implements (GET /retention-policies).
func (h Handlers) ListRetentionPolicies(w http.ResponseWriter, r *http.Request) {
	policies, err := h.policies.List(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	data := make([]crmcontracts.RetentionPolicy, 0, len(policies))
	for _, policy := range policies {
		data = append(data, policyToWire(policy))
	}
	// One page, always: the ladder is bounded by the authorable scope count, so
	// the collection shape keeps its `page` envelope and never fills a second one.
	httperr.WriteJSON(w, http.StatusOK, struct {
		Data []crmcontracts.RetentionPolicy `json:"data"`
		Page crmcontracts.PageInfo          `json:"page"`
	}{Data: data, Page: crmcontracts.PageInfo{HasMore: false}})
}

// CreateRetentionPolicy implements (POST /retention-policies).
func (h Handlers) CreateRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	var req crmcontracts.CreateRetentionPolicyRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	scope, err := ParseRetentionScope(string(req.Scope))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	// An omitted `enabled` means true — a policy authored to sit inert is the
	// unusual intent, and the contract's default says so.
	enabled := req.Enabled == nil || *req.Enabled
	policy, err := h.policies.Create(r.Context(), PolicyInput{
		Scope:       scope,
		RetainDays:  req.RetainDays,
		Action:      string(req.Action),
		LawfulBasis: req.LawfulBasis,
		Enabled:     enabled,
	})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, policyToWire(policy))
}

// UpdateRetentionPolicy implements (PATCH /retention-policies/{id}).
func (h Handlers) UpdateRetentionPolicy(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req crmcontracts.UpdateRetentionPolicyRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	patch := PolicyPatch{
		RetainDays:  req.RetainDays,
		LawfulBasis: req.LawfulBasis,
		Enabled:     req.Enabled,
	}
	if req.Action != nil {
		action := string(*req.Action)
		patch.Action = &action
	}
	policy, err := h.policies.Update(r.Context(), ids.UUID(id), patch)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, policyToWire(policy))
}

// DeleteRetentionPolicy implements (DELETE /retention-policies/{id}).
func (h Handlers) DeleteRetentionPolicy(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if err := h.policies.Delete(r.Context(), ids.UUID(id)); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetRetentionSettings implements (GET /retention/settings).
func (h Handlers) GetRetentionSettings(w http.ResponseWriter, r *http.Request) {
	retainOnly, err := h.posture.Posture(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.RetentionSettings{RetainOnly: retainOnly})
}

// UpdateRetentionSettings implements (PATCH /retention/settings).
func (h Handlers) UpdateRetentionSettings(w http.ResponseWriter, r *http.Request) {
	var req crmcontracts.UpdateRetentionSettingsRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	// A nil field is a patch that names nothing: the store answers with the
	// current posture rather than refusing, so an idempotent retry is not an
	// error. It still takes the update grant, which is why the branch lives
	// there and not here.
	retainOnly, err := h.posture.SetPosture(r.Context(), req.RetainOnly)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.RetentionSettings{RetainOnly: retainOnly})
}

// policyToWire renders a stored policy. `scope` is the authoritative pair;
// object_type and category are split out beside it so a client can group and
// label rows without parsing the enum.
func policyToWire(p Policy) crmcontracts.RetentionPolicy {
	out := crmcontracts.RetentionPolicy{
		Id:                  openapi_types.UUID(p.ID),
		Scope:               crmcontracts.RetentionScope(p.Scope.String()),
		ObjectType:          p.Scope.ObjectType,
		RetainDays:          p.RetainDays,
		Action:              crmcontracts.RetentionAction(p.Action),
		LawfulBasis:         p.LawfulBasis,
		Enabled:             p.Enabled,
		SuppressedByPosture: p.SuppressedByPosture,
	}
	out.Category = p.Scope.CategoryPtr()
	return out
}
