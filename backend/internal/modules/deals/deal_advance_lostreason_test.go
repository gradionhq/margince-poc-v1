// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"context"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// A reason for a loss must not outlive the loss. The report that counts why
// deals are lost reads this column, so a reason left behind by a re-decided
// deal answers that question with an outcome that no longer stands.
//
// The amount is left nil throughout: a closing deal with an amount freezes an
// FX rate, which needs a transaction, and the reason-clearing this proves is
// decided before any of that.
func TestALostReasonIsClearedOnEveryLandingThatIsNotLost(t *testing.T) {
	reason := "went with a competitor"
	lost := crmcontracts.Deal{
		Status:     crmcontracts.DealStatusLost,
		LostReason: &reason,
	}

	for name, semantic := range map[string]string{
		"re-decided as won": "won",
		"reopened":          "open",
	} {
		t.Run(name, func(t *testing.T) {
			store := &Store{}
			patch, status, err := store.stageTransitionPatch(
				context.Background(), nil, lost, AdvanceDealInput{}, semantic)
			if err != nil {
				t.Fatalf("stageTransitionPatch: %v", err)
			}
			if status == string(crmcontracts.DealStatusLost) {
				t.Fatalf("status = %q, want a landing that is not lost", status)
			}

			cleared, assigned := patch.After()["lost_reason"]
			if !assigned {
				t.Fatal("lost_reason was never assigned, so the previous reason survives the transition")
			}
			if cleared != nil {
				t.Errorf("lost_reason = %v, want nil — the reason describes a close that no longer stands", cleared)
			}
		})
	}
}

// The reason still has to be written when the deal actually lands on lost,
// which is the behaviour the clearing branch must not swallow.
func TestALostReasonIsWrittenWhenTheDealLandsOnLost(t *testing.T) {
	reason := "budget cut"
	open := crmcontracts.Deal{Status: crmcontracts.DealStatusOpen}

	store := &Store{}
	patch, status, err := store.stageTransitionPatch(
		context.Background(), nil, open, AdvanceDealInput{LostReason: &reason}, "lost")
	if err != nil {
		t.Fatalf("stageTransitionPatch: %v", err)
	}
	if status != string(crmcontracts.DealStatusLost) {
		t.Fatalf("status = %q, want lost", status)
	}
	if got := patch.After()["lost_reason"]; got != reason {
		t.Errorf("lost_reason = %v, want %q", got, reason)
	}
}
