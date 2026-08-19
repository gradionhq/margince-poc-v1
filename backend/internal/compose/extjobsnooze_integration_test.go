// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// A tick that postpones itself, over a real migrated Postgres and a real River
// runner.
//
// EVERY CLAIM HERE IS ABOUT RIVER'S BEHAVIOUR, which is exactly why it is proved
// against River instead of reasoned about. The unit-level suites can show that
// the seam returns a *river.JobSnoozeError; only a real run can show that River
// then does what the design assumed — and the design rests on two assumptions a
// reader has no way to check from our source:
//
//   - a postponement is honoured on the LAST attempt, not discarded as exhausted.
//     If it were discarded, the change would work for a blip and fail for exactly
//     the long outage it was written for, which is the failure nobody would find
//     until an outage lasted longer than a poll's attempt cap.
//   - a postponed row occupies one of the states the fan-out's uniqueness window
//     covers, so the dispatcher's next insert for that workspace collapses into
//     it. If it did not, every postponement would add a row and an outage would
//     trade dead work for an unbounded queue.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// unreachableProvider is the declared class the tick below postpones under. It
// is registered through the real boot path, because an unregistered class is not
// honoured and a suite that bypassed registration would prove nothing about the
// installation.
var unreachableProvider = extension.FailureClass{
	Class:    "provider_unavailable",
	Sentence: "the provider could not be reached from this installation",
	Remedy:   "Nothing to do: the poll catches up by itself and no message is lost.",
}

// postponedTickDelay is long enough that River schedules the row for the future
// rather than making it immediately available — its executor collapses a snooze
// shorter than the scheduler's own interval into `available`, and this suite is
// about the scheduled case, which is what a connector's cadence produces.
const postponedTickDelay = 10 * time.Minute

// TestAPostponedTickIsRescheduledOnItsLastAttempt.
//
// MaxAttempts is ONE here, deliberately: the tick's only attempt is also its
// last, so a postponement that River treated as an ordinary failure would have
// nowhere to go but `discarded`. That is the whole assumption the change rests
// on — during a real outage every attempt is the last one — and it is the one an
// implementation could get wrong while every unit test stayed green.
func TestAPostponedTickIsRescheduledOnItsLastAttempt(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	seedWorkspaces(t, e, 0)

	decl := testJobDecl()
	decl.MaxAttempts = 1
	composeJob(t, decl, func(context.Context, extension.Runtime) error {
		return extension.Reschedule(unreachableProvider, postponedTickDelay,
			errNoSuchHost)
	}, unreachableProvider)
	startRunner(t, e.Pool)

	waitUntil(t, func() bool {
		state, _, _, _ := postponedRow(t, e.Pool, decl.ChildKind())
		return state == "scheduled"
	}, "the postponed tick to be rescheduled rather than discarded")

	state, attempt, errCount, scheduledAhead := postponedRow(t, e.Pool, decl.ChildKind())
	if state != "scheduled" {
		t.Fatalf("the row is %q, want scheduled — a postponement on the last attempt must not be discarded", state)
	}
	// Attempt back to zero: River decrements on a postponement so that snoozing
	// never spends the retry budget. Asserted because it is what makes an outage
	// of unbounded length survivable, and it is not obvious from our own source.
	if attempt != 0 {
		t.Fatalf("the row is on attempt %d, want 0 — a postponement must not spend an attempt", attempt)
	}
	// NO attempt error. River records none for a postponement, which is why the
	// connection row and the WARN log line are the whole trail an outage leaves,
	// and why this change could not have been done by writing a nicer sentence.
	if errCount != 0 {
		t.Fatalf("the row records %d attempt error(s), want none — a postponement is not a failure", errCount)
	}
	if !scheduledAhead {
		t.Fatal("the row is scheduled for now or the past, so the postponement bought no gap at all")
	}
}

// TestAPostponedTickSuppressesTheNextDispatch.
//
// The other half, and the one that decides whether this change trades dead work
// for an unbounded queue. The dispatcher inserts one child per workspace per
// cadence under a uniqueness window over the active states; a postponed row must
// be inside that window, or an hour's outage leaves thirty live rows for one
// workspace instead of one.
func TestAPostponedTickSuppressesTheNextDispatch(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	seedWorkspaces(t, e, 0)

	decl := testJobDecl()
	composeJob(t, decl, func(context.Context, extension.Runtime) error {
		return extension.Reschedule(unreachableProvider, postponedTickDelay, errNoSuchHost)
	}, unreachableProvider)
	runner, _ := startRunner(t, e.Pool)

	waitUntil(t, func() bool {
		state, _, _, _ := postponedRow(t, e.Pool, decl.ChildKind())
		return state == "scheduled"
	}, "the first tick to postpone itself")

	// The dispatcher's own insert, with the dispatcher's own opts — not a
	// hand-built one. What is under test is the policy the fan-out actually
	// enqueues under, and a test that supplied its own opts would pass whatever
	// the dispatcher does.
	child := extJobWorkspaceArgs{JobKind: decl.ChildKind(), Workspace: e.WS, Principal: ids.NewV7()}
	if err := runner.Enqueue(context.Background(), child, workspaceSweepOpts(decl.ChildKind())); err != nil {
		t.Fatalf("re-enqueueing the workspace's child while the first is postponed: %v", err)
	}
	if got := countJobRows(t, e.Pool, decl.ChildKind()); got != 1 {
		t.Fatalf("child rows for one workspace while a tick is postponed: got %d, want 1 — a postponement must hold the workspace's slot", got)
	}
}

// errNoSuchHost stands in for the transport failure a real outage produces. Its
// text is deliberately the shape of one that names a host, so the assertions
// about what does NOT reach the row are about a cause that had something to leak.
var errNoSuchHost = &transportFailure{}

type transportFailure struct{}

func (*transportFailure) Error() string { return "dial tcp: lookup openapi.example: no such host" }

// postponedRow reads the one child row's disposition: its state, its attempt
// count, how many attempt errors it carries, and whether it is scheduled ahead
// of now.
//
// It answers all four from ONE read rather than four, because they describe a
// single moment and a row that moved between two of them would produce a set of
// facts that never held together.
func postponedRow(t *testing.T, pool *pgxpool.Pool, kind string) (state string, attempt, errCount int, scheduledAhead bool) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`SELECT state::text, attempt, coalesce(cardinality(errors), 0), scheduled_at > now()
		   FROM river_job WHERE kind = $1 LIMIT 1`, kind).
		Scan(&state, &attempt, &errCount, &scheduledAhead)
	if err != nil {
		return "", 0, 0, false
	}
	return state, attempt, errCount, scheduledAhead
}
