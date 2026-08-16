// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// Winning a deal means pointing at what was agreed, or saying why you cannot
// (ADR-0109 §6, A160).
//
// A won deal used to be a stage transition and nothing else: no signature, no
// paper, no commercial record. The number in the forecast was whatever somebody
// typed, and there was no way to ask which won deals had an agreement behind
// them.
//
// WHAT THIS REFUSES IS SILENCE, not a missing contract. Deals genuinely close
// on a purchase order, a phone call, or an import, and the product does not
// pretend otherwise — it asks which of those it was. Recording that makes the
// gap countable, and a rule with no exit would be answered by invented
// contracts, leaving data that reads as complete and is worthless.
//
// The check reads the contract table directly rather than through a port. A
// read is not a write, so it crosses no ownership boundary, and a port would
// need its own transaction — which is exactly what would let the evidence
// disappear between the check and the commit.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// WonWithoutContractReasons is the closed vocabulary a human picks from when a
// won deal has no agreement behind it. Closed on purpose: a free-text answer
// cannot be counted, and counting them is the entire justification for
// allowing the exit at all.
var WonWithoutContractReasons = []string{
	"imported", "purchase_order", "verbal", "renewal_by_email", "other",
}

// reasonRequiringDetail is the one member that explains nothing on its own.
const reasonRequiringDetail = "other"

// WinEvidenceMissingError reports a win with neither a signed agreement nor a
// stated reason. It names both ways forward, because a refusal that says only
// "no" leaves the caller guessing which of them the product wanted.
type WinEvidenceMissingError struct{}

func (e *WinEvidenceMissingError) Error() string {
	return "a won deal needs a signed contract with its paper attached, " +
		"or a reason why there is none (" + strings.Join(WonWithoutContractReasons, ", ") + ")"
}

// FieldFault answers 422 naming the field the caller can act on. Without it the
// refusal reaches a client as an opaque 500 — a rule the product enforces and
// cannot explain, which teaches a caller to retry rather than to answer.
func (e *WinEvidenceMissingError) FieldFault() (field, code, message string) {
	return "won_without_contract_reason", "win_evidence_required", e.Error()
}

// InvalidWonReasonError reports a reason outside the closed vocabulary.
type InvalidWonReasonError struct{ Reason string }

func (e *InvalidWonReasonError) Error() string {
	return "won_without_contract_reason must be one of " + strings.Join(WonWithoutContractReasons, ", ")
}

func (e *InvalidWonReasonError) FieldFault() (field, code, message string) {
	return "won_without_contract_reason", "invalid_enum", e.Error()
}

// WonReasonDetailRequiredError reports "other" with nothing after it.
type WonReasonDetailRequiredError struct{}

func (e *WonReasonDetailRequiredError) Error() string {
	return "won_without_contract_reason \"other\" needs a detail saying what it was"
}

func (e *WonReasonDetailRequiredError) FieldFault() (field, code, message string) {
	return "won_without_contract_detail", "required", e.Error()
}

// saysSomething reports whether a detail carries any visible character.
//
// TrimSpace alone is not enough: a zero-width space is not whitespace to Go and
// not whitespace to Postgres either, so "\u200b" would satisfy both the Go check
// and the column's CHECK while explaining precisely nothing — which is the state
// the whole reason vocabulary exists to refuse.
func saysSomething(detail *string) bool {
	if detail == nil {
		return false
	}
	for _, r := range *detail {
		if unicode.IsGraphic(r) && !unicode.IsSpace(r) && !unicode.Is(unicode.Cf, r) {
			return true
		}
	}
	return false
}

// ensureWinEvidence admits a transition to a won stage, or refuses it.
//
// The order matters. A stated reason is checked FIRST and, when present and
// valid, is accepted without looking for a contract: a person who has told the
// product there is no paper should not then be told there is none. Only a win
// that claims nothing goes looking for evidence.
func ensureWinEvidence(ctx context.Context, tx pgx.Tx, dealID ids.DealID, in AdvanceDealInput) error {
	if in.WonWithoutContractReason != nil {
		return validateWonReason(*in.WonWithoutContractReason, in.WonWithoutContractDetail)
	}
	signed, err := hasSignedContract(ctx, tx, dealID)
	if err != nil {
		return err
	}
	if !signed {
		return &WinEvidenceMissingError{}
	}
	return nil
}

func validateWonReason(reason string, detail *string) error {
	if !slices.Contains(WonWithoutContractReasons, reason) {
		return &InvalidWonReasonError{Reason: reason}
	}
	if reason == reasonRequiringDetail && !saysSomething(detail) {
		return &WonReasonDetailRequiredError{}
	}
	return nil
}

// evidenceQuery is the gate's one question, named so a test can read it
// rather than keeping a second copy that drifts.
const evidenceQuery = `
		SELECT EXISTS (
			SELECT 1
			FROM contract c
			JOIN attachment a ON a.contract_id = c.id
			WHERE c.deal_id = $1
			  AND c.archived_at IS NULL
			  AND c.status <> 'draft'
			  AND c.signed_on IS NOT NULL
			  AND a.archived_at IS NULL
			  AND a.category IN ('contract', 'legal')
			  AND a.doc_state IN ('current', 'final')
			FOR SHARE OF c, a
		)`

// hasSignedContract asks whether this deal has an agreement a human called
// signed, with paper a human called current.
//
// THREE DELIBERATE CHOICES, each of which the obvious query gets wrong.
//
// The contract must have LEFT DRAFT. A draft is the state an agreement is born
// in and has asserted nothing yet, so a draft with a file stapled to it is the
// unsigned template this rule exists to refuse. `superseded` still counts —
// a renewed agreement was real when the deal closed — and so does `expired` or
// `cancelled`, because a deal won in March is not un-won when the agreement
// later ends.
//
// `signed_on` is required, not merely a contract in `contract` category: the
// category says what KIND of document a file is, and only the date says a human
// asserted it was signed.
//
// WHAT THIS CANNOT DO, stated plainly rather than implied: every field it reads
// is written by the same principal who wins the deal, minutes earlier, in the
// same session. It is a record-keeping gate, not a proof of signature — it
// makes the honest case easy and the dishonest case deliberate, and the audit
// trail is what distinguishes them afterwards. Treating a passing gate as proof
// that somebody countersigned is the one reading of it that is wrong.
//
// The attachment must be live and in `current` or `final` state. Archive leaves
// the row in place, so an archived cancellation letter would pass an existence
// check, and a `draft` document is by its own definition not the agreement.
//
// FOR SHARE locks what it finds. The check and the write share a transaction on
// READ COMMITTED, so without the lock a concurrent archive could remove the
// evidence between the two and the deal would commit as won against nothing.
func hasSignedContract(ctx context.Context, tx pgx.Tx, dealID ids.DealID) (bool, error) {
	var found bool
	err := tx.QueryRow(ctx, evidenceQuery, dealID).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("check the deal's signed agreement: %w", err)
	}
	return found, nil
}
