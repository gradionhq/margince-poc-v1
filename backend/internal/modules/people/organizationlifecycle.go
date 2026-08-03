// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Moving an account's stage from a decision a human already made.
//
// The ordinary path is UpdateOrganization, which takes a caller's sparse edit
// and an If-Match version. A released approval has neither: it carries the
// stage the proposal named and the stage the record was in WHEN the proposal
// was made, and it must write only if the record has not moved since. So the
// pin is the stage itself rather than a row version — an unrelated write that
// bumps the row is not a reason to refuse, and a stage that someone else has
// already corrected is.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// SetOrganizationLifecycleTx moves the account's stage inside the caller's
// transaction, and reports whether it wrote.
//
// A false is not a failure. It means the record left the stage the proposal
// was made against — someone corrected it by hand, or a second proposal landed
// first — and in that case the human's own edit stands and the approval is
// simply spent. Guessing which of the two was right is not this writer's call.
func (s *Store) SetOrganizationLifecycleTx(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, from, to string,
) (bool, error) {
	if err := checkLifecycle(to); err != nil {
		return false, err
	}
	var current string
	// The row lock serializes this against a concurrent human edit: whoever
	// commits first is read by the other, so the comparison below cannot be
	// decided against a stage that has already been replaced.
	err := tx.QueryRow(ctx, `
		SELECT lifecycle FROM organization
		 WHERE id = $1 AND archived_at IS NULL
		 FOR UPDATE`, orgID).Scan(&current)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("people: reading the account's stage: %w", err)
	}
	if current != from || current == to {
		return false, nil
	}
	tag, err := tx.Exec(ctx, `
		UPDATE organization SET lifecycle = $2, updated_at = now(), version = version + 1
		 WHERE id = $1 AND lifecycle = $3`, orgID, to, from)
	if err != nil {
		return false, fmt.Errorf("people: moving the account's stage: %w", err)
	}
	// The row lock above makes this unreachable, and it is checked anyway: an
	// audit row and an organization.updated event describing a move that did
	// not happen are worse than the move being skipped.
	if tag.RowsAffected() == 0 {
		return false, nil
	}

	before := map[string]any{"lifecycle": from}
	after := map[string]any{"lifecycle": to, auditKeySource: "signal"}
	auditID, err := storekit.Audit(ctx, tx, actionUpdate, "organization", orgID.UUID, before, after)
	if err != nil {
		return false, fmt.Errorf("people: auditing the stage change: %w", err)
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, orgID.UUID,
		crmcontracts.PublicEventOrganizationUpdated{ChangedFields: after}); err != nil {
		return false, fmt.Errorf("people: emitting organization.updated for the stage change: %w", err)
	}
	return true, nil
}
