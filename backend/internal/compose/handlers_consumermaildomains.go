// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The workspace's consumer-mail list surface (CAP-PARAM-5): read what the
// workspace added to and carved out of the shipped baseline (every role),
// change it (admin/ops, human-only). Thin transport — the capture store owns
// the RBAC gate, the normalization and the audit-only write.

import (
	"encoding/json"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type consumerMailDomainHandlers struct {
	store *capture.FreemailDomainStore
}

func (h consumerMailDomainHandlers) ListConsumerMailDomains(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "ListConsumerMailDomains")
		return
	}
	entries, err := h.store.List(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	out := make([]crmcontracts.ConsumerMailDomain, 0, len(entries))
	for _, e := range entries {
		out = append(out, toContractConsumerMailDomain(e))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ConsumerMailDomainListResponse{Data: out})
}

func (h consumerMailDomainHandlers) AddConsumerMailDomain(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "AddConsumerMailDomain")
		return
	}
	// Human-only (x-agent-access): an agent never changes a workspace-wide
	// capture posture. The store re-checks the admin/ops object grant.
	if err := auth.RequireHuman(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	var req crmcontracts.AddConsumerMailDomainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.Write(w, r, httperr.Validation("body", "invalid_json", "request body is not valid JSON"))
		return
	}
	// Vetted here so a bad domain answers 422 naming what is wrong with it,
	// rather than reaching the store and surfacing as a constraint violation.
	if _, err := capture.ValidFreemailEntry(req.Domain, string(req.Kind)); err != nil {
		httperr.Write(w, r, httperr.Validation("domain", "invalid_domain", err.Error()))
		return
	}
	entry, err := h.store.Add(r.Context(), req.Domain, string(req.Kind))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, toContractConsumerMailDomain(entry))
}

func (h consumerMailDomainHandlers) RemoveConsumerMailDomain(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "RemoveConsumerMailDomain")
		return
	}
	if err := auth.RequireHuman(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	if err := h.store.Remove(r.Context(), ids.UUID(id)); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// toContractConsumerMailDomain maps one stored entry onto the wire shape.
func toContractConsumerMailDomain(e capture.FreemailDomain) crmcontracts.ConsumerMailDomain {
	return crmcontracts.ConsumerMailDomain{
		Id:        openapi_types.UUID(e.ID),
		Domain:    e.Domain,
		Kind:      crmcontracts.ConsumerMailDomainKind(e.Kind),
		CreatedAt: &e.CreatedAt,
	}
}
