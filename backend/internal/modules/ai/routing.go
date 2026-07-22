// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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

// defaultEmbedDimensions is the vector width an embeddings binding gets when
// it leaves `dimensions` unset (0) — matching the pgvector column the
// committed example config already assumes.
const defaultEmbedDimensions = 1024

// maxEmbedDimensions bounds an operator-set `dimensions` from above (spec
// ai-operational-spec.md §1.4); config/ai-routing.schema.json's generated
// embeddingsBinding $def carries the same bound.
const maxEmbedDimensions = 2000

// EmbeddingsConfig is the embeddings-lane binding: the shared ProviderConfig
// (provider/model/base_url, selectbrain.go) plus Dimensions, the vector width
// the provider is asked to emit. Chat tiers have no notion of output width,
// so Dimensions lives only here — never on the shared ProviderConfig every
// chat tier also binds through, where it would be meaningless.
type EmbeddingsConfig struct {
	ProviderConfig `yaml:",inline"`
	// Dimensions is the embedding vector width; 0 means "use the default"
	// (defaultEmbedDimensions). ParseRouting validates it into [1,2000] and
	// applies the default before any tier or role ever reads it — never
	// left for a downstream caller to re-check.
	Dimensions int `yaml:"dimensions"`
}

// RoutingConfig is the parsed ai-routing.yaml: the ONLY place vendor
// names appear (§1.4). A malformed binding fails at startup, not on the
// first model call at 3am.
type RoutingConfig struct {
	Tiers      map[Tier]ProviderConfig `yaml:"tiers"`
	Embeddings EmbeddingsConfig        `yaml:"embeddings"`
	Profile    Profile                 `yaml:"profile"`
	// sourceHash is the sha256 digest of the raw yaml bytes this config was
	// parsed from (spec §4) — the routing half of the ai_call_config
	// dimension key, alongside the generated TaskContractHash. Set by
	// ParseRouting so every caller (LoadRoutingFile, a direct ParseRouting
	// call in a test) gets the same deterministic digest for the same
	// bytes. Zero value "" on a config built by struct literal (FakeRoutingConfig,
	// most unit-test configs) rather than parsed from yaml.
	sourceHash string
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
	// The ONE validation + defaulting site for embeddings.dimensions: both
	// NewRouter and NewLocalRouter build their RoutingConfig through this
	// function (LoadRoutingFile or a direct ParseRouting call), so neither
	// role can construct a router with an out-of-range or undefaulted width.
	if d := cfg.Embeddings.Dimensions; d < 0 || d > maxEmbedDimensions {
		return RoutingConfig{}, fmt.Errorf("ai: routing config: embeddings dimensions %d out of range [1,%d]", d, maxEmbedDimensions)
	} else if d == 0 {
		cfg.Embeddings.Dimensions = defaultEmbedDimensions
	}
	if err := cfg.validate(); err != nil {
		return RoutingConfig{}, err
	}
	sum := sha256.Sum256(raw)
	cfg.sourceHash = hex.EncodeToString(sum[:])
	return cfg, nil
}

// localProviders can serve the sovereign zero-egress profile.
var localProviders = map[string]bool{providerOllama: true, providerVLLM: true, ProviderFake: true}

// ProviderIsLocal reports whether provider names same-host inference
// rather than a network-hosted vendor — the one exported spelling of
// this package's own localProviders set, so a caller outside it (the
// aicert cert lane's cloud-only P95 latency cap) never re-encodes the
// same "which providers are local" invariant as a second, driftable
// copy this package's own conformance test (
// TestLocalOnlyMatchesLocalProvidersForEveryProvider) cannot see.
func ProviderIsLocal(provider string) bool {
	return localProviders[provider]
}

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

// UnboundLadderWarnings reports every task whose entire fallback ladder
// has no bound tier in cfg.Tiers: a call for that task has nowhere to
// route and is refused outright, not merely degraded. This is not a
// startup error — a deployment legitimately narrows to only the
// workloads it actually runs — but it must be loud: an operator should
// read the gap off the boot log, not discover it from a refused call at
// 3am. AllTasks()'s sorted order keeps the result deterministic.
func (cfg RoutingConfig) UnboundLadderWarnings() []string {
	var warnings []string
	for _, task := range AllTasks() {
		ladder := taskLadders[task]
		bound := false
		for _, tier := range ladder {
			if _, ok := cfg.Tiers[tier]; ok {
				bound = true
				break
			}
		}
		if bound {
			continue
		}
		names := make([]string, len(ladder))
		for i, tier := range ladder {
			names[i] = string(tier)
		}
		warnings = append(warnings, fmt.Sprintf("task %s: no bound tier on ladder %v; calls will be refused", task, names))
	}
	return warnings
}

// TaskLadder reports task's routing fallback ladder — primary tier
// first, then the rungs a call walks on provider error or schema-
// validation failure. taskLadders (tasks_gen.go) is otherwise private to
// this package; this is the smallest export that lets a caller outside
// it (the aicert certification runner, compose/aicert) learn which tiers
// a MODEL= override must rebind for the task under test, without
// duplicating the routing table this package alone owns. The returned
// slice is a copy — a caller mutating it cannot corrupt the package's
// own table.
func TaskLadder(task Task) []Tier {
	return append([]Tier(nil), taskLadders[task]...)
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
	embedder, err := SelectBrain(cfg.Embeddings.ProviderConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("ai: embeddings lane: %w", err)
	}
	return clients, embedder, nil
}
