// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
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
	derived, err := deriveUnitManifest(unit, realVocabulary(t), nil)
	if err != nil {
		t.Fatalf("derivation should ignore _scratch.go and read u.go: %v", err)
	}
	if !strings.Contains(string(derived), `"name": "u"`) || strings.Contains(string(derived), "WRONG") {
		t.Fatalf("derivation read the go-ignored file:\n%s", derived)
	}
}

const repoRoot = "../../.."

// committedUnitVerbs merges the repository's real contracts with one unit's
// real fragments and returns that unit's declared operations. It composes the
// unit in ISOLATION (a one-element unit list) so a manifest assertion is not
// coupled to whatever else happens to be enabled in the tree.
func committedUnitVerbs(t *testing.T, unit extensionUnit) []declaredVerb {
	t.Helper()
	units := []extensionUnit{unit}
	contracts, err := composedContracts(repoRoot, units)
	if err != nil {
		t.Fatal(err)
	}
	verbs, err := extensionVerbs(units, contracts)
	if err != nil {
		t.Fatal(err)
	}
	return verbs
}

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
		"TierAutoExecute":          "auto_execute",
		"TierConfirmationRequired": "confirmation_required",
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
		`"tier": "confirmation_required"`,
		`"read"`,
		`"digest": "sha256:`)
}

func assertCommittedManifest(t *testing.T, dir, name string, wantSubstrings ...string) {
	t.Helper()
	unit, err := scanUnit(name, dir)
	if err != nil {
		t.Fatal(err)
	}
	// The verbs come from the MERGED contract, the same way `make gen` derives
	// them, so this test binds the committed manifest to both halves of the
	// declaration — the unit's Go file and its api/ fragment.
	derived, err := deriveUnitManifest(unit, realVocabulary(t), committedUnitVerbs(t, unit))
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
func deriveSynthetic(t *testing.T, name, source string, verbs ...declaredVerb) ([]byte, error) {
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
	return deriveUnitManifest(unit, realVocabulary(t), verbs)
}

// syntheticVerb is the contract-declared half of a unit under test: what the
// unit's api/ fragment would have contributed once merged. Tests that used to
// spell a tier and a scope inside a Go literal spell them here instead, which
// is where a unit author now spells them.
func syntheticVerb(unit, tool, tier, scope string) declaredVerb {
	return declaredVerb{
		verb: extension.Verb{
			Unit:           extension.Name(unit),
			Contract:       "crm.yaml",
			OperationID:    tool + "Op",
			Route:          "/v1/ext/" + unit + "/" + strings.ReplaceAll(tool, "_", "-"),
			Method:         http.MethodPost,
			Tool:           tool,
			Version:        "1.0.0",
			Description:    "Does the one thing its verb names, and reads nothing else.",
			Tier:           extension.Tier(tier),
			RequestedScope: extension.Scope(scope),
		},
		fragmentHash: "0000000000000000000000000000000000000000000000000000000000000000",
	}
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

// TestMigrationsLayerRequestsNoRiskTier: a unit's embedded SQL schema is
// not a governed operation an operator resolves, so the Migrations field is
// recognized and skipped like Jurisdictions. It must not be REFUSED either:
// cmd/migrate reads that field to apply the unit's namespace, so a generator
// rejecting it would make an extension with tables ungeneratable.
func TestMigrationsLayerRequestsNoRiskTier(t *testing.T) {
	const migrationsOnly = `package hello

import (
	"embed"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

//go:embed migrations
var sql embed.FS

func New() extension.Extension {
	return extension.Extension{
		Name:       "hello",
		Version:    "0.1.0",
		Migrations: sql,
	}
}
`
	derived, err := deriveSynthetic(t, "hello", migrationsOnly)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(derived), `"risk_tiers": []`) {
		t.Fatalf("a migrations-only unit must request no risk tier:\n%s", derived)
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

// TestToolDerivesIntoRiskTier is the happy path, and it now runs through the
// CONTRACT: the unit's Go file names a verb and nothing else, the fragment
// (here, its merged result) carries the tier and the scope, and the manifest
// records one risk-tier request whose descriptor digest is present and stable.
func TestToolDerivesIntoRiskTier(t *testing.T) {
	src := toolUnitSource("\t\t\tName: \"sync_contacts\",")
	verb := syntheticVerb("x", "sync_contacts", "auto_execute", "write")
	first, err := deriveSynthetic(t, "x", src, verb)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"id": "tool/sync_contacts"`,
		`"unit": "x"`,
		`"kind": "agent_tool"`,
		`"contract": "crm.yaml"`,
		`"operation": "agent.tool.invoke"`,
		`"operation_id": "sync_contactsOp"`,
		`"route": "/v1/ext/x/sync-contacts"`,
		`"method": "POST"`,
		`"tier": "auto_execute"`,
		`"write"`,
		`"fragment_hash": "0000`,
		`"digest": "sha256:`,
	} {
		if !strings.Contains(string(first), want) {
			t.Errorf("derived tool request misses %s:\n%s", want, first)
		}
	}
	second, err := deriveSynthetic(t, "x", src, verb)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("tool derivation not deterministic:\n%s\nvs\n%s", first, second)
	}
}

// TestEveryDescriptorFieldMovesTheDigest: the widened descriptor is only worth
// widening if each field it names actually re-opens operator resolution. One
// mutation per field, against the same baseline — a field present in the JSON
// and absent from the digest would leave a resolution binding to a capability
// that has since changed.
func TestEveryDescriptorFieldMovesTheDigest(t *testing.T) {
	base := riskTierRequest{
		ID: "tool/t", Unit: "x", Kind: kindAgentTool, Contract: "crm.yaml",
		Operation: opAgentToolInvoke, OperationID: "tOp", Route: "/v1/ext/x/t",
		Method: http.MethodPost, Scopes: []string{"read"}, Tier: "auto_execute",
		FragmentHash: "aa",
	}
	baseline, err := descriptorDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*riskTierRequest){
		"id":            func(c *riskTierRequest) { c.ID = "tool/other" },
		"unit":          func(c *riskTierRequest) { c.Unit = "y" },
		"kind":          func(c *riskTierRequest) { c.Kind = "scheduled_job" },
		"contract":      func(c *riskTierRequest) { c.Contract = "jobs.yaml" },
		"operation":     func(c *riskTierRequest) { c.Operation = "job.tick" },
		"operation_id":  func(c *riskTierRequest) { c.OperationID = "otherOp" },
		"route":         func(c *riskTierRequest) { c.Route = "/v1/ext/x/other" },
		"method":        func(c *riskTierRequest) { c.Method = http.MethodPut },
		"scopes":        func(c *riskTierRequest) { c.Scopes = []string{"send"} },
		"tier":          func(c *riskTierRequest) { c.Tier = "confirmation_required" },
		"fragment_hash": func(c *riskTierRequest) { c.FragmentHash = "bb" },
	} {
		t.Run(name, func(t *testing.T) {
			mutated := base
			mutate(&mutated)
			got, err := descriptorDigest(mutated)
			if err != nil {
				t.Fatal(err)
			}
			if got == baseline {
				t.Fatalf("changing %s did not move the digest — a resolution would carry across it", name)
			}
		})
	}
	// And the one field that must NOT move it: the digest is the descriptor's
	// own name, so carrying a previous one forward cannot change it.
	stale := base
	stale.Digest = "sha256:whatever"
	got, err := descriptorDigest(stale)
	if err != nil {
		t.Fatal(err)
	}
	if got != baseline {
		t.Fatal("the recorded digest fed itself back into the digest")
	}
}

