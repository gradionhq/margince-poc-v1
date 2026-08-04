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

	if err := dispatchWith(context.Background(), fleet, insert, workspaceSweepOpts(CloseDateWorkspaceArgs{}.Kind()), closeDateWorkspaceArgsFor); err != nil {
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

	err := dispatchWith(context.Background(), fleet, insert, workspaceSweepOpts(CloseDateWorkspaceArgs{}.Kind()), closeDateWorkspaceArgsFor)
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
	if err := dispatchWith(context.Background(), nil, insert, workspaceSweepOpts(CloseDateWorkspaceArgs{}.Kind()), closeDateWorkspaceArgsFor); err != nil {
		t.Fatalf("an empty fleet is not a failure: %v", err)
	}
	if called {
		t.Fatal("the fan-out called InsertMany with no params; River refuses an empty batch")
	}
}

// The queue and the ladder are the DECLARATION's, not the call site's: a
// number typed at the call site can drift from the one api/jobs.yaml
// publishes, and nothing would notice.
func TestWorkspaceSweepOptsReadsTheDeclaredQueueAndAttempts(t *testing.T) {
	opts := workspaceSweepOpts(PrivacyRetentionWorkspaceArgs{}.Kind())

	if opts.Queue != "privacy_retention" {
		t.Errorf("Queue = %q, want the declared privacy_retention", opts.Queue)
	}
	if opts.MaxAttempts != 3 {
		t.Errorf("MaxAttempts = %d, want the declared 3 — unset, River's 25-rung ladder silently replaces the tick as the retry cadence",
			opts.MaxAttempts)
	}
}

func TestWorkspaceSweepOptsTagsEveryFanOutChild(t *testing.T) {
	opts := workspaceSweepOpts(PrivacyRetentionWorkspaceArgs{}.Kind())

	if !slices.Contains(opts.Tags, jobs.SweepTag) {
		t.Errorf("Tags = %v, want jobs.SweepTag — an untagged child is invisible to both sweep gauges", opts.Tags)
	}
}

// Uniqueness stays the helper's, not the declaration's: it is the same window
// for every fan-out child, and by ARGS because one workspace's job is
// otherwise indistinguishable from another's.
func TestWorkspaceSweepOptsDedupesByArgsOnActiveStates(t *testing.T) {
	opts := workspaceSweepOpts(CaptureClassifyWorkspaceArgs{}.Kind())

	if !opts.UniqueOpts.ByArgs {
		t.Fatal("uniqueness must be by args, or one workspace's job is indistinguishable from another's — the whole fleet would collapse to one queued child")
	}
	for _, state := range opts.UniqueOpts.ByState {
		if state == "completed" {
			t.Fatal("completed must stay out of the uniqueness window, or a finished pass blocks the next tick")
		}
	}
}

// fans_out_to is the registry, and a registry that admits anything is not
// one: a kind no dispatcher declares a fan-out to must not be dispatchable,
// or the next untagged fan-out is one unreviewed call away.
func TestWorkspaceSweepOptsRefusesAKindNoDispatcherFansOutTo(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a kind no declared dispatcher names in fans_out_to must not get fan-out opts")
		}
	}()
	workspaceSweepOpts("site_deep_read") // enqueued by a human, fanned out to by nobody
}

// The helper supplies a queue and an attempt cap. Handing them to a kind
// whose opts are owned elsewhere would publish numbers the contract does not
// govern for it — and, for telegram_poll, would replace the per-bot
// uniqueness its own args declare.
func TestWorkspaceSweepOptsRefusesAKindWhoseOptsItDoesNotOwn(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a kind whose opts_owner is not fan_out must not silently get fan-out opts")
		}
	}()
	workspaceSweepOpts(TelegramPollArgs{}.Kind()) // opts_owner: args
}

// The args-owned posture, which is the one a well-meaning conversion breaks:
// River merges an explicit InsertOpts with the args' own field by field, and
// consults the args for uniqueness only while the explicit value leaves it
// empty. A populated opts here silently drops the per-bot rule.
func TestFanOutChildOptsLeavesAnArgsOwnedKindItsOwnInsertOpts(t *testing.T) {
	opts := fanOutChildOpts(TelegramPollArgs{}.Kind(), nil)

	if !slices.Contains(opts.Tags, jobs.SweepTag) {
		t.Errorf("Tags = %v, want jobs.SweepTag", opts.Tags)
	}
	if opts.Queue != "" || opts.MaxAttempts != 0 {
		t.Errorf("queue %q / attempts %d were supplied for a kind whose args own its opts",
			opts.Queue, opts.MaxAttempts)
	}
	// River's own isEmpty is unexported, so the fields it reads are checked
	// here directly: any one of them set makes River stop consulting the
	// args' own InsertOpts for uniqueness.
	u := opts.UniqueOpts
	if u.ByArgs || u.ByQueue || u.ExcludeKind || u.ByPeriod != 0 || len(u.ByState) != 0 {
		t.Errorf("a uniqueness window of its own (%+v) was declared for a kind whose args declare "+
			"one; River would then stop falling back, and one bot's poll could suppress another's", u)
	}
}

