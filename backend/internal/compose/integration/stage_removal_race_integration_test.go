// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The occupancy guard against genuinely concurrent writers. The refusal
// is only a guard if the stage a deal is being bound to is LOCKED by the
// binding write: as a plain read, a deal can resolve a live stage, the
// removal can count zero and archive it, and the deal's own write still
// lands — the FK on deal.stage_id asks whether the row exists, and
// archiving is exactly the operation that leaves it existing.
//
// The invariant asserted is the one that must hold whichever side wins:
// no LIVE deal ever sits on an archived stage. A deal that reached one
// is invisible on every board read, all of which filter to live stages.

import (
	"errors"
	"sync"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestNoDealLandsOnAStageBeingRemoved(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	admin := e.Admin()

	// Each round removes a fresh empty stage while a deal advances onto
	// it. Several rounds because the interleaving is the machine's to
	// choose — the assertion below is what makes any round conclusive.
	const rounds = 8
	for round := range rounds {
		target, err := e.Deals.CreateStage(admin, deals.CreateStageInput{
			PipelineID: pipeline, Name: "Racy", Position: 90 + round, Semantic: "open",
		})
		if err != nil {
			t.Fatal(err)
		}
		targetID := ids.From[ids.StageKind](ids.UUID(target.Id))
		deal := e.SeedDeal(t, "Racer", pipeline, open, &e.Rep1)

		var wg sync.WaitGroup
		var advanceErr, removeErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, advanceErr = e.Deals.AdvanceDeal(admin, ids.From[ids.DealKind](deal),
				deals.AdvanceDealInput{ToStageID: targetID})
		}()
		go func() {
			defer wg.Done()
			removeErr = e.Deals.ArchiveStage(admin, targetID, nil)
		}()
		wg.Wait()

		assertRemovalOutcome(t, round, advanceErr, removeErr)
	}

	// The invariant, over everything the rounds produced.
	if n := e.WsCount(t, `SELECT count(*) FROM deal d JOIN stage s ON s.id = d.stage_id
	                      WHERE d.archived_at IS NULL AND s.archived_at IS NOT NULL`); n != 0 {
		t.Fatalf("%d live deal(s) sit on a removed stage — the occupancy count was read, not held", n)
	}
	// And the guard is not passing by refusing everything: a removal that
	// always lost would satisfy the invariant while proving nothing.
	if n := e.WsCount(t, `SELECT count(*) FROM stage WHERE pipeline_id = $1 AND archived_at IS NOT NULL`,
		pipeline); n == 0 {
		t.Fatal("no stage was removed in any round — the race never ran")
	}
}

// assertRemovalOutcome holds the pair of outcomes to the two the race may
// legitimately produce: the removal wins and the advance finds no live
// stage, or the advance wins and the removal refuses on occupancy. Any
// other pairing — both succeeding above all — is the defect.
func assertRemovalOutcome(t *testing.T, round int, advanceErr, removeErr error) {
	t.Helper()
	var occupied *deals.StageOccupiedError
	switch {
	case advanceErr == nil && removeErr == nil:
		t.Fatalf("round %d: the deal advanced onto the stage AND the stage was removed", round)
	case advanceErr == nil && errors.As(removeErr, &occupied):
		return // the advance won; the removal saw the deal it had just gained.
	case removeErr == nil && errors.Is(advanceErr, apperrors.ErrNotFound):
		return // the removal won; the advance found no live stage to move to.
	default:
		t.Fatalf("round %d: advance=%v remove=%v — neither side won cleanly", round, advanceErr, removeErr)
	}
}
