// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseRoutingValidatesAtStartup(t *testing.T) {
	valid := `
tiers:
  local_small: {provider: fake}
  cheap_cloud: {provider: anthropic, model: claude-haiku}
embeddings: {provider: fake}
profile: eu_hosted
`
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"valid", valid, ""},
		{"missing profile", strings.Replace(valid, "profile: eu_hosted", "", 1), "profile is required"},
		{"unknown profile", strings.Replace(valid, "eu_hosted", "hybrid", 1), "unknown profile"},
		{"unknown tier", strings.Replace(valid, "local_small", "medium_cloud", 1), "unknown tier"},
		{"tier without provider", strings.Replace(valid, "{provider: fake}\n  cheap_cloud", "{model: gemma}\n  cheap_cloud", 1), "no provider"},
		{"no embeddings lane", strings.Replace(valid, "embeddings: {provider: fake}", "", 1), "embeddings lane has no provider"},
		{"typo'd key rejected", strings.Replace(valid, "tiers:", "tierz:", 1), "field tierz not found"},
		{"sovereign refuses cloud chat tier", strings.Replace(valid, "profile: eu_hosted", "profile: sovereign", 1), "sovereign forbids cloud provider"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRouting([]byte(tc.yaml))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("valid config rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestParseRoutingSetsDeterministicSourceHash pins the routing half of the
// spec §4 config-snapshot key: the same yaml bytes always produce the same
// digest (Router.installConfigSnapshot relies on this for the ON CONFLICT
// DO NOTHING dimension row to actually collapse), and a change to the
// bytes must change the digest — an operator swapping providers must
// produce a NEW config-snapshot row, not silently reuse the old one's hash.
func TestParseRoutingSetsDeterministicSourceHash(t *testing.T) {
	cfg := []byte("profile: eu_hosted\ntiers:\n  cheap_cloud: {provider: fake}\nembeddings: {provider: fake}\n")
	first, err := ParseRouting(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseRouting(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.sourceHash == "" {
		t.Fatal("sourceHash must be set on a successfully parsed config")
	}
	if first.sourceHash != second.sourceHash {
		t.Fatalf("identical bytes produced different hashes: %q vs %q", first.sourceHash, second.sourceHash)
	}
	changed := []byte("profile: eu_hosted\ntiers:\n  cheap_cloud: {provider: fake, model: other}\nembeddings: {provider: fake}\n")
	third, err := ParseRouting(changed)
	if err != nil {
		t.Fatal(err)
	}
	if third.sourceHash == first.sourceHash {
		t.Fatal("a changed routing config must produce a different sourceHash")
	}
}

// TestTaskLadderReportsTheRoutingTableAndNeverAliasesIt covers the aicert
// runner's dependency on TaskLadder: it must report exactly the routing
// table's rungs for a known task, empty for an unknown one (no panic on
// a bad key), and hand back a copy a caller can mutate freely without
// corrupting taskLadders for the next call.
func TestTaskLadderReportsTheRoutingTableAndNeverAliasesIt(t *testing.T) {
	got := TaskLadder(TaskSiteExtract)
	want := []Tier{TierPremium}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("TaskLadder(TaskSiteExtract) = %v, want %v", got, want)
	}
	got[0] = TierLocalSmall
	again := TaskLadder(TaskSiteExtract)
	if again[0] != TierPremium {
		t.Fatalf("mutating a returned ladder corrupted the package table: got %v on the next call", again)
	}
	if unknown := TaskLadder(Task("not_a_real_task")); unknown != nil {
		t.Fatalf("an unknown task should report a nil ladder, got %v", unknown)
	}
}

// TestProviderIsLocalMatchesTheUnexportedSet pins ProviderIsLocal (the
// aicert cert lane's cloud-only-latency-cap dependency) to exactly the
// same providers TestLocalOnlyMatchesLocalProvidersForEveryProvider
// already binds localProviders to — one invariant, one exported reader.
func TestProviderIsLocalMatchesTheUnexportedSet(t *testing.T) {
	local := []string{providerOllama, providerVLLM, ProviderFake}
	for _, p := range local {
		if !ProviderIsLocal(p) {
			t.Errorf("ProviderIsLocal(%q) = false, want true", p)
		}
	}
	cloud := []string{providerAnthropic, providerOpenAI, providerGemini, providerOpenAICompatible}
	for _, p := range cloud {
		if ProviderIsLocal(p) {
			t.Errorf("ProviderIsLocal(%q) = true, want false", p)
		}
	}
}

// A cloud provider on any tier or the embeddings lane is refused under the
// sovereign profile — zero egress by construction (spec §3.6).
func TestSovereignRefusesOpenAICompatible(t *testing.T) {
	cfg := []byte(`
profile: sovereign
tiers:
  cheap_cloud: {provider: openai_compatible, base_url: https://api.mistral.ai, model: m}
embeddings: {provider: ollama, model: bge-m3}
`)
	if _, err := ParseRouting(cfg); err == nil || !strings.Contains(err.Error(), "sovereign forbids cloud provider") {
		t.Fatalf("want sovereign-forbids-cloud, got %v", err)
	}
}

// The native cloud adapters are refused under sovereign too — the guarantee is
// bound to provider identity, not to any config flag (spec §3.6).
func TestSovereignRefusesNativeCloudProviders(t *testing.T) {
	for _, provider := range []string{"openai", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			cfg := []byte("profile: sovereign\ntiers:\n  premium: {provider: " + provider + ", model: m}\nembeddings: {provider: ollama, model: bge-m3}\n")
			if _, err := ParseRouting(cfg); err == nil || !strings.Contains(err.Error(), "sovereign forbids cloud provider") {
				t.Fatalf("%s: want sovereign-forbids-cloud, got %v", provider, err)
			}
		})
	}
}

// LocalOnly (the runtime capability) and localProviders (the parse-time set)
// are two encodings of "is this cloud"; they may never disagree.
func TestLocalOnlyMatchesLocalProvidersForEveryProvider(t *testing.T) {
	clearCloudKeyEnv(t)
	for _, env := range cloudKeyEnv {
		t.Setenv(env, "k") // every cloud provider has its BYOK key in the environment
	}
	built := map[string]ProviderConfig{
		"fake":              {Provider: "fake"},
		"anthropic":         {Provider: "anthropic", Model: "m"},
		"ollama":            {Provider: "ollama", Model: "m"},
		"vllm":              {Provider: "vllm", Model: "m"},
		"openai_compatible": {Provider: "openai_compatible", BaseURL: "https://x", Model: "m"},
		"openai":            {Provider: "openai", Model: "m"},
		"gemini":            {Provider: "gemini", Model: "m"},
	}
	for _, name := range knownProviders {
		cfg, ok := built[name]
		if !ok {
			t.Fatalf("knownProviders has %q with no build recipe in this test — add one", name)
		}
		client, err := SelectBrain(cfg)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, want := client.Caps().LocalOnly, localProviders[name]; got != want {
			t.Fatalf("%s: Caps().LocalOnly=%v but localProviders=%v — encodings disagree", name, got, want)
		}
	}
}

// UnboundLadderWarnings is boot-loud, not boot-fatal: a task with no
// bound rung anywhere on its ladder gets one warning naming the task and
// the ladder it can't reach; a task with at least one bound rung
// (fallback or primary) is silent.
func TestUnboundLadderWarnings(t *testing.T) {
	allTiers := map[Tier]ProviderConfig{
		TierLocalSmall: {Provider: "fake"},
		TierCheapCloud: {Provider: "fake"},
		TierPremium:    {Provider: "fake"},
		TierLocalLarge: {Provider: "fake"},
	}
	cases := []struct {
		name  string
		tiers map[Tier]ProviderConfig
		want  []string
	}{
		{
			name:  "fully bound config warns about nothing",
			tiers: allTiers,
			want:  nil,
		},
		{
			name: "one unbound rung but another bound on the same ladder stays silent",
			// TaskAgentLoop's ladder is {cheap_cloud, premium}: cheap_cloud is
			// missing but premium is bound, so the task still has a route.
			tiers: map[Tier]ProviderConfig{
				TierPremium: {Provider: "fake"},
			},
			want: []string{
				"task capture_classify: no bound tier on ladder [local_small cheap_cloud]; calls will be refused",
				"task enrich: no bound tier on ladder [local_small cheap_cloud]; calls will be refused",
			},
		},
		{
			name:  "ladder with zero bound rungs warns naming the task and its ladder",
			tiers: map[Tier]ProviderConfig{},
			want: []string{
				"task agent_loop: no bound tier on ladder [cheap_cloud premium]; calls will be refused",
				"task brief_ranking: no bound tier on ladder [premium cheap_cloud]; calls will be refused",
				"task capture_classify: no bound tier on ladder [local_small cheap_cloud]; calls will be refused",
				"task cert_judge: no bound tier on ladder [premium cheap_cloud]; calls will be refused",
				"task cold_start: no bound tier on ladder [cheap_cloud premium]; calls will be refused",
				"task deal_health: no bound tier on ladder [cheap_cloud premium]; calls will be refused",
				"task draft_reply: no bound tier on ladder [cheap_cloud premium]; calls will be refused",
				"task enrich: no bound tier on ladder [local_small cheap_cloud]; calls will be refused",
				"task nl_search: no bound tier on ladder [cheap_cloud premium]; calls will be refused",
				"task offer_draft: no bound tier on ladder [cheap_cloud premium]; calls will be refused",
				"task site_extract: no bound tier on ladder [premium]; calls will be refused",
				"task site_fact_extract: no bound tier on ladder [cheap_cloud premium]; calls will be refused",
				"task summarize: no bound tier on ladder [cheap_cloud premium]; calls will be refused",
				"task transcript: no bound tier on ladder [cheap_cloud premium]; calls will be refused",
				"task voice_build: no bound tier on ladder [cheap_cloud premium]; calls will be refused",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := RoutingConfig{Tiers: tc.tiers}
			got := cfg.UnboundLadderWarnings()
			if len(got) != len(tc.want) {
				t.Fatalf("got %d warnings, want %d:\ngot:  %v\nwant: %v", len(got), len(tc.want), got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("warning %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestParseRoutingEmbedDimensions pins the embeddings-lane `dimensions`
// contract (spec ai-operational-spec.md §1.4, embed-identity phase 2): unset
// (0) defaults to 1536 (a gemini-recommended width) — while anything outside
// [1,2000] fails at startup, the same boot-loud-not-3am-surprise posture
// every other routing-config defect gets.
func TestParseRoutingEmbedDimensions(t *testing.T) {
	const base = `
profile: eu_hosted
tiers:
  cheap_cloud: {provider: fake}
embeddings: {provider: fake, model: embed-model, dimensions: %d}
`
	for _, tc := range []struct {
		dims    int
		wantErr bool
	}{
		{0, false}, {-1, true}, {2001, true}, {1, false}, {768, false}, {2000, false},
	} {
		t.Run(fmt.Sprintf("dims=%d", tc.dims), func(t *testing.T) {
			got, err := ParseRouting([]byte(fmt.Sprintf(base, tc.dims)))
			if (err != nil) != tc.wantErr {
				t.Fatalf("dims=%d: err=%v wantErr=%v", tc.dims, err, tc.wantErr)
			}
			if err == nil {
				// An accepted width must be preserved verbatim (0 defaults
				// to 1536) — a parser silently rewriting 1/768/2000 to
				// another width would otherwise pass unnoticed.
				want := tc.dims
				if tc.dims == 0 {
					want = 1536
				}
				if got.Embeddings.Dimensions != want {
					t.Fatalf("dims=%d: parsed Dimensions=%d, want %d", tc.dims, got.Embeddings.Dimensions, want)
				}
			}
		})
	}
}

func TestParseRoutingSovereignAllLocalIsValid(t *testing.T) {
	cfg, err := ParseRouting([]byte(`
tiers:
  local_small: {provider: ollama, model: gemma3}
  local_large: {provider: ollama, model: llama3.3:70b}
embeddings: {provider: ollama, model: bge-m3}
profile: sovereign
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile != ProfileSovereign || len(cfg.Tiers) != 2 {
		t.Fatalf("unexpected parse: %+v", cfg)
	}
}
