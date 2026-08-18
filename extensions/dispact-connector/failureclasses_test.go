// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dispact

// The declared failure vocabulary, held to the rules that make it safe to
// persist into river_job.errors — a column with no workspace, no RLS and a
// retention the job runner chooses — plus the fleet classification that is this
// unit's own, since it polls many members where a single-connection unit polls
// one.

import (
	"errors"
	"strconv"
	"strings"
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
	for _, class := range returnable {
		if !declared[class] {
			t.Fatalf("the poll can return %q, which is not in the declared set", class.Class)
		}
	}
	if len(returnable) != len(failureClasses) {
		t.Fatalf("the poll returns %d classes and the unit declares %d — one of them is a class no operator will ever be shown or a promise nothing keeps", len(returnable), len(failureClasses))
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
	err := fleetFailure([]extension.FailureClass{
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
	err := fleetFailure([]extension.FailureClass{
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
	err := fleetFailure([]extension.FailureClass{classTokenRejected})
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
	err := fleetFailure(failed)
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
