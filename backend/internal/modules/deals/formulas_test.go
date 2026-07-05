// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// §8 fixed-clock table: the stalled boolean over status, idle duration
// and the wait suppression.

import (
	"testing"
	"time"
)

func TestIsStalled(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	at := func(daysAgo int) *time.Time {
		v := now.AddDate(0, 0, -daysAgo)
		return &v
	}

	cases := []struct {
		name    string
		status  string
		created time.Time
		lastAct *time.Time
		wait    *time.Time
		want    bool
	}{
		{"fresh open deal", "open", now.AddDate(0, 0, -5), at(2), nil, false},
		{"idle past threshold", "open", now.AddDate(0, 0, -90), at(61), nil, true},
		{"exactly at threshold is not stalled", "open", now.AddDate(0, 0, -90), at(60), nil, false},
		{"no activity ever, old deal", "open", now.AddDate(0, 0, -61), nil, nil, true},
		{"closed deals never stall", "won", now.AddDate(0, 0, -400), at(300), nil, false},
		{"active wait suppresses", "open", now.AddDate(0, 0, -90), at(80), at(-10), false},
		{"expired wait un-suppresses", "open", now.AddDate(0, 0, -90), at(80), at(5), true},
	}
	for _, c := range cases {
		if got := IsStalled(c.status, c.created, c.lastAct, c.wait, now); got != c.want {
			t.Errorf("%s: IsStalled = %v, want %v", c.name, got, c.want)
		}
		stalled, reason := StalledWithReason(c.status, c.created, c.lastAct, c.wait, now)
		switch {
		case stalled != c.want:
			t.Errorf("%s: StalledWithReason = %v, want %v", c.name, stalled, c.want)
		case stalled && reason != StalledReasonNoActivity:
			t.Errorf("%s: reason = %q, want the deterministic default %q", c.name, reason, StalledReasonNoActivity)
		case !stalled && reason != "":
			t.Errorf("%s: a non-stalled deal carries reason %q", c.name, reason)
		}
	}
}
