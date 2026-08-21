// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// One person's feed, read from the projection against a real database.
//
// Every row here arrives the way production writes one — a real reading moved
// by activities.Store, announced on the bus, projected by the real consumer.
// The one exception is ageing a lease, which no writer can do because
// stale_after is derived from timestamps the database stamped.

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/aiactivity"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// feed reads the caller's own view, with "today" stated rather than raced.
func (f *readingFixture) feed(t *testing.T, user ids.UUID) (live, settled []aiactivity.Item) {
	t.Helper()
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	live, settled, err := aiactivity.NewStore(f.env.DB()).Mine(context.Background(), user, midnight)
	if err != nil {
		t.Fatalf("Mine: %v", err)
	}
	return live, settled
}

// A queued reading is LIVE for the person who asked for it — queued is work in
// progress to them, not an absence.
func TestAQueuedReadingIsLiveInItsOwnPersonsFeed(t *testing.T) {
	f := newReadingFixture(t)
	f.drain(t)

	live, settled := f.feed(t, f.env.AdminUser)
	if len(live) != 1 || len(settled) != 0 {
		t.Fatalf("live/settled = %d/%d, want 1/0", len(live), len(settled))
	}
	if live[0].State != "queued" || live[0].Kind != "document_extract" {
		t.Fatalf("state/kind = %s/%s, want queued/document_extract", live[0].State, live[0].Kind)
	}
}

// The feed is PERSONAL. Another seat sees none of it, and the separation is the
// row's own actor rather than anything the caller passes.
func TestOnePersonsWorkIsNotInAnothersFeed(t *testing.T) {
	f := newReadingFixture(t)
	f.drain(t)

	if live, settled := f.feed(t, f.env.Rep2); len(live) != 0 || len(settled) != 0 {
		t.Fatalf("a different seat sees %d live and %d settled occurrences, want none", len(live), len(settled))
	}
}

// A live occurrence past the lease its own source declared reads as `stalled`,
// and it is DERIVED — nothing stores the word, so no writer can forget it.
func TestALiveOccurrencePastItsLeaseReadsAsStalled(t *testing.T) {
	f := newReadingFixture(t)
	if _, err := f.store.BeginExtractionRead(f.ctx, f.readID, activities.ExtractionReadLease); err != nil {
		t.Fatalf("BeginExtractionRead: %v", err)
	}
	f.drain(t)
	if live, _ := f.feed(t, f.env.AdminUser); live[0].State != "running" {
		t.Fatalf("state = %s, want running before the lease elapses", live[0].State)
	}

	// The projection's own column, aged: stale_after is computed from database
	// timestamps, so there is no writer that takes it as a parameter.
	if _, err := f.env.Pool.Exec(context.Background(),
		`UPDATE ai_task_run SET stale_after = now() - interval '1 minute'
		  WHERE source = $1 AND occurrence_key = $2`,
		"attachment_extraction", f.readID.String()); err != nil {
		t.Fatalf("ageing the lease: %v", err)
	}

	live, _ := f.feed(t, f.env.AdminUser)
	if len(live) != 1 || live[0].State != aiactivity.StateStalled {
		t.Fatalf("state = %v, want %q — a worker that died without saying so must not read as working",
			live, aiactivity.StateStalled)
	}
	if got := f.projection(t).State; got != "running" {
		t.Fatalf("the STORED state is %q; stalled is derived at read time and must never be written", got)
	}
}

// A settled occurrence never goes stale, whatever its lease said: it is not
// claiming to be working, so there is nothing to be past.
func TestASettledOccurrenceIsNeverStalled(t *testing.T) {
	f := newReadingFixture(t)
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

	live, settled := f.feed(t, f.env.AdminUser)
	if len(live) != 0 || len(settled) != 1 {
		t.Fatalf("live/settled = %d/%d, want 0/1", len(live), len(settled))
	}
	if settled[0].State != "done" {
		t.Fatalf("state = %s, want done", settled[0].State)
	}
	if settled[0].FinishedAt == nil {
		t.Fatal("a settled occurrence with no finish would break the feed's keyset ordering")
	}
}

// A read with no person is refused rather than answered with everybody's work.
func TestAFeedWithNoPersonIsRefused(t *testing.T) {
	f := newReadingFixture(t)
	if _, _, err := aiactivity.NewStore(f.env.DB()).
		Mine(context.Background(), ids.Nil, time.Now()); err == nil {
		t.Fatal("a personal read with no person must be refused, not answered")
	}
}
