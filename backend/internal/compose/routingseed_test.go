// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What a declared binding must survive on the way in. Everything a bad seed
// does wrong it does here, at BOOTSTRAP — the one moment somebody is watching —
// rather than at the first model call at 3am.

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func declaredNode(t *testing.T, doc string) *yaml.Node {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(doc), &node); err != nil {
		t.Fatalf("building the fixture: %v", err)
	}
	// Unmarshal wraps a document; the seed carries the mapping under the key.
	return node.Content[0]
}

const declaredRouting = `profile: eu_hosted
tiers:
  local_small: {provider: fake, model: small}
  cheap_cloud: {provider: fake, model: small}
  premium: {provider: fake, model: large}
  frontier: {provider: fake, model: large}
embeddings: {provider: fake, model: embed, dimensions: 8}
`

func TestADeclaredBindingIsDecodedAndFinalized(t *testing.T) {
	cfg, declared, err := routingSeedFrom(declaredNode(t, declaredRouting))
	if err != nil {
		t.Fatalf("routingSeedFrom: %v", err)
	}
	if !declared {
		t.Fatal("a declared binding read as nothing declared")
	}
	if m, ok := cfg.Tiers["premium"]; !ok || m.Model != "large" {
		t.Errorf("premium = %+v ok=%v", m, ok)
	}
	// Finalized on the way through, so what bootstrap stores is what a router
	// would serve — including the version, which is a cache key.
	if cfg.RoutingVersion() == "" {
		t.Error("the decoded binding carries no routing version")
	}
}

// Most deployments declare no binding, and bootstrap must not treat that as a
// fault. Nothing declared is not an error and not a binding.
func TestNoDeclaredBindingIsNotAnError(t *testing.T) {
	cfg, declared, err := routingSeedFrom(nil)
	if err != nil {
		t.Fatalf("routingSeedFrom(nil): %v", err)
	}
	if declared {
		t.Error("nothing was declared, but the seed reported one")
	}
	if !cfg.Unconfigured() {
		t.Errorf("got %+v, want the zero binding", cfg.Tiers)
	}
}

// The bar is the file loader's, applied at bootstrap. Each of these fails a
// boot rather than surfacing at the first model call, which is the whole reason
// the seed is decoded through the ai module's own parser rather than a mirror
// of it here.
func TestABadDeclaredBindingFailsTheBootstrap(t *testing.T) {
	for name, tc := range map[string]struct{ doc, want string }{
		"an unknown tier": {
			doc:  strings.Replace(declaredRouting, "premium:", "premiuum:", 1),
			want: "unknown tier",
		},
		"an unknown profile": {
			doc:  strings.Replace(declaredRouting, "eu_hosted", "nowhere", 1),
			want: "unknown profile",
		},
		// Written out rather than patched: sovereign means zero egress BY
		// CONSTRUCTION, and a fixture assembled by string surgery is one that
		// can stop expressing the thing it is named for without failing.
		"a cloud provider under the sovereign profile": {
			doc: `profile: sovereign
tiers:
  local_small: {provider: ollama, model: small}
  cheap_cloud: {provider: ollama, model: small}
  premium: {provider: gemini, model: large}
  frontier: {provider: ollama, model: large}
embeddings: {provider: ollama, model: embed, dimensions: 8}
`,
			want: "sovereign",
		},
		"an embeddings width out of range": {
			doc:  strings.Replace(declaredRouting, "dimensions: 8", "dimensions: 9000", 1),
			want: "out of range",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := routingSeedFrom(declaredNode(t, tc.doc))
			if err == nil {
				t.Fatal("the bootstrap accepted a binding the file loader would refuse")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to name %q", err, tc.want)
			}
		})
	}
}
