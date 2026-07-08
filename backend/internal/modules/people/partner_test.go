// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"errors"
	"testing"
)

func fitScore(v int16) *int16    { return &v }
func fitReason(v string) *string { return &v }

// The A68/ADR-0053 partner-fit override pair (formulas §17) — the exact
// sibling of the lead-score pair: a human score demands a written reason,
// the machine value survives in Computed while the override is in force,
// and clearing the reason hands the score back to the machine.

type fitOverrideCase struct {
	name        string
	current     partnerFitState
	score       *int16
	reason      *string
	want        partnerFitState
	wantRequire bool
}

var (
	noOverride = partnerFitState{Score: fitScore(40)}
	overridden = partnerFitState{Score: fitScore(90), Computed: fitScore(40), OverrideReason: fitReason("strategic account")}
)

func runFitOverrideCases(t *testing.T, cases []fitOverrideCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := applyPartnerFitOverride(tc.current, tc.score, tc.reason)
			if tc.wantRequire {
				var require *PartnerFitOverrideReasonRequiredError
				if !errors.As(err, &require) {
					t.Fatalf("expected PartnerFitOverrideReasonRequiredError, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("applyPartnerFitOverride: %v", err)
			}
			assertFitState(t, got, tc.want)
		})
	}
}

func TestApplyPartnerFitOverrideSetGestures(t *testing.T) {
	runFitOverrideCases(t, []fitOverrideCase{
		{
			name:        "setting a score without a reason is rejected",
			current:     noOverride,
			score:       fitScore(90),
			wantRequire: true,
		},
		{
			name:        "a whitespace-only reason is no reason",
			current:     noOverride,
			score:       fitScore(90),
			reason:      fitReason("   "),
			wantRequire: true,
		},
		{
			name:    "first override retains the machine value in Computed",
			current: noOverride,
			score:   fitScore(90),
			reason:  fitReason("strategic account"),
			want:    partnerFitState{Score: fitScore(90), Computed: fitScore(40), OverrideReason: fitReason("strategic account")},
		},
		{
			name:    "overriding a never-scored partner retains no machine value",
			current: partnerFitState{},
			score:   fitScore(75),
			reason:  fitReason("founder referral"),
			want:    partnerFitState{Score: fitScore(75), Computed: nil, OverrideReason: fitReason("founder referral")},
		},
		{
			name:    "re-overriding keeps the retained machine value, not the old human one",
			current: overridden,
			score:   fitScore(95),
			reason:  fitReason("expanded to second region"),
			want:    partnerFitState{Score: fitScore(95), Computed: fitScore(40), OverrideReason: fitReason("expanded to second region")},
		},
	})
}

func TestApplyPartnerFitOverrideClearAndAmendGestures(t *testing.T) {
	runFitOverrideCases(t, []fitOverrideCase{
		{
			name:    "clearing the reason restores the retained machine value",
			current: overridden,
			reason:  fitReason(""),
			want:    partnerFitState{Score: fitScore(40)},
		},
		{
			name:    "clearing with no override in force is a no-op",
			current: noOverride,
			reason:  fitReason(""),
			want:    noOverride,
		},
		{
			name:    "amending the reason keeps score and retained value",
			current: overridden,
			reason:  fitReason("strategic account, renewed"),
			want:    partnerFitState{Score: fitScore(90), Computed: fitScore(40), OverrideReason: fitReason("strategic account, renewed")},
		},
		{
			name:        "a reason with no score and no override sets nothing",
			current:     noOverride,
			reason:      fitReason("orphan note"),
			wantRequire: true,
		},
		{
			name:    "no gesture leaves the pair untouched",
			current: overridden,
			want:    overridden,
		},
	})
}

func assertFitState(t *testing.T, got, want partnerFitState) {
	t.Helper()
	assertInt16Ptr(t, "partner_fit_score", got.Score, want.Score)
	assertInt16Ptr(t, "partner_fit_score_computed", got.Computed, want.Computed)
	switch {
	case (got.OverrideReason == nil) != (want.OverrideReason == nil):
		t.Fatalf("partner_fit_override_reason = %v, want %v", got.OverrideReason, want.OverrideReason)
	case got.OverrideReason != nil && *got.OverrideReason != *want.OverrideReason:
		t.Fatalf("partner_fit_override_reason = %q, want %q", *got.OverrideReason, *want.OverrideReason)
	}
}

func assertInt16Ptr(t *testing.T, field string, got, want *int16) {
	t.Helper()
	switch {
	case (got == nil) != (want == nil):
		t.Fatalf("%s = %v, want %v", field, got, want)
	case got != nil && *got != *want:
		t.Fatalf("%s = %d, want %d", field, *got, *want)
	}
}

// The override reason is stored trimmed: audit text carries the words,
// never the caller's incidental whitespace.
func TestApplyPartnerFitOverrideTrimsTheReason(t *testing.T) {
	got, err := applyPartnerFitOverride(partnerFitState{}, fitScore(80), fitReason("  co-sell momentum  "))
	if err != nil {
		t.Fatalf("applyPartnerFitOverride: %v", err)
	}
	if got.OverrideReason == nil || *got.OverrideReason != "co-sell momentum" {
		t.Fatalf("reason = %v, want trimmed %q", got.OverrideReason, "co-sell momentum")
	}
}
