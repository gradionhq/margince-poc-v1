// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// OnboardingCompanyState resolves the anchor state inside the caller's
// transaction. The workspace lock serializes the no-anchor to anchor transition
// with SaveCompany, making creator/member routing one atomic decision.
func (s *Store) OnboardingCompanyState(ctx context.Context, tx pgx.Tx) (bool, bool, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return false, false, err
	}
	if err := lockCompanyState(ctx, tx); err != nil {
		return false, false, err
	}
	orgID, err := anchorOrganization(ctx, tx, false)
	if errors.Is(err, apperrors.ErrNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
		return false, false, err
	}
	company, err := readCompany(ctx, tx, orgID)
	if err != nil {
		return false, false, err
	}
	return true, company.MinimumComplete, nil
}

func lockCompanyState(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT id FROM workspace WHERE id = $1 FOR UPDATE`, workspaceID(ctx)); err != nil {
		return fmt.Errorf("lock company state: %w", err)
	}
	return nil
}
