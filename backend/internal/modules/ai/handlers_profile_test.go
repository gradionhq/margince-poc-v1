// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
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
			}, Embeddings: ProviderConfig{Provider: providerGemini}},
			wantState: "configured", wantMode: "cloud", want: []string{"anthropic", "gemini"},
		},
		{
			name:  "local",
			state: "configured",
			config: RoutingConfig{Tiers: map[Tier]ProviderConfig{
				TierLocalSmall: {Provider: providerOllama},
			}, Embeddings: ProviderConfig{Provider: providerOllama}},
			wantState: "configured", wantMode: "local", want: []string{"ollama"},
		},
		{
			name:  "hybrid omits fake",
			state: "configured",
			config: RoutingConfig{Tiers: map[Tier]ProviderConfig{
				TierLocalSmall: {Provider: providerOllama},
				TierPremium:    {Provider: providerAnthropic},
			}, Embeddings: ProviderConfig{Provider: ProviderFake}},
			wantState: "configured", wantMode: "hybrid", want: []string{"anthropic", "ollama"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewPublicProfile(tt.state, tt.config)
			if got.State != tt.wantState || got.InferenceMode != tt.wantMode || !reflect.DeepEqual(got.Providers, tt.want) {
				t.Fatalf("profile = %+v, want state=%s mode=%s providers=%v", got, tt.wantState, tt.wantMode, tt.want)
			}
		})
	}
}

func TestGetAssistantProfileReturnsOnlyPublicFields(t *testing.T) {
	h := NewHandlers(nil, nil).WithPublicProfile(PublicProfile{
		State: "configured", InferenceMode: "hybrid", Providers: []string{"anthropic", "ollama"},
	})
	recorder := httptest.NewRecorder()
	h.GetAssistantProfile(recorder, httptest.NewRequest("GET", "/v1/assistant/profile", nil))

	var body map[string]any
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
	for _, forbidden := range []string{"model", "endpoint", "budget", "workspace", "key"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Errorf("response contains forbidden %q detail: %s", forbidden, recorder.Body.String())
		}
	}
}