// TestANarrowedToolFieldIsRefusedAtTheDeclaration: after the narrowing, a Tool
// carries {Name, Handle}. A unit still declaring the governance in Go must be
// TOLD, at that line — not have it ignored while the contract's value governs,
// which is the failure mode where two documents disagree and only one is read.
func TestANarrowedToolFieldIsRefusedAtTheDeclaration(t *testing.T) {
	verb := syntheticVerb("x", "t", "auto_execute", "read")
	for _, field := range []string{
		"Tier:           extension.TierAutoExecute,",
		"RequestedScope: extension.ScopeRead,",
		"Description:    \"Reads nothing this workspace holds.\",",
		"Title:          \"T\",",
		"Version:        \"1.0.0\",",
		"InputSchema:    nil,",
		"OutputSchema:   nil,",
	} {
		t.Run(strings.SplitN(field, ":", 2)[0], func(t *testing.T) {
			_, err := deriveSynthetic(t, "x", toolUnitSource("\t\t\tName: \"t\",\n\t\t\t"+field), verb)
			if err == nil || !strings.Contains(err.Error(), "is not derivable by this generator") ||
				!strings.Contains(err.Error(), "fragment") {
				t.Fatalf("err = %v, want the moved-to-the-contract refusal", err)
			}
		})
	}
}

// TestBehaviorForAVerbNoContractDeclaresIsRefused: the one direction of the
// join that is a defect. A Tools entry the contract never declares would be
// registered into the same registry the core tools ride while nothing lists it,
// nothing documents it, and no manifest entry asks an operator about it.
//
// The reverse direction is asserted too, in the same test, because it must NOT
// be an error: a declared verb with no Go behavior is a contract-only governed
// request, which is exactly what fixtures/extensions/crm-hello ships.
func TestBehaviorForAVerbNoContractDeclaresIsRefused(t *testing.T) {
	_, err := deriveSynthetic(t, "x", toolUnitSource("\t\t\tName: \"orphan\","),
		syntheticVerb("x", "declared_elsewhere", "auto_execute", "read"))
	if err == nil || !strings.Contains(err.Error(), "no operation in this unit's api/ fragments declares it") {
		t.Fatalf("err = %v, want the undeclared-behavior refusal", err)
	}

	noGoEntry := `package x

import "github.com/gradionhq/margince/backend/pkg/extension"

func New() extension.Extension {
	return extension.Extension{Name: "x", Version: "0.1.0"}
}
`
	derived, err := deriveSynthetic(t, "x", noGoEntry, syntheticVerb("x", "inert_verb", "confirmation_required", "read"))
	if err != nil {
		t.Fatalf("a contract-only declaration must derive: %v", err)
	}
	if !strings.Contains(string(derived), `"id": "tool/inert_verb"`) {
		t.Fatalf("the contract-only request never reached the manifest:\n%s", derived)
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
		name: "no New constructor",
		// A plain literal, not a conversion: this case is about the missing
		// New(), and a call-bearing initializer (even a type conversion)
		// now trips the separate package-level-init gate first.
		source:  nonLiteralHeader + "var _ jurisdiction.Code = \"zz\"\n",
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
		name:    "tool name is not a verb",
		source:  toolUnitSource("\t\t\tName: \"Bad-Name\","),
		wantErr: "not a valid verb",
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
