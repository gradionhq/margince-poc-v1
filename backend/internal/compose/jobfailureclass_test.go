// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The composed half of the job-failure detail: that a unit's declared
// vocabulary reaches the read keyed by BOTH of its kinds, that it is refused at
// boot when it is malformed, and that one kind's vocabulary is never read for
// another kind's row.

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// providerUnavailable is a well-formed declared class, used as the thing that
// should survive the trip from a unit's declaration to an operator's screen.
var providerUnavailable = extension.FailureClass{
	Class:    "provider_unavailable",
	Sentence: "the provider was unreachable for the whole pass",
	Remedy:   "check this installation's network reach to the provider; the poll catches up on its own.",
}

// TestADeclaredVocabularyIsRegisteredForBothTheDispatcherAndTheChildKind — a
// dispatcher fails too, while it is deciding whom to fan out for, and its rows
// carry a different kind from the child's. Registering one kind would leave the
// other reporting a classified failure as unvettable.
func TestADeclaredVocabularyIsRegisteredForBothTheDispatcherAndTheChildKind(t *testing.T) {
	decl := jobDecl()
	unit := unitWithJob("refresh", noopTick)
	unit.FailureClasses = []extension.FailureClass{providerUnavailable}

	if err := RegisterExtensions([]extension.Extension{unit}, nil, []extension.JobDeclaration{decl}); err != nil {
		t.Fatalf("RegisterExtensions: %v", err)
	}
	restoreVanillaComposedTables(t)

	for _, kind := range []string{decl.DispatcherKind(), decl.ChildKind()} {
		declared := jobs.ComposedFailureClasses(kind)
		if len(declared) != 1 {
			t.Fatalf("kind %s carries %d declared classes, want the unit's one", kind, len(declared))
		}
		if declared[0] != providerUnavailable {
			t.Errorf("kind %s carries %+v, want the declared class verbatim", kind, declared[0])
		}
	}
}

// TestADeclaredClassRendersItsSentenceClassAndRemedyForItsOwnKindOnly is the
// point of keying the vocabulary by kind: two units are entitled to the same
// token, so the row's kind is what says whose vocabulary to read. Resolving
// another kind's row through this one would report a failure as something it is
// not.
func TestADeclaredClassRendersItsSentenceClassAndRemedyForItsOwnKindOnly(t *testing.T) {
	const mine, theirs = "ext_demo_refresh_workspace", "ext_other_sweep_workspace"
	if err := jobs.RegisterComposedFailureClasses(map[string][]extension.FailureClass{
		mine: {providerUnavailable},
	}); err != nil {
		t.Fatalf("RegisterComposedFailureClasses: %v", err)
	}
	restoreVanillaComposedTables(t)

	rendered := renderFailure(jobs.Failure{Kind: mine, StoredReason: providerUnavailable.Sentence})
	if rendered.Reason != providerUnavailable.Sentence {
		t.Errorf("reason = %q, want the declared sentence", rendered.Reason)
	}
	if rendered.Class == nil || *rendered.Class != providerUnavailable.Class {
		t.Errorf("class = %v, want %q", rendered.Class, providerUnavailable.Class)
	}
	if rendered.Remedy == nil || *rendered.Remedy != providerUnavailable.Remedy {
		t.Errorf("remedy = %v, want the declared remedy", rendered.Remedy)
	}

	// The same stored sentence under a kind that declared nothing. It is not a
	// leak — the text is the unit's own fixed prose — but asserting a class for
	// it would tell an operator which unit's failure this is on no evidence.
	other := renderFailure(jobs.Failure{Kind: theirs, StoredReason: providerUnavailable.Sentence})
	if other.Class != nil || other.Remedy != nil {
		t.Errorf("kind %s resolved another kind's vocabulary: class %v, remedy %v",
			theirs, other.Class, other.Remedy)
	}
	if other.Reason != jobs.UnvettedFailureReason {
		t.Errorf("reason = %q, want the fixed substitute for text this kind cannot vet", other.Reason)
	}
}

