// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestPublicProfileDerivesSafeRoutingPosture(t *testing.T) {
	tests := []struct {
		name      string
		state     string
		config    RoutingConfig
		wantState string
		wantMode  string
		want      []string
	}{
		{name: "unconfigured", state: "unconfigured", wantState: "unconfigured", wantMode: "none", want: []string{}},
		{name: "development flag", state: "fake", config: FakeRoutingConfig(), wantState: "development", wantMode: "development", want: []string{}},
		{
			name:  "cloud",
			state: "configured",
			config: RoutingConfig{Tiers: map[Tier]ProviderConfig{
				TierCheapCloud: {Provider: providerAnthropic},
				TierPremium:    {Provider: providerAnthropic},
			}, Embeddings: EmbeddingsConfig{ProviderConfig: ProviderConfig{Provider: providerGemini}}},
			wantState: "configured", wantMode: "cloud", want: []string{"anthropic", "gemini"},
		},
		{
			name:  "local",
			state: "configured",
			config: RoutingConfig{Tiers: map[Tier]ProviderConfig{
				TierLocalSmall: {Provider: providerOllama},
			}, Embeddings: EmbeddingsConfig{ProviderConfig: ProviderConfig{Provider: providerOllama}}},
			wantState: "configured", wantMode: "local", want: []string{"ollama"},
		},
		{
			name:  "hybrid omits fake",
			state: "configured",
			config: RoutingConfig{Tiers: map[Tier]ProviderConfig{
				TierLocalSmall: {Provider: providerOllama},
				TierPremium:    {Provider: providerAnthropic},
			}, Embeddings: EmbeddingsConfig{ProviderConfig: ProviderConfig{Provider: ProviderFake}}},
			wantState: "configured", wantMode: "hybrid", want: []string{"anthropic", "ollama"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewPublicProfile(tt.state, tt.config)
			providers := make([]string, len(got.Providers))
			for i, provider := range got.Providers {
				providers[i] = string(provider)
			}
			if string(got.State) != tt.wantState || string(got.InferenceMode) != tt.wantMode || !reflect.DeepEqual(providers, tt.want) {
				t.Fatalf("profile = %+v, want state=%s mode=%s providers=%v", got, tt.wantState, tt.wantMode, tt.want)
			}
		})
	}
}

func TestGetAssistantProfileReturnsOnlyPublicFields(t *testing.T) {
	h := NewHandlers(nil, nil).WithPublicProfile(NewPublicProfile("configured", RoutingConfig{
		Tiers: map[Tier]ProviderConfig{
			TierLocalSmall: {Provider: providerOllama, Model: "gemma3"},
			TierPremium:    {Provider: providerAnthropic, Model: "claude-sonnet"},
		},
	}))
	recorder := httptest.NewRecorder()
	h.GetAssistantProfile(recorder, httptest.NewRequest("GET", "/v1/assistant/profile", nil))

	var body map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	wantKeys := []string{"inference_mode", "kind", "name", "providers", "state"}
	for _, key := range wantKeys {
		if _, ok := body[key]; !ok {
			t.Errorf("response misses %q: %s", key, recorder.Body.String())
		}
	}
	if len(body) != len(wantKeys) {
		t.Fatalf("response exposes fields outside the public contract: %s", recorder.Body.String())
	}
	for _, forbidden := range []string{"endpoint", "budget", "workspace", "key"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Errorf("response contains forbidden %q detail: %s", forbidden, recorder.Body.String())
		}
	}
	for _, forbidden := range []string{"claude-sonnet", "gemma3", "premium", "local_small"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("public response exposes %q: %s", forbidden, recorder.Body.String())
		}
	}
}

func TestGetAiProfileReturnsAuthenticatedConfiguredModelsAndLocalDefaults(t *testing.T) {
	h := NewHandlers(nil, nil).WithPublicProfile(NewPublicProfile("configured", RoutingConfig{
		Tiers: map[Tier]ProviderConfig{
			TierLocalSmall: {Provider: providerOllama},
			TierLocalLarge: {Provider: providerVLLM},
			TierPremium:    {Provider: providerAnthropic, Model: "claude-sonnet"},
		},
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/v1/ai/profile", nil)
	request = request.WithContext(principal.WithActor(request.Context(), principal.Principal{
		Type: principal.PrincipalHuman,
		Permissions: principal.Permissions{Objects: map[string]principal.ObjectGrant{
			"automation": {Update: true},
		}},
	}))
	h.GetAiProfile(recorder, request)

	body := recorder.Body.String()
	for _, expected := range []string{defaultOllamaModel, defaultVLLMModel, "claude-sonnet", `"tier":"premium"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("authenticated profile misses %q: %s", expected, body)
		}
	}
}

func TestGetAiProfileRequiresOperationalConfigurationGrant(t *testing.T) {
	h := NewHandlers(nil, nil).WithPublicProfile(NewPublicProfile("configured", RoutingConfig{}))
	request := httptest.NewRequest("GET", "/v1/ai/profile", nil)
	request = request.WithContext(principal.WithActor(request.Context(), principal.Principal{
		Type: principal.PrincipalHuman,
	}))
	recorder := httptest.NewRecorder()

	h.GetAiProfile(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}
