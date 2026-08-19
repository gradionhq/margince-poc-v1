// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package briefs

// A persisted brief item is a REFERENCE to a deal, held for as long as the run
// lives — so the read that hands it back composes the deal's read predicate
// rather than trusting the ranking that queued it. A deal is workspace-shared
// identity (platform/auth tableclass.go): reassigning it to another team does
// not take it out of a team-scoped rep's brief, and the rep reads the same
// answer from the brief that `read_record` gives them on the same id.

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestBriefReadServesAReassignedDealLikeTheDealReadDoes(t *testing.T) {
	b := setupBrief(t)
	owner := integration.OwnerConn(t)
	// A team-scoped rep, not the all-scope fixture: an unbounded principal
	// renders no clause at all and would prove nothing about either read.
	rep := b.As(b.Rep1, []ids.UUID{b.Team1}, integration.RepPerms)

	run, err := b.engine.SnapshotRun(rep, briefClock)
	if err != nil {
		t.Fatal(err)
	}
	// Both must be queued BEFORE anything moves: a missing item reads back as
	// the zero value, whose nil id is refused by every path below — so the
	// assertions about the reassigned deal would pass without the read
	// predicate ever being the reason.
	queued := itemsByDeal(t, run)
	moved, movedQueued := queued[b.dealA]
	kept, keptQueued := queued[b.dealB]
	if !movedQueued || !keptQueued {
		t.Fatalf("the snapshot queued %v, want both fixture deals — nothing below tests the read predicate without them", queueItemDeals(run))
	}

	// The deal moves to the other team between the snapshot and the read.
	if _, err := owner.Exec(context.Background(),
		`UPDATE deal SET owner_id = $2 WHERE id = $1`, b.dealA, b.Rep3); err != nil {
		t.Fatal(err)
	}

	later := briefClock.Add(2 * time.Hour)
	after, err := b.engine.LatestRun(rep, later)
	if err != nil {
		t.Fatal(err)
	}
	served := itemsByDeal(t, after)
	if _, present := served[b.dealA]; !present {
		t.Errorf("the brief dropped deal %s after it moved to another team — a deal is readable by every seat, the item read narrows more than the deal read does", b.dealA)
	}
	if _, present := served[b.dealB]; !present {
		t.Errorf("the brief dropped deal %s, which the rep still owns", b.dealB)
	}

	// The single-item door answers the same way: the mark is a read-back of a
	// deal the rep can still see, so it succeeds for both items.
	if _, err := b.engine.MarkActed(rep, moved.ID, later); err != nil {
		t.Errorf("marking an item whose deal moved to another team: %v, want success (the deal stays readable)", err)
	}
	if _, err := b.engine.MarkActed(rep, kept.ID, later); err != nil {
		t.Errorf("marking an item the rep still owns: %v", err)
	}
}

// itemsByDeal indexes a run's served items by the deal each names.
func itemsByDeal(t *testing.T, run BriefRun) map[ids.UUID]BriefRunItem {
	t.Helper()
	byDeal := make(map[ids.UUID]BriefRunItem, len(run.Items))
	for _, item := range run.Items {
		byDeal[item.DealID] = item
	}
	return byDeal
}

// queueItemDeals names the deals a run's items are about, for a failure that
// has to say WHICH queue it got rather than only that it was wrong.
func queueItemDeals(run BriefRun) []ids.UUID {
	deals := make([]ids.UUID, 0, len(run.Items))
	for _, item := range run.Items {
		deals = append(deals, item.DealID)
	}
	return deals
}
