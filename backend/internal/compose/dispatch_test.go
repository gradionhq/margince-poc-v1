// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"testing"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The fan-out is ONE insert, not a loop of them. A partial fan-out that then
// failed the dispatcher would be re-run from the top, and the children that
// had already completed would run a SECOND time — activeSweepStates excludes
// completed, so ByArgs uniqueness does not suppress them.
func TestDispatchWithEnqueuesTheWholeFleetInOneInsert(t *testing.T) {
	fleet := []ids.UUID{ids.NewV7(), ids.NewV7(), ids.NewV7()}
	calls := 0
	var seen []ids.UUID
	insert := func(_ context.Context, params []river.InsertManyParams) error {
		calls++
		for _, p := range params {
			scoped, ok := p.Args.(jobs.WorkspaceScoped)
			if !ok {
				t.Fatalf("dispatcher built %T, which is not workspace-scoped", p.Args)
			}
			seen = append(seen, scoped.WorkspaceID())
		}
		return nil
	}

	if err := dispatchWith(context.Background(), fleet, insert, workspaceSweepOpts("", sweepWorkspaceMaxAttempts), closeDateWorkspaceArgsFor); err != nil {
		t.Fatalf("dispatching a healthy fleet: %v", err)
	}
	if calls != 1 {
		t.Fatalf("the fan-out made %d insert calls, want exactly 1 — a loop of single inserts can land partially", calls)
	}
	if len(seen) != len(fleet) {
		t.Fatalf("enqueued %d workspaces, want %d", len(seen), len(fleet))
	}
	for i, ws := range fleet {
		if seen[i] != ws {
			t.Fatalf("workspace %d enqueued as %s, want %s", i, seen[i], ws)
		}
	}
}

// A refused insert must fail the DISPATCHER. Swallowing it would leave the
// fleet un-swept while River recorded the tick as completed, which is the
// exact defect this phase removes one level down.
func TestDispatchWithFailsTheDispatcherWhenTheInsertIsRefused(t *testing.T) {
	fleet := []ids.UUID{ids.NewV7(), ids.NewV7()}
	refused := errors.New("insert refused")
	insert := func(context.Context, []river.InsertManyParams) error { return refused }

	err := dispatchWith(context.Background(), fleet, insert, workspaceSweepOpts("", sweepWorkspaceMaxAttempts), closeDateWorkspaceArgsFor)
	if err == nil {
		t.Fatal("a refused fan-out must surface, so the dispatcher row fails and the tick retries")
	}
	if !errors.Is(err, refused) {
		t.Fatalf("the dispatcher lost the cause: %v", err)
	}
}

// An installation with no live workspace has nothing to dispatch, and River
// rejects an empty InsertMany — so the fan-out must not reach it at all.
func TestDispatchWithEnqueuesNothingForAnEmptyFleet(t *testing.T) {
	called := false
	insert := func(context.Context, []river.InsertManyParams) error {
		called = true
		return nil
	}
	if err := dispatchWith(context.Background(), nil, insert, workspaceSweepOpts("", sweepWorkspaceMaxAttempts), closeDateWorkspaceArgsFor); err != nil {
		t.Fatalf("an empty fleet is not a failure: %v", err)
	}
	if called {
		t.Fatal("the fan-out called InsertMany with no params; River refuses an empty batch")
	}
}

// The ladder is capped on purpose: the dispatcher's tick owns the cadence.
func TestWorkspaceSweepOptsCapsTheLadderAndDedupesOnActiveStates(t *testing.T) {
	opts := workspaceSweepOpts("ai_capture", sweepWorkspaceMaxAttempts)
	if opts.MaxAttempts != sweepWorkspaceMaxAttempts {
		t.Fatalf("MaxAttempts = %d, want %d — unset, River's 25-rung ladder silently replaces the tick as the retry cadence",
			opts.MaxAttempts, sweepWorkspaceMaxAttempts)
	}
	if opts.Queue != "ai_capture" {
		t.Fatalf("Queue = %q, want the queue the caller named", opts.Queue)
	}
	if !opts.UniqueOpts.ByArgs {
		t.Fatal("uniqueness must be by args, or one workspace's job is indistinguishable from another's")
	}
	for _, state := range opts.UniqueOpts.ByState {
		if state == "completed" {
			t.Fatal("completed must stay out of the uniqueness window, or a finished pass blocks the next tick")
		}
	}
}

