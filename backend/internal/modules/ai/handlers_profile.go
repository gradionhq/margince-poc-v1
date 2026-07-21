// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"net/http"
	"sort"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

const developmentProfileValue = "development"

// PublicProfile is the deliberately small pre-auth view of the process's AI
// posture. It describes boot-time configuration, never provider health.
type PublicProfile struct {
	State         string
	InferenceMode string
	Providers     []string
}

// NewPublicProfile derives the anonymous view from the same validated routing
// config the process used to construct its model path. Fake is a development
// mechanism, not a provider identity, so it never crosses this boundary.
func NewPublicProfile(runtimeState string, cfg RoutingConfig) PublicProfile {
	switch runtimeState {
	case "fake":
		return PublicProfile{
			State:         developmentProfileValue,
			InferenceMode: developmentProfileValue,
			Providers:     []string{},
		}
	case "configured":
		providers := publicProviders(cfg)
		return PublicProfile{
			State:         "configured",
			InferenceMode: publicInferenceMode(providers),
			Providers:     providers,
		}
	default:
		return PublicProfile{State: "unconfigured", InferenceMode: "none", Providers: []string{}}
	}
}

func publicProviders(cfg RoutingConfig) []string {
	set := make(map[string]struct{}, len(cfg.Tiers)+1)
	for _, binding := range cfg.Tiers {
		if publicProvider(binding.Provider) {
			set[binding.Provider] = struct{}{}
		}
	}
	if publicProvider(cfg.Embeddings.Provider) {
		set[cfg.Embeddings.Provider] = struct{}{}
	}
	providers := make([]string, 0, len(set))
	for provider := range set {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	return providers
}

func publicProvider(provider string) bool {
	switch provider {
	case providerAnthropic, providerGemini, providerOllama, providerOpenAI, providerOpenAICompatible, providerVLLM:
		return true
	default:
		return false
	}
}

func publicInferenceMode(providers []string) string {
	if len(providers) == 0 {
		return developmentProfileValue
	}
	local, cloud := false, false
	for _, provider := range providers {
		if ProviderIsLocal(provider) {
			local = true
		} else {
			cloud = true
		}
	}
	switch {
	case local && cloud:
		return "hybrid"
	case local:
		return "local"
	default:
		return "cloud"
	}
}

// WithPublicProfile binds the boot-derived view to the transport surface.
func (h Handlers) WithPublicProfile(profile PublicProfile) Handlers {
	h.publicProfile = profile
	return h
}

// GetAssistantProfile implements (GET /assistant/profile).
func (h Handlers) GetAssistantProfile(w http.ResponseWriter, _ *http.Request) {
	providers := make([]crmcontracts.AssistantProfileProviders, len(h.publicProfile.Providers))
	for i, provider := range h.publicProfile.Providers {
		providers[i] = crmcontracts.AssistantProfileProviders(provider)
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.AssistantProfile{
		Name:          crmcontracts.Margince,
		Kind:          crmcontracts.Ai,
		State:         crmcontracts.AssistantProfileState(h.publicProfile.State),
		InferenceMode: crmcontracts.AssistantProfileInferenceMode(h.publicProfile.InferenceMode),
		Providers:     providers,
	})
}
