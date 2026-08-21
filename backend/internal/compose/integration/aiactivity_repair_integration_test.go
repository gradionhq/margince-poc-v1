// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The three ways a repair or a retry could quietly leave the display wrong.
//
// Each of these passed against the projection as first written, which is why
// they are here: none of them is an exotic interleaving, and all three end in a
// rail that says something false about a reading that is fine.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ageTheClaim pushes this reading's claim past its lease, which no writer can
// do: started_at is stamped by the database at claim time and nothing takes it
// as a parameter. It is the only way to reach the reclaim arm without waiting
// out a real five-minute lease.
func (f *readingFixture) ageTheClaim(t *testing.T, by time.Duration) {
	t.Helper()
	tag, err := f.env.Pool.Exec(context.Background(),
		`UPDATE attachment_extraction SET started_at = started_at - $2::interval WHERE id = $1`,
		f.readID, by.String())
	if err != nil {
		t.Fatalf("ageing the claim: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("aged %d row(s), want 1", tag.RowsAffected())
	}
}

// ageTheOccurrence pushes the whole reading back in time — its creation and its
// current attempt alike — which no writer can do: both are stamped by the
// database. It is the only way to reach a reading that has been alive for
// longer than a lease without waiting one out.
func (f *readingFixture) ageTheOccurrence(t *testing.T, by time.Duration) {
	t.Helper()
	tag, err := f.env.Pool.Exec(context.Background(), `
		UPDATE attachment_extraction
		   SET created_at = created_at - $2::interval,
		       attempt_at = attempt_at - $2::interval,
		       started_at = started_at - $2::interval
		 WHERE id = $1`, f.readID, by.String())
	if err != nil {
		t.Fatalf("ageing the occurrence: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("aged %d row(s), want 1", tag.RowsAffected())
	}
}

// A retry that takes a reading away from a DEAD holder is a new attempt, and
// the projection has to see it as one.
//
// It is the mirror of the release case, and the one a guard ordering on
// (attempt, state) gets wrong by default: the reclaim writes a fresh started_at
// under the SAME attempt, so the event is tuple-equal to what the projection
// already holds and is refused as a redelivery. The row then keeps the dead
// worker's start and its expired lease, and the rail renders an actively
// running retry as stalled for the whole of its run.
func TestALeaseExpiryReclaimIsANewAttemptTheProjectionCanSee(t *testing.T) {
	f := newReadingFixture(t)
	if _, err := f.store.BeginExtractionRead(f.ctx, f.readID, activities.ExtractionReadLease); err != nil {
		t.Fatalf("BeginExtractionRead: %v", err)
	}
	f.drain(t)
	first := f.projection(t)
	if first.State != "running" || first.Attempt != 1 {
		t.Fatalf("after the first claim, state/attempt = %s/%d, want running/1", first.State, first.Attempt)
	}

	f.ageTheClaim(t, 2*activities.ExtractionReadLease)
	if _, err := f.store.BeginExtractionRead(f.ctx, f.readID, activities.ExtractionReadLease); err != nil {
		t.Fatalf("reclaiming the dead attempt: %v", err)
	}
	f.drain(t)

	got := f.projection(t)
	if got.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2 — a reclaim at the same attempt is tuple-equal to what the projection holds and is refused forever", got.Attempt)
	}
	if got.StartedAt == nil || !got.StartedAt.After(*first.StartedAt) {
		t.Fatalf("started_at = %v, want later than the dead claim's %v", got.StartedAt, first.StartedAt)
	}
	if got.StaleAfter == nil || !got.StaleAfter.After(f.dbNow(t)) {
		t.Fatalf("stale_after = %v, want a future instant — a live retry must not render as stalled", got.StaleAfter)
	}
}

// A re-queued attempt gets a lease that has not already run out.
//
// The re-arm only fires on a reading ALREADY past its lease, so an occurrence
// dated by the row's created_at is stale the instant it is re-queued: the rail
// says stalled from the button press until a worker happens to claim it.
func TestAReQueuedAttemptGetsALeaseThatHasNotAlreadyExpired(t *testing.T) {
	f := newReadingFixture(t)
	// The reading has to be OLDER than a lease for this to be a test at all: a
	// re-queue dated by created_at is only expired once created_at is far
	// enough back, which on a fresh fixture it never is. This is the ordinary
	// case in production — a reading is released after it has been worked on.
	f.ageTheOccurrence(t, 3*activities.ExtractionReadLease)
	claim, err := f.store.BeginExtractionRead(f.ctx, f.readID, activities.ExtractionReadLease)
	if err != nil {
		t.Fatalf("BeginExtractionRead: %v", err)
	}
	if err := f.store.ReleaseExtractionRead(f.ctx, f.readID, *claim.StartedAt); err != nil {
		t.Fatalf("ReleaseExtractionRead: %v", err)
	}
	f.drain(t)

	got := f.projection(t)
	if got.State != "queued" || got.Attempt != 2 {
		t.Fatalf("state/attempt = %s/%d, want queued/2", got.State, got.Attempt)
	}
	if got.StaleAfter == nil || !got.StaleAfter.After(f.dbNow(t)) {
		t.Fatalf("stale_after = %v, want a future instant — a freshly re-queued reading has not gone stale", got.StaleAfter)
	}
}

// The repair keeps the reading with the person whose reading it is.
//
// This runs the pass under the SYSTEM principal production gives it, not the
// human context the rest of this suite carries — which is the whole point. The
// write shape stamps the envelope actor from the context, and the projection
// derives ownership from that actor, so a pass announcing under its bare system
// principal refiles every reading it repairs as workspace work. The person
// loses it from their display permanently, at exactly the moment the repair
// fires, and the source is settled so no later event ever puts it back.
func TestTheRepairKeepsTheReadingWithThePersonWhoAskedForIt(t *testing.T) {
	f := newReadingFixture(t)
	f.drain(t)
	before := f.projection(t)
	if before.ActorScope != "personal" || before.ActorUserID == nil || *before.ActorUserID != f.env.AdminUser {
		t.Fatalf("actor = %s/%v, want personal/%s before any repair", before.ActorScope, before.ActorUserID, f.env.AdminUser)
	}
	f.deleteProjection(t)

	if _, err := f.store.ReconcileExtractionActivity(f.reconcileCtx(), 100, f.dbNow(t)); err != nil {
		t.Fatalf("ReconcileExtractionActivity: %v", err)
	}
	f.drain(t)

	got := f.projection(t)
	if got.ActorScope != "personal" || got.ActorUserID == nil || *got.ActorUserID != f.env.AdminUser {
		t.Fatalf("after the repair, actor = %s/%v, want personal/%s — the repair filed one person's work as a system sweep",
			got.ActorScope, got.ActorUserID, f.env.AdminUser)
	}
}

// reconcileCtx is the context the RIVER JOB builds: the pass as the actor, the
// installation's workspace, and no human. Spelled here rather than reusing the
// suite's admin context because the defect above is invisible under a human
// principal — the test would pass while production filed the row wrong.
func (f *readingFixture) reconcileCtx() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), f.env.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:ai_activity_reconcile",
	})
}

