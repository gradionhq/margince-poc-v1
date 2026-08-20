// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/platform/config"
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
// it leaves `dimensions` unset (0) — 1536, one of gemini-embedding-001's
// recommended Matryoshka widths (768 / 1536 / 3072) and a safe mid-size
// default for the unbounded embedding column. An operator on a provider
// with a different native width (e.g. Ollama bge-m3 at 1024) sets
// `dimensions` explicitly.
const defaultEmbedDimensions = 1536

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
	Dimensions int `yaml:"dimensions" json:"dimensions"`
}

// RoutingConfig is the parsed ai-routing.yaml: the ONLY place vendor
// names appear (§1.4). A malformed binding fails at startup, not on the
// first model call at 3am.
type RoutingConfig struct {
	Tiers      map[Tier]ProviderConfig `yaml:"tiers" json:"tiers"`
	Embeddings EmbeddingsConfig        `yaml:"embeddings" json:"embeddings"`
	Profile    Profile                 `yaml:"profile" json:"profile"`
	// sourceHash is the sha256 digest of the raw yaml bytes this config was
	// parsed from (spec §4) — the routing half of the ai_call_config
	// dimension key, alongside the generated TaskContractHash. Set by
	// ParseRouting so every caller (LoadRoutingFile, a direct ParseRouting
	// call in a test) gets the same deterministic digest for the same
	// bytes. Zero value "" on a config built by struct literal (FakeRoutingConfig,
	// most unit-test configs) rather than parsed from yaml.
	sourceHash string
	// keys resolves a cloud provider's BYOK secret when the clients are built.
	// It travels on the config because that is where configuration enters this
	// package (LoadRoutingFile), and because the routing file names only the
	// provider — the key itself never appears in it (ADR-0020, 12-factor).
	//
	// Nil on a config built by struct literal, which resolves every key to
	// "unset". That is the honest default: a cloud binding without a key then
	// fails at construction with byokKeyRequired naming the variable to set,
	// which is exactly what an unconfigured deployment should get, while the
	// fake and local providers — the ones struct-literal configs actually use —
	// need no key at all.
	keys config.Lookup
}

// BoundModelIDsByProvider is every model this deployment actually calls, keyed
// by the PROVIDER that serves it: each bound tier's model plus the embeddings
// model. The identity is the model id as the VENDOR spells it, which is the
// same string that vendor's catalog entry names itself by.
//
// Keyed by provider rather than flattened because the cost refresh applies it
// per pricing SOURCE, and a source is one provider's catalog
// (rates.model_pricing names the provider exactly as this file binds it). A
// flat set would let one provider's bindings decide what another provider's
// catalog is filtered to, and — worse — make "none of the bound models appear
// here" indistinguishable from "this catalog belongs to a provider that binds
// nothing".
//
// Returns an empty (non-nil) map when nothing is bound, so a caller can tell
// "this deployment binds nothing" from a nil it forgot to build.
func (cfg RoutingConfig) BoundModelIDsByProvider() map[string]map[string]bool {
	bound := make(map[string]map[string]bool, len(cfg.Tiers)+1)
	add := func(provider, modelID string) {
		provider, modelID = strings.TrimSpace(provider), strings.TrimSpace(modelID)
		if provider == "" || modelID == "" {
			return
		}
		if bound[provider] == nil {
			bound[provider] = map[string]bool{}
		}
		bound[provider][modelID] = true
	}
	for _, tier := range cfg.Tiers {
		add(tier.Provider, tier.Model)
	}
	add(cfg.Embeddings.Provider, cfg.Embeddings.Model)
	return bound
}

// RoutingVersion identifies the model binding this config expresses — the
// same digest the ai_call_config dimension keys on. A caller that caches
// model-written content folds it into its cache key, so re-pointing a lane
// rewrites that content instead of leaving it attributed to a model which
// no longer produces it. Empty for a config built by struct literal rather
// than parsed from yaml (the fake lane), which is honest: there is no
// deployment binding to name.
//
// Two configs that route identically share a version however differently they
// were written — see bindingDigest for why that is the whole point.
func (cfg RoutingConfig) RoutingVersion() string { return cfg.sourceHash }

