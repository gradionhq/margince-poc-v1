// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The declared failure vocabulary, held to the rules that make it safe to
// persist into river_job.errors — a column with no workspace, no RLS and a
// retention the job runner chooses.

import (
	"context"
	"errors"
	"fmt"
	"github.com/gradionhq/margince/backend/pkg/extension"
	"testing"
	"time"
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

// postponingClasses is the expectation this unit's disposition is held to, WRITTEN
// OUT rather than derived from dispositionFor's own predicate.
//
// The derived version — `want := class.Class == classProviderUnavailable.Class` —
// reads like a spec and is a tautology: it restates the branch it is checking, so
// a class added to failureClasses later passes automatically under whichever
// answer the code happens to give it. That is the opposite of what this gate is
// for. A hand-written table plus the completeness check below means a new class
// makes somebody write down what it should DO, which is the decision that gets
// forgotten.
var postponingClasses = map[string]bool{
	classTokenRejected.Class:          false,
	classPackageTooLow.Class:          false,
	classAPINotRegistered.Class:       false,
	classMemberNotPermitted.Class:     false,
	classConnectionUnusable.Class:     false,
	classProviderUnavailable.Class:    true,
	classProviderAnswerUnusable.Class: false,
	classPollFailed.Class:             false,
}

func TestOnlyTheUnreachableProviderPostponesItself(t *testing.T) {
	// EVERY declared class has an entry, and no entry names a class that is not
	// declared. This is the half that makes the table above better than the
	// predicate it replaced: a class added without a disposition fails here rather
	// than silently inheriting one.
	if len(postponingClasses) != len(failureClasses) {
		t.Fatalf("%d declared classes and %d disposition expectations — a class added without a decision about what it DOES is the thing this table exists to catch",
			len(failureClasses), len(postponingClasses))
	}
	for _, class := range failureClasses {
		t.Run(class.Class, func(t *testing.T) {
			want, declared := postponingClasses[class.Class]
			if !declared {
				t.Fatalf("class %q has no disposition expectation — write down whether it postpones", class.Class)
			}
			_, asked := extension.RescheduleAfter(dispositionFor(t.Context(), class, errors.New("cause")))
			if asked != want {
				t.Fatalf("class %q postpones = %v, want %v — only a failure that needs nobody may reschedule itself", class.Class, asked, want)
			}
		})
	}
}

// TestATickWhoseWindowRanOutFailsEvenThoughItClassifiesAsUnreachable.
//
// The one case where the class and the disposition part company. A tick that ran
// out of wall clock did not meet an outage — it met its own window, because there
// is more work here than the window holds, and every later tick spends the same
// window and expires in the same place. Postponing that hides a tick that can
// NEVER finish behind a row that looks like it is waiting patiently, with no dead
// work and no error column anywhere to say otherwise. A cancelled context is a
// role shutting down, and postponing that delays the next poll by a whole cadence
// on every restart.
//
// The TICK'S CONTEXT is what decides, not the cause: the transport formats what
// the HTTP client said as text, so a deadline is not reachable through errors.Is
// by the time a disposition is chosen. Asserted here so that a later refactor
// reaching for the cause instead finds out that it cannot work.
func TestATickWhoseWindowRanOutFailsEvenThoughItClassifiesAsUnreachable(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  func() context.Context
	}{
		{"a window that ran out", func() context.Context {
			ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			t.Cleanup(cancel)
			return ctx
		}},
		{"a role shutting down", func() context.Context {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			return ctx
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := fmt.Errorf("%w: the request went out and nothing came back", errTransient)
			// The CLASS is unchanged — nothing was reached, and that is what an
			// operator should be told. Only the disposition differs.
			if got := failureClass(cause); got.Class != classProviderUnavailable.Class {
				t.Fatalf("the cause classifies as %q, want %q — this case is about the disposition, not the name", got.Class, classProviderUnavailable.Class)
			}
			if _, asked := extension.RescheduleAfter(dispositionFor(tc.ctx(), classProviderUnavailable, cause)); asked {
				t.Fatalf("%s postponed itself, so a tick that can never finish would retry forever with nothing to show an operator", tc.name)
			}
		})
	}
}
