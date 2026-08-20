// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The tier→model binding surface (ai-operational-spec §1.4): read what this
// installation is bound to, replace it without a restart.
//
// Thin transport. The ai store owns the RBAC gate, the validation the routing
// file was always held to, and the audit-only write; what this file adds is the
// wire mapping and the human-only refusal.

import (
	"encoding/json"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

type aiRoutingHandlers struct {
	store *ai.RoutingStore
}

func (h aiRoutingHandlers) GetAiRouting(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "GetAiRouting")
		return
	}
	cfg, err := h.store.Get(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, toContractAiRouting(cfg))
}

func (h aiRoutingHandlers) ReplaceAiRouting(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "ReplaceAiRouting")
		return
	}
	// Human-only (x-agent-access). An agent never re-points which vendor
	// processes the installation's correspondence, whatever its passport scopes
	// admit. The store re-checks the admin/ops object grant.
	if err := auth.RequireHuman(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	var req crmcontracts.AiRouting
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.Write(w, r, httperr.Validation("body", "invalid_json", "request body is not valid JSON"))
		return
	}
	cfg, err := h.store.Replace(r.Context(), fromContractAiRouting(req))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, toContractAiRouting(cfg))
}

// toContractAiRouting maps a stored binding onto the wire shape.
//
// Tiers is always a map, never nil: an unbound installation answers `{}`, which
// says "nothing is bound", where a null would leave a client guessing whether
// the field was omitted or the read failed.
func toContractAiRouting(cfg ai.RoutingConfig) crmcontracts.AiRouting {
	tiers := make(map[string]crmcontracts.AiTierBinding, len(cfg.Tiers))
	for tier, b := range cfg.Tiers {
		tiers[string(tier)] = crmcontracts.AiTierBinding{
			Provider: b.Provider, Model: b.Model,
			BaseUrl: optionalString(b.BaseURL), Input: optionalStrings(b.Input),
		}
	}
	return crmcontracts.AiRouting{
		Profile: crmcontracts.AiRoutingProfile(cfg.Profile),
		Tiers:   tiers,
		Embeddings: crmcontracts.AiEmbeddingsBinding{
			Provider: cfg.Embeddings.Provider, Model: cfg.Embeddings.Model,
			BaseUrl: optionalString(cfg.Embeddings.BaseURL),
			Input:   optionalStrings(cfg.Embeddings.Input),
			// Reported as stored rather than as defaulted, so a round-trip of
			// GET → PUT does not silently freeze today's compiled default into
			// the document as though an operator had chosen it.
			Dimensions: optionalInt(cfg.Embeddings.Dimensions),
		},
	}
}

// fromContractAiRouting maps a submitted document onto a routing config. It
// validates nothing: the store holds it to the same bar the file loader
// applies, so there is exactly one place a bad binding is refused.
func fromContractAiRouting(req crmcontracts.AiRouting) ai.RoutingConfig {
	cfg := ai.RoutingConfig{
		Profile:    ai.Profile(req.Profile),
		Embeddings: ai.EmbeddingsConfig{ProviderConfig: tierFromWire(req.Embeddings.Provider, req.Embeddings.Model, req.Embeddings.BaseUrl, req.Embeddings.Input)},
	}
	if req.Embeddings.Dimensions != nil {
		cfg.Embeddings.Dimensions = *req.Embeddings.Dimensions
	}
	if len(req.Tiers) > 0 {
		cfg.Tiers = make(map[ai.Tier]ai.ProviderConfig, len(req.Tiers))
		for name, b := range req.Tiers {
			cfg.Tiers[ai.Tier(name)] = tierFromWire(b.Provider, b.Model, b.BaseUrl, b.Input)
		}
	}
	return cfg
}

func tierFromWire(provider, model string, baseURL *string, input *[]string) ai.ProviderConfig {
	out := ai.ProviderConfig{Provider: provider, Model: model}
	if baseURL != nil {
		out.BaseURL = *baseURL
	}
	if input != nil {
		out.Input = *input
	}
	return out
}

// The three omitempty helpers exist so an absent value reads as absent rather
// than as a deliberate empty: "no base_url override" and "base_url set to the
// empty string" are the same to a Go zero value and different to an operator.
func optionalString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func optionalStrings(v []string) *[]string {
	if len(v) == 0 {
		return nil
	}
	return &v
}

func optionalInt(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}
