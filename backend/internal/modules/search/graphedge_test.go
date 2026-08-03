// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// Ranking the interaction edges. This is the arithmetic behind "who should
// make the introduction", and it is worth its own test because the ordering
// was wrong once already: the read orders by LAST CONTACT, and a surface that
// promises warmest-first has to re-rank — otherwise a one-line reply yesterday
// outranks a year of correspondence.

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/relstrength"
)

func edgeAt(user ids.UUID, daysAgo float64, count, in, out int) InteractionEdge {
	return InteractionEdge{
		UserID:      user,
		LastAt:      time.Now().Add(-time.Duration(daysAgo * float64(24*time.Hour))),
		Count90d:    count,
		InCount90d:  in,
		OutCount90d: out,
	}
}

func TestRankingIsByWarmthNotRecency(t *testing.T) {
	now := time.Now()
	recentButThin := ids.NewV7()
	oldButDeep := ids.NewV7()

	edges := []InteractionEdge{
		// One reply yesterday. The most RECENT contact by a mile.
		edgeAt(recentButThin, 1, 1, 1, 0),
		// A fortnight since the last message, but a real two-way history.
		edgeAt(oldButDeep, 14, 20, 10, 10),
	}
	SortByStrength(edges, now)

	if edges[0].UserID != oldButDeep {
		t.Errorf("ranked the recent one-liner first; the question is who KNOWS them, "+
			"and last-contact order answers a different one (got %s, want %s)",
			edges[0].UserID, oldButDeep)
	}
}

func TestRankingIsDeterministicOnATie(t *testing.T) {
	now := time.Now()
	// Two identical edges: same recency, same volume, same balance. Only the
	// id can separate them, and it must — an unordered payload renders the
	// same account differently on two loads.
	a, b := ids.NewV7(), ids.NewV7()
	low, high := a, b
	if b.String() < a.String() {
		low, high = b, a
	}
	first := []InteractionEdge{edgeAt(high, 3, 8, 4, 4), edgeAt(low, 3, 8, 4, 4)}
	SortByStrength(first, now)
	if first[0].UserID != low {
		t.Errorf("tie broken as %s first; the lower id must win so the order is stable", first[0].UserID)
	}
}

func TestNeverHavingSpokenSortsLast(t *testing.T) {
	now := time.Now()
	known := ids.NewV7()
	// An edge with no interactions in the window still exists — the pair have
	// corresponded at some point — but it must not outrank a live
	// relationship.
	edges := []InteractionEdge{
		edgeAt(ids.NewV7(), 200, 0, 0, 0),
		edgeAt(known, 2, 12, 6, 6),
	}
	SortByStrength(edges, now)
	if edges[0].UserID != known {
		t.Error("a colleague with no recent interaction outranked one with a live relationship")
	}
}

func TestStrengthOfMatchesTheSharedArithmetic(t *testing.T) {
	// The edge scores through the same leaf the workspace-wide score uses.
	// If these ever diverge, two numbers on one screen disagree.
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	last := now.AddDate(0, 0, -5)
	edge := InteractionEdge{LastAt: last, Count90d: 12, InCount90d: 7, OutCount90d: 5}

	got := edge.StrengthOf(now)
	want := relstrength.Compute(relstrength.Inputs{
		LastInteraction: &last, Count90d: 12, Inbound90d: 7, Outbound90d: 5,
	}, now)
	if got != want {
		t.Errorf("the edge scored %+v, the shared fold scored %+v", got, want)
	}
	// And it is the spec's worked example, so a drift in either shows here.
	if got.Strength != 47 {
		t.Errorf("strength = %d, want 47 — formulas-and-rules §4's worked example", got.Strength)
	}
}
