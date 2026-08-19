// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// WHAT A FAILED TICK IS CALLED AND WHAT IT DOES ABOUT IT, held as two separate
// obligations because they failed separately.
//
// This unit landed on main after the change that gave the other two connectors
// both halves, so it had neither. It published no class, which meant the job
// surface printed the unclassified substitute — the exact sentence the failure
// vocabulary exists to stop printing — and, with no class to travel under, a
// fleet-wide outage spent the child's attempts and left one dead row per tick for
// however long Zalo was unreachable, each one raising a banner about work no human
// can help with.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// TestAFleetWideOutageIsClassifiedAndPostponesTheTickInsteadOfDying.
//
// Driven through pollFleet rather than through fleetFailure, because the defect was
// never in the classification: this unit already computed `provider_unavailable`
// and wrote it to the member's own row. What it did not do was ROUTE it — the tick
// returned a bare fmt.Errorf, so everything the unit knew died at the job seam. A
// case that called fleetFailure directly would have passed against the bug.
func TestAFleetWideOutageIsClassifiedAndPostponesTheTickInsteadOfDying(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, oneMember(), chosen(), nil)
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}
	provider := newProvider(nil)
	provider.openErr = &transportError{
		Method: "GET",
		URL:    "https://wpa.chat.zalo.me/api/login/getLoginInfo",
		Err:    errors.New("dial tcp: connection refused"),
	}

	err := pollFleet(context.Background(), rt, provider.open())
	if err == nil {
		t.Fatal("a fleet-wide outage answered a successful tick")
	}

	class, classified := extension.FailureClassOf(err)
	if !classified {
		t.Fatalf("the tick failed unclassified, so the job surface can only say the diagnosis is in a log: %v", err)
	}
	if class != classProviderUnavailable {
		t.Fatalf("classified as %q, want %q", class.Class, classProviderUnavailable.Class)
	}
	in, postponed := extension.RescheduleAfter(err)
	if !postponed {
		t.Fatal("the tick asked to FAIL over an outage needing no intervention; every tick of it becomes dead work an operator is shown")
	}
	if in != pollRetryDelay {
		t.Fatalf("postponed by %s, want the dispatcher's own cadence %s", in, pollRetryDelay)
	}
	// The CAUSE still travels underneath, which is what keeps the count out of the
	// fleet-visible column and in the process log where it belongs.
	if !strings.Contains(err.Error(), "1 connection(s)") {
		t.Fatalf("the cause did not survive the wrapper, so the log line says less than the tick knew: %v", err)
	}
}

// TestAFleetFailingInSeveralDifferentWaysIsNotOneOutage.
//
// Reported as its own class rather than by picking one member's cause to speak for
// the rest: nothing is common to them, so there is no single thing to go fix and a
// shared class would send an operator chasing one. It also must NOT postpone —
// several problems with several owners are not an outage waiting to clear, and
// postponing would be this unit quietly deciding not to tell any of them.
func TestAFleetFailingInSeveralDifferentWaysIsNotOneOutage(t *testing.T) {
	t.Parallel()
	mixed := []extension.FailureClass{classProviderUnavailable, classSessionWithdrawn}

	err := fleetFailure(context.Background(), mixed)

	class, classified := extension.FailureClassOf(err)
	if !classified || class != classEveryMemberFailed {
		t.Fatalf("classified as %q, want %q", class.Class, classEveryMemberFailed.Class)
	}
	if _, postponed := extension.RescheduleAfter(err); postponed {
		t.Fatal("a fleet failing for unrelated reasons postponed itself, so nobody is told about any of them")
	}
}

// TestASharedFleetClassIsReportedAsItselfAndOnlyPostponesWhenNothingIsOwed.
//
// Every member failing the same way is not a fleet condition needing its own name —
// it is that one condition, happening everywhere — so the shared class is what turns
// a screenful of dead jobs into a sentence naming the thing to go fix.
//
// The disposition then splits on the class and not on the scale. A session Zalo has
// stopped accepting needs a specific human with a specific phone, so it stays dead
// work however many members it happened to; only the class whose own remedy says
// nothing needs re-scanning may disappear off the screen.
func TestASharedFleetClassIsReportedAsItselfAndOnlyPostponesWhenNothingIsOwed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		shared    extension.FailureClass
		postpones bool
	}{
		{"Zalo unreachable by anybody, which needs nobody", classProviderUnavailable, true},
		{"every session withdrawn, which needs every one of those people", classSessionWithdrawn, false},
		{"every connection unusable, which needs a re-scan each", classConnectionUnusable, false},
		{"a cause this connector cannot yet name", classPollFailed, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := fleetFailure(context.Background(), []extension.FailureClass{tc.shared, tc.shared})

			class, classified := extension.FailureClassOf(err)
			if !classified || class != tc.shared {
				t.Fatalf("classified as %q, want the shared class %q", class.Class, tc.shared.Class)
			}
			if _, postponed := extension.RescheduleAfter(err); postponed != tc.postpones {
				t.Fatalf("postponed = %v, want %v", postponed, tc.postpones)
			}
		})
	}
}

