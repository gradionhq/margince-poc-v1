// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

// The gates that hold the two halves of the fault vocabulary to one shape: the
// core table in fault.go and the composed table an extension unit declares.
//
// They are FITNESS FUNCTIONS rather than a checklist. The obligation is derived
// from the system — the sentinel registry's own source, and the composed half's
// own validator — so a sentinel added to apperrors or a field added to
// extension.FailureClass fails a gate here instead of quietly reaching an
// operator as "this could not be classified".

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// sentinelRegistrySource is the shared registry's one file. The gate below reads
// it rather than a list kept here, because a list kept here is the thing that
// goes stale — and it goes stale silently, since an unmapped sentinel is a
// perfectly compiling program that reports the wrong thing at run time.
const sentinelRegistrySource = "../../shared/apperrors/apperrors.go"

// TestEverySentinelIsClassifiedForTheJobSurface derives the coverage obligation
// from apperrors' own source: every exported sentinel it declares has an entry in
// the job vocabulary.
//
// It proves coverage by COUNT plus DISTINCTNESS rather than by matching names to
// values, which Go cannot do — a package's vars are not reflectable by name. The
// two together are sufficient: N distinct sentinel values drawn from a registry of
// N distinct sentinels can only be all of them.
func TestEverySentinelIsClassifiedForTheJobSurface(t *testing.T) {
	declared := exportedSentinelNames(t)
	if len(declared) == 0 {
		t.Fatalf("read no sentinels out of %s — the gate cannot derive its obligation, so fix the read before trusting a pass", sentinelRegistrySource)
	}
	seen := make(map[error]string, len(vocabulary))
	for _, known := range vocabulary {
		if known.sentinel == nil {
			t.Fatalf("vocabulary entry %q carries a nil sentinel, so errors.Is can never match it", known.class)
		}
		if prior, dup := seen[known.sentinel]; dup {
			t.Fatalf("classes %q and %q map the same sentinel — the first wins on every lookup, so the second is unreachable text", prior, known.class)
		}
		seen[known.sentinel] = known.class
	}
	if len(seen) != len(declared) {
		t.Fatalf("apperrors declares %d sentinels (%s) and the job vocabulary classifies %d — "+
			"a sentinel with no entry reports to an operator as unclassifiable the first time a job returns it; "+
			"add an entry with a class, a sentence and a remedy",
			len(declared), strings.Join(declared, ", "), len(seen))
	}
}

// exportedSentinelNames reads the exported Err* var names out of the registry's
// AST. It walks declarations rather than grepping, so a name inside a comment or a
// string cannot inflate the count the gate above compares against.
func exportedSentinelNames(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), sentinelRegistrySource, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", sentinelRegistrySource, err)
	}
	var names []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range value.Names {
				if strings.HasPrefix(name.Name, "Err") && name.IsExported() {
					names = append(names, name.Name)
				}
			}
		}
	}
	return names
}

// TestTheCoreVocabularyPassesTheComposedHalfsOwnValidator is the pairing gate.
//
// The two halves are one vocabulary rendered by one surface, so the core half is
// held to the rule the composed half is validated by — every core entry is a
// legal extension.FailureClass. That is what stops the halves drifting: a field
// or a bound added to FailureClass for units to satisfy is one the core entries
// must satisfy too, and this test fails until they do.
func TestTheCoreVocabularyPassesTheComposedHalfsOwnValidator(t *testing.T) {
	as := make([]extension.FailureClass, 0, len(vocabulary))
	for _, known := range vocabulary {
		as = append(as, extension.FailureClass{Class: known.class, Sentence: known.sentence, Remedy: known.remedy})
	}
	if err := extension.ValidateFailureClasses(as); err != nil {
		t.Fatalf("the core job vocabulary does not satisfy the rule composed units are held to: %v", err)
	}
}

// TestNoCoreSentenceIsTheUnclassifiedSubstitute keeps the one sentence on this
// surface that must go on meaning exactly what it says from being claimed by a
// class that DID classify.
func TestNoCoreSentenceIsTheUnclassifiedSubstitute(t *testing.T) {
	for _, known := range vocabulary {
		if known.sentence == unrecognised {
			t.Fatalf("core class %q declares the unclassified substitute as its sentence — that sentence means nobody has classified the failure", known.class)
		}
	}
}

// TestCoreSentencesAndClassesAreEachUnique holds the property both lookups
// depend on: a stored sentence resolves to one class, and a class token names one
// failure.
func TestCoreSentencesAndClassesAreEachUnique(t *testing.T) {
	sentences := make(map[string]string, len(vocabulary))
	classes := make(map[string]struct{}, len(vocabulary))
	for _, known := range vocabulary {
		if prior, dup := sentences[known.sentence]; dup {
			t.Fatalf("classes %q and %q declare one sentence — the stored row carries the sentence alone, so a read cannot tell them apart", prior, known.class)
		}
		sentences[known.sentence] = known.class
		if _, dup := classes[known.class]; dup {
			t.Fatalf("class %q is declared twice in the core vocabulary — one token names one failure", known.class)
		}
		classes[known.class] = struct{}{}
	}
}

