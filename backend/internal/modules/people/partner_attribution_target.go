// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The check a deal runs before it names a partner: does this organization
// actually have a partner programme? It lives here because `people` owns the
// `partner` table, and the deals module reaches it through the installation
// seam compose wires (ADR-0054) rather than reading a sibling's table.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// EnsureOrganizationIsPartner refuses an organization that carries no partner
// row, inside the caller's own transaction.
//
// A deal's partner is what the commission ledger prices from: accrual reads the
// margin tier off the partner row, so an attribution to a company that is not a
// partner produces a deal that LOOKS credited and can never earn anything. The
// contract has always said the target "must have a `partner` row"; this is what
// makes that true rather than aspirational.
//
// The row is LOCKED, not merely read. Running in the caller's transaction is
// not enough on its own: a plain SELECT takes no lock, so an archive could
// commit between the check and the write and leave a fresh deal naming an
// archived partner — the state this exists to refuse. FOR KEY SHARE is the
// weakest lock that conflicts with the archive's own write, so an unrelated
// edit to the partner (a tier change, a stage move) still runs alongside a deal
// being attributed. Whichever side takes it first the outcome is right: the
// archive waits and then sees the deal, or this check waits and re-evaluates
// against the committed archived_at and refuses.
//
// Archived partners are refused with the same answer as absent ones. A
// programme that has been retired is not one a NEW deal may be attributed to,
// and the deals already pointing at it keep their attribution untouched.
func EnsureOrganizationIsPartner(ctx context.Context, tx pgx.Tx, organizationID ids.OrganizationID) error {
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM partner
		                 WHERE organization_id = $1 AND archived_at IS NULL
		                 FOR KEY SHARE)`,
		organizationID).Scan(&exists); err != nil {
		return fmt.Errorf("check organization is a partner: %w", err)
	}
	if !exists {
		return &NotAPartnerError{}
	}
	return nil
}

// NotAPartnerError maps to 422: the organization exists and the caller may read
// it, but it carries no partner programme to attribute a deal to.
//
// Separate from a not-found: the caller named a real company they can see, and
// telling them "no such organization" would send them looking for the wrong
// problem. What they need to hear is that this company is not a partner yet,
// and what to do about it.
type NotAPartnerError struct{}

func (e *NotAPartnerError) Error() string {
	return "partner_org_id must name a company that is a partner — set up its partner programme first, or clear the field"
}

// FieldFault names the wire field the refusal belongs to.
func (e *NotAPartnerError) FieldFault() (field, code, message string) {
	return "partner_org_id", "not_a_partner", e.Error()
}