// A bounded pass ROTATES. Ordered by anything an announce does not change, the
// same batch is selected every tick forever: a reading past the bound is never
// reconciled, and a permanently-live one writes a ledger row every tick for an
// announcement the guard then refuses.
func TestABoundedReconcilePassRotatesRatherThanReselectingTheSameReading(t *testing.T) {
	first := newReadingFixture(t)
	second := first.secondReading(t)

	seen := map[string]bool{}
	for pass := range 2 {
		announced, err := first.store.ReconcileExtractionActivity(first.reconcileCtx(), 1, first.dbNow(t))
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if announced != 1 {
			t.Fatalf("pass %d announced %d reading(s), want exactly 1 at limit 1", pass, announced)
		}
		seen[first.lastAnnounced(t)] = true
	}
	if len(seen) != 2 {
		t.Fatalf("two passes at limit 1 announced %d distinct reading(s) across %v — the pass reselects its own head and the second reading is never repaired", len(seen), []ids.UUID{first.readID, second})
	}
}

// secondReading starts another live reading on the same attachment's deal, so
// the reconcile pass has two rows to choose between.
func (f *readingFixture) secondReading(t *testing.T) ids.UUID {
	t.Helper()
	// The in-flight index admits one live reading per ATTACHMENT, so the second
	// one hangs off a second document.
	att := uploadDealAttachment(f.ctx, t, f.handlers, f.deal, "second.pdf", []byte("second bytes"))
	read, _, err := f.store.StartExtractionReadQueued(f.ctx, ids.UUID(att.Id), "human:"+f.env.AdminUser.String(), nil)
	if err != nil {
		t.Fatalf("starting the second reading: %v", err)
	}
	return read.ID
}

