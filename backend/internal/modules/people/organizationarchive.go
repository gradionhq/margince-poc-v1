// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Retiring an account, and everything hanging off it.
//
// Archiving an organization is not one row: the domains it claims, the
// relationship types it wears, the partner program it is enrolled in and the
// edges it sits on all have to retire with it, or a dead account keeps
// answering the lists those tables feed. That is a concept of its own, which
// is why it lives beside the CRUD rather than inside it.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// RefuseArchiveOrganization answers every refusal ArchiveOrganization would
// answer with, and writes nothing. Its sibling on person says why.
//
// The anchor refusal runs here TOO, and it is not an authority question. It
// belongs because it can never come out the other way while a human decides:
// `is_anchor` is stamped at bootstrap (migration 0083), no verb moves it, and
// 0193 adds a CHECK that keeps an anchor from being archived or merged at all.
// So an archive staged against the installation's own company is refused by
// the store every single time — leaving it out would spend a human's approval
// on the one target that can never succeed.
func (s *Store) RefuseArchiveOrganization(ctx context.Context, id ids.OrganizationID) error {
	if err := auth.Require(ctx, "organization", principal.ActionDelete); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureWritable(ctx, tx, "organization", id.UUID); err != nil {
			return err
		}
		return refuseIfAnchor(ctx, tx, id, "id",
			"it cannot be archived. Archive a different company, or edit this one on the company page")
	})
}

// ArchiveOrganization retires the account and its cascade in ONE transaction,
// then answers the archived record.
//
// ArchiveOrganization retires one company and everything that answers a list on
// its behalf, conditioned on ifVersion wherever the caller's authority named a
// version.
func (s *Store) ArchiveOrganization(ctx context.Context, id ids.OrganizationID, ifVersion *int64) (crmcontracts.Organization, error) {
	if err := auth.Require(ctx, "organization", principal.ActionDelete); err != nil {
		return crmcontracts.Organization{}, err
	}
	active, err := s.activeColumns(ctx, "organization")
	if err != nil {
		return crmcontracts.Organization{}, err
	}
	var out crmcontracts.Organization
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureWritable(ctx, tx, "organization", id.UUID); err != nil {
			return err
		}
		if err := refuseIfAnchor(ctx, tx, id, "id", "it cannot be archived. Archive a different company, or edit this one on the company page"); err != nil {
			return err
		}
		if _, err := readOrganization(ctx, tx, id, storekit.LiveOnly, active); err != nil {
			return err
		}

		now := time.Now().UTC()
		// The COMPANY row rides the guarded patch, so an archive a human
		// released against version 4 lands on version 4 or answers skew.
		p := storekit.NewPatch()
		p.Set("archived_at", nil, now)
		if err := p.ApplyGuarded(ctx, tx, "organization", id.UUID, ifVersion); err != nil {
			return fmt.Errorf("archive the account: %w", err)
		}
		// Everything that answers a list on the account's behalf retires with
		// it. Every statement here covers a row somebody would otherwise still
		// find: a live child under an archived parent keeps feeding the list
		// its own table serves, which is how an archived account goes on
		// appearing as a partner. They stay plain statements because each is a
		// cascade off the row above rather than a second decision, and that
		// row's guard serializes all of them.
		for _, stmt := range []string{
			`UPDATE organization_domain SET archived_at = $2 WHERE organization_id = $1 AND archived_at IS NULL`,
			// ADR-0079's partner invariant runs over LIVE type rows, so the
			// types retire with their parent.
			`UPDATE organization_relationship_type SET archived_at = $2 WHERE organization_id = $1 AND archived_at IS NULL`,
			// The partner PROGRAM row goes with the type that admits it. Left
			// live, the extension and its type row disagree: the account is no
			// longer a partner by relationship type while partner.go's own
			// live-row reads still answer for it.
			`UPDATE partner SET archived_at = $2 WHERE organization_id = $1 AND archived_at IS NULL`,
			`UPDATE relationship SET archived_at = $2 WHERE (organization_id = $1 OR counterparty_org_id = $1) AND archived_at IS NULL`,
		} {
			if _, err := tx.Exec(ctx, stmt, id, now); err != nil {
				return fmt.Errorf("retire what hangs off the account: %w", err)
			}
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM list_member WHERE entity_type = 'organization' AND entity_id = $1`, id); err != nil {
			return fmt.Errorf("drop the account's list memberships: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM taggable WHERE entity_type = 'organization' AND entity_id = $1`, id); err != nil {
			return fmt.Errorf("drop the account's tags: %w", err)
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

const orgColumns = `id, display_name, legal_name, description, industry, size_band, owner_id,
	address_line1, address_line2, address_city, address_region, address_postal_code, address_country,
	classification, lifecycle, relevance, parent_org_id, merged_into_id, logo_object_key, linkedin_url, source, captured_by,
	version, created_at, updated_at, archived_at, is_anchor, last_activity_at`

// readOrganization resolves one organization row; active names the
// custom-field columns to carry alongside the core ones — nil for
// internal decision reads whose result never reaches the wire.