func closeDateWorkspaceArgsFor(ws ids.UUID) river.JobArgs {
	return CloseDateWorkspaceArgs{Workspace: ws}
}

// TestDispatchWithMarksEveryChildAsOneWorkspacesShareOfAFleetPass — the
// sweep gauges cannot tell a fleet pass from a hand-triggered workspace job
// by kind alone, because they are the same kind. The tag is the difference.
//
// This covers the dispatchWith choke point ONLY. The five dispatchers that
// loop single client.Insert calls (jobs_capture.go x2, telegrampoll.go,
// voicebuild.go, jobs_overlay.go) are not reachable from here — they resolve
// a River client from context — so a regression in any of them would still
// pass. Their tagging is held by markedAsFleetPass' own registry comment and
// by review, which is why a fitness test over fan-out SITES is carried as
// the highest-value follow-up in STATUS.md rather than claimed here.
func TestDispatchWithMarksEveryChildAsOneWorkspacesShareOfAFleetPass(t *testing.T) {
	var got []river.InsertManyParams
	insert := func(_ context.Context, params []river.InsertManyParams) error {
		got = params
		return nil
	}
	fleet := []ids.UUID{ids.NewV7(), ids.NewV7()}

	if err := dispatchWith(context.Background(), fleet, insert,
		workspaceSweepOpts("", sweepWorkspaceMaxAttempts), closeDateWorkspaceArgsFor); err != nil {
		t.Fatalf("dispatchWith: %v", err)
	}

	if len(got) != len(fleet) {
		t.Fatalf("inserted %d params, want %d", len(got), len(fleet))
	}
	for i, p := range got {
		if p.InsertOpts == nil {
			t.Fatalf("param %d carries no InsertOpts at all", i)
		}
		if !slices.Contains(p.InsertOpts.Tags, jobs.SweepTag) {
			t.Errorf("param %d tags = %v, want it to contain %q", i, p.InsertOpts.Tags, jobs.SweepTag)
		}
	}
}

// TestTheFanOutTagDoesNotMutateTheCallersInsertOpts — one dispatch shares
// ONE opts value across every workspace in its loop, and voiceBuildInsertOpts'
// value is shared with the user-initiated build path besides. Appending to
// it in place would accumulate one tag per workspace on a struct the caller
// still owns.
func TestTheFanOutTagDoesNotMutateTheCallersInsertOpts(t *testing.T) {
	opts := workspaceSweepOpts("default", sweepWorkspaceMaxAttempts)
	// Spare CAPACITY is the case a length check cannot see: append would
	// write into the caller's own backing array and leave len unchanged, so
	// the aliasing this test exists to catch would go unnoticed.
	opts.Tags = append(make([]string, 0, 4), "caller-owned")
	backing := opts.Tags[:cap(opts.Tags)]
	before := len(opts.Tags)
	insert := func(context.Context, []river.InsertManyParams) error { return nil }

	for range 3 {
		if err := dispatchWith(context.Background(), []ids.UUID{ids.NewV7()}, insert, opts,
			closeDateWorkspaceArgsFor); err != nil {
			t.Fatalf("dispatchWith: %v", err)
		}
	}
	if len(opts.Tags) != before {
		t.Errorf("the caller's opts grew to %d tags over three passes; the tag must be "+
			"applied to a copy", len(opts.Tags))
	}
	if opts.Tags[0] != "caller-owned" {
		t.Errorf("the caller's own tag was overwritten: %v", opts.Tags)
	}
	for i, tag := range backing[before:] {
		if tag != "" {
			t.Errorf("the fan-out wrote %q into the caller's spare capacity at index %d; "+
				"the copy must not alias the caller's backing array", tag, before+i)
		}
	}
}