// TestAComposedClassCannotImpersonateACoreClass is the other side of the pairing:
// the composed half may extend the vocabulary and may not shadow it.
func TestAComposedClassCannotImpersonateACoreClass(t *testing.T) {
	core := vocabulary[0]
	for _, tc := range []struct {
		name  string
		class extension.FailureClass
		want  string
	}{
		{
			name:  "the same sentence as a core class",
			class: extension.FailureClass{Class: "provider_unavailable", Sentence: core.sentence, Remedy: "check the provider"},
			want:  "read back through the core table first",
		},
		{
			name:  "the same token as a core class",
			class: extension.FailureClass{Class: core.class, Sentence: "the provider was unreachable", Remedy: "check the provider"},
			want:  "one token names one failure",
		},
		{
			name:  "the unclassified substitute",
			class: extension.FailureClass{Class: "provider_unavailable", Sentence: unrecognised, Remedy: "check the provider"},
			want:  "must keep meaning",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(resetComposedFailureClasses)
			err := RegisterComposedFailureClasses(map[string][]extension.FailureClass{
				"ext_unit_job_ws": {tc.class},
			})
			if err == nil {
				t.Fatalf("registered a composed class declaring %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal does not say why: got %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestVettedFailurePrefersTheCoreVocabulary states the precedence
// refuseCoreCollision defends. The check is only correct because of this order,
// and the order is only safe because of the check, so both are held by a test.
func TestVettedFailurePrefersTheCoreVocabulary(t *testing.T) {
	t.Cleanup(resetComposedFailureClasses)
	core := vocabulary[0]
	// Registered under a kind of its own so registration accepts it; the point is
	// the READ, which must still answer with the core class.
	if err := RegisterComposedFailureClasses(map[string][]extension.FailureClass{
		"ext_unit_job_ws": {{Class: "provider_unavailable", Sentence: "the provider could not be reached", Remedy: "check the network"}},
	}); err != nil {
		t.Fatalf("registering a well-formed composed vocabulary: %v", err)
	}
	got, ok := VettedFailure("ext_unit_job_ws", core.sentence)
	if !ok {
		t.Fatalf("a core sentence stored on an extension kind did not vet")
	}
	if got.Class != core.class {
		t.Fatalf("a core sentence resolved to class %q, want the core class %q", got.Class, core.class)
	}
	if got.Remedy != core.remedy {
		t.Fatalf("core class %q resolved with remedy %q, want %q", got.Class, got.Remedy, core.remedy)
	}
}

// TestVettedFailureRefusesWhatItCannotClassify holds the degradation the whole
// design leans on: an unknown sentence, an unknown kind and an empty column each
// report NOTHING rather than a nearest match, so the caller substitutes its own
// fixed text.
func TestVettedFailureRefusesWhatItCannotClassify(t *testing.T) {
	t.Cleanup(resetComposedFailureClasses)
	const kind = "ext_unit_job_ws"
	declared := extension.FailureClass{
		Class: "provider_unavailable", Sentence: "the provider could not be reached", Remedy: "check the network",
	}
	if err := RegisterComposedFailureClasses(map[string][]extension.FailureClass{kind: {declared}}); err != nil {
		t.Fatalf("registering a well-formed composed vocabulary: %v", err)
	}
	for _, tc := range []struct{ name, kind, stored string }{
		{"an empty column", kind, ""},
		{"a raw cause that embeds a vetted sentence", kind, "poll: " + declared.Sentence + " at 10.0.0.1"},
		{"another kind's sentence", "ext_other_job_ws", declared.Sentence},
		{"a sentence nothing declares", kind, "something else went wrong"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := VettedFailure(tc.kind, tc.stored); ok {
				t.Fatalf("vetted %s as class %q — an unclassifiable failure must report none", tc.name, got.Class)
			}
		})
	}
}

// TestAClassifiedFailurePersistsItsDeclaredSentence holds the write half: what
// River stores is the class's sentence, never the cause's own text, and the cause
// stays reachable underneath.
func TestAClassifiedFailurePersistsItsDeclaredSentence(t *testing.T) {
	declared := extension.FailureClass{
		Class:    "provider_unavailable",
		Sentence: "the provider could not be reached from this installation",
		Remedy:   "check the installation's network reach to the provider",
	}
	cause := errors.New("dial tcp: lookup openapi.example: no such host")
	stored := Fault(extension.Failure(declared, cause))
	if stored.Error() != declared.Sentence {
		t.Fatalf("persisted %q, want the declared sentence %q", stored.Error(), declared.Sentence)
	}
	if !errors.Is(stored, cause) {
		t.Fatalf("the cause is no longer reachable through errors.Is, so nothing downstream can classify on it")
	}
	if strings.Contains(stored.Error(), "openapi.example") {
		t.Fatalf("the cause's own text reached the persisted sentence: %q", stored.Error())
	}
}

// TestAUnitsClassWinsOverACoreSentinelItWrapped states the precedence
// FaultContext documents: the unit looked at the whole operation, so its class is
// the more useful of two true statements — and the sentinel underneath survives.
func TestAUnitsClassWinsOverACoreSentinelItWrapped(t *testing.T) {
	declared := extension.FailureClass{
		Class:    "connection_unusable",
		Sentence: "the connection this poll runs on is unusable",
		Remedy:   "re-authorize the connection",
	}
	core := vocabulary[0]
	stored := Fault(extension.Failure(declared, core.sentinel))
	if stored.Error() != declared.Sentence {
		t.Fatalf("persisted %q, want the unit's own sentence %q", stored.Error(), declared.Sentence)
	}
	if !errors.Is(stored, core.sentinel) {
		t.Fatalf("wrapping in a unit class lost the core sentinel underneath")
	}
}

// resetComposedFailureClasses clears the process table between cases.
//
// The table is boot state, so a test that registered one has to put it back or
// the next test in this package reads a vocabulary its own case never declared.
func resetComposedFailureClasses() {
	composedClasses.mu.Lock()
	defer composedClasses.mu.Unlock()
	composedClasses.byKind = nil
	composedClasses.declared = nil
}
