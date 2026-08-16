// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The closed vocabulary is what makes the escape hatch honest: a free-text
// answer cannot be counted, and counting them is the entire reason the exit
// exists rather than a hard requirement nobody could satisfy.
func TestAReasonOutsideTheVocabularyIsRefused(t *testing.T) {
	err := validateWonReason("because I said so", nil)

	var invalid *InvalidWonReasonError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want InvalidWonReasonError", err)
	}
	for _, allowed := range WonWithoutContractReasons {
		if !strings.Contains(invalid.Error(), allowed) {
			t.Errorf("the refusal does not name %q, so a caller cannot tell what to send", allowed)
		}
	}
}

// "Other" is the member that explains nothing on its own, which is the state
// this whole feature exists to refuse.
func TestOtherWithoutDetailIsRefused(t *testing.T) {
	blank := "   "
	for name, detail := range map[string]*string{
		"absent": nil,
		"blank":  &blank,
	} {
		t.Run(name, func(t *testing.T) {
			var needsDetail *WonReasonDetailRequiredError
			if !errors.As(validateWonReason("other", detail), &needsDetail) {
				t.Error("an unexplained \"other\" was accepted; it answers the report with nothing")
			}
		})
	}
}

func TestEveryVocabularyMemberIsAccepted(t *testing.T) {
	detail := "closed on a framework call-off"
	for _, reason := range WonWithoutContractReasons {
		var supplied *string
		if reason == reasonRequiringDetail {
			supplied = &detail
		}
		if err := validateWonReason(reason, supplied); err != nil {
			t.Errorf("%s: err = %v, want nil", reason, err)
		}
	}
}

// The refusal a caller meets when a win claims nothing must name BOTH ways
// forward. A refusal that says only "no" leaves them guessing which of the two
// the product wanted, and guessing produces the fabricated contract this rule
// exists to prevent.
func TestTheRefusalNamesBothWaysForward(t *testing.T) {
	message := (&WinEvidenceMissingError{}).Error()

	if !strings.Contains(message, "signed contract") {
		t.Errorf("the refusal does not mention attaching a contract: %q", message)
	}
	if !strings.Contains(message, "reason") {
		t.Errorf("the refusal does not mention stating a reason: %q", message)
	}
	for _, reason := range WonWithoutContractReasons {
		if !strings.Contains(message, reason) {
			t.Errorf("the refusal does not name the %q option: %q", reason, message)
		}
	}
}

// A stated reason is accepted without looking for paper: somebody who has told
// the product there is none should not then be told there is none.
func TestAStatedReasonNeedsNoContractLookup(t *testing.T) {
	reason := "purchase_order"
	in := AdvanceDealInput{WonWithoutContractReason: &reason}

	// A nil transaction proves the point: reaching the database here would
	// panic, so passing means the contract lookup was never attempted.
	if err := ensureWinEvidence(t.Context(), nil, ids.New[ids.DealKind](), in); err != nil {
		t.Fatalf("a stated reason was refused: %v", err)
	}
}
