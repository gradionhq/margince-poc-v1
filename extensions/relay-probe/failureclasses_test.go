// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relayprobe

// The declared failure vocabulary, held to the rules that make it safe to
// persist into river_job.errors — a column with no workspace, no RLS and a
// retention the job runner chooses — plus the fleet classification that is this
// unit's own, since it polls many members where a single-connection unit polls
// one.

import (
	"context"
	"errors"
	"fmt"
	"github.com/gradionhq/margince/backend/pkg/extension"
	"strconv"
	"strings"
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
// read that turns it back into a class and a remedy starts from that string. Two
// classes sharing a sentence would be indistinguishable on that read, and an
// operator would be handed one of two remedies by map order.
//
// The limit of what this test can prove, stated honestly: an extension module
// cannot import internal/platform/jobs, so it cannot compare these sentences
// against the CORE vocabulary's. That collision — a unit sentence equal to a core
// one, which would report as the core class and fire every alert keyed on it — is
// refused on the backend side at registration, and only the within-unit half is
// checkable from here.
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

// The declaration and the code that returns classes are one vocabulary. A class
// the poll can return and the unit never declared reaches the wire as the unvetted
// substitute — the vague sentence this whole catalog exists to stop — and a
// declared class nothing can return is a promise to an operator that no failure
// keeps.
//
// The fleet class is included by hand because it is the one class no per-member
// classification produces: only a whole tick can be in that state.
func TestTheDeclaredSetIsExactlyWhatThePollCanReturn(t *testing.T) {
	returnable := []extension.FailureClass{
		failureClass(errUnauthorized),
		failureClass(errTransient),
		failureClass(errProvider),
		failureClass(extension.ErrForbidden),
		failureClass(extension.ErrInvalid),
		failureClass(errors.New("unclassified")),
		classEveryMemberFailed,
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

// A fleet-wide outage must report the OUTAGE, not the fact that it was fleet-wide.
//
// This is the case the whole fleet classification exists for: every member failing
// because the provider cannot be reached from this installation is one condition
// happening in several places, and reporting the shared class is what names the
// thing to go fix. Answering "everybody failed" would discard the one fact the
// tick established.
func TestEveryMemberFailingTheSameWayReportsThatWayNotTheFleet(t *testing.T) {
	err := fleetFailure(t.Context(), []extension.FailureClass{
		classProviderUnavailable, classProviderUnavailable, classProviderUnavailable,
	})
	class, ok := extension.FailureClassOf(err)
	if !ok {
		t.Fatal("a fleet failure carried no class, so the job surface has nothing to report but that it could not classify it")
	}
	if class.Class != classProviderUnavailable.Class {
		t.Fatalf("three members failing on an unreachable provider reported %q, want %q", class.Class, classProviderUnavailable.Class)
	}
}

// Members failing for DIFFERENT reasons is the genuinely different situation, and
// the class for it must not pick one member's cause to speak for the rest: there
// is no single outage behind them, and a class implying one sends an operator
// chasing something that is not there.
func TestMembersFailingDifferentlyReportsTheFleetClass(t *testing.T) {
	err := fleetFailure(t.Context(), []extension.FailureClass{
		classProviderUnavailable, classTokenRejected,
	})
	class, ok := extension.FailureClassOf(err)
	if !ok {
		t.Fatal("a fleet failure carried no class")
	}
	if class.Class != classEveryMemberFailed.Class {
		t.Fatalf("two members failing differently reported %q, want %q", class.Class, classEveryMemberFailed.Class)
	}
}

// A single connected member that fails is still a whole fleet failing, and it must
// report its own class rather than the fleet's — an installation with one member
// is the common case, and telling its operator "every member failed, and not all
// for the same reason" about one member would be both useless and untrue.
func TestOneMemberFailingReportsItsOwnClass(t *testing.T) {
	err := fleetFailure(t.Context(), []extension.FailureClass{classTokenRejected})
	class, ok := extension.FailureClassOf(err)
	if !ok {
		t.Fatal("a fleet failure carried no class")
	}
	if class.Class != classTokenRejected.Class {
		t.Fatalf("one member failing on a refused token reported %q, want %q", class.Class, classTokenRejected.Class)
	}
}

// The cause survives underneath the class, and names how wide the outage was.
//
// Everything that classifies on a sentinel downstream reads through the wrapper,
// so a class that replaced its cause instead of wrapping it would break them. The
// count is a process-log detail rather than something a caller branches on, which
// is why it is asserted through the rendered string an operator actually reads.
func TestAFleetFailureKeepsItsCauseReachable(t *testing.T) {
	failed := []extension.FailureClass{classProviderUnavailable, classProviderUnavailable}
	err := fleetFailure(t.Context(), failed)
	if err == nil {
		t.Fatal("a fleet in which every member failed reported success")
	}
	cause := errors.Unwrap(err)
	if cause == nil {
		t.Fatal("the class replaced its cause instead of wrapping it, so nothing downstream can read through it")
	}
	if !strings.Contains(cause.Error(), strconv.Itoa(len(failed))) {
		t.Fatalf("the cause does not name how many members failed: %q", cause.Error())
	}
}

// TestAFleetWideOutagePostponesTheTickRatherThanFailingIt.
//
// This is the disposition the shared classification earns. Every member failing
// on an unreachable provider is one outage, its own remedy says nobody needs to
// do anything, and no cursor moved — so a tick that FAILED it would spend the
// child's attempts and leave dead work behind, at the cadence of the poll, for
// the length of the outage.
//
// The delay is asserted as well as the disposition: a postponement with no gap
// is a spin against a provider that is already refusing.
func TestAFleetWideOutagePostponesTheTickRatherThanFailingIt(t *testing.T) {
	err := fleetFailure(t.Context(), []extension.FailureClass{classProviderUnavailable, classProviderUnavailable})

	in, asked := extension.RescheduleAfter(err)
	if !asked {
		t.Fatalf("a fleet that could not reach the provider asked to fail rather than to run again: %v", err)
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
// answer the code happens to give it. A hand-written table plus the completeness
// check below means a new class makes somebody write down what it should DO.
var postponingClasses = map[string]bool{
	classTokenRejected.Class:          false,
	classMemberNotPermitted.Class:     false,
	classConnectionUnusable.Class:     false,
	classProviderUnavailable.Class:    true,
	classProviderAnswerUnusable.Class: false,
	classPollFailed.Class:             false,
	classEveryMemberFailed.Class:      false,
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

// TestOneMemberFailingOnAnUnreachableProviderStillPostpones.
//
// An installation with ONE connected member is the common case, and its whole
// fleet failing is the same outage a hundred members' would be. Reading the
// postponement off the shared class rather than off the member count is what
// makes the two behave alike.
func TestOneMemberFailingOnAnUnreachableProviderStillPostpones(t *testing.T) {
	if _, asked := extension.RescheduleAfter(fleetFailure(t.Context(), []extension.FailureClass{classProviderUnavailable})); !asked {
		t.Fatal("a single-member fleet meeting an unreachable provider asked to fail rather than to run again")
	}
}