// TestACoreSentinelsFailureCarriesItsClassAndRemedy — the core half of one
// vocabulary rendered by one surface. An operator must not be able to tell from
// a failure list which tier classified the failure, which means the core rows
// carry a class and a remedy exactly as a composed one does.
func TestACoreSentinelsFailureCarriesItsClassAndRemedy(t *testing.T) {
	const recordGone = "the record this job names no longer exists"
	rendered := renderFailure(jobs.Failure{Kind: "any_core_kind", StoredReason: recordGone})

	if rendered.Class == nil || *rendered.Class != "record_gone" {
		t.Fatalf("class = %v, want record_gone", rendered.Class)
	}
	if rendered.Remedy == nil || *rendered.Remedy == "" {
		t.Error("a classified failure reached the surface with no remedy; the class alone is " +
			"a label an operator cannot act on")
	}
}

// TestTheFaultSeamsOwnUnclassifiedSentenceSurvivesWithoutAClass — Fault wrote
// that sentence and wrote the log line it points at, so substituting it for the
// weaker "cannot vet" text would trade a true pointer for a vaguer one. There is
// still no class: an unclassified failure is the one nobody has named yet.
func TestTheFaultSeamsOwnUnclassifiedSentenceSurvivesWithoutAClass(t *testing.T) {
	const unclassified = "the job failed for a reason it could not classify; the diagnosis is in the process log"
	rendered := renderFailure(jobs.Failure{Kind: "any_core_kind", StoredReason: unclassified})

	if rendered.Reason != unclassified {
		t.Errorf("reason = %q, want the fault seam's own sentence passed through", rendered.Reason)
	}
	if rendered.Class != nil || rendered.Remedy != nil {
		t.Errorf("an unclassified failure was given class %v / remedy %v", rendered.Class, rendered.Remedy)
	}
}

// TestRegisterExtensionsRefusesAMalformedFailureClassAndNamesTheUnit — the
// registration is keyed by River kind, and a kind names a unit only to somebody
// who can decode the namespace. The unit is what an operator has to go fix.
func TestRegisterExtensionsRefusesAMalformedFailureClassAndNamesTheUnit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		class extension.FailureClass
		want  string
	}{
		{
			name:  "token that is not lower snake_case",
			class: extension.FailureClass{Class: "Provider Unavailable", Sentence: "it broke", Remedy: "look at it"},
			want:  "not a valid class token",
		},
		{
			// A class whose author could not say what to do about it has not
			// finished classifying, and an operator reading it at 2am has no
			// next step.
			name:  "no remedy",
			class: extension.FailureClass{Class: "provider_unavailable", Sentence: "it broke"},
			want:  "declares no remedy",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := RegisterExtensions([]extension.Extension{{
				Name: "bad-vocab", Version: "0.0.1",
				FailureClasses: []extension.FailureClass{tc.class},
			}}, nil, nil)

			if err == nil {
				t.Fatal("RegisterExtensions accepted a malformed failure class")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to carry %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), `extension "bad-vocab"`) {
				t.Errorf("err = %v, want it to name the unit that has to be fixed", err)
			}
		})
	}
}

// TestAUnitThatDeclaresNoVocabularyRegistersNoKind — ComposedFailureKinds
// answers what an installation composed, and an empty entry per kind would make
// every composed job look like it had declared a vocabulary it never wrote.
func TestAUnitThatDeclaresNoVocabularyRegistersNoKind(t *testing.T) {
	decl := jobDecl()
	byKind := composedFailureClasses(
		[]extension.Extension{unitWithJob("refresh", noopTick)},
		[]composedJob{{decl: decl, handle: noopTick}},
	)

	if len(byKind) != 0 {
		t.Errorf("a unit with no declared classes contributed %v", byKind)
	}
}

// restoreVanillaComposedTables puts the process back to a vanilla composition
// when the test that registered a composed set finishes.
//
// The composed kind and vocabulary tables are process-global and settled once at
// a real boot, so a test that registers into them leaves every later test in this
// binary running against an installation it did not compose. That is not a
// hypothetical: the workspace-guard suite derives its obligation from
// jobs.Declared(), and a composed workspace kind left behind here demands a
// refusal driver that only a real extension could supply.
//
// Both registrations REPLACE rather than merge, so an empty set is the reset.
func restoreVanillaComposedTables(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if err := jobs.RegisterComposed(nil); err != nil {
			t.Errorf("clearing the composed kind table: %v", err)
		}
		if err := jobs.RegisterComposedFailureClasses(nil); err != nil {
			t.Errorf("clearing the composed failure vocabulary: %v", err)
		}
	})
}
