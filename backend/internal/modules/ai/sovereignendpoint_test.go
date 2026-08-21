// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"strings"
	"testing"
)

// A local provider NAME does not by itself prove a local endpoint: both local
// providers take an operator-supplied base_url, so the profile that promises
// zero egress checks where each binding actually points.
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

// verdictName keeps a failure readable: the verdicts are an unexported int enum,
// so %v would report "= 1, want 0" about a table whose whole point is which of
// the three answers a host gets.
func verdictName(v hostVerdict) string {
	switch v {
	case hostIsLocal:
		return "local"
	case hostIsElsewhere:
		return "elsewhere"
	case hostIsAName:
		return "a name"
	}
	return "unknown"
}

func TestWhichHostsCountAsCustomerControlled(t *testing.T) {
	for host, want := range map[string]hostVerdict{
		"127.0.0.1":       hostIsLocal,
		"::1":             hostIsLocal,
		"localhost":       hostIsLocal,
		"LOCALHOST":       hostIsLocal, // host names are case-insensitive
		"gpu.localhost":   hostIsLocal,
		"10.4.1.20":       hostIsLocal,
		"172.16.0.9":      hostIsLocal,
		"172.32.0.9":      hostIsElsewhere, // just past RFC 1918, which ends at 172.31
		"192.168.1.5":     hostIsLocal,
		"fd00::1":         hostIsLocal, // IPv6 unique-local
		"169.254.7.7":     hostIsLocal, // link-local
		"8.8.8.8":         hostIsElsewhere,
		"2606:4700::1111": hostIsElsewhere,
		// An IPv4-mapped IPv6 address is judged by its IPv4 rules, so the
		// mapping buys a public address nothing.
		"::ffff:8.8.8.8":  hostIsElsewhere,
		"::ffff:10.0.0.1": hostIsLocal,
		// The unspecified addresses are what an operator copies out of
		// OLLAMA_HOST; they name no host to reach and are not local.
		"0.0.0.0": hostIsElsewhere,
		"::":      hostIsElsewhere,
		// A name is its own answer: what it resolves to is decided elsewhere and
		// can change after boot, so it is refused even when it looks internal.
		"ollama.internal":   hostIsAName,
		"elsewhere.example": hostIsAName,
		// A trailing dot makes a NAME fully qualified, so `localhost.` is still
		// the reserved loopback name. An ADDRESS with one is not an address at
		// all: Go's resolver sends `127.0.0.1.` to DNS, so accepting it would
		// call local an endpoint the dial resolves elsewhere.
		"localhost.": hostIsLocal,
		"127.0.0.1.": hostIsAName,
		// A zone says which interface a link-local address is reached on. It
		// cannot make that address non-local.
		"fe80::1%eth0": hostIsLocal,
	} {
		if got := classifyHost(host); got != want {
			t.Errorf("classifyHost(%q) = %s, want %s", host, verdictName(got), verdictName(want))
		}
	}
}

// A base_url naming no host is exactly the shape a string-comparison check waves
// through, so it is refused rather than read as "nothing to see". `host:port`
// with no scheme lands here too — url.Parse reads `localhost` as the scheme —
// so the error names the whole-url shape rather than blaming the host.
func TestABaseURLWithNoHostIsRefusedUnderSovereign(t *testing.T) {
	for _, baseURL := range []string{"http://", "localhost:11434"} {
		err := requireSovereignEndpoint("tier local_large", providerVLLM, baseURL)
		if err == nil || !strings.Contains(err.Error(), "names no host") {
			t.Fatalf("base_url %q must be refused as hostless, got %v", baseURL, err)
		}
		if !strings.Contains(err.Error(), "http://127.0.0.1:11434") {
			t.Errorf("the refusal must show the shape it wants, got %q", err)
		}
	}
}

// A local host behind a scheme this adapter cannot dial is not the reachable
// local endpoint the profile was promised — it is a deployment that fails on its
// first call with a transport error instead of at boot with a config one.
func TestASchemeThisAdapterCannotCallIsRefusedEvenOnALocalHost(t *testing.T) {
	for _, baseURL := range []string{"ollama://127.0.0.1:11434", "ftp://10.0.0.5:8000"} {
		err := requireSovereignEndpoint("tier local_large", providerVLLM, baseURL)
		if err == nil || !strings.Contains(err.Error(), "http(s)") {
			t.Errorf("base_url %q must be refused for its scheme, got %v", baseURL, err)
		}
	}
	// A bracketed IPv6 endpoint with a zone is the case this must not catch.
	if err := requireSovereignEndpoint("tier local_large", providerVLLM, "http://[fe80::1%25eth0]:8000"); err != nil {
		t.Errorf("a zoned link-local endpoint is local and must be accepted, got %v", err)
	}
}

