// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// Suggestion dismissals: the rep saying "not this, not now".
//
// Per user, because advice one rep has judged is not advice their colleague
// has seen. Keyed on the suggestion's evidence fingerprint, so it stays gone
// while the situation holds and re-arms by itself when the evidence changes.
//
// suggestion_dismissal is view state, not a record fact: written on a click,
// readable by nobody but its own user, actionable by no consumer. It carries
// no audit row and no outbox event — the same ruling as user_record_view.

import (
	"context"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// DismissSuggestion records that this human does not want this advice.
func (s *Service) DismissSuggestion(ctx context.Context, orgID ids.OrganizationID, fingerprint string) error {
	// An agent has no opinion to record, and consuming a human's dismissal on
	// their behalf would silence advice they never saw.
	if err := auth.RequireHuman(ctx); err != nil {
		return err
	}
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return err
	}
	userID, err := actingUser(ctx)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Anything that names a record is gated: dismissing advice about an
		// account the caller cannot read would confirm it exists.
		if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
			return err
		}
		// The body is checked AFTER the record gate, the house order: a caller
		// who may not read this account must get the same 404 whatever they put
		// in the body, or the shape of their mistake becomes an answer about
		// whether the account exists.
		if !isFingerprint(fingerprint) {
			return httperr.Validation("fingerprint", "malformed",
				"dismiss a suggestion by the fingerprint it was served with, unchanged")
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO suggestion_dismissal
			  (workspace_id, user_id, organization_id, fingerprint, dismissed_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (workspace_id, user_id, organization_id, fingerprint)
			DO UPDATE SET dismissed_at = EXCLUDED.dismissed_at`,
			storekit.MustWorkspace(ctx), userID, orgID, fingerprint, now)
		if err != nil {
			return fmt.Errorf("record the suggestion dismissal: %w", err)
		}
		return s.pruneDismissals(ctx, tx, userID, orgID)
	})
}

// fingerprintPattern is the shape fingerprint() produces: a sha256 digest in
// lowercase hex.
//
// Checking it is what keeps this endpoint from being a write-anything store.
// The server cannot re-derive the fingerprint to verify it — the situation may
// legitimately have moved on between the render and the click, and refusing
// then would lose a dismissal the rep meant — so the shape is the check that
// stays true. It also fixes the row size, and rejects the NUL byte Postgres
// would otherwise turn into a 500 for what is a client mistake.
var fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func isFingerprint(value string) bool {
	return fingerprintPattern.MatchString(value)
}

// dismissalsPerAccount bounds how many dismissals one person keeps for one
// account. The rules raise at most a handful at a time, so this is far above
// any honest use; it exists because a well-formed fingerprint that matches no
// suggestion is still storable, and nothing else would ever collect it.
const dismissalsPerAccount = 50

// pruneDismissals drops this person's oldest dismissals for this account past
// the bound. The oldest is the right one to lose: a dismissal that far back has
// almost certainly outlived the evidence it was keyed on, so losing it shows
// the advice again rather than hiding anything.
func (s *Service) pruneDismissals(
	ctx context.Context, tx pgx.Tx, userID ids.UserID, orgID ids.OrganizationID,
) error {
	_, err := tx.Exec(ctx, `
		DELETE FROM suggestion_dismissal
		WHERE user_id = $1 AND organization_id = $2
		  AND id NOT IN (
		    SELECT id FROM suggestion_dismissal
		    WHERE user_id = $1 AND organization_id = $2
		    ORDER BY dismissed_at DESC, id DESC
		    LIMIT $3)`, userID, orgID, dismissalsPerAccount)
	if err != nil {
		return fmt.Errorf("bound the stored suggestion dismissals: %w", err)
	}
	return nil
}

// dismissedFingerprints reads this caller's own dismissals for one account.
// The user_id predicate is explicit in SQL: RLS binds the workspace, so
// without it one rep's judgment would silence their colleague's suggestions.
func (s *Service) dismissedFingerprints(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID,
) (map[string]bool, error) {
	userID, err := actingUser(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT fingerprint FROM suggestion_dismissal
		WHERE user_id = $1 AND organization_id = $2`, userID, orgID)
	if err != nil {
		return nil, fmt.Errorf("read the suggestion dismissals: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var fingerprint string
		if err := rows.Scan(&fingerprint); err != nil {
			return nil, err
		}
		out[fingerprint] = true
	}
	return out, rows.Err()
}
