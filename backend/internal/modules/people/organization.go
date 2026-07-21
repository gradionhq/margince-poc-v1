// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

type CreateOrganizationInput struct {
	DisplayName string
	LegalName   *string
	Industry    *string
	SizeBand    *string
	OwnerID     *ids.UserID
	ParentOrgID *ids.OrganizationID
	Address     *crmcontracts.Address
	Domains     []OrgDomainInput
	Source      string
	// CustomFields carries the request body's extra top-level keys
	// (additionalProperties); only active cf_* catalog columns land,
	// drop-on-mismatch (customfields.go).
	CustomFields map[string]any
}

func (s *Store) CreateOrganization(ctx context.Context, in CreateOrganizationInput) (crmcontracts.Organization, error) {
	if err := auth.Require(ctx, "organization", principal.ActionCreate); err != nil {
		return crmcontracts.Organization{}, err
	}
	if err := parseOrgDomains(in.Domains); err != nil {
		return crmcontracts.Organization{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Organization{}, err
	}
	active, err := s.activeColumns(ctx, "organization")
	if err != nil {
		return crmcontracts.Organization{}, err
	}

	var out crmcontracts.Organization
	err = s.tx(ctx, func(tx pgx.Tx) error {
		wsID := workspaceID(ctx)

		if err := ensureOrgDomainsUnclaimed(ctx, tx, in.Domains); err != nil {
			return err
		}

		match, err := manualDedupeOrganization(ctx, tx, in)
		if err != nil {
			return err
		}

		// Naming a parent is a read of the parent: the child discloses the
		// hierarchy edge, so the target must be visible under the caller's
		// row scope, not merely same-workspace (H1 — an FK argument to a
		// row-scoped record is a read of that record).
		if in.ParentOrgID != nil {
			if err := auth.EnsureLinkTarget(ctx, tx, "organization", in.ParentOrgID.UUID); err != nil {
				return err
			}
		}

		id := ids.New[ids.OrganizationKind]()
		addr := addressColumns(in.Address)
		cfCols, cfHolders, cfArgs := storekit.InsertFragments(active, in.CustomFields, 17)
		args := []any{
			id, wsID, in.DisplayName, in.LegalName, in.Industry, in.SizeBand, in.OwnerID, in.ParentOrgID,
			addr.Line1, addr.Line2, addr.City, addr.Region, addr.PostalCode, addr.Country,
			in.Source, by,
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO organization (id, workspace_id, display_name, legal_name, industry, size_band, owner_id, parent_org_id,
			                           address_line1, address_line2, address_city, address_region, address_postal_code, address_country,
			                           source, captured_by`+cfCols+`)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16`+cfHolders+`)`,
			append(args, cfArgs...)...)
		if err != nil {
			return fmt.Errorf("insert organization: %w", err)
		}

		if err := insertOrgDomains(ctx, tx, wsID, id, in.Source, by, in.Domains); err != nil {
			return err
		}

		auditID, err := storekit.Audit(ctx, tx, "create", "organization", id.UUID, nil, map[string]any{"display_name": in.DisplayName})
		if err != nil {
			return fmt.Errorf("audit organization create: %w", err)
		}
		if err := storekit.Emit(ctx, tx, auditID, "organization.created", "organization", id.UUID, map[string]any{"display_name": in.DisplayName}); err != nil {
			return fmt.Errorf("emit organization.created: %w", err)
		}
		if err := match.recordIfReview(ctx, tx, id, in.DisplayName, in.Source, by); err != nil {
			return err
		}
		if out, err = readOrganization(ctx, tx, id, storekit.LiveOnly, active); err != nil {
			return fmt.Errorf("read created organization: %w", err)
		}
		return nil
	})
	return out, err
}

func (s *Store) GetOrganization(ctx context.Context, id ids.OrganizationID, archived storekit.ArchivedFilter) (crmcontracts.Organization, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return crmcontracts.Organization{}, err
	}
	active, err := s.activeColumns(ctx, "organization")
	if err != nil {
		return crmcontracts.Organization{}, err
	}
	var out crmcontracts.Organization
	err = s.tx(ctx, func(tx pgx.Tx) (err error) {
		if err := auth.EnsureVisible(ctx, tx, "organization", id.UUID); err != nil {
			return err
		}
		if out, err = readOrganization(ctx, tx, id, archived, active); err != nil {
			return err
		}
		// STATE-4: the gate is a pure permission check (no query), so a
		// caller whose role lacks computed_field:read never pays for the
		// rollup read below, and out.ComputedFields stays its nil zero
		// value — omitempty then drops the key entirely on marshal (T1).
		if computedFieldsVisible(ctx) {
			minor, dealCount, err := openPipelineRollup(ctx, tx, id)
			if err != nil {
				return fmt.Errorf("read open pipeline rollup: %w", err)
			}
			rows := organizationComputedFields(minor, dealCount)
			out.ComputedFields = &rows
		}
		return nil
	})
	return out, err
}

type UpdateOrganizationInput struct {
	DisplayName *string
	LegalName   *string
	Industry    *string
	SizeBand    *string
	OwnerID     *ids.UserID
	ParentOrgID *ids.OrganizationID
	Address     *crmcontracts.Address
	IfVersion   *int64
	// Domains, when non-nil, is the desired live domain set (replace-set:
	// add missing, archive removed, flip is_primary). nil leaves domains
	// untouched; an empty slice clears them.
	Domains *[]OrgDomainInput
	// CustomFields carries the request body's extra top-level keys
	// (additionalProperties); only active cf_* catalog columns land,
	// drop-on-mismatch (customfields.go).
	CustomFields map[string]any
}

func (s *Store) UpdateOrganization(ctx context.Context, id ids.OrganizationID, in UpdateOrganizationInput) (crmcontracts.Organization, error) {
	if err := auth.Require(ctx, "organization", principal.ActionUpdate); err != nil {
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
		current, err := readOrganization(ctx, tx, id, storekit.LiveOnly, active)
		if err != nil {
			return fmt.Errorf("read organization before update: %w", err)
		}
		p, err := buildOrganizationPatch(ctx, tx, current, in)
		if err != nil {
			return err
		}
		storekit.SetCustomFieldPatch(p, active, in.CustomFields, current.AdditionalProperties)

		// Validate the domain replace-set up front (bad request fails before
		// the version bump). The reconcile itself rides the org row's version
		// bump (updated_at below), so If-Match still guards it and the audit
		// row records the transition — the same shape as UpdatePerson/social.
		var by string
		if in.Domains != nil {
			if by, err = storekit.CapturedBy(ctx); err != nil {
				return err
			}
			if err := parseOrgDomains(*in.Domains); err != nil {
				return err
			}
			if err := ensureOrgDomainsUnclaimedExcept(ctx, tx, id, *in.Domains); err != nil {
				return err
			}
			p.Set("updated_at", current.UpdatedAt, time.Now().UTC())
		}
		if p.Empty() {
			out = current
			return nil
		}
		if err := p.ApplyGuarded(ctx, tx, "organization", id.UUID, in.IfVersion); err != nil {
			return fmt.Errorf("apply organization patch: %w", err)
		}

		before, after := p.Before(), p.After()
		if in.Domains != nil {
			domainsBefore, err := reconcileOrgDomains(ctx, tx, workspaceID(ctx), id, by, *in.Domains)
			if err != nil {
				return err
			}
			before["domains"] = domainsBefore
			after["domains"] = domainSummaries(*in.Domains)
		}

		auditID, err := storekit.Audit(ctx, tx, "update", "organization", id.UUID, before, after)
		if err != nil {
			return fmt.Errorf("audit organization update: %w", err)
		}
		if err := storekit.Emit(ctx, tx, auditID, "organization.updated", "organization", id.UUID, after); err != nil {
			return fmt.Errorf("emit organization.updated: %w", err)
		}
		if out, err = readOrganization(ctx, tx, id, storekit.LiveOnly, active); err != nil {
			return fmt.Errorf("read updated organization: %w", err)
		}
		return nil
	})
	return out, err
}

// buildOrganizationPatch folds the caller's sparse org edit into a patch.
// Naming a new parent is a read of that parent (the create-path rule), so
// it is visibility-probed before the edge lands.
func buildOrganizationPatch(ctx context.Context, tx pgx.Tx, current crmcontracts.Organization, in UpdateOrganizationInput) (*storekit.Patch, error) {
	p := storekit.NewPatch()
	if in.DisplayName != nil {
		p.Set("display_name", current.DisplayName, *in.DisplayName)
	}
	if in.LegalName != nil {
		p.Set("legal_name", current.LegalName, *in.LegalName)
	}
	if in.Industry != nil {
		p.Set("industry", current.Industry, *in.Industry)
	}
	if in.SizeBand != nil {
		p.Set("size_band", current.SizeBand, *in.SizeBand)
	}
	if in.OwnerID != nil {
		p.Set(ownerIDColumn, current.OwnerId, *in.OwnerID)
	}
	if in.ParentOrgID != nil {
		if err := auth.EnsureLinkTarget(ctx, tx, "organization", in.ParentOrgID.UUID); err != nil {
			return nil, err
		}
		p.Set("parent_org_id", current.ParentOrgId, *in.ParentOrgID)
	}
	if in.Address != nil {
		cur := addressColumns(current.Address)
		p.Set("address_line1", cur.Line1, in.Address.Line1)
		p.Set("address_line2", cur.Line2, in.Address.Line2)
		p.Set("address_city", cur.City, in.Address.City)
		p.Set("address_region", cur.Region, in.Address.Region)
		p.Set("address_postal_code", cur.PostalCode, in.Address.PostalCode)
		p.Set("address_country", cur.Country, in.Address.Country)
	}
	return p, nil
}

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
		if err := storekit.Emit(ctx, tx, auditID, "organization.archived", "organization", id.UUID, nil); err != nil {
			return err
		}
		out, err = readOrganization(ctx, tx, id, storekit.IncludeArchived, active)
		return err
	})
	return out, err
}

const orgColumns = `id, workspace_id, display_name, legal_name, industry, size_band, owner_id,
	address_line1, address_line2, address_city, address_region, address_postal_code, address_country,
	classification, relevance, parent_org_id, merged_into_id, source, captured_by,
	version, created_at, updated_at, archived_at`

// readOrganization resolves one organization row; active names the
// custom-field columns to carry alongside the core ones — nil for
// internal decision reads whose result never reaches the wire.
func readOrganization(ctx context.Context, tx pgx.Tx, id ids.OrganizationID, archived storekit.ArchivedFilter, active []fieldcatalog.Column) (crmcontracts.Organization, error) {
	q := `SELECT ` + orgColumns + storekit.SelectSuffix(active) + ` FROM organization WHERE id = $1`
	if archived == storekit.LiveOnly {
		q += ` AND archived_at IS NULL`
	}
	o, err := scanOrganization(tx.QueryRow(ctx, q, id), active)
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Organization{}, apperrors.ErrNotFound
	}
	if err != nil {
		return crmcontracts.Organization{}, err
	}
	orgs := []crmcontracts.Organization{o}
	if err := attachOrgDomains(ctx, tx, orgs); err != nil {
		return crmcontracts.Organization{}, err
	}
	return orgs[0], nil
}

// scanOrganization scans core + active custom columns; extra receives
// any trailing expressions the caller's SELECT appended (the sorted
// list's cursor key).
func scanOrganization(row pgx.Row, active []fieldcatalog.Column, extra ...any) (crmcontracts.Organization, error) {
	var o crmcontracts.Organization
	var id, wsID ids.UUID
	var ownerID, parentID, mergedInto *ids.UUID
	var classification string
	var relevance *int16
	var addr crmcontracts.Address
	var version int64

	dests := []any{
		&id, &wsID, &o.DisplayName, &o.LegalName, &o.Industry, &o.SizeBand, &ownerID,
		&addr.Line1, &addr.Line2, &addr.City, &addr.Region, &addr.PostalCode, &addr.Country,
		&classification, &relevance, &parentID, &mergedInto, &o.Source, &o.CapturedBy,
		&version, &o.CreatedAt, &o.UpdatedAt, &o.ArchivedAt,
	}
	cf := storekit.ScanDests(active)
	if err := row.Scan(append(append(dests, cf...), extra...)...); err != nil {
		return o, err
	}
	if values := storekit.ExtractValues(active, cf); len(values) > 0 {
		o.AdditionalProperties = values
	}

	o.Id = openapi_types.UUID(id)
	o.WorkspaceId = openapi_types.UUID(wsID)
	o.OwnerId = uuidPtr(ownerID)
	o.ParentOrgId = uuidPtr(parentID)
	o.MergedIntoId = uuidPtr(mergedInto)
	cls := crmcontracts.OrganizationClassification(classification)
	o.Classification = &cls
	if a := addressOrNil(addr); a != nil {
		o.Address = a
	}
	o.Version = &version
	return o, nil
}
