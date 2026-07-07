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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// DuplicateDomainError carries the org already owning a domain: a domain
// maps to at most one org per workspace (data-model §4.2).
type DuplicateDomainError struct {
	Domain     string
	ExistingID ids.UUID
}

func (e *DuplicateDomainError) Error() string {
	return "domain " + e.Domain + " already belongs to an organization"
}
func (e *DuplicateDomainError) Is(target error) bool { return target == apperrors.ErrConflict }

type OrgDomainInput struct {
	Domain    string
	IsPrimary bool
}

type CreateOrganizationInput struct {
	DisplayName string
	LegalName   *string
	Industry    *string
	SizeBand    *string
	OwnerID     *ids.UUID
	ParentOrgID *ids.UUID
	Address     *crmcontracts.Address
	Domains     []OrgDomainInput
	Source      string
}

// parseOrgDomains is the parse-don't-validate seam for an org's domain
// rows: URL forms, www. prefixes, ports and case all reduce to the one
// normalized host the dedupe index compares (the SQL lower() stays as
// defense in depth). Values are written back in place.
func parseOrgDomains(domains []OrgDomainInput) error {
	for i, d := range domains {
		parsed, err := values.ParseDomain(d.Domain)
		if err != nil {
			return err
		}
		domains[i].Domain = parsed.String()
	}
	return nil
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

	var out crmcontracts.Organization
	err = s.tx(ctx, func(tx pgx.Tx) error {
		wsID := storekit.MustWorkspace(ctx)

		if err := ensureOrgDomainsUnclaimed(ctx, tx, in.Domains); err != nil {
			return err
		}

		// Naming a parent is a read of the parent: the child discloses the
		// hierarchy edge, so the target must be visible under the caller's
		// row scope, not merely same-workspace (H1 — an FK argument to a
		// row-scoped record is a read of that record).
		if in.ParentOrgID != nil {
			if err := auth.EnsureLinkTarget(ctx, tx, "organization", *in.ParentOrgID); err != nil {
				return err
			}
		}

		id := ids.NewV7()
		addr := addressColumns(in.Address)
		_, err := tx.Exec(ctx,
			`INSERT INTO organization (id, workspace_id, display_name, legal_name, industry, size_band, owner_id, parent_org_id,
			                           address_line1, address_line2, address_city, address_region, address_postal_code, address_country,
			                           source, captured_by)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
			id, wsID, in.DisplayName, in.LegalName, in.Industry, in.SizeBand, in.OwnerID, in.ParentOrgID,
			addr.Line1, addr.Line2, addr.City, addr.Region, addr.PostalCode, addr.Country,
			in.Source, by)
		if err != nil {
			return fmt.Errorf("insert organization: %w", err)
		}

		if err := insertOrgDomains(ctx, tx, wsID, id, in.Source, by, in.Domains); err != nil {
			return err
		}

		auditID, err := storekit.Audit(ctx, tx, "create", "organization", id, nil, map[string]any{"display_name": in.DisplayName})
		if err != nil {
			return fmt.Errorf("audit organization create: %w", err)
		}
		if err := storekit.Emit(ctx, tx, auditID, "organization.created", "organization", id, map[string]any{"display_name": in.DisplayName}); err != nil {
			return fmt.Errorf("emit organization.created: %w", err)
		}
		if out, err = readOrganization(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read created organization: %w", err)
		}
		return nil
	})
	return out, err
}

// ensureOrgDomainsUnclaimed answers the domain dedupe probe with the
// contract's 409, disclosing the existing org id only when the caller
// could read that row (a domain maps to at most one org per workspace,
// data-model §4.2).
func ensureOrgDomainsUnclaimed(ctx context.Context, tx pgx.Tx, domains []OrgDomainInput) error {
	for _, d := range domains {
		var existing ids.UUID
		err := tx.QueryRow(ctx,
			`SELECT organization_id FROM organization_domain WHERE domain = lower($1) AND archived_at IS NULL`,
			d.Domain).Scan(&existing)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("probe domain dedupe: %w", err)
		}
		dup := &DuplicateDomainError{Domain: d.Domain}
		visible, verr := auth.VisibleTo(ctx, tx, "organization", existing)
		if verr != nil {
			return verr
		}
		if visible {
			dup.ExistingID = existing
		}
		return dup
	}
	return nil
}

// insertOrgDomains lands the org's domains; the unique index remains the
// structural guarantee under races, mapping uq_org_domain to the typed
// 409 and a second primary domain to a plain conflict.
func insertOrgDomains(ctx context.Context, tx pgx.Tx, wsID, orgID ids.UUID, source, by string, domains []OrgDomainInput) error {
	for _, d := range domains {
		if _, err := tx.Exec(ctx,
			`INSERT INTO organization_domain (workspace_id, organization_id, domain, is_primary, source, captured_by)
			 VALUES ($1, $2, lower($3), $4, $5, $6)`,
			wsID, orgID, d.Domain, d.IsPrimary, source, by); err != nil {
			if name, ok := storekit.UniqueViolation(err); ok {
				if name == "uq_org_domain" {
					return &DuplicateDomainError{Domain: d.Domain}
				}
				return apperrors.ErrConflict // e.g. a second primary domain
			}
			return fmt.Errorf("insert organization domain: %w", err)
		}
	}
	return nil
}

func (s *Store) GetOrganization(ctx context.Context, id ids.UUID, archived storekit.ArchivedFilter) (crmcontracts.Organization, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return crmcontracts.Organization{}, err
	}
	var out crmcontracts.Organization
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		if err := auth.EnsureVisible(ctx, tx, "organization", id); err != nil {
			return err
		}
		out, err = readOrganization(ctx, tx, id, archived)
		return err
	})
	return out, err
}

type ListOrganizationsInput struct {
	Cursor          *string
	Limit           *int
	Query           *string
	OwnerID         *ids.UUID
	Classification  *string
	IncludeArchived bool
}

func (s *Store) ListOrganizations(ctx context.Context, in ListOrganizationsInput) ([]crmcontracts.Organization, storekit.Page, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
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
		where = append(where, storekit.SQLf("search_tsv @@ plainto_tsquery('simple', $%d)", arg(*in.Query)))
	}
	if in.Cursor != nil && *in.Cursor != "" {
		c, err := storekit.DecodeCursor(*in.Cursor)
		if err != nil {
			return nil, storekit.Page{}, err
		}
		where = append(where, storekit.SQLf("(created_at, id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID)))
	}

	var orgs []crmcontracts.Organization
	var page storekit.Page
	err = s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+orgColumns+` FROM organization WHERE `+strings.Join(where, " AND ")+
				storekit.SQLf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scanOrganization(rows)
			if err != nil {
				return err
			}
			orgs = append(orgs, o)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(orgs) > limit {
			orgs = orgs[:limit]
			last := orgs[len(orgs)-1]
			page = storekit.Page{HasMore: true, NextCursor: storekit.EncodeCursor(last.CreatedAt, ids.UUID(last.Id))}
		}
		return attachOrgDomains(ctx, tx, orgs)
	})
	if orgs == nil {
		orgs = []crmcontracts.Organization{}
	}
	return orgs, page, err
}

type UpdateOrganizationInput struct {
	DisplayName *string
	LegalName   *string
	Industry    *string
	SizeBand    *string
	OwnerID     *ids.UUID
	ParentOrgID *ids.UUID
	Address     *crmcontracts.Address
	IfVersion   *int64
}

func (s *Store) UpdateOrganization(ctx context.Context, id ids.UUID, in UpdateOrganizationInput) (crmcontracts.Organization, error) {
	if err := auth.Require(ctx, "organization", principal.ActionUpdate); err != nil {
		return crmcontracts.Organization{}, err
	}
	var out crmcontracts.Organization
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "organization", id); err != nil {
			return err
		}
		current, err := readOrganization(ctx, tx, id, storekit.LiveOnly)
		if err != nil {
			return fmt.Errorf("read organization before update: %w", err)
		}
		p, err := buildOrganizationPatch(ctx, tx, current, in)
		if err != nil {
			return err
		}
		if p.Empty() {
			out = current
			return nil
		}
		out, err = writeOrganizationUpdate(ctx, tx, id, in.IfVersion, p)
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
		p.Set("owner_id", current.OwnerId, *in.OwnerID)
	}
	if in.ParentOrgID != nil {
		if err := auth.EnsureLinkTarget(ctx, tx, "organization", *in.ParentOrgID); err != nil {
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
func writeOrganizationUpdate(ctx context.Context, tx pgx.Tx, id ids.UUID, ifVersion *int64, p *storekit.Patch) (crmcontracts.Organization, error) {
	if err := p.ApplyGuarded(ctx, tx, "organization", id, ifVersion); err != nil {
		return crmcontracts.Organization{}, fmt.Errorf("apply organization patch: %w", err)
	}
	auditID, err := storekit.Audit(ctx, tx, "update", "organization", id, p.Before(), p.After())
	if err != nil {
		return crmcontracts.Organization{}, fmt.Errorf("audit organization update: %w", err)
	}
	if err := storekit.Emit(ctx, tx, auditID, "organization.updated", "organization", id, p.After()); err != nil {
		return crmcontracts.Organization{}, fmt.Errorf("emit organization.updated: %w", err)
	}
	out, err := readOrganization(ctx, tx, id, storekit.LiveOnly)
	if err != nil {
		return crmcontracts.Organization{}, fmt.Errorf("read updated organization: %w", err)
	}
	return out, nil
}

func (s *Store) ArchiveOrganization(ctx context.Context, id ids.UUID) (crmcontracts.Organization, error) {
	if err := auth.Require(ctx, "organization", principal.ActionDelete); err != nil {
		return crmcontracts.Organization{}, err
	}
	var out crmcontracts.Organization
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "organization", id); err != nil {
			return err
		}
		if _, err := readOrganization(ctx, tx, id, storekit.LiveOnly); err != nil {
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

		auditID, err := storekit.Audit(ctx, tx, "archive", "organization", id, nil, nil)
		if err != nil {
			return err
		}
		if err := storekit.Emit(ctx, tx, auditID, "organization.archived", "organization", id, nil); err != nil {
			return err
		}
		out, err = readOrganization(ctx, tx, id, storekit.IncludeArchived)
		return err
	})
	return out, err
}

const orgColumns = `id, workspace_id, display_name, legal_name, industry, size_band, owner_id,
	address_line1, address_line2, address_city, address_region, address_postal_code, address_country,
	classification, relevance, parent_org_id, merged_into_id, source, captured_by,
	version, created_at, updated_at, archived_at`

func readOrganization(ctx context.Context, tx pgx.Tx, id ids.UUID, archived storekit.ArchivedFilter) (crmcontracts.Organization, error) {
	q := `SELECT ` + orgColumns + ` FROM organization WHERE id = $1`
	if archived == storekit.LiveOnly {
		q += ` AND archived_at IS NULL`
	}
	o, err := scanOrganization(tx.QueryRow(ctx, q, id))
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

func scanOrganization(row pgx.Row) (crmcontracts.Organization, error) {
	var o crmcontracts.Organization
	var id, wsID ids.UUID
	var ownerID, parentID, mergedInto *ids.UUID
	var classification string
	var relevance *int16
	var addr crmcontracts.Address
	var version int64

	err := row.Scan(&id, &wsID, &o.DisplayName, &o.LegalName, &o.Industry, &o.SizeBand, &ownerID,
		&addr.Line1, &addr.Line2, &addr.City, &addr.Region, &addr.PostalCode, &addr.Country,
		&classification, &relevance, &parentID, &mergedInto, &o.Source, &o.CapturedBy,
		&version, &o.CreatedAt, &o.UpdatedAt, &o.ArchivedAt)
	if err != nil {
		return o, err
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

func attachOrgDomains(ctx context.Context, tx pgx.Tx, orgs []crmcontracts.Organization) error {
	if len(orgs) == 0 {
		return nil
	}
	idx := make(map[openapi_types.UUID]*crmcontracts.Organization, len(orgs))
	orgIDs := make([]ids.UUID, len(orgs))
	for i := range orgs {
		idx[orgs[i].Id] = &orgs[i]
		orgIDs[i] = ids.UUID(orgs[i].Id)
	}

	rows, err := tx.Query(ctx,
		`SELECT organization_id, id, domain, is_primary, source, captured_by
		 FROM organization_domain WHERE organization_id = ANY($1) AND archived_at IS NULL
		 ORDER BY is_primary DESC, created_at`, orgIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var orgID, domainID ids.UUID
		var d crmcontracts.OrganizationDomain
		if err := rows.Scan(&orgID, &domainID, &d.Domain, &d.IsPrimary, &d.Source, &d.CapturedBy); err != nil {
			return err
		}
		d.Id = openapi_types.UUID(domainID)
		o := idx[openapi_types.UUID(orgID)]
		if o.Domains == nil {
			o.Domains = &[]crmcontracts.OrganizationDomain{}
		}
		*o.Domains = append(*o.Domains, d)
	}
	return rows.Err()
}
