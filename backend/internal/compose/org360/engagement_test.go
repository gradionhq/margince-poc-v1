// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

import (
	"testing"
	"time"
)

// PO-F-4's five states, in the order that keeps them mutually exclusive. The
// order is the point: a silent account must read as dormant rather than as
// whichever side happened to write last a year ago.
func TestEngagementStateAnswersWhoseMoveItIs(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	day := 24 * time.Hour

	cases := []struct {
		name string
		in   suggestionInputs
		want string
	}{
		{"nothing captured", suggestionInputs{}, "never_contacted"},
		{"they wrote this morning", suggestionInputs{
			hasNewest: true, newest: lastMessage{Direction: "inbound", At: ago(4 * time.Hour)},
		}, "active"},
		{"we wrote yesterday", suggestionInputs{
			hasNewest: true, newest: lastMessage{Direction: "outbound", At: ago(day)},
		}, "active"},
		// The threshold is shared with the no_reply suggestion, so the strip and
		// the nudge below it cannot disagree about whether an account is waiting.
		{"we wrote a fortnight ago", suggestionInputs{
			hasNewest: true, newest: lastMessage{Direction: "outbound", At: ago(14 * day)},
		}, "waiting_on_them"},
		{"they wrote a fortnight ago", suggestionInputs{
			hasNewest: true, newest: lastMessage{Direction: "inbound", At: ago(14 * day)},
		}, "waiting_on_us"},
		{"exactly at the waiting threshold", suggestionInputs{
			hasNewest: true, newest: lastMessage{Direction: "outbound", At: ago(7 * day)},
		}, "waiting_on_them"},
		// Dormancy outranks direction: after a quarter, whose move it is stops
		// being a question anyone can act on.
		{"silent for four months, we wrote last", suggestionInputs{
			hasNewest: true, newest: lastMessage{Direction: "outbound", At: ago(120 * day)},
		}, "dormant"},
		{"silent for four months, they wrote last", suggestionInputs{
			hasNewest: true, newest: lastMessage{Direction: "inbound", At: ago(120 * day)},
		}, "dormant"},
		{"just inside dormancy", suggestionInputs{
			hasNewest: true, newest: lastMessage{Direction: "inbound", At: ago(89 * day)},
		}, "waiting_on_us"},
		// A message with no recorded direction cannot say whose move it is. It
		// still proves contact happened, so the account is not "never contacted".
		{"newest message has no direction", suggestionInputs{
			hasNewest: true, newest: lastMessage{Direction: "", At: ago(day)},
		}, "active"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(engagementState(tc.in, now)); got != tc.want {
				t.Errorf("engagementState = %q, want %q", got, tc.want)
			}
		})
	}
}
