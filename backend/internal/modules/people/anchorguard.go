// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The installation's own company outlives every ordinary record operation
// (ADR-0082/A127).
//
// The schema refuses the write either way (0193). These checks exist so a human
// gets a sentence naming what they tried and why it is refused, instead of a
// constraint violation — and so the refusal is decided before a merge has taken
// its locks and rewritten half a customer's history.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// AnchorProtectedError refuses an operation that would retire the workspace's
// own company. Losing it is not a lost record: the company read resolves only a
// live anchor, so the whole workspace reads as one that was never configured.
type AnchorProtectedError struct {
	// Field names the request value that carried the anchor, so a caller sent
	// two ids knows which one is refused: a merge names its source in the path
	// and its target in the body, and blaming the path id for a bad target
	// points the client at a value that was fine.
	Field  string
	Action string
}

func (e *AnchorProtectedError) Error() string {
	return "this is the workspace's own company; " + e.Action
}

// FieldFault maps it onto the wire as a 422 naming the value to change.
func (e *AnchorProtectedError) FieldFault() (field, code, message string) {
	return e.Field, "anchor_protected", e.Error()
}

// refuseIfAnchor stops an operation naming the installation's own company.
func refuseIfAnchor(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, field, action string) error {
	var anchor bool
	switch err := tx.QueryRow(ctx,
		`SELECT is_anchor FROM organization WHERE id = $1`, orgID).Scan(&anchor); {
	case err == nil:
	case errors.Is(err, pgx.ErrNoRows):
		// A row that is not there is not the anchor, and the caller's own
		// not-found path is the one that should say so.
		return nil
	default:
		// Anything else is unknown, not absent. Returning nil here would let a
		// dead connection or an aborted transaction PERMIT the operation this
		// guard exists to refuse — and in a merge that means relinking a
		// customer's people, deals and history before the schema stops it.
		return fmt.Errorf("people: reading the anchor flag for organization %s: %w", orgID, err)
	}
	if anchor {
		return &AnchorProtectedError{Field: field, Action: action}
	}
	return nil
}