// lastAnnounced names the reading the most recent pass rotated to.
func (f *readingFixture) lastAnnounced(t *testing.T) string {
	t.Helper()
	var id ids.UUID
	if err := f.env.Pool.QueryRow(context.Background(),
		`SELECT id FROM attachment_extraction
		  WHERE activity_announced_at IS NOT NULL
		  ORDER BY activity_announced_at DESC, id DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("reading the rotation marker: %v", err)
	}
	return id.String()
}

// The reconcile pass's two reads are INDEXABLE, which is the whole reason the
// predicate is a union of two arms rather than one OR.
//
// Asserted with seq scans disabled, which tests the right thing: on a table
// this small the planner would pick a seq scan whichever way the query is
// written, so "did it choose the index" would pass over an unusable predicate.
// What must hold is that an index CAN answer each arm — an
// `status IN (...) OR finished_at > $1` cannot use either partial index, and on
// a real reading history every fifteen-minute pass would scan and sort the lot
// until it exceeded the job's own timeout and rolled back every repair it had
// staged.
func TestTheReconcilePassesArmsCanEachBeAnsweredByAnIndex(t *testing.T) {
	f := newReadingFixture(t)
	ctx := context.Background()
	for _, arm := range []struct {
		name, query, wantIndex string
	}{{
		name:      "live",
		query:     `SELECT id FROM attachment_extraction WHERE status IN ('queued','running') ORDER BY activity_announced_at ASC NULLS FIRST LIMIT 1`,
		wantIndex: "idx_attachment_extraction_activity_live",
	}, {
		name:      "settled inside the window",
		query:     `SELECT id FROM attachment_extraction WHERE status IN ('done','failed') AND finished_at > now() - interval '24 hours' LIMIT 1`,
		wantIndex: "idx_attachment_extraction_activity_settled",
	}} {
		t.Run(arm.name, func(t *testing.T) {
			plan := explain(ctx, t, f, arm.query)
			if !strings.Contains(plan, arm.wantIndex) {
				t.Fatalf("the %s arm plans as:\n%s\nwant it to reach %s", arm.name, plan, arm.wantIndex)
			}
		})
	}
}

// explain returns the plan for one statement with sequential scans disabled,
// so the plan says what an index COULD answer rather than what the planner
// prefers over a nearly empty table.
func explain(ctx context.Context, t *testing.T, f *readingFixture, query string) string {
	t.Helper()
	conn, err := f.env.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquiring a connection to plan on: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatalf("disabling seq scans: %v", err)
	}
	rows, err := conn.Query(ctx, "EXPLAIN "+query)
	if err != nil {
		t.Fatalf("planning %q: %v", query, err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("reading the plan: %v", err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the plan: %v", err)
	}
	return plan.String()
}

// A backlog of LIVE readings must not starve the settled arm.
//
// Each arm of the reconcile union carries its own budget for exactly this: with
// one shared budget, Postgres fills it from the live arm first — UNION ALL is
// evaluated in order — so an installation holding a full batch of live readings
// would never re-announce a settled one. That is the arm repairing the worst
// display there is: a reading whose closing event was lost renders as running
// forever, and nothing else will ever correct it.
func TestALiveBacklogDoesNotStarveTheSettledArmOfTheReconcilePass(t *testing.T) {
	f := newReadingFixture(t)

	// The settled reading whose closing event was lost.
	claim, err := f.store.BeginExtractionRead(f.ctx, f.readID, activities.ExtractionReadLease)
	if err != nil {
		t.Fatalf("BeginExtractionRead: %v", err)
	}
	if err := f.store.FinishExtractionRead(f.ctx, f.readID, activities.ExtractionReadOutcome{
		Status: activities.ExtractionReadDone, ClaimedAt: *claim.StartedAt,
		Detail: "the document states none of the four fields",
	}); err != nil {
		t.Fatalf("FinishExtractionRead: %v", err)
	}
	f.drain(t)
	f.deleteProjection(t)

	// Enough live readings to fill a whole pass on their own.
	const budget = 4
	for range budget {
		f.secondReading(t)
	}

	if _, err := f.store.ReconcileExtractionActivity(f.reconcileCtx(), budget, f.dbNow(t)); err != nil {
		t.Fatalf("ReconcileExtractionActivity: %v", err)
	}
	f.drain(t)

	if got := f.projection(t); got.State != "done" {
		t.Fatalf("the settled reading reconciled as %q; a live backlog starved the arm that repairs it", got.State)
	}
}
