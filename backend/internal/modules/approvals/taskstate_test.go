// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// The deadline a durable handle to a decision must respect: not when the OFFER
// lapses, but when the decision stops being actionable. The two differ, and the
// gap is where an approved effect would otherwise be stranded.

import (
	"testing"
	"time"
)

func TestAHandleOutlivesTheOfferForAsLongAsADecisionCanStillBeSpent(t *testing.T) {
	staged := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	decided := staged.Add(-time.Second)

	for _, tc := range []struct {
		name      string
		row       row
		status    string
		want      time.Time
		rationale string
	}{
		{
			name:      "a pending offer covers the decision somebody may still make in its last second",
			row:       row{ExpiresAt: staged},
			status:    StatusPending,
			want:      staged.Add(RedemptionWindow),
			rationale: "an approval granted at the deadline stays redeemable for the window after it",
		},
		{
			name:      "a decided one is bounded by its own decision, not by the offer",
			row:       row{ExpiresAt: staged, DecidedAt: &decided},
			status:    StatusApproved,
			want:      decided.Add(RedemptionWindow),
			rationale: "the yes is spendable for the window after decided_at and worthless afterwards",
		},
		{
			name:      "a lapsed offer nobody answered extends nothing",
			row:       row{ExpiresAt: staged},
			status:    StatusExpired,
			want:      staged,
			rationale: "there is no decision left to spend, so there is nothing to stay answerable for",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := actionableUntil(tc.row, tc.status); !got.Equal(tc.want) {
				t.Errorf("actionable until %s, want %s — %s", got, tc.want, tc.rationale)
			}
		})
	}
}

// The window a handle is sized against is the one redemption actually enforces.
// Two constants would drift, and the drift would show up as a handle expiring
// while its approval was still live.
func TestTheExportedRedemptionWindowIsTheOneRedemptionEnforces(t *testing.T) {
	if RedemptionWindow != redemptionTTL {
		t.Errorf("RedemptionWindow = %s but redemption enforces %s", RedemptionWindow, redemptionTTL)
	}
}
