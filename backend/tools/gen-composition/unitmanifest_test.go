// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCollectStringConstsHandlesRepeatedValues: Go repeats a grouped
// const's expression list when omitted, so a repeated STRING constant
// must carry forward into the vocabulary, while a repeated int/iota
// constant must not leak in as a string.
func TestCollectStringConstsHandlesRepeatedValues(t *testing.T) {
	const src = `package p
const (
	A = "green"
	B
)
const (
	I = iota
	J
)
`
	file, err := parser.ParseFile(token.NewFileSet(), "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	vocab := map[string]string{}
	collectStringConsts(file, vocab)
	if vocab["A"] != "green" || vocab["B"] != "green" {
		t.Fatalf("repeated string constant B not carried forward: %v", vocab)
	}
	if _, ok := vocab["I"]; ok {
		t.Fatalf("iota constant leaked into the vocabulary: %v", vocab)
	}
	if _, ok := vocab["J"]; ok {
		t.Fatalf("repeated iota constant leaked into the vocabulary: %v", vocab)
	}
}

// TestAddTreeHashesEveryRegularFile: the digest classifies nothing by
// name — a change to ANY shipping file alters it, including a dot-prefixed
// asset an `all:` go:embed can embed and one that happens to end in
// _test.go. Conservative by design: the staleness probe never misses.
func TestAddTreeHashesEveryRegularFile(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "pkg")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(base, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package a\n")
	write(".embedded", "v1")
	write("schema_test.go", "asset-not-source-v1") // an embedded asset that merely ends in _test.go
	digest := func() string {
		h := newTreeHasher(root)
		if err := h.addTree("pkg"); err != nil {
			t.Fatal(err)
		}
		return h.sum()
	}
	for _, edit := range []struct{ name, body string }{
		{".embedded", "v2"},
		{"schema_test.go", "asset-not-source-v2"},
	} {
		before := digest()
		write(edit.name, edit.body)
		if digest() == before {
			t.Fatalf("a change to %s was not reflected in the digest", edit.name)
		}
	}
}

// TestDeriveUnitManifestIgnoresGoIgnoredFiles: a file the go tool never
// compiles (dot- or underscore-prefixed) must not feed the New() scan —
// otherwise a stray New() in _scratch.go could bind the manifest to source
// the binary never sees, or trip the multiple-New guard.
func TestDeriveUnitManifestIgnoresGoIgnoredFiles(t *testing.T) {
	root := t.TempDir()
	bogus := "package u\n\nimport \"github.com/gradionhq/margince/backend/pkg/extension\"\n\nfunc New() extension.Extension { return extension.Extension{Name: \"WRONG\", Version: \"9\"} }\n"
	writeUnit(t, root, "u", map[string]string{
		"go.mod": "module example.test/ext/u\n\ngo 1.26.5\n",
		"u.go":   "package u\n\nimport \"github.com/gradionhq/margince/backend/pkg/extension\"\n\nfunc New() extension.Extension { return extension.Extension{Name: \"u\", Version: \"1.0.0\"} }\n",
		// Both go/build name-ignored forms carry a bogus New(); neither may
		// feed the scan (else the multiple-New guard would trip).
		"_scratch.go": bogus,
		".scratch.go": bogus,
	})
	unit, err := scanUnit("u", filepath.Join(root, "extensions", "u"))
	if err != nil {
		t.Fatal(err)
	}
	derived, err := deriveUnitManifest(unit, realVocabulary(t))
	if err != nil {
		t.Fatalf("derivation should ignore _scratch.go and read u.go: %v", err)
	}
	if !strings.Contains(string(derived), `"name": "u"`) || strings.Contains(string(derived), "WRONG") {
		t.Fatalf("derivation read the go-ignored file:\n%s", derived)
	}
}

const repoRoot = "../../.."

func realVocabulary(t *testing.T) map[string]string {
	t.Helper()
	vocab, err := publishedVocabulary(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	return vocab
}

// TestPublishedVocabularyDerivesFromTheSeamSource: the reader's Tier and
// Scope table comes from parsing the published package, so a constant
// added to the seam is derivable without touching this tool.
func TestPublishedVocabularyDerivesFromTheSeamSource(t *testing.T) {
	vocab := realVocabulary(t)
	for ident, want := range map[string]string{
		"TierAutoExecute":          "green",
		"TierConfirmationRequired": "yellow",
		"ScopeRead":                "read",
		"ScopeWrite":               "write",
		"ScopeSend":                "send",
	} {
		if got := vocab[ident]; got != want {
			t.Errorf("vocab[%s] = %q, want %q", ident, got, want)
		}
	}
}

// TestDeManifestMatchesItsDerivation binds the committed artifact to the
// committed declaration: de is a jurisdiction-only pack (passive policy,
// requesting no autonomy tier), so its manifest is identity with an empty
// autonomy-tiers list.
func TestDeManifestMatchesItsDerivation(t *testing.T) {
	assertCommittedManifest(t, filepath.Join(repoRoot, "extensions", "de"), "de",
		`"name": "de"`, `"version": "1.0.0"`, `"autonomy_tiers": []`)
}

// TestCrmHelloManifestMatchesItsDerivation is the worked example: the
// crm-hello fixture declares a jurisdiction pack (skipped) AND a governed
// 🟡 tool, so its committed manifest carries exactly one autonomy-tier
// request with its §5 descriptor and digest.
func TestCrmHelloManifestMatchesItsDerivation(t *testing.T) {
	assertCommittedManifest(t, filepath.Join(repoRoot, "fixtures", "extensions", "crm-hello"), "crm-hello",
		`"id": "tool/hello_ping"`,
		`"operation": "agent.tool.invoke"`,
		`"tier": "yellow"`,
		`"read"`,
		`"digest": "sha256:`)
}

func assertCommittedManifest(t *testing.T, dir, name string, wantSubstrings ...string) {
	t.Helper()
	unit, err := scanUnit(name, dir)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := deriveUnitManifest(unit, realVocabulary(t))
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(filepath.Join(dir, unitManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(derived, committed) {
		t.Fatalf("%s/%s differs from its derivation — run 'make gen'\n--- committed ---\n%s\n--- derived ---\n%s", name, unitManifestFile, committed, derived)
	}
	for _, want := range wantSubstrings {
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

// TestJurisdictionPackRequestsNoAutonomyTier: a jurisdiction pack is
// passive policy the core consults — it requests no scope or tier, so it
// contributes NO autonomy-tier request. The Jurisdictions field is
// recognized and skipped, never derived into an entry.
func TestJurisdictionPackRequestsNoAutonomyTier(t *testing.T) {
	const jurisdictionOnly = `package hello

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
	derived, err := deriveSynthetic(t, "hello", jurisdictionOnly)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(derived), `"autonomy_tiers": []`) {
		t.Fatalf("a jurisdiction-only unit must request no autonomy tier:\n%s", derived)
	}
	if strings.Contains(string(derived), "jurisdiction") {
		t.Fatalf("the manifest leaked jurisdiction policy into the autonomy-tier surface:\n%s", derived)
	}
}

// toolUnitSource is a unit declaring one governed tool with the given
// field body.
func toolUnitSource(toolFields string) string {
	return `package x

import "github.com/gradionhq/margince/backend/pkg/extension"

func New() extension.Extension {
	return extension.Extension{
		Name:    "x",
		Version: "0.1.0",
		Tools: []extension.Tool{{
` + toolFields + `
		}},
	}
}
`
}

// TestToolDerivesIntoAutonomyTier is the happy path: a declared 🟢 tool
// with a required scope becomes one autonomy-tier request whose
// descriptor digest is present and stable across derivations.
func TestToolDerivesIntoAutonomyTier(t *testing.T) {
	src := toolUnitSource("\t\t\tName: \"sync_contacts\", Version: \"2.1.0\",\n\t\t\tTier: extension.TierAutoExecute,\n\t\t\tRequestedScope: extension.ScopeWrite,")
	first, err := deriveSynthetic(t, "x", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"id": "tool/sync_contacts"`,
		`"operation": "agent.tool.invoke"`,
		`"tier": "green"`,
		`"write"`,
		`"digest": "sha256:`,
	} {
		if !strings.Contains(string(first), want) {
			t.Errorf("derived tool request misses %s:\n%s", want, first)
		}
	}
	second, err := deriveSynthetic(t, "x", src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("tool derivation not deterministic:\n%s\nvs\n%s", first, second)
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
// an UNRECOGNIZED field, which could be a future governed capability the
// generator must be taught before it ships, and a tool whose declared
// tier or scope is outside the published vocabulary.
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
	{
		name:    "tool tier outside the extension vocabulary",
		source:  toolUnitSource("\t\t\tName: \"t\", Version: \"1.0.0\", Tier: \"dynamic\", RequestedScope: extension.ScopeRead,"),
		wantErr: "not one an extension may request",
	},
	{
		name:    "tool scope outside the passport vocabulary",
		source:  toolUnitSource("\t\t\tName: \"t\", Version: \"1.0.0\", Tier: extension.TierAutoExecute, RequestedScope: \"admin\","),
		wantErr: "not in the Passport scope vocabulary",
	},
	{
		name:    "tool name is not a verb",
		source:  toolUnitSource("\t\t\tName: \"Bad-Name\", Version: \"1.0.0\", Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,"),
		wantErr: "not a valid verb",
	},
	{
		name:    "computed tool tier",
		source:  toolUnitSource("\t\t\tName: \"t\", Version: \"1.0.0\", Tier: tierOf(), RequestedScope: extension.ScopeRead,") + "\nfunc tierOf() extension.Tier { return extension.TierAutoExecute }\n",
		wantErr: "published extension constant",
	},
	{
		name: "multiple New constructors",
		source: nonLiteralHeader +
			"func New() extension.Extension { return extension.Extension{Name: \"x\", Version: \"1.0.0\"} }\n" +
			"func New() extension.Extension { return extension.Extension{Name: \"x\", Version: \"2.0.0\"} }\n",
		wantErr: "multiple New() constructors",
	},
	{
		name:    "version with surrounding whitespace",
		source:  nonLiteralNew("\t\tName: \"x\",\n\t\tVersion: \" 1.0.0\","),
		wantErr: "surrounding whitespace",
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
