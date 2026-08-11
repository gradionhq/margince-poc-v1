// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The ONE fleet enumeration. Before this file every sweep carried its own
// copy of the workspace scan and its own inline per-workspace loop, which
// meant a failed tenant pass became a log line inside a job row River then
// recorded as completed — success on the outside, silent failure within.
// Dispatching instead gives each workspace its own row to succeed or fail
// as, and leaves exactly one place where the fleet is enumerated at all.

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// enumerateWorkspaces reads every live workspace. This is the sanctioned
// fleet scan for a pass that works on behalf of an active tenant.
func enumerateWorkspaces(ctx context.Context, pool *pgxpool.Pool) ([]ids.UUID, error) {
	// rls-exempt: fleet enumeration — the workspace table is not workspace-scoped; this reads every tenant before any per-workspace tx exists.
	rows, err := pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("compose: enumerating workspaces: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
}

// enumerateEveryWorkspace reads EVERY workspace, archived ones included —
// unlike the passes that work on behalf of a live tenant. Archiving a workspace
// does not un-store the data inside it, and both retention passes fan out over
// this enumeration for that reason.
//
// GDPR storage limitation does not pause because a tenant stopped logging in:
// skipping archived rows would hold personal data past its retention floor in
// exactly the workspaces nobody looks at any more. Idempotency claim retention
// needs them for a second reason — idempotency_key.workspace_id is ON DELETE
// RESTRICT, so leftover claims also refuse the eventual hard delete.
func enumerateEveryWorkspace(ctx context.Context, pool *pgxpool.Pool) ([]ids.UUID, error) {
	// rls-exempt: fleet enumeration — the workspace table is not workspace-scoped; retention reads every tenant, archived included, before any per-workspace tx exists.
	rows, err := pool.Query(ctx, `SELECT id FROM workspace ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("compose: enumerating every workspace: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
}

// The bounded queues a fanned-out pass lands on. MaxWorkers: 5 bounds only
// the default queue, and deep_read and rate_refresh already run alongside it
// on the same process against one pgx pool — so converted sweeps must not
// simply pile onto default.
const (
	// aiCaptureQueue carries the passes that make serial model calls. Two
	// workers, matching deep_read: the same species of work — long,
	// model-bound, and fine to run behind the short maintenance jobs. WHICH
	// kinds land here is api/jobs.yaml's to say, not this comment's.
	aiCaptureQueue      = "ai_capture"
	aiCaptureMaxWorkers = 2

	// overlayReconcileQueue is serial. See the queue table in jobQueues
	// for why per-workspace parallelism is not what this phase is after.
	overlayReconcileQueue = "overlay_reconcile"
)

// workspaceSweepOpts is the enqueue policy for one fanned-out child, read
// from the child kind's own declaration. Taking the KIND rather than the
// numbers is what makes the sweep tag unforgettable: a FLEET PASS's child opts
// are built here or in fanOutChildOpts and nowhere else, and both stamp it.
//
// oneOffChildOpts below builds the same kinds' opts WITHOUT the tag, and is
// the only thing that does. That is not a hole in the argument, it is the
// distinction the tag exists to draw: a job enqueued for one already-known
// workspace by an event is not one workspace's share of a fleet pass, and the
// helper takes no fleet as an argument, so it cannot be reached from a
// dispatcher's loop by accident.
//
// The attempt cap is small on every fan-out kind because a fanned-out pass's
// real retry cadence is the dispatcher's tick — a workspace that fails is
// re-enqueued on the next pass — and River's ladder is there only to ride out
// a transient blip inside one tick window. Left unset it would silently
// become River's default ladder of 25 attempts on attempt⁴ backoff, which
// retries far more aggressively than the tick early on and far less often
// later; and because retryable is one of activeSweepStates, a backing-off job
// suppresses the tick's own re-enqueue of that workspace until it discards.
// api/jobs.yaml carries each kind's number and the reason for it.
func workspaceSweepOpts(childKind string) *river.InsertOpts {
	spec := fanOutChildSpec(childKind)
	if spec.OptsOwner != jobs.OptsFanOut {
		panic("compose: " + childKind + " declares an opts_owner other than fan_out, so its queue and attempt cap are not this helper's to set")
	}
	return markedAsFleetPass(&river.InsertOpts{
		Queue:       spec.Queue,
		MaxAttempts: spec.MaxAttempts,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByState: activeSweepStates},
	})
}

// oneOffChildOpts is workspaceSweepOpts' counterpart for the same child kind
// enqueued for ONE already-known workspace by an event — a tenant's backfill
// finishing, not a clock reaching a fleet. It reads the queue and the attempt
// cap from the same declaration, so the two doors cannot route one workspace's
// pass onto a queue the fleet's share of it never lands on.
//
// It deliberately omits BOTH of the things that make a fleet child a fleet
// child, and each omission is the point rather than an oversight:
//
//   - No sweep tag. The tag is what lets a reader tell one workspace's share
//     of a fleet pass from the same kind enqueued for its own reason, and the
//     sweep gauges count tagged rows. A one-off tagged as a fleet pass would
//     inflate the coverage reading of a pass that never ran.
//   - No active-state uniqueness. The event's whole claim is that new rows
//     landed, and a pass already RUNNING may have read the workspace before
//     they did. Deduplicating against it would drop exactly the data the
//     event fired about, and the caller would have no way to tell.
//
// Both are safe to omit only because a one-off enqueue is a single insert for
// a single known workspace: it fires once per event, so nothing here bounds a
// fan-out that could otherwise repeat per tick.
func oneOffChildOpts(childKind string) *river.InsertOpts {
	spec := fanOutChildSpec(childKind)
	if spec.OptsOwner != jobs.OptsFanOut {
		panic("compose: " + childKind + " declares an opts_owner other than fan_out, so its queue and attempt cap are not this helper's to set")
	}
	return &river.InsertOpts{Queue: spec.Queue, MaxAttempts: spec.MaxAttempts}
}

// fanOutChildren is every kind some dispatcher DECLARES it fans out to —
// fans_out_to, read as the registry it is, over BOTH halves of the declaration
// table (jobs.Declared covers the compiled core kinds and this installation's
// composed extension kinds).
//
// Recomputed per call rather than memoised. It used to be a sync.OnceValue,
// which was correct while the table was compiled and therefore fixed at link
// time; it is not correct now that an installation's composed kinds are
// declared at BOOT. A memoised answer is whatever the table held at the first
// insert of the process, so a fan-out that happened to run before
// jobs.RegisterComposed would cache the core set and then PANIC on every
// extension child forever after — a boot-ordering bug that would look like a
// bad kind. The cost it buys back is one map build over a few dozen specs per
// dispatched insert, which is beside a database round trip.
func fanOutChildren() map[string]struct{} {
	children := map[string]struct{}{}
	for _, spec := range jobs.Declared() {
		if spec.FanOutTo != "" {
			children[spec.FanOutTo] = struct{}{}
		}
	}
	return children
}

// fanOutChildSpec answers the declaration for a kind a dispatcher may fan out
// to, and refuses one no dispatcher declares. Both refusals are programming
// errors a boot-time tick surfaces immediately, not conditions a running
// deployment can reach: what a kind is, and who fans out to it, are fixed at
// compile time by the contract the generated table is built from.
func fanOutChildSpec(childKind string) jobs.Spec {
	spec, ok := jobs.SpecFor(childKind)
	if !ok {
		panic("compose: fanning out to " + childKind + ", which api/jobs.yaml does not declare")
	}
	if _, ok := fanOutChildren()[childKind]; !ok {
		panic("compose: fanning out to " + childKind + ", which no declared kind names in fans_out_to — declare the dispatcher's fan-out in api/jobs.yaml")
	}
	return spec
}

// insertManyFunc is the slice of River's insert surface a dispatch needs.
// Narrowed to the one method so the fan-out's failure posture can be proven
// without a database or a live client.
type insertManyFunc func(ctx context.Context, params []river.InsertManyParams) error

// dispatchPerWorkspace enqueues one job per live workspace, built by argsFor,
// as ONE atomic insert.
func dispatchPerWorkspace(ctx context.Context, pool *pgxpool.Pool, opts *river.InsertOpts, argsFor func(ids.UUID) river.JobArgs) error {
	workspaces, err := enumerateWorkspaces(ctx, pool)
	if err != nil {
		return err
	}
	return dispatchWith(ctx, workspaces, clientInsertMany(ctx), opts, argsFor)
}

// clientInsertMany binds the fan-out to the River client already in context —
// the shape gmailSyncWorker's dispatcher uses.
//
// The client is resolved INSIDE the closure, not when the closure is built:
// river.ClientFromContext panics when there is none, and a dispatch over an
// empty fleet never inserts, so resolving eagerly would turn "no workspaces
// yet" into a panic on a path that has nothing to do.
func clientInsertMany(ctx context.Context) insertManyFunc {
	return func(ctx context.Context, params []river.InsertManyParams) error {
		_, err := river.ClientFromContext[pgx.Tx](ctx).InsertMany(ctx, params)
		return err
	}
}

// dispatchWith is dispatchPerWorkspace over an already-read fleet and an
// injected inserter.
//
// Atomicity is not a nicety here, it is the correctness argument. A
// per-workspace loop of single inserts that fails partway leaves some
// children queued, and the dispatcher then fails and is retried. By the time
// it retries, the children that were enqueued may already have COMPLETED —
// and activeSweepStates deliberately excludes completed (so a finished sweep
// never blocks the next scheduled run), so ByArgs uniqueness does NOT
// suppress them. The retry silently runs those workspaces a second time: a
// second overlay reconcile spending incumbent API quota, a second AI-backed
// capture pass spending model budget.
//
// InsertMany is all-or-nothing, so a dispatch whose INSERT failed enqueued
// nothing and its retry starts from a clean slate. Logging the failure and
// carrying on is the swallowed-error shape this whole phase removes, one level
// up: either the fan-out lands or the dispatcher fails and says so.
//
// What this does NOT buy is exactly-once. River is at-least-once: the insert
// commits in its own transaction, and the dispatcher is marked completed
// afterwards, so a process that dies between the two is rescued and re-runs the
// fan-out over children that may already have completed — which ByArgs
// uniqueness does not suppress, because completed is outside activeSweepStates.
// The bound on that is the workspace passes themselves: each re-reads its own
// backlog and a caught-up one costs a probe. A dispatcher whose children are
// expensive enough to care (overlay, the AI-backed captures) carries its own
// pacing in the row it works from, not in this helper.
func dispatchWith(ctx context.Context, workspaces []ids.UUID, insert insertManyFunc, opts *river.InsertOpts, argsFor func(ids.UUID) river.JobArgs) error {
	if len(workspaces) == 0 {
		return nil
	}
	params := make([]river.InsertManyParams, 0, len(workspaces))
	for _, ws := range workspaces {
		params = append(params, river.InsertManyParams{Args: argsFor(ws), InsertOpts: markedAsFleetPass(opts)})
	}
	if err := insert(ctx, params); err != nil {
		return fmt.Errorf("compose: dispatching %d workspace jobs: %w", len(params), err)
	}
	return nil
}

// dispatchOne enqueues ONE fan-out child through the River client working the
// current job. It serves the dispatchers that cannot use dispatchWith because
// they fan out per CONNECTION or per BUILD rather than per workspace: their
// unit is not the fleet, so there is no one enumeration to insert atomically
// and they loop single inserts instead.
//
// The client is resolved per insert, and only inside the loop, for the reason
// clientInsertMany states: river.ClientFromContext panics when there is none,
// and a tick with nothing due must not turn "nothing to do" into a panic.
func dispatchOne(ctx context.Context, args river.JobArgs, callerOpts *river.InsertOpts) error {
	_, err := river.ClientFromContext[pgx.Tx](ctx).Insert(ctx, args, fanOutChildOpts(args.Kind(), callerOpts))
	return err
}

// fanOutChildOpts is what dispatchOne hands River, decided by the child's
// declared opts_owner. The three owners genuinely differ, and routing the
// choice through the declaration is what keeps a dispatcher from flattening
// them into one:
//
//   - fan_out — the queue and attempt cap come from the declaration, exactly
//     as dispatchWith's children get them.
//   - args — the child's own InsertOpts() owns them, so it gets a TAG-ONLY
//     value and River's field-by-field merge leaves the args' rule standing.
//     Handing telegram_poll a populated opts would drop its per-bot
//     uniqueness, and two in-flight polls for one bot steal each other's
//     batches.
//   - caller — callerOpts, which is the caller's to build precisely because
//     the same value serves a path that is NOT a fan-out: voiceBuildInsertOpts
//     is shared with the user-initiated build. The tag goes on this call and
//     never into that shared helper, because a build someone asked for is not
//     one workspace's share of a fleet pass.
//
// callerOpts is read only for opts_owner: caller, and the guard runs in BOTH
// directions — supplying one for another owner is refused, and so is omitting
// one for caller. The two are the same defect: the reason a caller-owned kind
// carries that owner is that its options exist and are nobody else's to
// reconstruct, so a nil here yields the tag-only value and drops them, and a
// uniqueness window that was silently dropped reads, in the source, exactly
// like one that was applied.
//
// It is separate from dispatchOne so a unit test can assert all three
// postures directly, without a River client or a database: the postures are
// the whole content of the decision, and the insert below them is one line.
func fanOutChildOpts(childKind string, callerOpts *river.InsertOpts) *river.InsertOpts {
	spec := fanOutChildSpec(childKind)
	if spec.OptsOwner != jobs.OptsCaller && callerOpts != nil {
		panic("compose: insert options passed for " + childKind + ", whose opts_owner is not caller — they would be dropped, not merged")
	}
	if spec.OptsOwner == jobs.OptsCaller && callerOpts == nil {
		panic("compose: no insert options passed for " + childKind + ", whose opts_owner is caller — its queue and uniqueness window are the caller's to supply, and this child would be inserted with neither")
	}
	switch spec.OptsOwner {
	case jobs.OptsFanOut:
		return workspaceSweepOpts(childKind)
	case jobs.OptsArgs:
		return markedAsFleetPass(nil)
	case jobs.OptsCaller:
		return markedAsFleetPass(callerOpts)
	}
	panic("compose: " + childKind + " declares no opts_owner; every kind the contract compiles has one")
}

// markedAsFleetPass copies opts with the sweep tag added, so a reader can
// tell one workspace's share of a fleet pass from the same kind enqueued by
// hand — they are the same kind, and the tag is the only difference in the
// row. Tags are validated for format only and take no part in River's
// unique key, so this changes no scheduling behaviour.
//
// Every production caller is in this file. A FLEET PASS's child opts are built
// by workspaceSweepOpts (a fleet insert) or fanOutChildOpts (a single one) and
// nowhere else, and both stamp the tag — so a dispatcher cannot forget it,
// which is what an untagged child costs: silent absence from the sweep
// gauges, while the gauge's own HELP text blames River's retention for the
// gap. The third builder of the same kinds' opts, oneOffChildOpts, omits the
// tag deliberately and states why; it is reachable only from a site that
// already knows the one workspace it is enqueueing for. WHICH kinds may be fanned out to is the contract's to say and not this
// comment's: fans_out_to in api/jobs.yaml is the registry, and both builders
// refuse a kind no dispatcher declares there.
//
// It COPIES because a single dispatch shares ONE opts value across every
// workspace in its loop, and voiceBuildInsertOpts' value is shared with the
// user-initiated build path besides. Appending in place would grow one tag
// per workspace and hand the caller back a mutated struct.
//
// A nil opts yields a tag-ONLY value on purpose. For the fields that matter
// here — queue, max attempts, priority, tags and the uniqueness window —
// River falls back to the args' own InsertOpts whenever the explicit value
// leaves that field at its zero, so a caller that passes nil to let its
// args declare the uniqueness window (the telegram poll's per-bot rule)
// keeps that fallback intact. It is NOT a blanket rule over every field:
// metadata, for one, is defaulted rather than inherited.
func markedAsFleetPass(opts *river.InsertOpts) *river.InsertOpts {
	marked := river.InsertOpts{}
	if opts != nil {
		marked = *opts
	}
	if slices.Contains(marked.Tags, jobs.SweepTag) {
		return &marked
	}
	marked.Tags = append(slices.Clone(marked.Tags), jobs.SweepTag)
	return &marked
}
