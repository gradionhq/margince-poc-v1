// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"strings"
	"testing"
)

// The defect this file exists for: `sovereign` was enforced by provider NAME, so
// a vllm tier pointed at a third-party host validated and ran, and every call in
// a deployment that declared zero egress went over the public internet.
func TestSovereignRefusesALocalProviderPointedAtSomebodyElsesHost(t *testing.T) {
	_, err := ParseRouting([]byte(`
profile: sovereign
tiers:
  local_large:
    provider: vllm
    base_url: https://elsewhere.example
    model: m
embeddings:
  provider: ollama
  model: bge-m3
`))
	if err == nil {
		t.Fatal("a sovereign profile must not accept a vllm tier on a public host")
	}
	// The error names the host that failed, because the operator's next action is
	// to look at that line of their config.
	if !strings.Contains(err.Error(), "elsewhere.example") {
		t.Errorf("the refusal must name the host, got %q", err)
	}
}

// The mirror: the ordinary sovereign deployment must still boot. An omitted
// base_url is the provider default, which IS loopback, so treating "unknown" as
// "refuse" would break every deployment this profile is for.
func TestSovereignAcceptsTheDefaultedAndTheExplicitlyLocalEndpoint(t *testing.T) {
	for name, routing := range map[string]string{
		"defaulted": `
profile: sovereign
tiers:
  local_large: { provider: vllm, model: m }
embeddings: { provider: ollama, model: bge-m3 }
`,
		// A customer's own GPU box on their own network is their infrastructure:
		// the guarantee is about where data goes, not which process it lands in.
		"private range on another machine": `
profile: sovereign
tiers:
  local_large: { provider: vllm, base_url: http://10.4.1.20:8000, model: m }
embeddings: { provider: ollama, base_url: http://192.168.1.5:11434, model: bge-m3 }
`,
		"explicit loopback": `
profile: sovereign
tiers:
  local_large: { provider: ollama, base_url: http://127.0.0.1:11434, model: m }
embeddings: { provider: ollama, base_url: http://localhost:11434, model: bge-m3 }
`,
		// fake reaches no endpoint at all, so there is nothing to check.
		"the offline stub": `
profile: sovereign
tiers:
  local_large: { provider: fake, model: m }
embeddings: { provider: fake, model: m }
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseRouting([]byte(routing)); err != nil {
				t.Fatalf("a local sovereign binding must boot, got %v", err)
			}
		})
	}
}

// The embed lane egresses the same content the chat lanes do — a document
// reaches it as the thing being embedded — so it carries the same rule.
func TestSovereignChecksTheEmbeddingsEndpointToo(t *testing.T) {
	_, err := ParseRouting([]byte(`
profile: sovereign
tiers:
  local_large: { provider: vllm, model: m }
embeddings: { provider: ollama, base_url: https://embeddings.example, model: bge-m3 }
`))
	if err == nil || !strings.Contains(err.Error(), "embeddings.example") {
		t.Fatalf("the embed lane must carry the endpoint rule, got %v", err)
	}
}

// Only the profile that promises zero egress constrains the endpoint. A local
// model reached over a network is an ordinary eu_hosted deployment, and refusing
// it there would be a rule nothing asked for.
func TestTheEndpointRuleAppliesOnlyUnderSovereign(t *testing.T) {
	if _, err := ParseRouting([]byte(`
profile: eu_hosted
tiers:
  local_large: { provider: vllm, base_url: https://elsewhere.example, model: m }
embeddings: { provider: ollama, model: bge-m3 }
`)); err != nil {
		t.Fatalf("eu_hosted may reach a networked local model, got %v", err)
	}
}

func TestWhichHostsCountAsCustomerControlled(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1":       true,
		"::1":             true,
		"localhost":       true,
		"LOCALHOST":       true, // host names are case-insensitive
		"gpu.localhost":   true,
		"10.4.1.20":       true,
		"172.16.0.9":      true,
		"172.32.0.9":      false, // just past the RFC 1918 block, which ends at 172.31
		"192.168.1.5":     true,
		"fd00::1":         true, // IPv6 unique-local
		"169.254.7.7":     true, // link-local
		"8.8.8.8":         false,
		"2606:4700::1111": false,
		// A DNS name is refused even when it looks internal: resolving it at boot
		// says only where it pointed at boot.
		"ollama.internal":   false,
		"elsewhere.example": false,
	} {
		if got := hostIsCustomerControlled(host); got != want {
			t.Errorf("hostIsCustomerControlled(%q) = %v, want %v", host, got, want)
		}
	}
}

// A base_url naming no host is exactly the shape a string-comparison check waves
// through, so it is refused rather than read as "nothing to see".
func TestABaseURLWithNoHostIsRefusedUnderSovereign(t *testing.T) {
	err := requireSovereignEndpoint("tier local_large", providerVLLM, "http://")
	if err == nil || !strings.Contains(err.Error(), "names no host") {
		t.Fatalf("a hostless base_url must be refused, got %v", err)
	}
}
