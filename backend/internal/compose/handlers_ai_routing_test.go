// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The wire mapping, which is where a binding loses a field silently. A dropped
// base_url or input list does not fail anything — it routes to a different
// endpoint, or refuses a document the operator bound a model to carry.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestABindingSurvivesTheRoundTripToTheWireAndBack(t *testing.T) {
	original := ai.RoutingConfig{
		Profile: ai.ProfileEUHosted,
		Tiers: map[ai.Tier]ai.ProviderConfig{
			ai.TierPremium: {
				Provider: "gemini", Model: "gemini-3.5-flash",
				BaseURL: "https://eu-gateway.example", Input: []string{"text", "image"},
			},
			ai.TierCheapCloud: {Provider: "gemini", Model: "gemini-3.1-flash-lite"},
		},
		Embeddings: ai.EmbeddingsConfig{
			ProviderConfig: ai.ProviderConfig{Provider: "gemini", Model: "gemini-embedding-001"},
			Dimensions:     1536,
		},
	}

	back := fromContractAiRouting(toContractAiRouting(original))

	if back.Profile != original.Profile {
		t.Errorf("profile = %q, want %q", back.Profile, original.Profile)
	}
	if len(back.Tiers) != len(original.Tiers) {
		t.Fatalf("tiers = %v, want %d of them", back.Tiers, len(original.Tiers))
	}
	for tier, want := range original.Tiers {
		got := back.Tiers[tier]
		if got.Provider != want.Provider || got.Model != want.Model || got.BaseURL != want.BaseURL {
			t.Errorf("tier %s = %+v, want %+v", tier, got, want)
		}
		if len(got.Input) != len(want.Input) {
			t.Errorf("tier %s input = %v, want %v — a narrowed carriage that vanishes silently widens what the model may be sent", tier, got.Input, want.Input)
		}
	}
	if back.Embeddings.Dimensions != original.Embeddings.Dimensions {
		t.Errorf("embeddings width = %d, want %d", back.Embeddings.Dimensions, original.Embeddings.Dimensions)
	}
}

// Absent and empty are different to an operator and the same to a Go zero
// value. "No base_url override" must not read back as "base_url set to the
// empty string", which is why these fields are pointers on the wire.
func TestAnUnsetOptionalIsAbsentRatherThanEmpty(t *testing.T) {
	wire := toContractAiRouting(ai.RoutingConfig{
		Profile: ai.ProfileEUHosted,
		Tiers:   map[ai.Tier]ai.ProviderConfig{ai.TierPremium: {Provider: "fake", Model: "m"}},
	})
	tier := wire.Tiers["premium"]
	if tier.BaseUrl != nil {
		t.Errorf("base_url = %q, want absent", *tier.BaseUrl)
	}
	if tier.Input != nil {
		t.Errorf("input = %v, want absent", *tier.Input)
	}
	// Reported as STORED, not as defaulted: a GET → PUT round-trip must not
	// freeze today's compiled default into the document as though an operator
	// had chosen it, because then tomorrow's default would not reach them.
	if wire.Embeddings.Dimensions != nil {
		t.Errorf("dimensions = %d, want absent for an unset width", *wire.Embeddings.Dimensions)
	}
}

// An unbound installation answers `{}`, never null. A null leaves a client
// unable to tell "nothing is bound" from "the field was omitted".
func TestAnUnboundInstallationReportsAnEmptyTierMapNotNull(t *testing.T) {
	wire := toContractAiRouting(ai.RoutingConfig{})
	if wire.Tiers == nil {
		t.Error("tiers is null; an unbound installation must say so with an empty object")
	}
	if len(wire.Tiers) != 0 {
		t.Errorf("tiers = %v, want empty", wire.Tiers)
	}
}

// A submitted document with no tiers maps to the unconfigured config, which is
// what lets an operator unbind every model deliberately.
func TestASubmittedDocumentWithNoTiersIsUnconfigured(t *testing.T) {
	cfg := fromContractAiRouting(crmcontracts.AiRouting{Profile: "eu_hosted"})
	if !cfg.Unconfigured() {
		t.Errorf("tiers = %v, want unconfigured", cfg.Tiers)
	}
}

// agentReq is a request carrying an AGENT principal — a passport-authenticated
// caller rather than a signed-in human.
func agentReq(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPut, "/v1/ai/routing", strings.NewReader(body))
	ctx := principal.WithWorkspaceID(req.Context(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:" + ids.NewV7().String(),
		Permissions: principal.Permissions{
			// Deliberately granted the object. The refusal under test must not
			// depend on the agent lacking the grant — an agent could hold one
			// through a passport whose scopes admit it.
			Objects:  map[string]principal.ObjectGrant{"ai_routing": {Read: true, Update: true}},
			RowScope: principal.RowScopeAll,
		},
	})
	return req.WithContext(ctx)
}

// An agent never re-points which vendor processes the installation's
// correspondence, WHATEVER its passport scopes admit. This is the governance
// claim the contract makes with x-agent-access: human-only, and it must hold at
// the handler rather than only in the document.
func TestAnAgentCannotReplaceTheModelBinding(t *testing.T) {
	h := aiRoutingHandlers{store: &ai.RoutingStore{}}
	rec := httptest.NewRecorder()

	h.ReplaceAiRouting(rec, agentReq(`{"profile":"eu_hosted","tiers":{}}`))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d — an agent re-pointed the model binding", rec.Code, http.StatusForbidden)
	}
}

// A malformed body is a 422 naming the fault, not a panic and not a partially
// applied binding.
func TestAMalformedBindingDocumentIsRefused(t *testing.T) {
	h := aiRoutingHandlers{store: &ai.RoutingStore{}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v1/ai/routing", strings.NewReader("{not json"))
	ctx := principal.WithWorkspaceID(req.Context(), ids.NewV7())
	req = req.WithContext(principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"ai_routing": {Read: true, Update: true}},
			RowScope: principal.RowScopeAll,
		},
	}))

	h.ReplaceAiRouting(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want %d for a body that is not JSON", rec.Code, http.StatusUnprocessableEntity)
	}
}

// A role that wired no store answers 501, not a nil dereference. Both verbs,
// because a role wires them together and a half-wired surface is the state
// nobody would think to check.
func TestAnUnwiredRoutingSurfaceIsNotImplemented(t *testing.T) {
	var h aiRoutingHandlers
	for name, call := range map[string]func(http.ResponseWriter, *http.Request){
		"GetAiRouting":     h.GetAiRouting,
		"ReplaceAiRouting": h.ReplaceAiRouting,
	} {
		rec := httptest.NewRecorder()
		call(rec, httptest.NewRequest(http.MethodGet, "/v1/ai/routing", nil))
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s: status = %d, want %d", name, rec.Code, http.StatusNotImplemented)
		}
	}
}
