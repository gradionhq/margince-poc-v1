// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package briefs

// The L2 Morning-Brief ranker over real rows (B-E05.2): the model
// re-orders WITHIN the deterministic §10.1 candidate set, and the
// deterministic guarantee stays real — the AI can re-order but can never
// inject a deal below the §10 cutoff, and an unavailable model falls back
// to the composite order. The same seeded fixture as the deterministic
// spine (Deal A ≫ Deal B, Deal C below the bar).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// scriptedBrain is a model double: it returns a fixed {"order":[...]} of
// deal ids, or an error to exercise the deterministic fallback. It never
// reaches the network — the L2 ranker's contract is purely the ordered id
// list it parses back.
type scriptedBrain struct {
	order []ids.UUID
	err   error
}

func (b scriptedBrain) Complete(_ context.Context, _ model.Request) (model.Response, error) {
	if b.err != nil {
		return model.Response{}, b.err
	}
	quoted := make([]string, len(b.order))
	for i, id := range b.order {
		quoted[i] = fmt.Sprintf("%q", id.String())
	}
	return model.Response{Text: `{"order":[` + strings.Join(quoted, ",") + `]}`}, nil
}

// The L2 layer re-orders within the candidate set: the model promotes
// Deal B above the deterministically-higher Deal A, and the queue honours
// it. The deterministic engine on the same seed yields [A, B] — proving
// the re-order is the model's, bounded by the same candidates.
func TestBriefL2ReordersWithinTheCandidateSet(t *testing.T) {
	b := setupBrief(t)

	deterministic, err := b.engine.Rank(b.repCtx, briefClock)
	if err != nil {
		t.Fatal(err)
	}
	if want := []ids.UUID{b.dealA, b.dealB}; !sameOrder(deterministic.Queue, want) {
		t.Fatalf("deterministic queue = %v, want [Deal A, Deal B]", queueDeals(deterministic.Queue))
	}

	l2 := NewBriefEngine(b.Pool, b.People).WithL2Ranker(scriptedBrain{order: []ids.UUID{b.dealB, b.dealA}}, nil)
	ranked, err := l2.Rank(b.repCtx, briefClock)
	if err != nil {
		t.Fatal(err)
	}
	if want := []ids.UUID{b.dealB, b.dealA}; !sameOrder(ranked.Queue, want) {
		t.Fatalf("L2 queue = %v, want the model's [Deal B, Deal A]", queueDeals(ranked.Queue))
	}
	// The re-order preserves the candidate set and every item's evidence.
	if ranked.CandidateCount != 2 {
		t.Fatalf("L2 candidate count = %d, want 2 (the re-order never changes the set)", ranked.CandidateCount)
	}
	for _, item := range ranked.Queue {
		if len(item.EvidenceIDs) == 0 {
			t.Fatalf("L2 item %s lost its evidence — evidence-or-omit must survive the re-order", item.DealID)
		}
	}
}

// The deterministic guarantee that stays real: whatever the model
// returns, the queue is bounded to the §10 candidate set. Deal C is below
// the honest-short bar (not a candidate) and a fabricated id is unknown —
// the model naming both cannot pull either into the queue.
func TestBriefL2CannotBreachTheCutoffOrInventDeals(t *testing.T) {
	b := setupBrief(t)

	fabricated := ids.NewV7()
	brain := scriptedBrain{order: []ids.UUID{b.dealC, fabricated, b.dealB, b.dealA}}
	l2 := NewBriefEngine(b.Pool, b.People).WithL2Ranker(brain, nil)

	ranked, err := l2.Rank(b.repCtx, briefClock)
	if err != nil {
		t.Fatal(err)
	}
	// Deal C (below the bar) and the fabricated id are dropped; the queue
	// is the candidate set in the model's order for the deals it may touch.
	if want := []ids.UUID{b.dealB, b.dealA}; !sameOrder(ranked.Queue, want) {
		t.Fatalf("bounded L2 queue = %v, want [Deal B, Deal A] with Deal C and the invented id dropped", queueDeals(ranked.Queue))
	}
	for _, item := range ranked.Queue {
		if item.DealID == b.dealC || item.DealID == fabricated {
			t.Fatalf("item %s breached the candidate set — the L2 layer must never inject below the cutoff", item.DealID)
		}
	}
}

// An unavailable model degrades to the deterministic composite order — the
// AI-off fallback rank (§10.1), never a failed brief.
func TestBriefL2FallsBackToTheDeterministicOrder(t *testing.T) {
	b := setupBrief(t)

	l2 := NewBriefEngine(b.Pool, b.People).WithL2Ranker(scriptedBrain{err: errors.New("model unavailable")}, nil)
	ranked, err := l2.Rank(b.repCtx, briefClock)
	if err != nil {
		t.Fatalf("an unavailable L2 model must not fail the brief: %v", err)
	}
	if want := []ids.UUID{b.dealA, b.dealB}; !sameOrder(ranked.Queue, want) {
		t.Fatalf("fallback queue = %v, want the deterministic [Deal A, Deal B]", queueDeals(ranked.Queue))
	}
}

func sameOrder(queue []BriefQueueItem, want []ids.UUID) bool {
	if len(queue) != len(want) {
		return false
	}
	for i, id := range want {
		if queue[i].DealID != id {
			return false
		}
	}
	return true
}
