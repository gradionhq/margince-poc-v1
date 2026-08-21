// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agentactivity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The read has no path around the workspace-bound transaction contract: with no
// handle it refuses, so an empty feed is never mistaken for "nothing is running".
// No database is needed to state that — DB.Tx answers the sentinel before it
// touches a pool — and a claim this size belongs where `make check` reads it.
func TestMineOnAnUnboundHandleFailsRatherThanReadingAnything(t *testing.T) {
	store := NewStore(nil, time.Now)
	_, _, err := store.Mine(context.Background(), ids.NewV7())
	if err == nil {
		t.Fatal("a store with no database handle must refuse, not answer empty")
	}
	if !errors.Is(err, database.ErrNoWorkspace) {
		t.Fatalf("want the no-workspace sentinel, got %v", err)
	}
}

// Two occurrences stamped the same instant must hold one order across polls: the
// panel re-renders on every answer, and a pair that swaps places makes the rail
// flicker between two truthful readings of the same feed. The id is the only
// stable tiebreak available, so the assertion is that the order is the SAME both
// ways round, not that some particular id wins.
func TestNewestFirstKeepsTiedItemsInAStableOrder(t *testing.T) {
	instant := time.Date(2026, 8, 21, 6, 0, 0, 0, time.UTC)
	low, high := ids.NewV7(), ids.NewV7()
	if low.String() > high.String() {
		low, high = high, low
	}

	for _, order := range [][]Item{
		{{ID: low, StartedAt: instant}, {ID: high, StartedAt: instant}},
		{{ID: high, StartedAt: instant}, {ID: low, StartedAt: instant}},
	} {
		newestFirst(order)
		if order[0].ID != high || order[1].ID != low {
			t.Fatalf("tied items must land in one id-determined order, got %v then %v",
				order[0].ID, order[1].ID)
		}
	}
}

func TestNewestFirstPutsTheMostRecentOccurrenceFirst(t *testing.T) {
	older := Item{ID: ids.NewV7(), StartedAt: time.Date(2026, 8, 21, 2, 0, 0, 0, time.UTC)}
	newer := Item{ID: ids.NewV7(), StartedAt: time.Date(2026, 8, 21, 6, 0, 0, 0, time.UTC)}
	items := []Item{older, newer}
	newestFirst(items)
	if items[0].ID != newer.ID {
		t.Fatalf("the newest occurrence must lead the feed, got %v", items[0].StartedAt)
	}
}

// The cap is a wire bound, so the boundary is the assertion: exactly the bound
// passes through untouched, one rune more comes back at the bound and says so.
func TestCappedTruncatesOnlyBeyondTheBound(t *testing.T) {
	for _, tc := range []struct {
		name     string
		length   int
		bound    int
		wantRune int
		wantCut  bool
	}{
		{"one below the bound", 9, 10, 9, false},
		{"exactly the bound", 10, 10, 10, false},
		{"one above the bound", 11, 10, 10, true},
		{"a model transcript", 50_000, summaryBound, summaryBound, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.Repeat("a", tc.length)
			got := capped(&text, tc.bound)
			if got == nil {
				t.Fatal("capped dropped a present string")
			}
			if n := len([]rune(*got)); n != tc.wantRune {
				t.Errorf("capped to %d runes, want %d", n, tc.wantRune)
			}
			if cut := strings.HasSuffix(*got, "…"); cut != tc.wantCut {
				t.Errorf("truncation marker present = %v, want %v", cut, tc.wantCut)
			}
		})
	}
}

// A multi-byte string must never be cut mid-character: the wire is JSON, and a
// half-written rune is a response no client can decode.
func TestCappedCutsOnRuneBoundaries(t *testing.T) {
	text := strings.Repeat("日", 40)
	got := capped(&text, 10)
	if got == nil {
		t.Fatal("capped dropped a present string")
	}
	if *got != strings.Repeat("日", 9)+"…" {
		t.Errorf("capped mid-character: %q", *got)
	}
	if !utf8.ValidString(*got) {
		t.Errorf("capped produced invalid UTF-8: %q", *got)
	}
}

func TestCappedLeavesAnAbsentColumnAbsent(t *testing.T) {
	if got := capped(nil, summaryBound); got != nil {
		t.Errorf("an absent column must stay absent, got %q", *got)
	}
}
