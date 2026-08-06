// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The installation's own company outlives every ordinary record operation
// (ADR-0082/A127).
//
// The schema refuses the write either way (0190). These checks exist so a human
// gets a sentence naming what they tried and why it is refused, instead of a
// constraint violation — and so the refusal is decided before a merge has taken
// its locks and rewritten half a customer's history.

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// AnchorProtectedError refuses an operation that would retire the workspace's
// own company. Losing it is not a lost record: the company read resolves only a
// live anchor, so the whole workspace reads as one that was never configured.
type AnchorProtectedError struct{ Action string }

func (e *AnchorProtectedError) Error() string {
	return "this is the workspace's own company; it cannot be " + e.Action
}

// FieldFault maps it onto the wire as a 422 naming the record.
func (e *AnchorProtectedError) FieldFault() (field, code, message string) {
	return "id", "anchor_protected", e.Error()
}

// refuseIfAnchor stops an operation naming the installation's own company.
func refuseIfAnchor(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, action string) error {
	var anchor bool
	if err := tx.QueryRow(ctx,
		`SELECT is_anchor FROM organization WHERE id = $1`, orgID).Scan(&anchor); err != nil {
		// A row that is not there is not the anchor; the caller's own
		// not-found path reports it.
		return nil //nolint:nilerr // deliberate: absence is the caller's answer to give, not this guard's
	}
	if anchor {
		return &AnchorProtectedError{Action: action}
	}
	return nil
}
