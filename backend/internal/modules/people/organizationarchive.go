// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Retiring an account, and everything hanging off it.
//
// Archiving an organization is not one row: the domains it claims, the
// relationship types it wears and the edges it sits on all have to retire with
// it, or a dead account keeps answering the lists those tables feed. That is a
// concept of its own, which is why it lives beside the CRUD rather than inside
// it.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ArchiveOrganization retires the account and everything hanging off it in one
// transaction: the domains it claims, the relationship types it wears, and the
// edges it sits on. A live child row under an archived parent keeps answering
// the list its table feeds, which is how an archived account goes on appearing
// as a partner.
func (s *Store) ArchiveOrganization(ctx context.Context, id ids.OrganizationID) (crmcontracts.Organization, error) {
	if err := auth.Require(ctx, "organization", principal.ActionDelete); err != nil {
		return crmcontracts.Organization{}, err
	}
	active, err := s.activeColumns(ctx, "organization")
	if err != nil {
		return crmcontracts.Organization{}, err
	}
	var out crmcontracts.Organization
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "organization", id.UUID); err != nil {
			return err
		}
		if _, err := readOrganization(ctx, tx, id, storekit.LiveOnly, active); err != nil {
			return err
		}

		now := time.Now().UTC()
		for _, stmt := range []string{
			`UPDATE organization SET archived_at = $2 WHERE id = $1 AND archived_at IS NULL`,
			`UPDATE organization_domain SET archived_at = $2 WHERE organization_id = $1 AND archived_at IS NULL`,
			// An archived account that still carried a live 'partner' type row
			// would keep answering the partner list (ADR-0079's invariant runs
			// over LIVE rows), so the types retire with their parent.
			`UPDATE organization_relationship_type SET archived_at = $2 WHERE organization_id = $1 AND archived_at IS NULL`,
			`UPDATE relationship SET archived_at = $2 WHERE (organization_id = $1 OR counterparty_org_id = $1) AND archived_at IS NULL`,
		} {
			if _, err := tx.Exec(ctx, stmt, id, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM list_member WHERE entity_type = 'organization' AND entity_id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM taggable WHERE entity_type = 'organization' AND entity_id = $1`, id); err != nil {
			return err
		}

		auditID, err := storekit.Audit(ctx, tx, "archive", "organization", id.UUID, nil, nil)
		if err != nil {
			return err
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventOrganizationArchived{}); err != nil {
			return err
		}
		out, err = readOrganization(ctx, tx, id, storekit.IncludeArchived, active)
		return err
	})
	return out, err
}

const orgColumns = `id, workspace_id, display_name, legal_name, industry, size_band, owner_id,
	address_line1, address_line2, address_city, address_region, address_postal_code, address_country,
	classification, lifecycle, relevance, parent_org_id, merged_into_id, logo_object_key, source, captured_by,
	version, created_at, updated_at, archived_at`

// readOrganization resolves one organization row; active names the
// custom-field columns to carry alongside the core ones — nil for
// internal decision reads whose result never reaches the wire.
