// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// unitGoSrc is the root package that makes a temp directory a composable
// unit; the fragment fixtures below care only about its api/ layer.
// repoRoot (unitmanifest_test.go) is the real tree — the byte-identity test
// reads the COMMITTED contracts rather than a synthetic stand-in.
const unitGoSrc = "package u\n"

func unitGoMod(name string) string {
	return "module example.test/ext/" + name + "\n\ngo 1.26.5\n"
}

// fragmentRoot lays out a temp repository holding the four base contracts
// (small stand-ins with the same top-level shape as the real ones) plus one
// extension per entry in units, each shipping the given api/ files.
func fragmentRoot(t *testing.T, units map[string]map[string]string) string {
	t.Helper()
	root := t.TempDir()
	bases := map[string]string{
		"crm.yaml": `openapi: 3.1.0
paths:
  /v1/deals:
    get: {operationId: listDeals}
components:
  schemas:
    Deal:
      type: object
`,
		"jobs.yaml":          "queues: {}\nkinds:\n  core_thing:\n    timeout: 2m\n",
		"ai-tasks.yaml":      "tiers: [alpha]\ntasks: {}\n",
		"public-events.yaml": "openapi: 3.1.0\ncomponents:\n  schemas: {}\n",
	}
	for name, content := range bases {
		path := filepath.Join(root, "backend", "api", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, api := range units {
		files := map[string]string{"go.mod": unitGoMod(name), "u.go": unitGoSrc}
		for rel, content := range api {
			files["api/"+rel] = content
		}
		writeUnit(t, root, name, files)
	}
	return root
}

// composeFrom scans the temp root and composes its contracts the way
// composedFiles does — the end-to-end path, not a hand-built unit list.
func composeFrom(t *testing.T, root string) (map[string][]byte, error) {
	t.Helper()
	units, err := scanExtensions(root)
	if err != nil {
		return nil, err
	}
	return composedContracts(root, units)
}

// overlayFor renders a one-action overlay document targeting target.
func overlayFor(unit, target, update string) string {
	return "overlay: " + overlayVersion + "\ninfo:\n  title: " + unit +
		"\n  version: \"1\"\nactions:\n  - target: " + target + "\n    update:\n" + update
}

// committedBaseRoot builds a temp repository whose backend/api/ holds the
// REAL committed contracts, copied byte for byte, and NO extensions at all.
//
// The zero-fragment condition is structural here rather than incidental: the
// tree has no extensions/ directory, so scanExtensions returns the empty set by
// construction and no future unit can change that. Reading the live tree
// instead would tie the empty-tree guarantee to the accident that no unit
// currently ships an api/ layer — and Task 10 lands the first one, at which
// point the test would fail for a reason unrelated to what it proves, and the
// tempting fix under time pressure is to delete it.
func committedBaseRoot(t *testing.T) (root string, committed map[string][]byte) {
	t.Helper()
	repo, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "backend", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	committed = make(map[string][]byte, len(composedContractBases))
	for _, base := range composedContractBases {
		raw, err := os.ReadFile(filepath.Join(repo, "backend", "api", base))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "backend", "api", base), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		committed[base] = raw
	}
	units, err := scanExtensions(root)
	if err != nil || len(units) != 0 {
		t.Fatalf("units, err = %v, %v — the fixture must carry no extensions", units, err)
	}
	return root, committed
}

// TestComposedContractIsByteIdenticalWithNoFragments is the empty-tree
// guarantee, and it is a COMPARISON against the committed bytes rather than
// a property the composer asserts about itself: with no extension shipping
// an api/ layer, every composed contract must be the base file, byte for
// byte. Parse-and-reserialize would pass every semantic check here and still
// break it — comments, key order, quoting and indentation all move — so the
// vanilla composed contract would stop matching the file every other lane
// reads, and the empty-tree property the composition rests on would die
// silently.
//
// The bytes are the real committed contracts (up to 839 KB of comments,
// anchors, flow mappings and folded scalars — everything a round trip
// rewrites); only the zero-fragment condition is synthesised. See
// committedBaseRoot for why.
func TestComposedContractIsByteIdenticalWithNoFragments(t *testing.T) {
	root, committed := committedBaseRoot(t)
	got, err := composedContracts(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range composedContractBases {
		t.Run(base, func(t *testing.T) {
			want := committed[base]
			composed, ok := got[base]
			if !ok {
				t.Fatalf("the composition does not emit %s at all", base)
			}
			if !bytes.Equal(composed, want) {
				t.Fatalf("composed %s is not the base byte-for-byte: %d bytes vs %d; first difference at offset %d",
					base, len(composed), len(want), firstDifference(composed, want))
			}
		})
	}

	// The same property stated against the composer's own seam, so a future
	// caller that assembles fragments differently still cannot slip a
	// reserialization in: no fragments means the input slice, untouched.
	t.Run("mergeContract returns the base slice itself", func(t *testing.T) {
		base := []byte("openapi: 3.1.0\n# a comment reserialization would drop\npaths: {}\n")
		merged, err := mergeContract(base, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(merged, base) {
			t.Fatalf("merged = %q, want the base unchanged", merged)
		}
	})
}

// TestComposedFilesEmitsEveryContract is the wiring half, separated from the
// byte-identity half deliberately: this one reads the LIVE tree, so it keeps
// holding once a unit ships fragments (the merged contract will then differ
// from its base, which is the point of the feature), while the test above
// keeps proving the vanilla case against a tree that has none.
func TestComposedFilesEmitsEveryContract(t *testing.T) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	files, _, _, err := composedFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range composedContractBases {
		if _, ok := files["api/"+base]; !ok {
			t.Errorf("the composition does not emit api/%s", base)
		}
	}
}

// firstDifference reports the byte offset the two slices diverge at, so a
// byte-identity failure points at the drift instead of dumping two contracts.
func firstDifference(a, b []byte) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// TestOverlayOnSameJSONPathIsABuildError is the collision rule. Two units
// both adding a node at one JSONPath have no defined winner: extension-name
// order would silently decide which contract the installation publishes, and
// the loser's operations would vanish from the client types and the docs
// while its Go registrations still exist. The merge refuses instead.
func TestOverlayOnSameJSONPathIsABuildError(t *testing.T) {
	const target = "$.components.schemas.Shared"
	root := fragmentRoot(t, map[string]map[string]string{
		"alpha": {"crm.yaml": overlayFor("alpha", target, "      type: object\n      title: from alpha\n")},
		"beta":  {"crm.yaml": overlayFor("beta", target, "      type: string\n")},
	})
	_, err := composeFrom(t, root)
	if err == nil {
		t.Fatal("two overlays on one JSONPath composed successfully")
	}
	for _, want := range []string{target, "alpha", "beta"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q — the operator cannot find the collision", err, want)
		}
	}

	t.Run("one unit colliding with itself is refused the same way", func(t *testing.T) {
		doc := "overlay: " + overlayVersion + "\ninfo:\n  title: solo\n  version: \"1\"\nactions:\n" +
			"  - target: " + target + "\n    update:\n      type: object\n" +
			"  - target: " + target + "\n    update:\n      type: string\n"
		root := fragmentRoot(t, map[string]map[string]string{"solo": {"crm.yaml": doc}})
		if _, err := composeFrom(t, root); err == nil || !strings.Contains(err.Error(), target) {
			t.Fatalf("err = %v, want the collision refusal", err)
		}
	})
}

// A fragment must actually reach the composed contract, in a form the
// downstream consumers can read — otherwise the whole layer is inert.
func TestFragmentAddsItsNodesToTheComposedContract(t *testing.T) {
	root := fragmentRoot(t, map[string]map[string]string{
		"yogi": {"crm.yaml": overlayFor("yogi", "$.paths['/ext/yogi/quote']", "      get:\n        operationId: yogiQuote\n")},
	})
	files, err := composeFrom(t, root)
	if err != nil {
		t.Fatal(err)
	}
	crm := string(files["crm.yaml"])
	for _, want := range []string{"/ext/yogi/quote:", "yogiQuote", "/v1/deals:", "listDeals"} {
		if !strings.Contains(crm, want) {
			t.Fatalf("composed crm.yaml misses %q:\n%s", want, crm)
		}
	}
	// A contract with no fragment for it stays the base even when a SIBLING
	// contract was merged — the byte-identity rule is per contract.
	wantJobs, err := os.ReadFile(filepath.Join(root, "backend", "api", "jobs.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(files["jobs.yaml"], wantJobs) {
		t.Fatalf("jobs.yaml was reserialized despite carrying no fragment:\n%s", files["jobs.yaml"])
	}
}

// Every way a fragment can be wrong is a build error, named. The api/ layer
// is fail-closed for the same reason astreader.go's manifest reader is: a
// capability this generator does not understand must not compose silently.
func TestFragmentRefusals(t *testing.T) {
	cases := []struct {
		name    string
		api     map[string]string
		wantErr string
	}{
		{
			name:    "a file not named after a core contract",
			api:     map[string]string{"api.yaml": overlayFor("u", "$.paths['/ext/u/x']", "      get: {}\n")},
			wantErr: "does not name a core contract",
		},
		{
			name:    "a subdirectory",
			api:     map[string]string{"v2/crm.yaml": overlayFor("u", "$.paths['/ext/u/x']", "      get: {}\n")},
			wantErr: "flat set of overlay documents",
		},
		{
			name:    "an unknown overlay version",
			api:     map[string]string{"crm.yaml": "overlay: 2.0.0\ninfo:\n  title: u\n  version: \"1\"\nactions:\n  - target: $.paths['/ext/u/x']\n    update: {}\n"},
			wantErr: "overlay 2.0.0",
		},
		{
			name:    "an unknown field in the overlay document",
			api:     map[string]string{"crm.yaml": overlayFor("u", "$.paths['/ext/u/x']", "      get: {}\n") + "extends: crm.yaml\n"},
			wantErr: "extends",
		},
		{
			name:    "a remove action",
			api:     map[string]string{"crm.yaml": "overlay: " + overlayVersion + "\ninfo:\n  title: u\n  version: \"1\"\nactions:\n  - target: $.paths['/v1/deals']\n    remove: true\n"},
			wantErr: "remove",
		},
		{
			name:    "no actions",
			api:     map[string]string{"crm.yaml": "overlay: " + overlayVersion + "\ninfo:\n  title: u\n  version: \"1\"\nactions: []\n"},
			wantErr: "declares no actions",
		},
		{
			name:    "a second YAML document",
			api:     map[string]string{"crm.yaml": overlayFor("u", "$.paths['/ext/u/x']", "      get: {}\n") + "---\nactions: []\n"},
			wantErr: "more than one YAML document",
		},
		{
			name:    "a target this composer cannot evaluate",
			api:     map[string]string{"crm.yaml": overlayFor("u", "$.paths[*].get", "      x: 1\n")},
			wantErr: "target",
		},
		{
			name:    "a target that overwrites an existing node",
			api:     map[string]string{"crm.yaml": overlayFor("u", "$.components.schemas.Deal", "      type: string\n")},
			wantErr: "already declares",
		},
		{
			// A container that exists in one contract but not in the one being
			// targeted: jobs.yaml has no components block, so the walk must
			// refuse rather than invent one.
			name:    "a container the targeted contract does not have",
			api:     map[string]string{"jobs.yaml": overlayFor("u", "$.components.schemas.Thing", "      type: object\n")},
			wantErr: "no components to extend",
		},
		{
			name:    "a target outside every extendable container",
			api:     map[string]string{"crm.yaml": overlayFor("u", "$.webhooks.thing", "      x: 1\n")},
			wantErr: "never the shape of the document",
		},
		{
			name:    "a route outside the unit's namespace",
			api:     map[string]string{"crm.yaml": overlayFor("u", "$.paths['/deals/hijack']", "      get: {}\n")},
			wantErr: "/ext/u",
		},
		{
			// The mistake the contract-relative convention exists to prevent.
			// Every path in these documents is relative to the contract's own
			// servers url, which ends in /v1 — so a fragment spelling the
			// prefix itself would publish https://host/v1/v1/ext/u/x to every
			// generated client, every SDK and the rendered docs at once. It has
			// to be refused here, at the fragment, because by the time the
			// merged document exists nothing downstream can tell the two
			// conventions apart.
			name:    "a route that spells the API base path itself",
			api:     map[string]string{"crm.yaml": overlayFor("u", "$.paths['/v1/ext/u/x']", "      get: {}\n")},
			wantErr: "/ext/u",
		},
		{
			name:    "a route that only looks like the unit's namespace",
			api:     map[string]string{"crm.yaml": overlayFor("u", "$.paths['/ext/undercover/x']", "      get: {}\n")},
			wantErr: "/ext/u",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := fragmentRoot(t, map[string]map[string]string{"u": tc.api})
			_, err := composeFrom(t, root)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// TestFragmentCannotReachInsideACoreNode is the additive-only rule at the
// depth that matters. A leaf-only check is not enough: every target below adds
// a NEW key, so "the final key must not exist" is satisfied, and each one still
// mutates the interior of a node core owns.
//
// This is not theoretical. Before the ownership rule existed,
// $.components.schemas.Deal.properties.hijacked composed successfully into the
// REAL crm.yaml — `injected: true` landed inside the core Deal schema, where
// gen-recordfields and gen-agentpolicy would compile it into a core type the
// moment they read the composed lane.
func TestFragmentCannotReachInsideACoreNode(t *testing.T) {
	cases := []struct{ name, base, target string }{
		{"a core schema's properties", "crm.yaml", "$.components.schemas.Deal.properties.hijacked"},
		{"a core schema directly", "crm.yaml", "$.components.schemas.Deal.hijacked"},
		{"a core job kind", "jobs.yaml", "$.kinds.core_thing.retry"},
		{"a core path item", "crm.yaml", "$.paths['/v1/deals'].post"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := fragmentRoot(t, map[string]map[string]string{
				"u": {tc.base: overlayFor("u", tc.target, "      injected: true\n")},
			})
			files, err := composeFrom(t, root)
			if err == nil {
				t.Fatalf("composed an interior mutation of a core node:\n%s", files[tc.base])
			}
			if !strings.Contains(err.Error(), "never a field inside one") && !strings.Contains(err.Error(), "route namespace") {
				t.Fatalf("err = %v, want the ownership (or route-namespace) refusal", err)
			}
		})
	}

	// The same rule against the REAL contracts, not a stand-in — the shape that
	// actually composed before the fix.
	t.Run("against the committed crm.yaml", func(t *testing.T) {
		_, committed := committedBaseRoot(t)
		frags := []contractFragment{{Unit: "u", Source: "extensions/u/api/crm.yaml",
			Actions: []overlayAction{{
				Target: "$.components.schemas.Deal.properties.hijacked",
				Update: yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"},
			}}}}
		if _, err := mergeContract(committed["crm.yaml"], frags); err == nil {
			t.Fatal("a field was injected into the committed Deal schema")
		} else if !strings.Contains(err.Error(), "components.schemas.Deal") {
			t.Fatalf("err = %v, want the refusal to name the core node", err)
		}
	})
}

// TestOneUnitCannotExtendAnotherUnitsNode closes the ordering-decides-validity
// class the `claimed` map structurally cannot see, because the two targets are
// different strings.
//
// alpha declares $.components.schemas.Shared; beta targets
// $.components.schemas.Shared.properties, whose parent exists ONLY because
// alpha ran first. Before the ownership rule, alpha→beta composed and
// beta→alpha errored — deterministic, so never a flake, but the validity of a
// unit's fragment depended on another unit's name sorting earlier.
func TestOneUnitCannotExtendAnotherUnitsNode(t *testing.T) {
	units := map[string]map[string]string{
		"alpha": {"crm.yaml": overlayFor("alpha", "$.components.schemas.Shared", "      type: object\n")},
		"beta":  {"crm.yaml": overlayFor("beta", "$.components.schemas.Shared.properties", "      stolen: {type: string}\n")},
	}
	files, err := composeFrom(t, fragmentRoot(t, units))
	if err == nil {
		t.Fatalf("beta extended alpha's node:\n%s", files["crm.yaml"])
	}
	for _, want := range []string{"components.schemas.Shared", "alpha", "beta"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err, want)
		}
	}

	// The refusal must not depend on which unit sorts first: reversing the
	// names must refuse too, and for the same reason. Previously exactly one
	// of the two orders composed.
	t.Run("and not the other way round either", func(t *testing.T) {
		reversed := map[string]map[string]string{
			"zeta": {"crm.yaml": overlayFor("zeta", "$.components.schemas.Shared", "      type: object\n")},
			"aaa":  {"crm.yaml": overlayFor("aaa", "$.components.schemas.Shared.properties", "      stolen: {type: string}\n")},
		}
		if _, err := composeFrom(t, fragmentRoot(t, reversed)); err == nil {
			t.Fatal("the reverse order composed — validity still depends on unit name order")
		}
	})
}

// A unit MAY reach inside a node it declared itself: that is the difference
// between an ownership rule and a blanket depth limit, and without it a unit
// could not split a schema across two actions.
func TestAUnitMayExtendItsOwnNode(t *testing.T) {
	doc := "overlay: " + overlayVersion + "\ninfo:\n  title: solo\n  version: \"1\"\nactions:\n" +
		"  - target: $.components.schemas.SoloThing\n    update:\n      type: object\n" +
		"  - target: $.components.schemas.SoloThing.properties\n    update:\n      id: {type: string}\n"
	files, err := composeFrom(t, fragmentRoot(t, map[string]map[string]string{"solo": {"crm.yaml": doc}}))
	if err != nil {
		t.Fatal(err)
	}
	crm := string(files["crm.yaml"])
	for _, want := range []string{"SoloThing:", "properties:", "id: {type: string}"} {
		if !strings.Contains(crm, want) {
			t.Fatalf("composed contract misses %q:\n%s", want, crm)
		}
	}
}

// A top-level target is refused: $.webhooks would add a block of contract
// STRUCTURE, and it also has no owner node for the ownership rule to judge.
func TestFragmentCannotAddATopLevelBlock(t *testing.T) {
	root := fragmentRoot(t, map[string]map[string]string{
		"u": {"crm.yaml": overlayFor("u", "$.webhooks", "      thing: {}\n")},
	})
	_, err := composeFrom(t, root)
	if err == nil || !strings.Contains(err.Error(), "never the shape of the document") {
		t.Fatalf("err = %v, want the container refusal", err)
	}

	// Naming a container itself is refused by the same rule, and must be: it
	// would replace every path in the contract at once.
	t.Run("nor the container itself", func(t *testing.T) {
		root := fragmentRoot(t, map[string]map[string]string{
			"u": {"crm.yaml": overlayFor("u", "$.paths", "      /ext/u/x: {}\n")},
		})
		if _, err := composeFrom(t, root); err == nil || !strings.Contains(err.Error(), "never the shape of the document") {
			t.Fatalf("err = %v, want the container refusal", err)
		}
	})
}

// TestUpdateBlockRejectsADuplicateKey closes the last hole in "can a second
// declaration reach the emitters unseen?". `update:` is a yaml.Node
// destination, which yaml.v3 assigns raw — so neither KnownFields nor its own
// uniqueKeys default applies, and a duplicate would ride verbatim into the
// composed contract for a downstream parser to arbitrate.
func TestUpdateBlockRejectsADuplicateKey(t *testing.T) {
	cases := map[string]string{
		"at the top of the update":  "      type: object\n      type: string\n",
		"nested inside the update":  "      properties:\n        id: {type: string}\n        id: {type: integer}\n",
		"inside a sequence element": "      oneOf:\n        - {type: string, type: integer}\n",
	}
	for name, update := range cases {
		t.Run(name, func(t *testing.T) {
			root := fragmentRoot(t, map[string]map[string]string{
				"u": {"crm.yaml": overlayFor("u", "$.components.schemas.UThing", update)},
			})
			files, err := composeFrom(t, root)
			if err == nil {
				t.Fatalf("a duplicate key rode into the composed contract:\n%s", files["crm.yaml"])
			}
			if !strings.Contains(err.Error(), "declared twice") {
				t.Fatalf("error %q does not report the duplicate", err)
			}
		})
	}
}

// TestParseTargetGrammar pins the constrained JSONPath subset directly. The
// grammar is a refusal surface: mergeContract detects collisions by string
// equality on the target, which is only sound while a target can select at
// most one node — so every selector that could match more than one must be
// refused here rather than evaluated approximately.
func TestParseTargetGrammar(t *testing.T) {
	accepted := map[string][]string{
		"$.paths['/ext/u/thing']":        {"paths", "/ext/u/thing"},
		"$.components.schemas.Thing":     {"components", "schemas", "Thing"},
		"$.kinds.ext_u_send":             {"kinds", "ext_u_send"},
		"$['paths']['/ext/u']":           {"paths", "/ext/u"},
		"$.paths['/ext/u/x'].get":        {"paths", "/ext/u/x", "get"},
		"$.components.schemas.Thing-Two": {"components", "schemas", "Thing-Two"},
	}
	for target, want := range accepted {
		t.Run("accepts "+target, func(t *testing.T) {
			got, err := parseTarget(target)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("steps = %v, want %v", got, want)
			}
		})
	}
	refused := []string{
		"paths.thing",              // not rooted at $
		"$",                        // selects the whole document
		"$.paths[*].get",           // a wildcard selects many
		"$.paths[0]",               // an index is not a child key
		"$..schemas.Thing",         // recursive descent
		"$.paths[?(@.get)]",        // a filter
		"$.paths['/ext/u/x",        // an unterminated literal
		"$.components.schemas.1st", // not an identifier
	}
	for _, target := range refused {
		t.Run("refuses "+target, func(t *testing.T) {
			if steps, err := parseTarget(target); err == nil {
				t.Fatalf("accepted %q as %v", target, steps)
			}
		})
	}
}

// The remaining ways an overlay document can be incomplete. Each is a
// refusal rather than a default, because every default here would publish
// something the unit did not write.
func TestOverlayDocumentCompleteness(t *testing.T) {
	head := "overlay: " + overlayVersion + "\n"
	for name, tc := range map[string]struct{ doc, wantErr string }{
		"no info":      {head + "actions:\n  - target: $.paths['/ext/u/x']\n    update: {}\n", "info.title and info.version"},
		"no version":   {head + "info: {title: u}\nactions:\n  - target: $.paths['/ext/u/x']\n    update: {}\n", "info.title and info.version"},
		"empty target": {head + "info: {title: u, version: \"1\"}\nactions:\n  - target: \"\"\n    update: {}\n", "declares no target"},
		"no update":    {head + "info: {title: u, version: \"1\"}\nactions:\n  - target: $.paths['/ext/u/x']\n", "declares no update"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseOverlay([]byte(tc.doc)); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// mergeContract's two structural refusals about the BASE. Neither can happen
// with today's four contracts; both are here because the composer must fail
// with a sentence rather than a nil dereference if one ever does.
func TestMergeRefusesABaseItCannotExtend(t *testing.T) {
	frag := func(target string) []contractFragment {
		return []contractFragment{{
			Unit:   "u",
			Source: "extensions/u/api/crm.yaml",
			Actions: []overlayAction{{
				Target: target,
				Update: yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "y"},
			}},
		}}
	}
	for name, tc := range map[string]struct{ base, target, wantErr string }{
		"a sequence document":       {"- one\n- two\n", "$.paths['/ext/u/x']", "not a YAML mapping"},
		"an empty document":         {"", "$.paths['/ext/u/x']", "not a YAML mapping"},
		"a non-mapping parent node": {"paths: a string\n", "$.paths['/ext/u/x']", "is not a mapping"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := mergeContract([]byte(tc.base), frag(tc.target)); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// The api/ layer left unbuiltCapabilityLayers, so — exactly as migrations/
// did — it also left that list's subpackage-walk exemption. A Go package
// under api/ runs its init() as unchecked as one anywhere else in the unit.
func TestApiLayerIsGovernedByItsOwnRule(t *testing.T) {
	root := fragmentRoot(t, map[string]map[string]string{
		"u": {"live.go": "package live\n\nfunc init() { panic(\"unchecked\") }\n"},
	})
	_, err := scanExtensions(root)
	if err == nil || !strings.Contains(err.Error(), "holds a Go package outside the unit root") {
		t.Fatalf("err = %v, want the subpackage refusal", err)
	}
}

// Applying two units' fragments must be a function of the unit set, not of
// the order the filesystem happened to hand them over: composedContracts
// walks the already-sorted unit list, so the merge is reproducible.
func TestMergeIsDeterministicInExtensionNameOrder(t *testing.T) {
	units := map[string]map[string]string{
		"beta":  {"crm.yaml": overlayFor("beta", "$.paths['/ext/beta/x']", "      get: {operationId: betaX}\n")},
		"alpha": {"crm.yaml": overlayFor("alpha", "$.paths['/ext/alpha/x']", "      get: {operationId: alphaX}\n")},
	}
	first, err := composeFrom(t, fragmentRoot(t, units))
	if err != nil {
		t.Fatal(err)
	}
	second, err := composeFrom(t, fragmentRoot(t, units))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first["crm.yaml"], second["crm.yaml"]) {
		t.Fatalf("two runs produced different contracts:\n%s\n---\n%s", first["crm.yaml"], second["crm.yaml"])
	}
	crm := string(first["crm.yaml"])
	if !strings.Contains(crm, "alphaX") || !strings.Contains(crm, "betaX") {
		t.Fatalf("composed contract lost a unit's operations:\n%s", crm)
	}
	if strings.Index(crm, "/ext/alpha/x") > strings.Index(crm, "/ext/beta/x") {
		t.Fatalf("units were not applied in name order:\n%s", crm)
	}
}
