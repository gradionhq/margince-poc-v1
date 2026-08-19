// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The declared failure vocabulary, held to the rules that make it safe to
// persist into river_job.errors — a column with no workspace, no RLS and a
// retention the job runner chooses.

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// Every declared class must survive the boot check, because a set with one bad
// entry registers NONE: a 201-rune sentence or a stray line break in one class
// would cost this unit its whole vocabulary and send every failure back to
// reporting as unclassifiable.
func TestEveryDeclaredFailureClassPassesTheBootValidation(t *testing.T) {
	if err := extension.ValidateFailureClasses(failureClasses); err != nil {
		t.Fatalf("the declared set would be refused at boot: %v", err)
	}
	for _, class := range failureClasses {
		t.Run(class.Class, func(t *testing.T) {
			if err := class.Validate(); err != nil {
				t.Fatalf("%q: %v", class.Class, err)
			}
		})
	}
}

// A stored failure carries the SENTENCE ALONE — no token, no envelope — so the
// read that turns it back into a class and a remedy starts from that string.
// Two classes sharing a sentence would be indistinguishable on that read, and
// an operator would be handed one of two remedies by map order.
//
// The limit of what this test can prove, stated honestly: an extension module
// cannot import internal/platform/jobs, so it cannot compare these sentences
// against the CORE vocabulary's. That collision — a unit sentence equal to a
// core one, which would report as the core class and fire every alert keyed on
// it — is refused on the backend side at registration, and only the within-unit
// half is checkable from here.
func TestNoTwoDeclaredClassesShareATokenOrASentence(t *testing.T) {
	tokens := make(map[string]struct{}, len(failureClasses))
	sentences := make(map[string]string, len(failureClasses))
	for _, class := range failureClasses {
		if _, dup := tokens[class.Class]; dup {
			t.Fatalf("token %q is declared twice — one token names one failure", class.Class)
		}
		tokens[class.Class] = struct{}{}
		if prior, dup := sentences[class.Sentence]; dup {
			t.Fatalf("%q and %q declare one sentence — the stored row carries the sentence alone, so the two are indistinguishable on read", prior, class.Class)
		}
		sentences[class.Sentence] = class.Class
	}
}

// The declaration and the code that classifies are one vocabulary. A class
// failureClass can produce and the unit never declared reaches the wire as the
// unvetted substitute — the vague sentence this whole catalog exists to stop —
// and a declared class nothing can produce is a promise to an operator that no
// failure keeps.
//
// TWO SURFACES, and this test spans both deliberately. Three of these classes
// (token_rejected, package_too_low, api_not_registered) PARK the connection and
// return nil to the runner, so they never reach a river row at all: they are read
// off last_error_class on the connector screen. The rest reach the job surface.
// Both surfaces render the same declared value, which is the property worth
// holding — so the set is checked against what failureClass can produce, not
// against what one of the two surfaces happens to see.
func TestTheDeclaredSetIsExactlyWhatThePollCanReturn(t *testing.T) {
	returnable := []extension.FailureClass{
		failureClass(errUnauthorized),
		failureClass(errTierTooLow),
		failureClass(errAPINotRegistered),
		failureClass(errTransient),
		failureClass(errProvider),
		failureClass(extension.ErrForbidden),
		failureClass(extension.ErrInvalid),
		failureClass(errors.New("unclassified")),
	}
	declared := make(map[extension.FailureClass]bool, len(failureClasses))
	for _, class := range failureClasses {
		declared[class] = true
	}
	produced := make(map[extension.FailureClass]bool, len(returnable))
	for _, class := range returnable {
		produced[class] = true
		if !declared[class] {
			t.Errorf("failureClass can produce %q, which is not in the declared set", class.Class)
		}
	}
	// BOTH DIRECTIONS, because equal lengths prove neither on their own: two
	// causes collapsing onto one class would leave a declared class nothing can
	// produce while every count still matched, and that class is a remedy shown
	// to nobody.
	for _, class := range failureClasses {
		if !produced[class] {
			t.Errorf("%q is declared and nothing produces it — a class no failure can reach is a promise to an operator that nothing keeps", class.Class)
		}
	}
}

// The tick's OWN failure carries the class as well as the cause. Without the
// class the job layer has a cause it may not persist and nothing else to say, so
// an operator reading a dead job is told the failure could not be classified even
// though this unit knew exactly what went wrong. Without the cause underneath,
// everything that classifies on the sentinels downstream breaks.
func TestAFailedTickCarriesItsClassAndItsCauseToTheJobRunner(t *testing.T) {
	rt := connectedRuntime(t, cursor{floor: 900})
	fake := newZaloFake(t)
	fake.errorCode = codeRateLimited

	err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0)))
	if err == nil {
		t.Fatal("a tick that could not reach the provider reported success")
	}
	class, ok := extension.FailureClassOf(err)
	if !ok {
		t.Fatalf("the tick's failure carries no class, so the job surface can only report it as unclassifiable: %v", err)
	}
	if class != classProviderUnavailable {
		t.Fatalf("the tick reported class %q, want %q", class.Class, classProviderUnavailable.Class)
	}
	if !errors.Is(err, errTransient) {
		t.Fatalf("the cause did not survive the classification: %v", err)
	}
}

// TestAnUnreachableProviderPostponesTheTickRatherThanFailingIt.
//
// This is the disposition the classification earns. The class's own remedy says
// nobody needs to do anything — the cursor did not move, so the next reachable
// tick walks the same region and loses nothing — and a tick that FAILED that
// would spend the child's attempts and leave dead work behind, at the cadence of
// the poll, for the length of the outage.
//
// It asks for the delay AS WELL as the disposition, because a postponement with
// no gap is a spin against a provider that is already refusing.
func TestAnUnreachableProviderPostponesTheTickRatherThanFailingIt(t *testing.T) {
	rt := connectedRuntime(t, cursor{floor: 900})
	fake := newZaloFake(t)
	fake.errorCode = codeRateLimited

	err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0)))

	in, asked := extension.RescheduleAfter(err)
	if !asked {
		t.Fatalf("a tick that could not reach the provider asked to fail rather than to run again: %v", err)
	}
	if in != pollRetryDelay {
		t.Fatalf("the tick asked to run again in %s, want the dispatcher's own cadence %s", in, pollRetryDelay)
	}
}

// TestOnlyTheUnreachableProviderPostponesItself is the asymmetry the change
// turns on, held over the WHOLE declared set rather than over the one class that
// motivated it.
//
// Every other class needs a human — a refused credential, a lapsed package, an
// unregistered API group, a demoted authorizing member, an answer this connector
// cannot read — and postponing any of them would be this unit quietly deciding
// not to tell anybody. Deriving the expectation from the declared set means a
// class added later has to make this decision deliberately instead of inheriting
// whichever branch it happens to fall into.
func TestOnlyTheUnreachableProviderPostponesItself(t *testing.T) {
	for _, class := range failureClasses {
		t.Run(class.Class, func(t *testing.T) {
			_, asked := extension.RescheduleAfter(dispositionFor(class, errors.New("cause")))
			want := class.Class == classProviderUnavailable.Class
			if asked != want {
				t.Fatalf("class %q postpones = %v, want %v — only a failure that needs nobody may reschedule itself", class.Class, asked, want)
			}
		})
	}
}