// TestTheFanOutTagCarriesTheCallersEnqueuePolicyThrough — the tag is
// additive. A copy that dropped the queue, the attempt cap or the
// uniqueness window would change how the fleet is enqueued in order to
// describe it, which is the one thing an observability change may not do.
func TestTheFanOutTagCarriesTheCallersEnqueuePolicyThrough(t *testing.T) {
	opts := workspaceSweepOpts("ai_capture", sweepWorkspaceMaxAttempts)
	marked := markedAsFleetPass(opts)

	if marked.Queue != opts.Queue {
		t.Errorf("Queue = %q, want %q", marked.Queue, opts.Queue)
	}
	if marked.MaxAttempts != opts.MaxAttempts {
		t.Errorf("MaxAttempts = %d, want %d", marked.MaxAttempts, opts.MaxAttempts)
	}
	if !marked.UniqueOpts.ByArgs {
		t.Error("the uniqueness window was dropped by the copy")
	}
	if !slices.Equal(marked.UniqueOpts.ByState, opts.UniqueOpts.ByState) {
		t.Errorf("ByState = %v, want %v", marked.UniqueOpts.ByState, opts.UniqueOpts.ByState)
	}
}

// TestTheFanOutTagIsStampedOnceEvenIfTheCallerAlreadySetIt — the five
// dispatchers that loop single inserts call markedAsFleetPass directly, and
// a caller that had already tagged its opts must not end up with the tag
// twice: River validates tags but does not deduplicate them, and a
// duplicated tag is noise in a column an operator reads.
func TestTheFanOutTagIsStampedOnceEvenIfTheCallerAlreadySetIt(t *testing.T) {
	marked := markedAsFleetPass(&river.InsertOpts{Tags: []string{jobs.SweepTag}})

	var seen int
	for _, tag := range marked.Tags {
		if tag == jobs.SweepTag {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("the sweep tag appears %d times, want exactly 1: %v", seen, marked.Tags)
	}
}

// TestTheFanOutTagLeavesNilOptsUsable — the telegram dispatcher passes nil
// opts on purpose, because TelegramPollArgs declares its own InsertOpts so
// no inserter can forget the per-bot uniqueness by omission. River merges
// the two field by field, and UniqueOpts falls back to the args' own
// whenever the explicit opts leave it empty — so a tag-only opts value
// preserves that property rather than silently replacing it.
func TestTheFanOutTagLeavesNilOptsUsable(t *testing.T) {
	marked := markedAsFleetPass(nil)

	if marked == nil {
		t.Fatal("markedAsFleetPass(nil) returned nil; a dispatcher would then insert untagged")
	}
	if !slices.Contains(marked.Tags, jobs.SweepTag) {
		t.Errorf("tags = %v, want the sweep tag", marked.Tags)
	}
	// River's own isEmpty is unexported, so the fields it reads are checked
	// here directly: any one of them set makes River stop consulting the
	// args' own InsertOpts for uniqueness.
	u := marked.UniqueOpts
	if u.ByArgs || u.ByQueue || u.ExcludeKind || u.ByPeriod != 0 || len(u.ByState) != 0 {
		t.Errorf("a tag-only opts value declared a uniqueness window of its own (%+v); River "+
			"would then stop falling back to the one the args declare", u)
	}
}

// TestTheFanOutTagIsAcceptedByRiversOwnTagValidation — the tag reaches a
// column River validates on insert. A value River refuses would fail every
// fan-out in the fleet at once, and no test below the insert would notice.
func TestTheFanOutTagIsAcceptedByRiversOwnTagValidation(t *testing.T) {
	if len(jobs.SweepTag) > 255 {
		t.Fatalf("the sweep tag is %d characters; River refuses a tag over 255", len(jobs.SweepTag))
	}
	if !regexp.MustCompile(`\A[\w][\w\-]+[\w]\z`).MatchString(jobs.SweepTag) {
		t.Errorf("the sweep tag %q does not match the format River validates tags against",
			jobs.SweepTag)
	}
}
