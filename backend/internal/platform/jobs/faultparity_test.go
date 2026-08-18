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
	"strconv"
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
// Go cannot reflect a package's vars by NAME, so the gate proves coverage from
// three properties instead, and it needs all three:
//
//   - every entry's sentinel is DISTINCT, so no two entries cover one sentinel;
//   - every entry's sentinel MESSAGE is one the registry's source declares, which
//     is what rules out an entry covering an error from somewhere else entirely;
//   - the COUNT matches the number of exported sentinels.
//
// Distinctness and count alone would not do it. An entry keyed on, say,
// context.DeadlineExceeded plus one missing apperrors sentinel keeps both numbers
// equal and the gate would pass while the obligation was broken. Membership is
// what closes that, and it is checkable because errors.New's message is a literal
// in the source the AST can read.
func TestEverySentinelIsClassifiedForTheJobSurface(t *testing.T) {
	names, messages := exportedSentinels(t)
	if len(names) == 0 || len(messages) == 0 {
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
		if _, member := messages[known.sentinel.Error()]; !member {
			t.Fatalf("class %q is keyed on an error %s does not declare (%q) — an entry for something outside the registry inflates the count and hides a sentinel that has no entry at all",
				known.class, sentinelRegistrySource, known.sentinel.Error())
		}
		seen[known.sentinel] = known.class
	}
	if len(seen) != len(names) {
		t.Fatalf("apperrors declares %d sentinels (%s) and the job vocabulary classifies %d — "+
			"a sentinel with no entry reports to an operator as unclassifiable the first time a job returns it; "+
			"add an entry with a class, a sentence and a remedy",
			len(names), strings.Join(names, ", "), len(seen))
	}
}

// exportedSentinels reads the registry's AST for the exported Err* var NAMES and
// the set of MESSAGES they are constructed with.
//
// It walks declarations rather than grepping, so a name inside a comment or a
// string cannot inflate the count the gate above compares against. The messages
// come from the errors.New literal on each var, which is what lets the gate ask
// whether a vocabulary entry's sentinel belongs to this registry at all — the
// value cannot be matched to its name, but its message can be matched to the set.
func exportedSentinels(t *testing.T) ([]string, map[string]struct{}) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), sentinelRegistrySource, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", sentinelRegistrySource, err)
	}
	var names []string
	messages := map[string]struct{}{}
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
			for i, name := range value.Names {
				if !strings.HasPrefix(name.Name, "Err") || !name.IsExported() {
					continue
				}
				names = append(names, name.Name)
				if msg, ok := sentinelMessage(value, i); ok {
					messages[msg] = struct{}{}
				}
			}
		}
	}
	return names, messages
}

