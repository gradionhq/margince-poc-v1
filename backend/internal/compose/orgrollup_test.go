// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestQuarterBounds(t *testing.T) {
	losAngeles, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("load America/Los_Angeles: %v", err)
	}

	cases := []struct {
		name      string
		now       time.Time
		loc       *time.Location
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			name:      "mid-quarter",
			now:       time.Date(2026, time.May, 15, 10, 30, 0, 0, time.UTC),
			loc:       time.UTC,
			wantStart: time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			// The instant exactly on a quarter boundary belongs to the
			// quarter it opens, never the one it closes.
			name:      "quarter boundary instant",
			now:       time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
			loc:       time.UTC,
			wantStart: time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			// One nanosecond before the boundary must still resolve to
			// the closing quarter, proving the end bound is exclusive.
			name:      "instant before quarter boundary stays in prior quarter",
			now:       time.Date(2026, time.June, 30, 23, 59, 59, 999999999, time.UTC),
			loc:       time.UTC,
			wantStart: time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			// UTC instant lands early morning Jan 1, but Los Angeles is
			// still Dec 31 the prior year — the workspace timezone must
			// shift which quarter (and year) this resolves to, not UTC.
			name:      "timezone shifts the calendar date across a year boundary",
			now:       time.Date(2026, time.January, 1, 4, 0, 0, 0, time.UTC),
			loc:       losAngeles,
			wantStart: time.Date(2025, time.October, 1, 0, 0, 0, 0, losAngeles),
			wantEnd:   time.Date(2026, time.January, 1, 0, 0, 0, 0, losAngeles),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end := currentQuarterBounds(tc.now, tc.loc)
			if !start.Equal(tc.wantStart) {
				t.Errorf("start = %v, want %v", start, tc.wantStart)
			}
			if !end.Equal(tc.wantEnd) {
				t.Errorf("end = %v, want %v", end, tc.wantEnd)
			}
		})
	}
}

func TestWeightedValue(t *testing.T) {
	cases := []struct {
		name       string
		baseMinor  int64
		winPercent int
		want       int64
	}{
		{name: "exact quotient needs no rounding", baseMinor: 100000, winPercent: 50, want: 50000},
		{name: "positive half rounds away from zero", baseMinor: 1, winPercent: 50, want: 1},
		{name: "negative half rounds away from zero", baseMinor: -1, winPercent: 50, want: -1},
		{name: "positive one-and-half rounds up", baseMinor: 3, winPercent: 50, want: 2},
		{name: "negative one-and-half rounds down", baseMinor: -3, winPercent: 50, want: -2},
		{name: "0% probability is a real zero", baseMinor: 123456, winPercent: 0, want: 0},
		{name: "100% probability passes the amount through", baseMinor: 123456, winPercent: 100, want: 123456},
		{name: "zero amount stays zero at any probability", baseMinor: 0, winPercent: 75, want: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := weightedValue(tc.baseMinor, tc.winPercent)
			if got != tc.want {
				t.Errorf("weightedValue(%d, %d) = %d, want %d", tc.baseMinor, tc.winPercent, got, tc.want)
			}
		})
	}
}

func TestConvertToBase(t *testing.T) {
	cases := []struct {
		name        string
		amountMinor int64
		rate        float64
		want        int64
	}{
		{name: "rate of 1.0 is a passthrough", amountMinor: 123456, rate: 1.0, want: 123456},
		{name: "positive half rounds away from zero", amountMinor: 1, rate: 0.5, want: 1},
		{name: "negative half rounds away from zero", amountMinor: -1, rate: 0.5, want: -1},
		{name: "positive one-and-half rounds up", amountMinor: 3, rate: 0.5, want: 2},
		{name: "negative one-and-half rounds down", amountMinor: -3, rate: 0.5, want: -2},
		{name: "zero amount converts to zero at any rate", amountMinor: 0, rate: 1.37, want: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := convertToBase(tc.amountMinor, tc.rate)
			if got != tc.want {
				t.Errorf("convertToBase(%d, %v) = %d, want %d", tc.amountMinor, tc.rate, got, tc.want)
			}
		})
	}
}

