// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
		_, err := tx.Exec(ctx,
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
			minor, _, err := openPipelineRollup(ctx, tx, id)
			if err != nil {
				return fmt.Errorf("read open pipeline rollup: %w", err)
			}
			rows := organizationComputedFields(minor)
			out.ComputedFields = &rows
		}
		return nil
	})
	return out, err
}

type ListOrganizationsInput struct {
	Cursor          *string
	Limit           *int
	Query           *string
	OwnerID         *ids.UserID
	Classification  *string
	IncludeArchived bool
	// Sort is the contract's sort spec, validated against the core
	// vocabulary below plus the workspace's active cf_ columns.
	Sort *string
	// CustomFilters carries the request's cf_* query parameters —
	// equality matches against active custom columns (storekit listquery).
	CustomFilters map[string]string
}

// organizationListFields is the organization list's core sortable
// vocabulary — exactly the data-model §13.5 DM-VOCAB-2 set; active cf_
// columns join it per request.
var organizationListFields = map[string]string{
	"created_at":   storekit.KindTimestamp,
	"updated_at":   storekit.KindTimestamp,
	"display_name": fieldcatalog.TypeText,
	ownerIDColumn:  storekit.KindUUID,
}

func (s *Store) ListOrganizations(ctx context.Context, in ListOrganizationsInput) ([]crmcontracts.Organization, storekit.Page, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	active, err := s.activeColumns(ctx, "organization")
	if err != nil {
		return nil, storekit.Page{}, err
	}
	sorted, err := storekit.ParseListSort(in.Sort, storekit.SortVocabulary(organizationListFields, active))
	if err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(in.Limit)

	where := []string{"1=1"}
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }

	scope, err := auth.ScopeClauseFor(ctx, "organization", "", arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	if scope != "" {
		where = append(where, scope)
	}

	if !in.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if in.OwnerID != nil {
		where = append(where, storekit.SQLf("owner_id = $%d", arg(*in.OwnerID)))
	}
	if in.Classification != nil {
		where = append(where, storekit.SQLf("classification = $%d", arg(*in.Classification)))
	}
	if in.Query != nil && *in.Query != "" {
		where = append(where, storekit.QuickFindClause(arg(*in.Query), "display_name"))
	}
	cfClauses, err := storekit.CustomFilterClauses(active, in.CustomFilters, arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	where = append(where, cfClauses...)
	if in.Cursor != nil && *in.Cursor != "" {
		clause, err := sorted.KeysetClause(*in.Cursor, arg)
		if err != nil {
			return nil, storekit.Page{}, err
		}
		where = append(where, clause)
	}

	var orgs []crmcontracts.Organization
	var page storekit.Page
	err = s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+orgColumns+storekit.SelectSuffix(active)+sorted.CursorKeySuffix()+
				` FROM organization WHERE `+strings.Join(where, " AND ")+
				sorted.OrderBy()+storekit.SQLf(` LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		var cursorKeys []*string
		if orgs, cursorKeys, err = scanOrganizationPage(rows, active, sorted); err != nil {
			return err
		}
		if len(orgs) > limit {
			orgs = orgs[:limit]
			last := orgs[len(orgs)-1]
			page = storekit.Page{HasMore: true, NextCursor: sorted.EncodePageCursor(cursorKeys[limit-1], last.CreatedAt, ids.UUID(last.Id))}
		}
		return attachOrgDomains(ctx, tx, orgs)
	})
	if orgs == nil {
		orgs = []crmcontracts.Organization{}
	}
	return orgs, page, err
}

// scanOrganizationPage drains one list query's rows: each organization
// plus, under a non-default sort, the row's cursor key (the trailing
// __cursor_key column CursorKeySuffix appended).
func scanOrganizationPage(rows pgx.Rows, active []fieldcatalog.Column, sorted *storekit.ListSort) ([]crmcontracts.Organization, []*string, error) {
	var orgs []crmcontracts.Organization
	var cursorKeys []*string
	for rows.Next() {
		var key *string
		extra := []any{}
		if sorted != nil {
			extra = append(extra, &key)
		}
		o, err := scanOrganization(rows, active, extra...)
		if err != nil {
			return nil, nil, err
		}
		orgs = append(orgs, o)
		cursorKeys = append(cursorKeys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return orgs, cursorKeys, nil
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
		if p.Empty() {
			out = current
			return nil
		}
		out, err = writeOrganizationUpdate(ctx, tx, id, in.IfVersion, p, active)
		return err
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

// writeOrganizationUpdate lands the patch on the write shape — domain row,
// audit row, and organization.updated event in the one transaction — and
// returns the reloaded survivor.
func writeOrganizationUpdate(ctx context.Context, tx pgx.Tx, id ids.OrganizationID, ifVersion *int64, p *storekit.Patch, active []fieldcatalog.Column) (crmcontracts.Organization, error) {
	if err := p.ApplyGuarded(ctx, tx, "organization", id.UUID, ifVersion); err != nil {
		return crmcontracts.Organization{}, fmt.Errorf("apply organization patch: %w", err)
	}
	auditID, err := storekit.Audit(ctx, tx, "update", "organization", id.UUID, p.Before(), p.After())
	if err != nil {
		return crmcontracts.Organization{}, fmt.Errorf("audit organization update: %w", err)
	}
	if err := storekit.Emit(ctx, tx, auditID, "organization.updated", "organization", id.UUID, p.After()); err != nil {
		return crmcontracts.Organization{}, fmt.Errorf("emit organization.updated: %w", err)
	}
	out, err := readOrganization(ctx, tx, id, storekit.LiveOnly, active)
	if err != nil {
		return crmcontracts.Organization{}, fmt.Errorf("read updated organization: %w", err)
	}
	return out, nil
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
