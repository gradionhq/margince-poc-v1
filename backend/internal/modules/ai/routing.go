// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// Profile is the §4 location ladder — the privacy choice is WHERE the
// model runs, not a redaction setting.
type Profile string

const (
	ProfileEUHosted      Profile = "eu_hosted"
	ProfileSovereign     Profile = "sovereign"
	ProfileCloudFrontier Profile = "cloud_frontier"
)

// RoutingConfig is the parsed ai-routing.yaml: the ONLY place vendor
// names appear (§1.4). A malformed binding fails at startup, not on the
// first model call at 3am.
type RoutingConfig struct {
	Tiers      map[Tier]ProviderConfig `yaml:"tiers"`
	Embeddings ProviderConfig          `yaml:"embeddings"`
	Profile    Profile                 `yaml:"profile"`
}

// LoadRoutingFile reads and validates a deployment's routing config.
func LoadRoutingFile(path string) (RoutingConfig, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- deployment config path, operator-chosen
	if err != nil {
		return RoutingConfig{}, fmt.Errorf("ai: routing config: %w", err)
	}
	return ParseRouting(raw)
}

// ParseRouting decodes + validates. Unknown keys are errors: a typo'd
// tier name silently ignored would route tasks to the wrong model.
func ParseRouting(raw []byte) (RoutingConfig, error) {
	var cfg RoutingConfig
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return RoutingConfig{}, fmt.Errorf("ai: routing config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return RoutingConfig{}, err
	}
	return cfg, nil
}

var knownTiers = map[Tier]bool{
	TierLocalSmall: true, TierCheapCloud: true, TierPremium: true, TierLocalLarge: true,
}

// localProviders can serve the sovereign zero-egress profile.
var localProviders = map[string]bool{"ollama": true, "vllm": true, "fake": true}

func (cfg RoutingConfig) validate() error {
	switch cfg.Profile {
	case ProfileEUHosted, ProfileSovereign, ProfileCloudFrontier:
	case "":
		return fmt.Errorf("ai: routing config: profile is required (eu_hosted | sovereign | cloud_frontier)")
	default:
		return fmt.Errorf("ai: routing config: unknown profile %q", cfg.Profile)
	}
	if len(cfg.Tiers) == 0 {
		return fmt.Errorf("ai: routing config: no tiers bound")
	}
	for tier, binding := range cfg.Tiers {
		if !knownTiers[tier] {
			return fmt.Errorf("ai: routing config: unknown tier %q", tier)
		}
		if binding.Provider == "" {
			return fmt.Errorf("ai: routing config: tier %s has no provider", tier)
		}
		// Sovereign means zero egress BY CONSTRUCTION: a cloud provider
		// in any chat tier is a config error, not a runtime surprise.
		if cfg.Profile == ProfileSovereign && !localProviders[binding.Provider] {
			return fmt.Errorf("ai: routing config: profile sovereign forbids cloud provider %q on tier %s", binding.Provider, tier)
		}
	}
	if cfg.Embeddings.Provider == "" {
		return fmt.Errorf("ai: routing config: embeddings lane has no provider")
	}
	if cfg.Profile == ProfileSovereign && !localProviders[cfg.Embeddings.Provider] {
		return fmt.Errorf("ai: routing config: profile sovereign forbids cloud provider %q on the embeddings lane", cfg.Embeddings.Provider)
	}
	return nil
}

// buildClients turns validated bindings into live Clients via
// SelectBrain. Construction errors (missing BYOK key, unknown provider)
// surface here — still startup, still loud.
func (cfg RoutingConfig) buildClients() (map[Tier]model.Client, model.Client, error) {
	clients := make(map[Tier]model.Client, len(cfg.Tiers))
	for tier, binding := range cfg.Tiers {
		client, err := SelectBrain(binding)
		if err != nil {
			return nil, nil, fmt.Errorf("ai: tier %s: %w", tier, err)
		}
		clients[tier] = client
	}
	embedder, err := SelectBrain(cfg.Embeddings)
	if err != nil {
		return nil, nil, fmt.Errorf("ai: embeddings lane: %w", err)
	}
	return clients, embedder, nil
}
