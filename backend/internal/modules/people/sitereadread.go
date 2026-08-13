// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Reading a dossier back: one read by id, the newest read on an account, and
// the unbound onboarding read.
//
// All three answer the same question — what did a crawl find — and all three
// hide a record the caller may not see behind the same ErrNotFound the id that
// names nothing produces. They live apart from the write half because the
// writes are a state machine (create-or-join, claim, defer, finish) while these
// are plain reads with a scope rule.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// GetSiteRead reads one dossier, scoped to the organization the caller
// named: a read id that exists under another org — or an org the caller
// cannot see — is ErrNotFound (existence-hiding).
func (s *Store) GetSiteRead(ctx context.Context, orgID ids.OrganizationID, readID ids.UUID) (SiteRead, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return SiteRead{}, err
	}
	var out SiteRead
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `
			SELECT `+siteReadColumns+` FROM site_read
			WHERE id = $1 AND organization_id = $2`, readID, orgID)
		var err error
		out, err = scanSiteRead(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get site read: %w", err)
		}
		return nil
	})
	if err != nil {
		return SiteRead{}, err
	}
	return out, nil
}

// LatestSiteRead reads the most recent deep read on an account, or reports
// ErrNotFound when the account has never been read.
//
// The company page needs this because a read id lives only in the browser tab
// that started the crawl. A read that failed after the rep navigated away was
// therefore invisible: the page showed an account with no industry, no
// description and no facts, which looks exactly like an account nobody has
// tried to enrich — and a draft written from it invented what it could not
// find. Newest by created_at, so a retry supersedes the attempt before it.
func (s *Store) LatestSiteRead(ctx context.Context, orgID ids.OrganizationID) (SiteRead, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return SiteRead{}, err
	}
	var out SiteRead
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `
			SELECT `+siteReadColumns+` FROM site_read
			WHERE organization_id = $1
			ORDER BY created_at DESC, id DESC
			LIMIT 1`, orgID)
		var err error
		out, err = scanSiteRead(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get latest site read: %w", err)
		}
		return nil
	})
	if err != nil {
		return SiteRead{}, err
	}
	return out, nil
}

// GetOnboardingSiteRead reads an unbound dossier without requiring an anchor
// row to exist. Workspace RLS and the normal organization read/create authority
// still gate the operational draft.
func (s *Store) GetOnboardingSiteRead(ctx context.Context, readID ids.UUID) (SiteRead, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		if createErr := auth.Require(ctx, "organization", principal.ActionCreate); createErr != nil {
			return SiteRead{}, createErr
		}
	}
	var out SiteRead
	err := s.tx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT `+siteReadColumns+` FROM site_read
			WHERE id = $1 AND target_kind = 'onboarding'`, readID)
		var err error
		out, err = scanSiteRead(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get onboarding site read: %w", err)
		}
		return nil
	})
	return out, err
}
