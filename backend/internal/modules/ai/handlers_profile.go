// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"net/http"
	"sort"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// PublicProfile is the deliberately small pre-auth view of the process's AI
// posture. It describes boot-time configuration, never provider health.
type PublicProfile struct {
	State         crmcontracts.AssistantProfileState
	InferenceMode crmcontracts.AssistantProfileInferenceMode
	Providers     []crmcontracts.AssistantProfileProviders
}

// NewPublicProfile derives the anonymous view from the same validated routing
// config the process used to construct its model path. Fake is a development
// mechanism, not a provider identity, so it never crosses this boundary.
func NewPublicProfile(runtimeState string, cfg RoutingConfig) PublicProfile {
	switch runtimeState {
	case "fake":
		return PublicProfile{
			State:         crmcontracts.AssistantProfileStateDevelopment,
			InferenceMode: crmcontracts.AssistantProfileInferenceModeDevelopment,
			Providers:     []crmcontracts.AssistantProfileProviders{},
		}
	case "configured":
		providers := publicProviders(cfg)
		return PublicProfile{
			State:         crmcontracts.AssistantProfileStateConfigured,
			InferenceMode: publicInferenceMode(providers),
			Providers:     providers,
		}
	default:
		return PublicProfile{
			State:         crmcontracts.AssistantProfileStateUnconfigured,
			InferenceMode: crmcontracts.AssistantProfileInferenceModeNone,
			Providers:     []crmcontracts.AssistantProfileProviders{},
		}
	}
}

func publicProviders(cfg RoutingConfig) []crmcontracts.AssistantProfileProviders {
	set := make(map[crmcontracts.AssistantProfileProviders]struct{}, len(cfg.Tiers)+1)
	for _, binding := range cfg.Tiers {
		if provider, ok := publicProvider(binding.Provider); ok {
			set[provider] = struct{}{}
		}
	}
	if provider, ok := publicProvider(cfg.Embeddings.Provider); ok {
		set[provider] = struct{}{}
	}
	providers := make([]crmcontracts.AssistantProfileProviders, 0, len(set))
	for provider := range set {
		providers = append(providers, provider)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i] < providers[j] })
	return providers
}

func publicProvider(provider string) (crmcontracts.AssistantProfileProviders, bool) {
	switch provider {
	case providerAnthropic:
		return crmcontracts.Anthropic, true
	case providerGemini:
		return crmcontracts.Gemini, true
	case providerOllama:
		return crmcontracts.Ollama, true
	case providerOpenAI:
		return crmcontracts.Openai, true
	case providerOpenAICompatible:
		return crmcontracts.OpenaiCompatible, true
	case providerVLLM:
		return crmcontracts.Vllm, true
	default:
		return "", false
	}
}

func publicInferenceMode(providers []crmcontracts.AssistantProfileProviders) crmcontracts.AssistantProfileInferenceMode {
	if len(providers) == 0 {
		return crmcontracts.AssistantProfileInferenceModeDevelopment
	}
	local, cloud := false, false
	for _, provider := range providers {
		if ProviderIsLocal(string(provider)) {
			local = true
		} else {
			cloud = true
		}
	}
	switch {
	case local && cloud:
		return crmcontracts.AssistantProfileInferenceModeHybrid
	case local:
		return crmcontracts.AssistantProfileInferenceModeLocal
	default:
		return crmcontracts.AssistantProfileInferenceModeCloud
	}
}

// WithPublicProfile binds the boot-derived view to the transport surface.
func (h Handlers) WithPublicProfile(profile PublicProfile) Handlers {
	h.publicProfile = profile
	return h
}

// GetAssistantProfile implements (GET /assistant/profile).
func (h Handlers) GetAssistantProfile(w http.ResponseWriter, _ *http.Request) {
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.AssistantProfile{
		Name:          crmcontracts.Margince,
		Kind:          crmcontracts.Ai,
		State:         h.publicProfile.State,
		InferenceMode: h.publicProfile.InferenceMode,
		Providers:     h.publicProfile.Providers,
	})
}