// TestATickThatRanOutOfItsOwnWallClockIsNotAnOutageToPostpone.
//
// THIS UNIT IS THE ONE MOST ABLE TO REACH THIS STATE, which is why the case is here
// rather than left to the sibling connectors' suites: a tick drains one socket per
// member until it goes quiet, so a fleet larger than the job's 300s window expires
// every single time. The classification is still right — nobody was reached — but
// postponing would hide a fan-out that can NEVER finish behind a row that looks like
// it is waiting patiently, with no dead work and no error column anywhere to say
// otherwise. It needs a wider timeout or fewer members per tick, and a human to
// choose between them.
func TestATickThatRanOutOfItsOwnWallClockIsNotAnOutageToPostpone(t *testing.T) {
	t.Parallel()
	for name, done := range map[string]func() (context.Context, context.CancelFunc){
		// A NEGATIVE budget rather than a short one that is waited out: the
		// deadline is already past the moment the context exists, so the case
		// carries no sleep and cannot race a slow machine.
		"our own wall clock ran out": func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), -time.Second)
		},
		"the role is shutting down": func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, func() {}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := done()
			defer cancel()

			err := dispositionFor(ctx, classProviderUnavailable, errors.New("nobody answered"))

			class, classified := extension.FailureClassOf(err)
			if !classified || class != classProviderUnavailable {
				t.Fatalf("classified as %q, want %q — what was reached is still nothing", class.Class, classProviderUnavailable.Class)
			}
			if _, postponed := extension.RescheduleAfter(err); postponed {
				t.Fatalf("%s postponed the tick, hiding a fan-out that cannot finish behind a row that looks patient", name)
			}
		})
	}
}

// TestEveryClassThisUnitAnswersIsOneItDeclared.
//
// An undeclared class is not honoured by the job seam: the sentence is published
// only when the installation registered exactly that value for the failing kind, so
// a class the unit computes and forgets to declare reaches an operator as the
// unclassified substitute. That is the failure mode this unit shipped with, and a
// count is not enough to catch it coming back — a fifth branch added to failureClass
// without a fifth entry in the declaration is invisible to any assertion about
// length.
//
// The causes come from failuresWorthClassing (contractparity_test.go), which is
// already the one list of "a cause per branch failureClass draws", so the two gates
// cannot end up asking about different sets of branches.
func TestEveryClassThisUnitAnswersIsOneItDeclared(t *testing.T) {
	t.Parallel()
	declared := map[extension.FailureClass]bool{}
	for _, class := range failureClasses {
		declared[class] = true
	}
	for _, cause := range failuresWorthClassing() {
		if class := failureClass(cause); !declared[class] {
			t.Fatalf("failureClass answers %q for %v and the unit declares no such value; the job surface would print the unclassified substitute instead of this unit's own words",
				class.Class, cause)
		}
	}
	// The FLEET class is declared but answered by nothing above, and that is
	// correct rather than an oversight: it describes the TICK, which has no
	// connection row and so no per-member cause to classify. Asserted so the
	// asymmetry is a stated property instead of a gap the loop happens not to see.
	if !declared[classEveryMemberFailed] {
		t.Fatalf("the fleet-wide class is not in the declared set, so a mixed fleet failure reaches an operator as the unclassified substitute")
	}
}

// TestTheDeclaredVocabularyIsOneTheSeamWillAccept.
//
// The declaration is validated at BOOT, and a unit whose vocabulary is refused
// aborts it — so an invalid sentence is a failure to start the installation rather
// than a bad screen. Running the same validation here means that is a red test
// instead, which is where a typo should be caught.
func TestTheDeclaredVocabularyIsOneTheSeamWillAccept(t *testing.T) {
	t.Parallel()
	if err := extension.ValidateFailureClasses(failureClasses); err != nil {
		t.Fatalf("this unit's declared vocabulary would refuse the boot: %v", err)
	}
	if declared := New().FailureClasses; len(declared) != len(failureClasses) {
		t.Fatalf("the Extension declares %d classes and the unit defines %d; a class the unit never publishes is one the job surface cannot honour",
			len(declared), len(failureClasses))
	}
}