// sentinelMessage reads the string literal one sentinel is constructed with, and
// reports absence for anything that is not that shape.
//
// A var this cannot read contributes no message, which makes the membership check
// STRICTER rather than weaker: a vocabulary entry keyed on it then fails the
// member test and the author has to make the registry readable or say why. The
// alternative — treating an unreadable var as admitting anything — is how a gate
// starts passing for the wrong reason.
func sentinelMessage(spec *ast.ValueSpec, i int) (string, bool) {
	if i >= len(spec.Values) {
		return "", false
	}
	call, ok := spec.Values[i].(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return "", false
	}
	literal, ok := call.Args[0].(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	unquoted, err := strconv.Unquote(literal.Value)
	if err != nil {
		return "", false
	}
	return unquoted, true
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

// TestNoCoreSentenceIsASubstitute keeps the sentences this surface uses when it
// has NOTHING to say about a failure from being claimed by a class that DID
// classify — held over the core half, as refuseCoreCollision holds it over the
// composed half.
func TestNoCoreSentenceIsASubstitute(t *testing.T) {
	for _, known := range vocabulary {
		for _, substitute := range substitutes {
			if known.sentence == substitute {
				t.Fatalf("core class %q declares a substitute as its sentence — a substitute means nobody classified the failure", known.class)
			}
		}
	}
}

// TestEverySubstituteIsDistinct: the three say three different things — nobody
// classified it, nobody could vet it, nothing was recorded — and a reader tells
// them apart by the string alone. Two that collided would collapse three facts
// into two while every test above still passed.
func TestEverySubstituteIsDistinct(t *testing.T) {
	seen := make(map[string]struct{}, len(substitutes))
	for _, substitute := range substitutes {
		if _, dup := seen[substitute]; dup {
			t.Fatalf("two substitutes share the sentence %q, so a reader cannot tell which fact it reports", substitute)
		}
		seen[substitute] = struct{}{}
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
		// ALL THREE substitutes, not just the one this check sits next to. Two of
		// them were constants of the HTTP surface, out of reach of this refusal —
		// and a class claiming one would have rendered as the substitute WITH a
		// class attached, indistinguishable from a genuinely unclassifiable
		// failure except for the class nobody should have been able to attach.
		{
			name:  "the unclassified substitute",
			class: extension.FailureClass{Class: "provider_unavailable", Sentence: unrecognised, Remedy: "check the provider"},
			want:  "may not claim one",
		},
		{
			name:  "the unvetted substitute",
			class: extension.FailureClass{Class: "provider_unavailable", Sentence: UnvettedFailureReason, Remedy: "check the provider"},
			want:  "may not claim one",
		},
		{
			name:  "the no-recorded-cause substitute",
			class: extension.FailureClass{Class: "provider_unavailable", Sentence: NoRecordedCause, Remedy: "check the provider"},
			want:  "may not claim one",
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
	t.Cleanup(resetComposedFailureClasses)
	const kind = "ext_unit_job_ws"
	declared := extension.FailureClass{
		Class:    "provider_unavailable",
		Sentence: "the provider could not be reached from this installation",
		Remedy:   "check the installation's network reach to the provider",
	}
	registerForTest(t, kind, declared)
	cause := errors.New("dial tcp: lookup openapi.example: no such host")
	stored := FaultForKind(t.Context(), kind, extension.Failure(declared, cause))
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

// TestAnUnregisteredClassNeverPublishesItsSentence is the security gate on the
// WRITE path, and the reason the write path verifies instead of trusting.
//
// extension.Failure is a plain constructor that accepts any value a unit builds,
// so a unit can format a sentence out of the cause — the exact accident this seam
// exists to prevent, because a provider's prose routinely names the address it
// refused. Boot validation cannot see a value made at tick time. So an
// unregistered class is refused where it would otherwise be persisted, and the
// text it carried never reaches river_job.errors: a column with no workspace, no
// RLS, and every workspace's admin reading it.
func TestAnUnregisteredClassNeverPublishesItsSentence(t *testing.T) {
	t.Cleanup(resetComposedFailureClasses)
	const kind = "ext_unit_job_ws"
	registerForTest(t, kind, extension.FailureClass{
		Class:    "provider_unavailable",
		Sentence: "the provider could not be reached from this installation",
		Remedy:   "check the installation's network reach to the provider",
	})
	// Well-formed, and never declared: it would satisfy Validate and pass every
	// bound. What it carries is a phone number.
	adHoc := extension.FailureClass{
		Class:    "provider_refused",
		Sentence: "the provider refused the message to +84901234567",
		Remedy:   "check the recipient",
	}
	for _, tc := range []struct{ name, kind string }{
		{"a class this installation never declared", kind},
		{"a declared class under another kind", "ext_other_job_ws"},
		{"no kind at all, as FaultContext has none", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := FaultForKind(t.Context(), tc.kind, extension.Failure(adHoc, errors.New("upstream said no")))
			if stored.Error() != unrecognised {
				t.Fatalf("persisted %q, want the unclassified substitute — an unverified class must not publish its sentence", stored.Error())
			}
			if strings.Contains(stored.Error(), "+84901234567") {
				t.Fatalf("an undeclared class published caller-formatted text into the failure column: %q", stored.Error())
			}
		})
	}
}

// TestAUnitsClassWinsOverACoreSentinelItWrapped states the precedence
// FaultForKind documents: the unit looked at the whole operation, so its class is
// the more useful of two true statements — and the sentinel underneath survives.
func TestAUnitsClassWinsOverACoreSentinelItWrapped(t *testing.T) {
	t.Cleanup(resetComposedFailureClasses)
	const kind = "ext_unit_job_ws"
	declared := extension.FailureClass{
		Class:    "connection_unusable",
		Sentence: "the connection this poll runs on is unusable",
		Remedy:   "re-authorize the connection",
	}
	registerForTest(t, kind, declared)
	core := vocabulary[0]
	stored := FaultForKind(t.Context(), kind, extension.Failure(declared, core.sentinel))
	if stored.Error() != declared.Sentence {
		t.Fatalf("persisted %q, want the unit's own sentence %q", stored.Error(), declared.Sentence)
	}
	if !errors.Is(stored, core.sentinel) {
		t.Fatalf("wrapping in a unit class lost the core sentinel underneath")
	}
}

// TestAnUnregisteredClassStillGetsItsWrappedSentinelsSentence: refusing the class
// must not cost the failure a classification it would have had anyway. A unit that
// forgot to declare a class loses ITS OWN detail and nothing else.
func TestAnUnregisteredClassStillGetsItsWrappedSentinelsSentence(t *testing.T) {
	t.Cleanup(resetComposedFailureClasses)
	core := vocabulary[0]
	undeclared := extension.FailureClass{
		Class:    "never_declared",
		Sentence: "something the boot never validated",
		Remedy:   "nothing",
	}
	stored := FaultForKind(t.Context(), "ext_unit_job_ws", extension.Failure(undeclared, core.sentinel))
	if stored.Error() != core.sentence {
		t.Fatalf("persisted %q, want the wrapped sentinel's own sentence %q", stored.Error(), core.sentence)
	}
}

// registerForTest settles a one-kind composed vocabulary, failing the test rather
// than the assertion below it when the registration itself is refused.
func registerForTest(t *testing.T, kind string, classes ...extension.FailureClass) {
	t.Helper()
	if err := RegisterComposedFailureClasses(map[string][]extension.FailureClass{kind: classes}); err != nil {
		t.Fatalf("registering a well-formed composed vocabulary: %v", err)
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
