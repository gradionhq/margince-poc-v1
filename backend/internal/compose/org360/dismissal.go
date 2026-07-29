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
	"strings"

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
	if strings.TrimSpace(fingerprint) == "" {
		return httperr.Validation("fingerprint", "required",
			"name the suggestion to dismiss by the fingerprint it was served with")
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
		return nil
	})
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