// The caller-owned posture. voiceBuildInsertOpts is shared with the
// user-initiated build path, so the tag goes on the retry dispatcher's call
// and the shared value carries it through unchanged.
func TestFanOutChildOptsCarriesACallerOwnedKindsOptsThrough(t *testing.T) {
	callerOpts := voiceBuildInsertOpts()
	opts := fanOutChildOpts(VoiceBuildArgs{}.Kind(), callerOpts)

	if !slices.Contains(opts.Tags, jobs.SweepTag) {
		t.Errorf("Tags = %v, want jobs.SweepTag", opts.Tags)
	}
	if !opts.UniqueOpts.ByArgs {
		t.Error("the caller's uniqueness window was dropped; a deferred build could then be offered twice")
	}
	if !slices.Equal(opts.UniqueOpts.ByState, callerOpts.UniqueOpts.ByState) {
		t.Errorf("ByState = %v, want the caller's %v", opts.UniqueOpts.ByState, callerOpts.UniqueOpts.ByState)
	}
}

// The shared helper stays clean. A build a human asked for is not one
// workspace's share of a fleet pass, and counting it as one is exactly what
// the tag exists to prevent.
func TestVoiceBuildInsertOptsCarriesNoSweepTag(t *testing.T) {
	if tags := voiceBuildInsertOpts().Tags; slices.Contains(tags, jobs.SweepTag) {
		t.Errorf("the shared build opts carry %v; the tag belongs on the retry dispatcher's call alone", tags)
	}
}

// Opts for a kind that does not own them would be dropped by the switch
// below, not merged — and a dropped uniqueness window reads, in the source,
// exactly like an applied one.
func TestFanOutChildOptsRefusesOptsForAKindThatDoesNotOwnThem(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("insert options for a kind whose opts_owner is not caller must be refused, not ignored")
		}
	}()
	fanOutChildOpts(CaptureSyncArgs{}.Kind(), &river.InsertOpts{MaxAttempts: 2})
}

// The other direction, and the same defect: a caller-owned kind reaching here
// with nothing would get the tag-only value, which is precisely the shape that
// makes River fall back to the ARGS' opts — and a caller-owned kind carries
// that owner because its args declare none. The window would be gone, and the
// call site would still read as one that supplied it.
func TestFanOutChildOptsRefusesACallerOwnedKindWithNoOpts(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a caller-owned kind with no opts must be refused, not degraded to a tag-only value")
		}
	}()
	fanOutChildOpts(VoiceBuildArgs{}.Kind(), nil)
}

// A one-off child takes the same declared queue and attempt cap the fleet's
// children take — routing one workspace's pass onto a different queue is drift
// the contract exists to make impossible — and takes NEITHER of the two things
// that would make it read as a fleet pass. Both omissions are asserted, because
// a value that merely happened not to carry them is one field away from doing
// so, and neither failure is visible at the call site.
func TestOneOffChildOptsTakeTheDeclaredQueueButNotTheFleetPassMarkings(t *testing.T) {
	kind := CaptureDigestWorkspaceArgs{}.Kind()
	spec := specFor(t, kind)
	opts := oneOffChildOpts(kind)

	if opts.Queue != spec.Queue {
		t.Errorf("Queue = %q, want the declared %q", opts.Queue, spec.Queue)
	}
	if opts.MaxAttempts != spec.MaxAttempts {
		t.Errorf("MaxAttempts = %d, want the declared %d", opts.MaxAttempts, spec.MaxAttempts)
	}
	if slices.Contains(opts.Tags, jobs.SweepTag) {
		t.Errorf("Tags = %v, want no %q — a job one tenant's backfill asked for is not one "+
			"workspace's share of a fleet pass, and the sweep gauges count tagged rows",
			opts.Tags, jobs.SweepTag)
	}
	if len(opts.UniqueOpts.ByState) != 0 {
		t.Errorf("ByState = %v, want none — the event fired BECAUSE new rows landed, and an "+
			"already-running pass may have read the workspace before they did",
			opts.UniqueOpts.ByState)
	}
}

// The same guard workspaceSweepOpts carries: a kind whose queue and attempt cap
// are owned elsewhere must not have them supplied here either, or a one-off
// would publish numbers the contract does not govern for that kind.
func TestOneOffChildOptsRefusesAKindWhoseOptsItDoesNotOwn(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a kind whose opts_owner is not fan_out must not silently get fan-out opts")
		}
	}()
	oneOffChildOpts(TelegramPollArgs{}.Kind()) // opts_owner: args
}

func closeDateWorkspaceArgsFor(ws ids.UUID) river.JobArgs {
	return CloseDateWorkspaceArgs{Workspace: ws}
}

// TestDispatchWithMarksEveryChildAsOneWorkspacesShareOfAFleetPass — the
// sweep gauges cannot tell a fleet pass from a hand-triggered workspace job
// by kind alone, because they are the same kind. The tag is the difference.
//
// This covers the dispatchWith builder. The other one, fanOutChildOpts, is
// covered by the posture tests above — between them every fan-out child's
// opts are built here or there, and TestNoScheduledDispatcherIsEnqueuedByHand
// is what proves no dispatcher is placed by a third spelling.
func TestDispatchWithMarksEveryChildAsOneWorkspacesShareOfAFleetPass(t *testing.T) {
	var got []river.InsertManyParams
	insert := func(_ context.Context, params []river.InsertManyParams) error {
		got = params
		return nil
	}
	fleet := []ids.UUID{ids.NewV7(), ids.NewV7()}

	if err := dispatchWith(context.Background(), fleet, insert,
		workspaceSweepOpts(CloseDateWorkspaceArgs{}.Kind()), closeDateWorkspaceArgsFor); err != nil {
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
	opts := workspaceSweepOpts(CloseDateWorkspaceArgs{}.Kind())
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
	opts := workspaceSweepOpts(CaptureClassifyWorkspaceArgs{}.Kind())
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
