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

	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
)

// declaredSeed builds the fixture the way PRODUCTION does: a margince.yaml
// document decoded through deployconfig, not a node assembled by hand.
//
// The distinction is the whole reason this helper exists. A hand-built node
// routes around the one thing that can fail — whether a `seeds.ai_routing`
// mapping survives being decoded out of a deployconfig.Seeds field at all — and
// a fixture shaped that way was green while the field's type made the feature
// impossible to use.
func declaredSeed(t *testing.T, routing string) yaml.Node {
	t.Helper()
	doc := "version: 1\nseeds:\n  ai_routing:\n" + indent(routing, "    ")
	cfg, err := deployconfig.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("deployconfig.Parse refused the declared binding: %v", err)
	}
	return cfg.Seeds.AIRouting
}

func indent(body, pad string) string {
	var out strings.Builder
	for line := range strings.SplitSeq(strings.TrimRight(body, "\n"), "\n") {
		out.WriteString(pad + line + "\n")
	}
	return out.String()
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
	cfg, declared, err := routingSeedFrom(declaredSeed(t, declaredRouting))
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
	cfg, declared, err := routingSeedFrom(yaml.Node{})
	if err != nil {
		t.Fatalf("routingSeedFrom(yaml.Node{}): %v", err)
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
			_, _, err := routingSeedFrom(declaredSeed(t, tc.doc))
			if err == nil {
				t.Fatal("the bootstrap accepted a binding the file loader would refuse")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to name %q", err, tc.want)
			}
		})
	}
}

// A binding may reference an anchor defined elsewhere in margince.yaml. That is
// ordinary YAML and must not fail the boot.
//
// Marshalling the subtree alone emits the alias with no anchor in scope, so the
// re-parse died with "unknown anchor 'v' referenced" — an internal parser
// detail handed to an operator whose file is valid.
//
// Only the scalar case is exercised, and not for want of trying: a MERGE key
// needs a mapping anchor, and strict decoding leaves one nowhere legal to live
// outside `ai_routing` — every section of this schema is a closed set of keys.
// resolveAliases handles both idioms; this is the half the schema can express.
func TestABindingThatReferencesAnAnchorElsewhereInTheFileStillSeeds(t *testing.T) {
	const doc = `version: 1
organization:
  name: &v fake
seeds:
  ai_routing:
    profile: eu_hosted
    tiers:
      local_small: {provider: *v, model: m}
      cheap_cloud: {provider: *v, model: m}
      premium: {provider: *v, model: m}
      frontier: {provider: *v, model: m}
    embeddings: {provider: *v, model: e, dimensions: 8}
`
	cfg, err := deployconfig.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("deployconfig.Parse: %v", err)
	}
	bound, declared, err := routingSeedFrom(cfg.Seeds.AIRouting)
	if err != nil {
		t.Fatalf("routingSeedFrom: %v", err)
	}
	if !declared {
		t.Fatal("the binding read as undeclared")
	}
	if m, ok := bound.Tiers["premium"]; !ok || m.Provider != "fake" {
		t.Errorf("premium = %+v ok=%v; the alias did not resolve to what it points at", m, ok)
	}
}
