// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

// The two wrappers a unit's tick returns, held to the properties the job seam
// relies on: the class and the disposition survive wrapping, the cause stays
// reachable, and a successful tick cannot be turned into a failed one.
//
// The seam's own behaviour — which classes are honoured, and what the bounds on
// a delay are — is the job package's to prove. What is provable HERE is only
// what this constructor promises, which is deliberately little: it holds a value
// and answers questions about it.

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// unreachable is the class these cases carry. Its content is irrelevant to what
// they prove; it exists so the assertions read about a value rather than about a
// zero struct.
var unreachable = FailureClass{
	Class:    "provider_unavailable",
	Sentence: "the provider could not be reached",
	Remedy:   "Nothing to do: the poll catches up by itself.",
}

// TestARescheduleCarriesItsClassItsDelayAndItsCause.
func TestARescheduleCarriesItsClassItsDelayAndItsCause(t *testing.T) {
	cause := errors.New("dial tcp: no such host")
	err := Reschedule(unreachable, 90*time.Second, cause)

	class, ok := FailureClassOf(err)
	if !ok || class != unreachable {
		t.Fatalf("FailureClassOf = (%v, %v), want the declared class — a postponement is still a classified failure", class, ok)
	}
	in, asked := RescheduleAfter(err)
	if !asked || in != 90*time.Second {
		t.Fatalf("RescheduleAfter = (%s, %v), want (90s, true)", in, asked)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("the cause is no longer reachable, so nothing downstream can classify on it")
	}
}

// TestAPlainFailureAsksForNoPostponement is the pair to the case above, and it
// is the one that keeps the disposition a DECISION. Both wrappers carry a class,
// so a seam that read the class alone would postpone every classified failure —
// including a rejected credential, which needs a human and must become dead work.
func TestAPlainFailureAsksForNoPostponement(t *testing.T) {
	if in, asked := RescheduleAfter(Failure(unreachable, errors.New("refused"))); asked {
		t.Fatalf("RescheduleAfter = (%s, true) for a plain failure, want no request at all", in)
	}
}

// TestAPostponementSurvivesWrapping. A tick adds context on the way out of a
// call stack, and a disposition that only survived at the outermost layer would
// be lost by the first fmt.Errorf above it — silently turning a postponement
// back into the dead row it was written to remove.
func TestAPostponementSurvivesWrapping(t *testing.T) {
	err := fmt.Errorf("zalo-oa: the poll failed: %w",
		Reschedule(unreachable, time.Minute, errors.New("no such host")))

	if in, asked := RescheduleAfter(err); !asked || in != time.Minute {
		t.Fatalf("RescheduleAfter through a wrapper = (%s, %v), want (1m, true)", in, asked)
	}
}

// TestAZeroDelayIsARequestAndNotAnAbsence.
//
// Zero is a delay a unit may legitimately mean — River reads it as "run me
// immediately" — so it must not be indistinguishable from never having asked.
// Reading it as absence would turn the most explicit request a unit can make
// into a dead row, which is why the disposition is a separate field rather than
// a sentinel duration.
func TestAZeroDelayIsARequestAndNotAnAbsence(t *testing.T) {
	in, asked := RescheduleAfter(Reschedule(unreachable, 0, errors.New("refused")))
	if !asked {
		t.Fatalf("a zero delay read as no request at all")
	}
	if in != 0 {
		t.Fatalf("RescheduleAfter = %s, want the zero the unit asked for", in)
	}
}

// TestNeitherWrapperInventsAFailure. A nil cause is a tick that SUCCEEDED, and a
// wrapper answering non-nil for one would fail — or endlessly postpone — work
// that was already done.
func TestNeitherWrapperInventsAFailure(t *testing.T) {
	if got := Failure(unreachable, nil); got != nil {
		t.Errorf("Failure(class, nil) = %v, want nil", got)
	}
	if got := Reschedule(unreachable, time.Minute, nil); got != nil {
		t.Errorf("Reschedule(class, 1m, nil) = %v, want nil", got)
	}
}

// TestRescheduleAfterIgnoresAnUnrelatedError. A plain error has asked for
// nothing, and reading a zero delay out of one would postpone every unclassified
// failure in the tree by whatever the seam's floor happens to be.
func TestRescheduleAfterIgnoresAnUnrelatedError(t *testing.T) {
	if in, asked := RescheduleAfter(errors.New("something else")); asked {
		t.Fatalf("RescheduleAfter = (%s, true) for an unrelated error, want no request", in)
	}
	if in, asked := RescheduleAfter(nil); asked {
		t.Fatalf("RescheduleAfter(nil) = (%s, true), want no request", in)
	}
}

// TestTheOUTERMOSTWrapperDecidesTheDisposition.
//
// Both accessors run their own errors.As from the same error and each takes the
// FIRST *classifiedFailure in the chain, so they always resolve the same value and
// the outermost wins for both. Nothing pinned it, and this is the one place where
// getting it wrong is silent and serious: an implementation that read "through
// wrapping" literally — hunting the chain for any value with a reschedule request
// — would turn a rejected credential wrapped around a transient one into a
// forever-postponement, with every other test in this package still green.
//
// The nesting is not hypothetical. A tick classifies at the point it knows, and a
// caller above it classifies again for the operation as a whole; the outer call is
// the one that saw everything, which is why it decides both the name and the
// disposition rather than one each.
func TestTheOUTERMOSTWrapperDecidesTheDisposition(t *testing.T) {
	needsAHuman := FailureClass{
		Class:    "token_rejected",
		Sentence: "the stored credential was refused",
		Remedy:   "Re-authorize the connection.",
	}
	cause := errors.New("dial tcp: no such host")

	// A plain failure OUTSIDE a postponement: the outer call decided a human is
	// needed, so the tick fails. This is the arm that must not leak a postponement.
	outerFails := Failure(needsAHuman, Reschedule(unreachable, time.Minute, cause))
	if in, asked := RescheduleAfter(outerFails); asked {
		t.Fatalf("RescheduleAfter = (%s, true) for a failure wrapping a postponement — the inner request must not survive a caller that decided somebody is needed", in)
	}
	if class, _ := FailureClassOf(outerFails); class != needsAHuman {
		t.Fatalf("FailureClassOf = %q, want the outer %q — the name and the disposition must come from the same value", class.Class, needsAHuman.Class)
	}

	// And the other way round: a postponement outside a plain failure postpones,
	// under the OUTER class, so neither accessor reads a different value than the
	// other.
	outerPostpones := Reschedule(unreachable, time.Minute, Failure(needsAHuman, cause))
	if _, asked := RescheduleAfter(outerPostpones); !asked {
		t.Fatal("RescheduleAfter found no request on a postponement wrapping a failure")
	}
	if class, _ := FailureClassOf(outerPostpones); class != unreachable {
		t.Fatalf("FailureClassOf = %q, want the outer %q", class.Class, unreachable.Class)
	}
}
