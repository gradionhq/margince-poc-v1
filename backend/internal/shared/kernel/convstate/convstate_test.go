// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package convstate_test

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/convstate"
)

// A fixed instant, so every case reads as a date rather than as arithmetic
// against whatever today happens to be.
var now = time.Date(2026, time.August, 11, 9, 0, 0, 0, time.UTC)

func daysAgo(n int) time.Time { return now.AddDate(0, 0, -n) }

func TestClassifyPlacesACorrespondenceOnTheAxis(t *testing.T) {
	cases := []struct {
		name          string
		lastIn        time.Time
		lastOut       time.Time
		wantBand      convstate.Band
		wantDays      int
		wantDirection convstate.Direction
	}{
		{
			name:          "nothing exchanged is a first touch",
			wantBand:      convstate.BandNone,
			wantDays:      0,
			wantDirection: convstate.DirectionNone,
		},
		{
			name:          "they wrote yesterday",
			lastIn:        daysAgo(1),
			wantBand:      convstate.BandFresh,
			wantDays:      1,
			wantDirection: convstate.DirectionInbound,
		},
		{
			name:          "we wrote three weeks ago and got no answer",
			lastOut:       daysAgo(21),
			wantBand:      convstate.BandWeeks,
			wantDays:      21,
			wantDirection: convstate.DirectionOutbound,
		},
		{
			name:          "they wrote eight months ago",
			lastIn:        daysAgo(240),
			wantBand:      convstate.BandMonths,
			wantDays:      240,
			wantDirection: convstate.DirectionInbound,
		},
		{
			name:          "the later of the two messages decides the direction",
			lastIn:        daysAgo(30),
			lastOut:       daysAgo(4),
			wantBand:      convstate.BandFresh,
			wantDays:      4,
			wantDirection: convstate.DirectionOutbound,
		},
		{
			name:          "an inbound after our own reply reads as inbound",
			lastIn:        daysAgo(2),
			lastOut:       daysAgo(9),
			wantBand:      convstate.BandFresh,
			wantDays:      2,
			wantDirection: convstate.DirectionInbound,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := convstate.Classify(now, c.lastIn, c.lastOut)
			if got.Band != c.wantBand {
				t.Errorf("Band = %q, want %q", got.Band, c.wantBand)
			}
			if got.SilenceDays != c.wantDays {
				t.Errorf("SilenceDays = %d, want %d", got.SilenceDays, c.wantDays)
			}
			if got.LastDirection != c.wantDirection {
				t.Errorf("LastDirection = %q, want %q", got.LastDirection, c.wantDirection)
			}
		})
	}
}

// Mutation check on the two boundaries: each is pinned from both sides, so
// moving either constant flips an answer here.
func TestTheBandBoundariesAreExactlyWhereTheyAreWritten(t *testing.T) {
	cases := []struct {
		days int
		want convstate.Band
	}{
		{days: convstate.FreshMaxDays, want: convstate.BandFresh},
		{days: convstate.FreshMaxDays + 1, want: convstate.BandWeeks},
		{days: convstate.WeeksMaxDays, want: convstate.BandWeeks},
		{days: convstate.WeeksMaxDays + 1, want: convstate.BandMonths},
	}
	for _, c := range cases {
		got := convstate.Classify(now, daysAgo(c.days), time.Time{})
		if got.Band != c.want {
			t.Errorf("at %d days of silence Band = %q, want %q", c.days, got.Band, c.want)
		}
	}
}

// Clock skew between a mail host and this one is ordinary. A message dated in
// the future just arrived; it is not negative silence.
func TestAMessageDatedInTheFutureIsTreatedAsJustArrived(t *testing.T) {
	got := convstate.Classify(now, now.AddDate(0, 0, 2), time.Time{})

	if got.SilenceDays != 0 {
		t.Errorf("SilenceDays = %d, want 0", got.SilenceDays)
	}
	if got.Band != convstate.BandFresh {
		t.Errorf("Band = %q, want %q", got.Band, convstate.BandFresh)
	}
}

// The two questions a drafter actually asks the axis. These are what
// DRAFT-AC-E-3 and DRAFT-AC-E-4 turn into code: at BandNone a draft may not
// imply any earlier contact, and outside BandFresh it may not assume the other
// party remembers the exchange.
func TestTheAxisAnswersWhatADraftMayAssume(t *testing.T) {
	cases := []struct {
		band             convstate.Band
		wantPriorContact bool
		wantSharedMemory bool
	}{
		{band: convstate.BandNone, wantPriorContact: false, wantSharedMemory: false},
		{band: convstate.BandFresh, wantPriorContact: true, wantSharedMemory: true},
		{band: convstate.BandWeeks, wantPriorContact: true, wantSharedMemory: false},
		{band: convstate.BandMonths, wantPriorContact: true, wantSharedMemory: false},
	}
	for _, c := range cases {
		state := convstate.State{Band: c.band}
		if got := state.ImpliesPriorContact(); got != c.wantPriorContact {
			t.Errorf("%q: ImpliesPriorContact() = %v, want %v", c.band, got, c.wantPriorContact)
		}
		if got := state.AssumesSharedMemory(); got != c.wantSharedMemory {
			t.Errorf("%q: AssumesSharedMemory() = %v, want %v", c.band, got, c.wantSharedMemory)
		}
	}
}
