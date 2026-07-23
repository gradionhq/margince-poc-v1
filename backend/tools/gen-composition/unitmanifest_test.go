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

func realVocabulary(t *testing.T) map[string]string {
	t.Helper()
	vocab, err := publishedVocabulary(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	return vocab
}

// TestPublishedVocabularyDerivesFromTheSeamSource: the reader's constant
// table comes from parsing the published package, so a constant added to
// the seam is derivable without touching this tool.
func TestPublishedVocabularyDerivesFromTheSeamSource(t *testing.T) {
	vocab := realVocabulary(t)
	for ident, want := range map[string]string{
		"CommercialCorrespondence": "commercial_correspondence",
		"AccountingRecords":        "accounting_records",
		"AnchorOccurrence":         "occurrence",
		"AnchorCalendarYearEnd":    "calendar_year_end",
	} {
		if got := vocab[ident]; got != want {
			t.Errorf("vocab[%s] = %q, want %q", ident, got, want)
		}
	}
}

// TestDeManifestMatchesItsDerivation binds the committed artifact to the
// committed declaration: the in-tree de unit must carry exactly the
// manifest this generator derives from its source.
func TestDeManifestMatchesItsDerivation(t *testing.T) {
	unit, err := scanUnit("de", filepath.Join(repoRoot, "extensions", "de"))
	if err != nil {
		t.Fatal(err)
	}
	derived, err := deriveUnitManifest(unit, realVocabulary(t))
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
	for _, want := range []string{
		`"id": "jurisdiction/de"`,
		`"keep": "P6Y"`,
		`"keep": "P8Y"`,
		`"anchor": "calendar_year_end"`,
	} {
		if !strings.Contains(string(derived), want) {
			t.Errorf("derived manifest misses %s:\n%s", want, derived)
		}
	}
}

// deriveSynthetic lays a one-file unit under a temp root and derives its
// manifest with the real published vocabulary.
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
	return deriveUnitManifest(unit, realVocabulary(t))
}

const helloLikeSource = `package hello

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

// TestDeriveUnitManifestHandlesANilRetentionPack: a pack declaring no
// floors (the crm-hello fixture shape) yields an empty retention list,
// never a null — the manifest schema stays uniform for consumers.
func TestDeriveUnitManifestHandlesANilRetentionPack(t *testing.T) {
	derived, err := deriveSynthetic(t, "hello", helloLikeSource)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"id": "jurisdiction/zz"`, `"retention": []`, `"scopes": []`} {
		if !strings.Contains(string(derived), want) {
			t.Errorf("derived manifest misses %s:\n%s", want, derived)
		}
	}
}

// TestDeriveUnitManifestIsDeterministic: same source, same bytes — the
// property the drift gate and the digest binding rest on.
func TestDeriveUnitManifestIsDeterministic(t *testing.T) {
	first, err := deriveSynthetic(t, "hello", helloLikeSource)
	if err != nil {
		t.Fatal(err)
	}
	second, err := deriveSynthetic(t, "hello", helloLikeSource)
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

// nonLiteralCases: every computed value is a hard, positioned error — a
// silent gap here would be a capability invisible to review while
// present at boot.
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
		name:    "unknown extension field",
		source:  nonLiteralNew("\t\tName: \"x\",\n\t\tVersion: \"1.0.0\",\n\t\tFuture: nil,"),
		wantErr: "field Future is not derivable",
	},
	{
		name:    "name differing from the directory",
		source:  nonLiteralNew("\t\tName: \"other\",\n\t\tVersion: \"1.0.0\","),
		wantErr: "the directory name IS the unit name",
	},
	{
		name: "computed pack code",
		source: nonLiteralNew("\t\tName: \"x\",\n\t\tVersion: \"1.0.0\",\n\t\tJurisdictions: []jurisdiction.Pack{pack{}},") + `type pack struct{}

func (pack) Code() jurisdiction.Code { return code() }

func code() jurisdiction.Code { return "zz" }

func (pack) Retention() jurisdiction.Retention { return nil }
`,
		wantErr: "Code() must be a string literal",
	},
	{
		name: "conditional retention",
		source: nonLiteralNew("\t\tName: \"x\",\n\t\tVersion: \"1.0.0\",\n\t\tJurisdictions: []jurisdiction.Pack{pack{}},") + `type pack struct{}

func (pack) Code() jurisdiction.Code { return "zz" }

func (pack) Retention() jurisdiction.Retention {
	if true {
		return nil
	}
	return nil
}
`,
		wantErr: "exactly one return statement",
	},
	{
		name: "constant outside the published vocabulary",
		source: nonLiteralNew("\t\tName: \"x\",\n\t\tVersion: \"1.0.0\",\n\t\tJurisdictions: []jurisdiction.Pack{pack{}},") + `type pack struct{}

func (pack) Code() jurisdiction.Code { return "zz" }

func (pack) Retention() jurisdiction.Retention { return ret{} }

type ret struct{}

const myClass = "commercial_correspondence"

func (ret) Classes() []jurisdiction.RetentionClass {
	return []jurisdiction.RetentionClass{{Name: myClass, Keep: jurisdiction.Period{Years: 6}}}
}
`,
		wantErr: "expected a string literal or a published jurisdiction constant",
	},
	{
		name: "negative period caught at gen time",
		source: nonLiteralNew("\t\tName: \"x\",\n\t\tVersion: \"1.0.0\",\n\t\tJurisdictions: []jurisdiction.Pack{pack{}},") + `type pack struct{}

func (pack) Code() jurisdiction.Code { return "zz" }

func (pack) Retention() jurisdiction.Retention { return ret{} }

type ret struct{}

func (ret) Classes() []jurisdiction.RetentionClass {
	return []jurisdiction.RetentionClass{{Name: jurisdiction.CommercialCorrespondence, Keep: jurisdiction.Period{Years: -6}}}
}
`,
		wantErr: "negative component",
	},
	{
		name: "duplicate capability id",
		source: nonLiteralNew("\t\tName: \"x\",\n\t\tVersion: \"1.0.0\",\n\t\tJurisdictions: []jurisdiction.Pack{pack{}, pack{}},") + `type pack struct{}

func (pack) Code() jurisdiction.Code { return "zz" }

func (pack) Retention() jurisdiction.Retention { return nil }
`,
		wantErr: "capability jurisdiction/zz declared twice",
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
