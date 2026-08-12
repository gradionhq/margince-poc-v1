// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The one invariant user administration cannot be allowed to break: an
// organization always keeps at least one administrator who can actually
// administer it. Every path that could remove the last one — deactivating an
// admin, demoting an admin — asks here first.

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// lastActiveAdmin reports whether userID is an active admin and the ONLY one —
// deactivating them would leave the organization with no administrator and no
// in-app way to recover. Runs inside the caller's row-locked transaction.
func lastActiveAdmin(ctx context.Context, tx pgx.Tx, userID ids.UserID) (bool, error) {
	// Serialize the admin-count check+act across the whole workspace: without
	// this, two transactions each deactivating a DIFFERENT admin would both see
	// the other still active (their target-row FOR UPDATE lock doesn't cover the
	// other admin's row) and both commit, leaving zero admins. A transaction
	// advisory lock keyed on the workspace makes admin-management serial, so the
	// second transaction re-reads the first's committed change and refuses.
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext('margince:admin-guard:' || current_setting('app.workspace_id', true))::bigint)`); err != nil {
		return false, err
	}
	var targetIsAdmin bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM role_assignment ra JOIN role r ON r.id = ra.role_id
			WHERE ra.user_id = $1 AND r.key = 'admin')`, userID).Scan(&targetIsAdmin); err != nil {
		return false, err
	}
	if !targetIsAdmin {
		return false, nil
	}
	// NOT is_agent, because the question this count answers is "would anyone
	// still be able to administer users afterwards" — and the agent seat could
	// not, whatever its role assignments say. It carries no password by
	// construction, so it signs in nowhere and reaches no admin screen.
	//
	// Nothing should be able to give it the admin role (ChangeUserRole refuses an
	// agent target), so this is the second lock on the same door rather than the
	// only one. It earns its place because a grant through that door is not the
	// only way the row could come to hold one: an installation that hand-inserted
	// its agent seat while the product created none may have granted it anything,
	// and this count is what turns such a row into a lockout — the last human
	// administrator is deactivated on the strength of a "colleague" who can never
	// sign in, and there is no way back in.
	var otherAdmins int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM app_user u
		JOIN role_assignment ra ON ra.user_id = u.id
		JOIN role r ON r.id = ra.role_id
		WHERE r.key = 'admin' AND u.status = 'active' AND u.archived_at IS NULL
		  AND NOT u.is_agent
		  AND u.id <> $1`, userID).Scan(&otherAdmins); err != nil {
		return false, err
	}
	return otherAdmins == 0, nil
}