func TestFxRateUnavailableErrorMessage(t *testing.T) {
	asOf := time.Date(2026, time.July, 11, 0, 0, 0, 0, time.UTC)
	err := fxRateUnavailableError{Currency: "JPY", AsOf: asOf}

	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() returned an empty message")
	}
	// The message must be actionable: it names the currency and the date
	// the caller needs to go store a rate for, not an opaque failure.
	if !contains(msg, "JPY") || !contains(msg, "2026-07-11") {
		t.Errorf("Error() = %q, want it to name the currency and date", msg)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func uuidPtr(id ids.UUID) *ids.UUID { return &id }

func TestPruneUnreadable(t *testing.T) {
	root := ids.NewV7()
	childA := ids.NewV7()
	childB := ids.NewV7()
	grandchildA1 := ids.NewV7()
	grandchildB1 := ids.NewV7()

	tree := []orgTreeNode{
		{id: root, parentID: nil, displayName: "Root Co"},
		{id: childA, parentID: uuidPtr(root), displayName: "Child A"},
		{id: childB, parentID: uuidPtr(root), displayName: "Child B"},
		{id: grandchildA1, parentID: uuidPtr(childA), displayName: "Grandchild A1"},
		{id: grandchildB1, parentID: uuidPtr(childB), displayName: "Grandchild B1"},
	}

	t.Run("all readable includes the whole tree, root-first", func(t *testing.T) {
		readable := func(ids.UUID) bool { return true }
		included, restricted, rootReadable := pruneUnreadable(root, tree, readable)

		if !rootReadable {
			t.Fatal("rootReadable = false, want true")
		}
		if len(included) != 5 || included[0] != root {
			t.Fatalf("included = %v, want all 5 nodes root-first", included)
		}
		if len(restricted) != 0 {
			t.Fatalf("restricted = %v, want empty", restricted)
		}
	})

	t.Run("root unreadable yields empty sets and rootReadable=false", func(t *testing.T) {
		readable := func(ids.UUID) bool { return false }
		included, restricted, rootReadable := pruneUnreadable(root, tree, readable)

		if rootReadable {
			t.Fatal("rootReadable = true, want false")
		}
		if len(included) != 0 {
			t.Fatalf("included = %v, want empty", included)
		}
		if len(restricted) != 0 {
			t.Fatalf("restricted = %v, want empty", restricted)
		}
	})

	t.Run("mid-branch unreadable is disclosed once and its subtree is never visited", func(t *testing.T) {
		readable := func(id ids.UUID) bool { return id != childA }
		included, restricted, rootReadable := pruneUnreadable(root, tree, readable)

		if !rootReadable {
			t.Fatal("rootReadable = false, want true")
		}
		wantIncluded := map[ids.UUID]bool{root: true, childB: true, grandchildB1: true}
		if len(included) != len(wantIncluded) {
			t.Fatalf("included = %v, want exactly %v", included, wantIncluded)
		}
		for _, id := range included {
			if !wantIncluded[id] {
				t.Errorf("included unexpectedly contains %v", id)
			}
			if id == childA || id == grandchildA1 {
				t.Errorf("included must never contain the restricted branch, got %v", id)
			}
		}
		if len(restricted) != 1 || restricted[0].ID != childA || restricted[0].DisplayName != "Child A" {
			t.Fatalf("restricted = %v, want exactly [{%v Child A}]", restricted, childA)
		}
		for _, r := range restricted {
			if r.ID == grandchildA1 {
				t.Error("grandchild of a restricted node must not be separately disclosed")
			}
		}
	})

	t.Run("two restricted siblings are both disclosed", func(t *testing.T) {
		readable := func(id ids.UUID) bool { return id == root }
		included, restricted, rootReadable := pruneUnreadable(root, tree, readable)

		if !rootReadable {
			t.Fatal("rootReadable = false, want true")
		}
		if len(included) != 1 || included[0] != root {
			t.Fatalf("included = %v, want only [root]", included)
		}
		if len(restricted) != 2 {
			t.Fatalf("restricted = %v, want both children disclosed", restricted)
		}
		gotIDs := map[ids.UUID]bool{restricted[0].ID: true, restricted[1].ID: true}
		if !gotIDs[childA] || !gotIDs[childB] {
			t.Fatalf("restricted ids = %v, want {%v, %v}", gotIDs, childA, childB)
		}
	})

	t.Run("leaf-only tree is a one-node rollup", func(t *testing.T) {
		leaf := ids.NewV7()
		leafTree := []orgTreeNode{{id: leaf, parentID: nil, displayName: "Leaf Co"}}
		readable := func(ids.UUID) bool { return true }

		included, restricted, rootReadable := pruneUnreadable(leaf, leafTree, readable)

		if !rootReadable {
			t.Fatal("rootReadable = false, want true")
		}
		if len(included) != 1 || included[0] != leaf {
			t.Fatalf("included = %v, want [leaf]", included)
		}
		if len(restricted) != 0 {
			t.Fatalf("restricted = %v, want empty", restricted)
		}
	})

	t.Run("a restored grant flips the node and its readable subtree back in", func(t *testing.T) {
		// Same shape as the mid-branch case, but the grant now reads
		// childA (and by extension its readable descendants) back in —
		// exercising that pruneUnreadable makes no assumption from any
		// prior evaluation, it only ever consults readable() fresh.
		readable := func(ids.UUID) bool { return true }
		included, restricted, rootReadable := pruneUnreadable(root, tree, readable)

		if !rootReadable {
			t.Fatal("rootReadable = false, want true")
		}
		wantIncluded := map[ids.UUID]bool{root: true, childA: true, childB: true, grandchildA1: true, grandchildB1: true}
		if len(included) != len(wantIncluded) {
			t.Fatalf("included = %v, want all 5 nodes back in", included)
		}
		for id := range wantIncluded {
			found := false
			for _, gotID := range included {
				if gotID == id {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("included is missing restored node %v", id)
			}
		}
		if len(restricted) != 0 {
			t.Fatalf("restricted = %v, want empty once the grant is restored", restricted)
		}
	})
}
