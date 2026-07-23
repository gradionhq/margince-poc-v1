// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const repoRoot = "../../.."

// TestDeManifestMatchesItsDerivation binds the committed artifact to the
// committed declaration: the in-tree de unit must carry exactly the
// manifest this generator derives from its source — and, being a
// jurisdiction-only pack (passive policy, requesting no autonomy tier), that
// manifest is identity with an empty autonomy-tiers list.
func TestDeManifestMatchesItsDerivation(t *testing.T) {
	unit, err := scanUnit("de", filepath.Join(repoRoot, "extensions", "de"))
	if err != nil {
		t.Fatal(err)
	}
	derived, err := deriveUnitManifest(unit)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(filepath.Join(repoRoot, "extensions", "de", unitManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(derived, committed) {
		t.Fatalf("extensions/de/%s differs from its derivation — run 'make gen'\n--- committed ---\n%s\n--- derived ---\n%s", unitManifestFile, committed, derived)
	}
	for _, want := range []string{`"name": "de"`, `"version": "1.0.0"`, `"autonomy_tiers": []`} {
		if !strings.Contains(string(derived), want) {
			t.Errorf("derived manifest misses %s:\n%s", want, derived)
		}
	}
}

// deriveSynthetic lays a one-file unit under a temp root and derives its
// manifest.
func deriveSynthetic(t *testing.T, name, source string) ([]byte, error) {
	t.Helper()
	root := t.TempDir()
	writeUnit(t, root, name, map[string]string{
		"go.mod": "module example.test/ext/" + name + "\n\ngo 1.26.5\n",
		"x.go":   source,
	})
	unit, err := scanUnit(name, filepath.Join(root, "extensions", name))
	if err != nil {
		t.Fatal(err)
	}
	return deriveUnitManifest(unit)
}

// jurisdictionOnlySource is the crm-hello / de declaration shape: a unit
// whose only contribution is a jurisdiction pack.
const jurisdictionOnlySource = `package hello

import (
	"github.com/gradionhq/margince/backend/pkg/extension"
	"github.com/gradionhq/margince/backend/pkg/extension/jurisdiction"
)

func New() extension.Extension {
	return extension.Extension{
		Name:          "hello",
		Version:       "0.1.0",
		Jurisdictions: []jurisdiction.Pack{pack{}},
	}
}

type pack struct{}

func (pack) Code() jurisdiction.Code { return "zz" }

func (pack) Retention() jurisdiction.Retention { return nil }
`

// TestJurisdictionPackRequestsNoAutonomyTier: a jurisdiction pack is
// passive policy the core consults — it requests no scope or tier, so it
// contributes NO autonomy-tier request. The Jurisdictions field is
// recognized and skipped, never derived into an entry.
func TestJurisdictionPackRequestsNoAutonomyTier(t *testing.T) {
	derived, err := deriveSynthetic(t, "hello", jurisdictionOnlySource)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(derived), `"autonomy_tiers": []`) {
		t.Fatalf("a jurisdiction-only unit must declare no autonomy tier:\n%s", derived)
	}
	if strings.Contains(string(derived), "jurisdiction") {
		t.Fatalf("the manifest leaked jurisdiction policy into the autonomy-tier surface:\n%s", derived)
	}
}

// TestDeriveUnitManifestIsDeterministic: same source, same bytes — the
// property the drift gate and the digest binding rest on.
func TestDeriveUnitManifestIsDeterministic(t *testing.T) {
	first, err := deriveSynthetic(t, "hello", jurisdictionOnlySource)
	if err != nil {
		t.Fatal(err)
	}
	second, err := deriveSynthetic(t, "hello", jurisdictionOnlySource)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("derivation not deterministic:\n%s\nvs\n%s", first, second)
	}
}

// nonLiteralHeader opens every rejection case's synthetic unit.
const nonLiteralHeader = `package x

import (
	"github.com/gradionhq/margince/backend/pkg/extension"
	"github.com/gradionhq/margince/backend/pkg/extension/jurisdiction"
)
`

// nonLiteralNew wraps a field list into a New() constructor on the
// synthetic unit.
func nonLiteralNew(body string) string {
	return nonLiteralHeader + "func New() extension.Extension {\n\treturn extension.Extension{\n" + body + "\n\t}\n}\n"
}

// nonLiteralCases: a declaration the reader cannot resolve statically is a
// positioned error, never a manifest silently missing a claim — including
// an UNRECOGNIZED field, which could be a future autonomy-tier request the
// generator must be taught before it ships.
var nonLiteralCases = []struct {
	name    string
	source  string
	wantErr string
}{
	{
		name:    "no New constructor",
		source:  nonLiteralHeader + "var _ = jurisdiction.Code(\"zz\")\n",
		wantErr: "no New()",
	},
	{
		name:    "computed version",
		source:  nonLiteralNew("\t\tName: \"x\",\n\t\tVersion: version(),") + "func version() string { return \"1.0.0\" }\n",
		wantErr: "Version must be a string literal",
	},
	{
		name:    "unrecognized extension field fails closed",
		source:  nonLiteralNew("\t\tName: \"x\",\n\t\tVersion: \"1.0.0\",\n\t\tFuture: nil,"),
		wantErr: "field Future is not derivable",
	},
	{
		name:    "name differing from the directory",
		source:  nonLiteralNew("\t\tName: \"other\",\n\t\tVersion: \"1.0.0\","),
		wantErr: "the directory name IS the unit name",
	},
}

func TestDeriveUnitManifestRefusesNonLiteralDeclarations(t *testing.T) {
	for _, tc := range nonLiteralCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := deriveSynthetic(t, "x", tc.source)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// TestDigestTreeExcludesTheUnitManifest: the manifest derives from the
// tree, so its own bytes must not feed the tree digest — otherwise every
// regeneration would invalidate the digest it just recorded.
func TestDigestTreeExcludesTheUnitManifest(t *testing.T) {
	root := t.TempDir()
	writeUnit(t, root, "u", map[string]string{"go.mod": "module m\n", "a.go": "package a\n"})
	dir := filepath.Join(root, "extensions", "u")
	before, err := digestTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, unitManifestFile), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := digestTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("the unit manifest's bytes leaked into the tree digest")
	}
}
