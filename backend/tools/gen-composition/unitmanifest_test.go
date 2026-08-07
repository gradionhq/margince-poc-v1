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
// requesting no risk tier), so its manifest is identity with an empty
// risk-tiers list.
func TestDeManifestMatchesItsDerivation(t *testing.T) {
	assertCommittedManifest(t, filepath.Join(repoRoot, "extensions", "de"), "de",
		`"name": "de"`, `"version": "1.0.0"`, `"risk_tiers": []`)
}

// TestCrmHelloManifestMatchesItsDerivation is the worked example: the
// crm-hello fixture declares a jurisdiction pack (skipped) AND a governed
// 🟡 tool, so its committed manifest carries exactly one risk-tier
// request with its security descriptor and digest.
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

// TestJurisdictionPackRequestsNoRiskTier: a jurisdiction pack is
// passive policy the core consults — it requests no scope or tier, so it
// contributes NO risk-tier request. The Jurisdictions field is
// recognized and skipped, never derived into an entry.
func TestJurisdictionPackRequestsNoRiskTier(t *testing.T) {
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
	if !strings.Contains(string(derived), `"risk_tiers": []`) {
		t.Fatalf("a jurisdiction-only unit must request no risk tier:\n%s", derived)
	}
	if strings.Contains(string(derived), "jurisdiction") {
		t.Fatalf("the manifest leaked jurisdiction policy into the risk-tier surface:\n%s", derived)
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

// TestToolDerivesIntoRiskTier is the happy path: a declared 🟢 tool
// with a required scope becomes one risk-tier request whose
// descriptor digest is present and stable across derivations.
func TestToolDerivesIntoRiskTier(t *testing.T) {
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

// TestADeclaredTitleDerivesButStaysOutOfTheDescriptor: a display string
// grants nothing, so declaring one must neither fail the derivation (it did:
// the field was unrecognized, which made a unit that named its tool
// unbuildable) nor move the digest an operator decision binds to.
func TestADeclaredTitleDerivesButStaysOutOfTheDescriptor(t *testing.T) {
	fields := "\t\t\tName: \"sync_contacts\", Version: \"2.1.0\",\n\t\t\tTier: extension.TierAutoExecute,\n\t\t\tRequestedScope: extension.ScopeWrite,"
	untitled, err := deriveSynthetic(t, "x", toolUnitSource(fields))
	if err != nil {
		t.Fatal(err)
	}
	titled, err := deriveSynthetic(t, "x", toolUnitSource("\t\t\tTitle: \"Sync contacts\",\n"+fields))
	if err != nil {
		t.Fatalf("a declared title must derive: %v", err)
	}
	if !bytes.Equal(untitled, titled) {
		t.Fatalf("the title reached the governance descriptor:\n%s\nvs\n%s", untitled, titled)
	}
}

// Prose that will not fit on one line is written as literals joined by +, and
// that is still a value fixed at the declaration — the generator computes it
// without evaluating anything. Refusing it would push a unit author into one
// unreadable line to satisfy a tool that never had to be satisfied.
func TestAConcatenatedLiteralDerivesLikeASingleOne(t *testing.T) {
	fields := "\t\t\tName: \"sync_contacts\", Version: \"2.1.0\",\n\t\t\tTier: extension.TierAutoExecute,\n\t\t\tRequestedScope: extension.ScopeWrite,"
	joined, err := deriveSynthetic(t, "x", toolUnitSource("\t\t\tDescription: \"Keep the contacts in step. \" +\n\t\t\t\t\"It reads nothing this workspace holds.\",\n"+fields))
	if err != nil {
		t.Fatalf("literals joined by + must derive: %v", err)
	}
	// Like the title, it is validated and then left out of the governance
	// descriptor: a resolution binds to a digest, never to prose.
	plain, err := deriveSynthetic(t, "x", toolUnitSource(fields))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(joined, plain) {
		t.Fatalf("the description reached the governance descriptor:\n%s\nvs\n%s", joined, plain)
	}
}

// A served tool with no description is refused by the composition at boot. The
// generator can see the same thing — a Handle key and no Description — so it
// says so at the declaration, with a line and a column, rather than leaving the
// author a boot failure in whatever process composes their unit. An INERT tool
// is untouched: it is a manifest request nothing serves to a client.
func TestAServedToolWithNoDescriptionIsRefusedAtTheDeclaration(t *testing.T) {
	handler := "\nfunc handle(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }\n"
	imports := "package x\n\nimport (\n\t\"context\"\n\t\"encoding/json\"\n\n\t\"github.com/gradionhq/margince/backend/pkg/extension\"\n)\n"
	unit := func(fields string) string {
		return imports + "\nfunc New() extension.Extension {\n\treturn extension.Extension{\n\t\tName:    \"x\",\n\t\tVersion: \"0.1.0\",\n\t\tTools: []extension.Tool{{\n" + fields + "\n\t\t}},\n\t}\n}\n" + handler
	}
	base := "\t\t\tName: \"t\", Version: \"1.0.0\", Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,"

	_, err := deriveSynthetic(t, "x", unit(base+"\n\t\t\tHandle: handle,"))
	if err == nil || !strings.Contains(err.Error(), "serves a handler but declares no Description") {
		t.Fatalf("err = %v, want the undescribed-served-tool refusal", err)
	}
	if _, err := deriveSynthetic(t, "x", unit(base)); err != nil {
		t.Fatalf("an undescribed INERT tool must still derive: %v", err)
	}
	// The two spellings of an inert handler. Both reach the adapter as the same
	// nil function value, so a reader that recognised only one would refuse a
	// declaration the runtime serves nothing for.
	for _, spelling := range []string{"nil", "extension.ToolHandler(nil)", "(nil)"} {
		if _, err := deriveSynthetic(t, "x", unit(base+"\n\t\t\tHandle: "+spelling+",")); err != nil {
			t.Errorf("an undescribed tool with Handle: %s must derive as inert: %v", spelling, err)
		}
	}
	described := base + "\n\t\t\tDescription: \"Keeps the contacts in step, and reads nothing else.\",\n\t\t\tHandle: handle,"
	if _, err := deriveSynthetic(t, "x", unit(described)); err != nil {
		t.Fatalf("a described served tool must derive: %v", err)
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
	{
		// The core registry refuses a blank title with a boot panic, so the
		// unit author has to hear it here, at the declaration, instead.
		name:    "tool title that renders as nothing",
		source:  toolUnitSource("\t\t\tName: \"t\", Title: \"   \", Version: \"1.0.0\", Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,"),
		wantErr: "is blank or carries surrounding whitespace",
	},
	{
		// A concatenation is resolved literal by literal, so a computed piece
		// on either side of the + is still a value the manifest cannot derive
		// — and this is the shape it hides in most easily, next to real prose.
		name:    "a computed piece opening a concatenated description",
		source:  toolUnitSource("\t\t\tName: \"t\", Description: opening() + \" It reads nothing.\", Version: \"1.0.0\", Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,") + "\nfunc opening() string { return \"D\" }\n",
		wantErr: "Tool.Description must be a string literal",
	},
	{
		name:    "a computed piece closing a concatenated description",
		source:  toolUnitSource("\t\t\tName: \"t\", Description: \"Keeps the contacts in step. \" + closing(), Version: \"1.0.0\", Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,") + "\nfunc closing() string { return \"D\" }\n",
		wantErr: "Tool.Description must be a string literal",
	},
	{
		// Selection prose is the one field a unit author is most likely to
		// compute — from a constant, a helper, a template. The manifest derives
		// statically, so it has to be told here rather than at boot.
		name:    "computed tool description",
		source:  toolUnitSource("\t\t\tName: \"t\", Description: describe(), Version: \"1.0.0\", Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,") + "\nfunc describe() string { return \"D\" }\n",
		wantErr: "Tool.Description must be a string literal",
	},
	{
		name:    "tool description that renders as nothing",
		source:  toolUnitSource("\t\t\tName: \"t\", Description: \"   \", Version: \"1.0.0\", Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,"),
		wantErr: "is blank or carries surrounding whitespace",
	},
	{
		name:    "computed tool title",
		source:  toolUnitSource("\t\t\tName: \"t\", Title: titleOf(), Version: \"1.0.0\", Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,") + "\nfunc titleOf() string { return \"T\" }\n",
		wantErr: "Tool.Title must be a string literal",
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
