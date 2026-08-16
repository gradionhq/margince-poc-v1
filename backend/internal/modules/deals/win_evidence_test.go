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

// A detail made of invisible characters explains exactly nothing, which is the
// state the reason vocabulary exists to refuse. A zero-width space is not
// whitespace to Go's TrimSpace and not whitespace to Postgres's btrim either,
// so it would otherwise satisfy both the Go check and the column's CHECK.
func TestADetailOfInvisibleCharactersIsRefused(t *testing.T) {
	for name, detail := range map[string]string{
		"zero-width space":   "​",
		"non-breaking space": " ",
		"tab":                "\t",
		"newline":            "\n",
		"soft hyphen":        "­",
	} {
		t.Run(name, func(t *testing.T) {
			var needsDetail *WonReasonDetailRequiredError
			if !errors.As(validateWonReason("other", &detail), &needsDetail) {
				t.Errorf("%q was accepted as an explanation", detail)
			}
		})
	}
}

func TestADetailWithRealWordsIsAccepted(t *testing.T) {
	detail := "closed on a framework call-off"
	if err := validateWonReason("other", &detail); err != nil {
		t.Errorf("a real explanation was refused: %v", err)
	}
}

// A draft contract has asserted nothing — it is the state an agreement is born
// in — so paper stapled to one is the unsigned template the gate exists to
// refuse. The query must say so, or the gate's hardest case passes.
func TestTheEvidenceQueryRefusesADraftContract(t *testing.T) {
	if !strings.Contains(evidenceQuery, "c.status <> 'draft'") {
		t.Error("the evidence query admits a draft contract; an unsigned template would satisfy the gate")
	}
	if !strings.Contains(evidenceQuery, "c.signed_on IS NOT NULL") {
		t.Error("the evidence query does not require a signed date")
	}
	if !strings.Contains(evidenceQuery, "a.archived_at IS NULL") {
		t.Error("the evidence query admits an archived attachment; archive leaves the row in place")
	}
	if !strings.Contains(evidenceQuery, "doc_state IN ('current', 'final')") {
		t.Error("the evidence query admits a draft document")
	}
	// The malware verdict is deliberately absent: no scanner is integrated, so
	// requiring `clean` would reject every genuine upload.
	if strings.Contains(evidenceQuery, "scan_status") {
		t.Error("the evidence query tests the scan verdict, which no scanner ever sets")
	}
}