// LoadRoutingFile reads and validates a deployment's routing config. keys is
// where the BYOK secrets come from — the file names providers, never their
// credentials, so the two arrive together only here.
func LoadRoutingFile(path string, keys config.Lookup) (RoutingConfig, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- deployment config path, operator-chosen
	if err != nil {
		return RoutingConfig{}, fmt.Errorf("ai: routing config: %w", err)
	}
	cfg, err := ParseRouting(raw)
	if err != nil {
		return RoutingConfig{}, err
	}
	return cfg.WithKeys(keys), nil
}

// WithKeys binds where this config's BYOK secrets come from, and returns the
// bound copy. ParseRouting works on bytes alone and so cannot know: the routing
// file names providers and never their credentials. LoadRoutingFile binds it
// for the path it read, and this is the same binding for a caller that already
// holds the bytes.
func (cfg RoutingConfig) WithKeys(keys config.Lookup) RoutingConfig {
	cfg.keys = keys
	return cfg
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
	// Before finalize(), which sees only the decoded value and so cannot tell a
	// blank declaration from an absent one. This check is YAML's alone: in JSON
	// an explicit null and an omitted key both decode to nil, so a config read
	// back from the settings store cannot express the mistake it catches.
	blank, err := blankInputDeclarations(raw)
	if err != nil {
		return RoutingConfig{}, err
	}
	if len(blank) > 0 {
		return RoutingConfig{}, fmt.Errorf(
			"ai: routing config: %s: `input` is written with no value; omit the field to bind a text-only model, or name the modalities the bound model accepts",
			strings.Join(blank, ", "))
	}
	return cfg.finalize()
}

// finalize applies the defaults, validates, and stamps the version — the steps
// every routing config takes however it arrived.
//
// It is the ONE defaulting site for embeddings.dimensions: NewRouter and
// NewLocalRouter both build their config through a function that ends here, so
// no role can construct a router with an out-of-range or undefaulted width. The
// digest is taken last, over the DEFAULTED value, which is what makes an
// omitted width and an explicitly-written default the same binding.
func (cfg RoutingConfig) finalize() (RoutingConfig, error) {
	if d := cfg.Embeddings.Dimensions; d < 0 || d > maxEmbedDimensions {
		return RoutingConfig{}, fmt.Errorf("ai: routing config: embeddings dimensions %d out of range [1,%d]", d, maxEmbedDimensions)
	} else if d == 0 {
		cfg.Embeddings.Dimensions = defaultEmbedDimensions
	}
	if err := cfg.validate(); err != nil {
		return RoutingConfig{}, err
	}
	cfg.sourceHash = cfg.bindingDigest()
	return cfg, nil
}

// FromStored finalizes a binding read back from the settings store and binds
// where its BYOK secrets come from.
//
// A config that arrived as JSON carries neither of the two things ParseRouting
// stamps on: no version, because sourceHash is derived rather than stored, and
// no key lookup, because a routing document names providers and never their
// credentials. Returning it unfinalized would hand the Router a config whose
// RoutingVersion is empty — and that value is a cache key, so every brief in
// the installation would fingerprint against nothing.
func FromStored(stored RoutingConfig, keys config.Lookup) (RoutingConfig, error) {
	cfg, err := stored.finalize()
	if err != nil {
		return RoutingConfig{}, err
	}
	return cfg.WithKeys(keys), nil
}

