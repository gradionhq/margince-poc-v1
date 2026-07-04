package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/crm-contracts"
	"github.com/gradionhq/margince/backend/crmctx"
	"github.com/gradionhq/margince/backend/kernel/errs"
	"github.com/gradionhq/margince/backend/kernel/ids"
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
func (e *DuplicateDomainError) Is(target error) bool { return target == errs.ErrConflict }

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
	Domains     []OrgDomainInput
	Source      string
}

func (s *Store) CreateOrganization(ctx context.Context, in CreateOrganizationInput) (crmcontracts.Organization, error) {
	if err := require(ctx, "organization", crmctx.ActionCreate); err != nil {
		return crmcontracts.Organization{}, err
	}
	by, err := capturedBy(ctx)
	if err != nil {
		return crmcontracts.Organization{}, err
	}

	var out crmcontracts.Organization
	err = s.tx(ctx, func(tx pgx.Tx) error {
		wsID := mustWorkspace(ctx)

		for _, d := range in.Domains {
			var existing ids.UUID
			err := tx.QueryRow(ctx,
				`SELECT organization_id FROM organization_domain WHERE domain = lower($1) AND archived_at IS NULL`,
				d.Domain).Scan(&existing)
			if err == nil {
				dup := &DuplicateDomainError{Domain: d.Domain}
				visible, verr := visibleTo(ctx, tx, "organization", existing)
				if verr != nil {
					return verr
				}
				if visible {
					dup.ExistingID = existing
				}
				return dup
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}

		id := ids.NewV7()
		_, err := tx.Exec(ctx,
			`INSERT INTO organization (id, workspace_id, display_name, legal_name, industry, size_band, owner_id, parent_org_id, source, captured_by)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			id, wsID, in.DisplayName, in.LegalName, in.Industry, in.SizeBand, in.OwnerID, in.ParentOrgID, in.Source, by)
		if err != nil {
			return err
		}

		for _, d := range in.Domains {
			if _, err := tx.Exec(ctx,
				`INSERT INTO organization_domain (workspace_id, organization_id, domain, is_primary, source, captured_by)
				 VALUES ($1, $2, lower($3), $4, $5, $6)`,
				wsID, id, d.Domain, d.IsPrimary, in.Source, by); err != nil {
				if name, ok := uniqueViolation(err); ok {
					if name == "uq_org_domain" {
						return &DuplicateDomainError{Domain: d.Domain}
					}
					return errs.ErrConflict // e.g. a second primary domain
				}
				return err
			}
		}

		auditID, err := audit(ctx, tx, "create", "organization", id, nil, map[string]any{"display_name": in.DisplayName})
		if err != nil {
			return err
		}
		if err := emit(ctx, tx, auditID, "organization.created", "organization", id, map[string]any{"display_name": in.DisplayName}); err != nil {
			return err
		}
		out, err = readOrganization(ctx, tx, id, false)
		return err
	})
	return out, err
}

func (s *Store) GetOrganization(ctx context.Context, id ids.UUID, includeArchived bool) (crmcontracts.Organization, error) {
	if err := require(ctx, "organization", crmctx.ActionRead); err != nil {
		return crmcontracts.Organization{}, err
	}
	var out crmcontracts.Organization
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		if err := ensureVisible(ctx, tx, "organization", id); err != nil {
			return err
		}
		out, err = readOrganization(ctx, tx, id, includeArchived)
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

func (s *Store) ListOrganizations(ctx context.Context, in ListOrganizationsInput) ([]crmcontracts.Organization, Page, error) {
	if err := require(ctx, "organization", crmctx.ActionRead); err != nil {
		return nil, Page{}, err
	}
	limit := clampLimit(in.Limit)

	where := []string{"1=1"}
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }

	scope, err := scopeClause(ctx, arg)
	if err != nil {
		return nil, Page{}, err
	}
	if scope != "" {
		where = append(where, scope)
	}

	if !in.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if in.OwnerID != nil {
		where = append(where, sprintf("owner_id = $%d", arg(*in.OwnerID)))
	}
	if in.Classification != nil {
		where = append(where, sprintf("classification = $%d", arg(*in.Classification)))
	}
	if in.Query != nil && *in.Query != "" {
		where = append(where, sprintf("search_tsv @@ plainto_tsquery('simple', $%d)", arg(*in.Query)))
	}
	if in.Cursor != nil && *in.Cursor != "" {
		c, err := decodeCursor(*in.Cursor)
		if err != nil {
			return nil, Page{}, err
		}
		where = append(where, sprintf("(created_at, id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID)))
	}

	var orgs []crmcontracts.Organization
	var page Page
	err = s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+orgColumns+` FROM organization WHERE `+strings.Join(where, " AND ")+
				sprintf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit+1),
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
			page = Page{HasMore: true, NextCursor: encodeCursor(last.CreatedAt, ids.UUID(last.Id))}
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
	IfVersion   *int64
}

func (s *Store) UpdateOrganization(ctx context.Context, id ids.UUID, in UpdateOrganizationInput) (crmcontracts.Organization, error) {
	if err := require(ctx, "organization", crmctx.ActionUpdate); err != nil {
		return crmcontracts.Organization{}, err
	}
	var out crmcontracts.Organization
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := ensureVisible(ctx, tx, "organization", id); err != nil {
			return err
		}
		current, err := readOrganization(ctx, tx, id, false)
		if err != nil {
			return err
		}

		p := newPatch()
		if in.DisplayName != nil {
			p.set("display_name", current.DisplayName, *in.DisplayName)
		}
		if in.LegalName != nil {
			p.set("legal_name", current.LegalName, *in.LegalName)
		}
		if in.Industry != nil {
			p.set("industry", current.Industry, *in.Industry)
		}
		if in.SizeBand != nil {
			p.set("size_band", current.SizeBand, *in.SizeBand)
		}
		if in.OwnerID != nil {
			p.set("owner_id", current.OwnerId, *in.OwnerID)
		}
		if in.ParentOrgID != nil {
			p.set("parent_org_id", current.ParentOrgId, *in.ParentOrgID)
		}
		if p.empty() {
			out = current
			return nil
		}

		if err := p.apply(ctx, tx, "organization", id, in.IfVersion); err != nil {
			return err
		}
		auditID, err := audit(ctx, tx, "update", "organization", id, p.before, p.after)
		if err != nil {
			return err
		}
		if err := emit(ctx, tx, auditID, "organization.updated", "organization", id, p.after); err != nil {
			return err
		}
		out, err = readOrganization(ctx, tx, id, false)
		return err
	})
	return out, err
}

func (s *Store) ArchiveOrganization(ctx context.Context, id ids.UUID) (crmcontracts.Organization, error) {
	if err := require(ctx, "organization", crmctx.ActionDelete); err != nil {
		return crmcontracts.Organization{}, err
	}
	var out crmcontracts.Organization
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := ensureVisible(ctx, tx, "organization", id); err != nil {
			return err
		}
		if _, err := readOrganization(ctx, tx, id, false); err != nil {
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

		auditID, err := audit(ctx, tx, "archive", "organization", id, nil, nil)
		if err != nil {
			return err
		}
		if err := emit(ctx, tx, auditID, "organization.archived", "organization", id, nil); err != nil {
			return err
		}
		out, err = readOrganization(ctx, tx, id, true)
		return err
	})
	return out, err
}

const orgColumns = `id, workspace_id, display_name, legal_name, industry, size_band, owner_id,
	classification, relevance, parent_org_id, merged_into_id, source, captured_by,
	version, created_at, updated_at, archived_at`

func readOrganization(ctx context.Context, tx pgx.Tx, id ids.UUID, includeArchived bool) (crmcontracts.Organization, error) {
	q := `SELECT ` + orgColumns + ` FROM organization WHERE id = $1`
	if !includeArchived {
		q += ` AND archived_at IS NULL`
	}
	o, err := scanOrganization(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Organization{}, errs.ErrNotFound
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
	var version int64

	err := row.Scan(&id, &wsID, &o.DisplayName, &o.LegalName, &o.Industry, &o.SizeBand, &ownerID,
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