// A name and a public address are different mistakes with different fixes, and
// an operator on Kubernetes or Compose writes a service name — an error about
// private ranges would read to them as "your host is public", which it may not
// be. The refusal has to say the problem is that it IS a name.
func TestTheRefusalTellsANameApartFromAPublicAddress(t *testing.T) {
	byName := requireSovereignEndpoint("tier local_large", providerOllama, "http://ollama.ai.svc.cluster.local:11434")
	if byName == nil || !strings.Contains(byName.Error(), "is a name") {
		t.Fatalf("a service name must be refused AS a name, got %v", byName)
	}
	if !strings.Contains(byName.Error(), "Give the address instead") {
		t.Errorf("the refusal must name the fix, got %q", byName)
	}
	byAddress := requireSovereignEndpoint("tier local_large", providerOllama, "http://8.8.8.8:11434")
	if byAddress == nil || strings.Contains(byAddress.Error(), "is a name") {
		t.Fatalf("a public address must be refused as an address, got %v", byAddress)
	}
}

// A bracketed IPv6 base_url is the one spelling where the host is not the whole
// authority, so it goes through the real parse rather than the classifier alone.
func TestABracketedIPv6EndpointIsAccepted(t *testing.T) {
	if _, err := ParseRouting([]byte(`
profile: sovereign
tiers:
  local_large: { provider: vllm, base_url: "http://[fd00::1]:8000", model: m }
embeddings: { provider: ollama, model: bge-m3 }
`)); err != nil {
		t.Fatalf("a unique-local IPv6 endpoint must boot, got %v", err)
	}
}

// The check keys off its own map of provider defaults, and the profile gate keys
// off localProviders. A local provider added to one and not the other would pass
// unchecked — which is exactly the hole this file closes, reopened by drift.
func TestEveryLocalProviderWithAnEndpointIsChecked(t *testing.T) {
	for provider := range localProviders {
		if provider == ProviderFake {
			continue // reaches no endpoint at all; there is nothing to check
		}
		if _, ok := localBaseURLDefaults[provider]; !ok {
			t.Errorf("local provider %q has no entry in localBaseURLDefaults, so a sovereign binding to it is never endpoint-checked", provider)
		}
	}
}

// A malformed base_url is refused WITHOUT its value reaching the message.
//
// The value is the point, not the refusal. A base_url may carry userinfo
// (http://user:token@host), this error lands in a boot log, and for a binding
// declared in margince.yaml it comes from the one file whose whole promise is
// that it carries no credential. Both malformed shapes are covered because
// they take different exits and only one of them can lean on url.Redacted.
func TestAMalformedBaseURLIsRefusedWithoutEchoingItsCredential(t *testing.T) {
	const password = "s3cr3t-token"
	for _, tc := range []struct {
		name    string
		baseURL string
		names   string
	}{
		// A control character is a parse failure, so this exits before
		// url.URL exists and cannot redact anything — it must therefore say
		// nothing about the value at all.
		{"unparseable", "http://user:" + password + "@host\x7f/", "cannot be parsed"},
		// This one parses, so the host check is what refuses it, and
		// Redacted() is what keeps the password out.
		{"no host", "http://user:" + password + "@", "names no host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, err := hostOf(tc.baseURL)
			if err == nil {
				t.Fatalf("hostOf(%q) returned host %q and no error", tc.baseURL, host)
			}
			if strings.Contains(err.Error(), password) {
				t.Errorf("the refusal carries the credential: %v", err)
			}
			if !strings.Contains(err.Error(), tc.names) {
				t.Errorf("the refusal %q does not say %q, so an operator cannot tell which shape was wrong", err, tc.names)
			}
		})
	}
}

// The third exit redacts too. A scheme this adapter cannot dial is refused by
// naming the scheme, not the value — and a value with an unusable scheme is as
// likely to carry a pasted credential as one with no host.
func TestAnUndialableSchemeIsRefusedWithoutEchoingItsCredential(t *testing.T) {
	const password = "s3cr3t-token"
	host, err := hostOf("ftp://user:" + password + "@example.test/")
	if err == nil {
		t.Fatalf("hostOf accepted an ftp base_url, returning %q", host)
	}
	if strings.Contains(err.Error(), password) {
		t.Errorf("the refusal carries the credential: %v", err)
	}
	if !strings.Contains(err.Error(), "ftp") {
		t.Errorf("the refusal %q does not name the scheme the operator has to change", err)
	}
}