// bindingDigest is the routing half of the ai_call_config dimension key: a
// digest of what this config BINDS, not of the bytes it arrived in.
//
// It is taken after defaulting and validation, over the canonical JSON
// encoding, which makes two configs hash alike exactly when they route alike.
// json.Marshal orders struct fields by declaration and sorts map keys, so the
// encoding is deterministic across processes — the property this digest has to
// have to be compared at all.
//
// The distinction is not academic. This value reaches personbrief.Fingerprint
// and its siblings, where it decides whether a stored brief may be reused; a
// digest of raw bytes meant that ADDING A COMMENT to the routing file
// invalidated every cached brief, dossier and growth-fit in the installation
// and regenerated them through paid models. Reindenting did it too, and so did
// writing `dimensions: 1536` where the default was already 1536.
//
// What must still change the digest is any change to the binding itself — a
// re-pointed tier, a different base URL, a narrowed `input`. Those are exactly
// the cases where content a model wrote must stop being attributed to a model
// that no longer produces it.
func (cfg RoutingConfig) bindingDigest() string {
	// A plain struct of strings, ints and a string-keyed map — marshal cannot
	// fail on it, and the same spelling guards the sibling fingerprints in
	// compose/orgbrief and compose/orgdossier that this digest feeds.
	encoded, _ := json.Marshal(cfg) //nolint:errchkjson // plain scalars and a string-keyed map; marshal cannot fail
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
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

// Validate runs the same rule set ParseRouting applies — the profile, the
// tier bindings, the sovereign endpoint rule, the `input:` declarations and
// the embed lane — over a config that reached its bindings some other way.
//
// It exists for one caller and would not otherwise be exported: the aicert
// runner's MARGINCE_AICERT_MODEL override rebinds a task's ladder AFTER the
// file was parsed, so the loaded file's guarantees do not survive it. A config
// that never comes back through here can carry a cloud provider under
// `profile: sovereign` with nothing said about it. Same reasoning as
// TaskLadder and ProviderIsLocal above: the smallest export that stops a
// caller outside this package re-encoding a rule this package owns.
//
// Pure over the receiver, so running it twice costs nothing.
func (cfg RoutingConfig) Validate() error { return cfg.validate() }

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
		// Sovereign means zero egress BY CONSTRUCTION, which takes both halves:
		// a cloud provider in any chat tier is a config error, and so is a local
		// provider pointed at somebody else's host (sovereignendpoint.go).
		// Neither is a runtime surprise.
		if cfg.Profile == ProfileSovereign {
			if !localProviders[binding.Provider] {
				return fmt.Errorf("ai: routing config: profile sovereign forbids cloud provider %q on tier %s", binding.Provider, tier)
			}
			if err := requireSovereignEndpoint(fmt.Sprintf("tier %s", tier), binding.Provider, binding.BaseURL); err != nil {
				return err
			}
		}
		if err := validateInput(fmt.Sprintf("tier %s", tier), binding.Input); err != nil {
			return err
		}
	}
	if cfg.Embeddings.Provider == "" {
		return fmt.Errorf("ai: routing config: embeddings lane has no provider")
	}
	// EmbeddingsConfig embeds ProviderConfig INLINE, so `input:` under
	// `embeddings:` decodes happily and would reach the embedder's client. The
	// embedding lane sends no attachments, so the declaration could only mislead
	// — refuse it here, where the parser is the gate. The generated schema omits
	// it from embeddingsBinding for the same reason, but the schema is editor
	// tooling and cannot be the thing that holds this.
	if cfg.Embeddings.Input != nil {
		return fmt.Errorf("ai: routing config: the embeddings lane takes no `input` — it sends no attachments; declare it on the chat tier that reads documents")
	}
	if cfg.Profile == ProfileSovereign {
		if !localProviders[cfg.Embeddings.Provider] {
			return fmt.Errorf("ai: routing config: profile sovereign forbids cloud provider %q on the embeddings lane", cfg.Embeddings.Provider)
		}
		// The embed lane egresses the same text the chat lanes do — a document's
		// content reaches it as the thing being embedded — so it carries the same
		// endpoint rule rather than a weaker one.
		if err := requireSovereignEndpoint("the embeddings lane", cfg.Embeddings.Provider, cfg.Embeddings.BaseURL); err != nil {
			return err
		}
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
		client, err := SelectBrain(binding, cfg.keys)
		if err != nil {
			return nil, nil, fmt.Errorf("ai: tier %s: %w", tier, err)
		}
		clients[tier] = client
	}
	embedder, err := SelectBrain(cfg.Embeddings.ProviderConfig, cfg.keys)
	if err != nil {
		return nil, nil, fmt.Errorf("ai: embeddings lane: %w", err)
	}
	return clients, embedder, nil
}
