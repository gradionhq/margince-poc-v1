// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// DuplicateDomainError carries the org already owning a domain: a domain
// maps to at most one org per workspace (data-model §4.2).
type DuplicateDomainError struct {
	Domain     string
	ExistingID ids.OrganizationID
}

func (e *DuplicateDomainError) Error() string {
	return "domain " + e.Domain + " already belongs to an organization"
}
func (e *DuplicateDomainError) Is(target error) bool { return target == apperrors.ErrConflict }

type OrgDomainInput struct {
	Domain    string
	IsPrimary bool
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

// ensureOrgDomainsUnclaimed answers the domain dedupe probe with the
// contract's 409, disclosing the existing org id only when the caller
// could read that row (a domain maps to at most one org per workspace,
// data-model §4.2).
func ensureOrgDomainsUnclaimed(ctx context.Context, tx pgx.Tx, domains []OrgDomainInput) error {
	for _, d := range domains {
		var existing ids.OrganizationID
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
		visible, verr := auth.VisibleTo(ctx, tx, "organization", existing.UUID)
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
func insertOrgDomains(ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, orgID ids.OrganizationID, source, by string, domains []OrgDomainInput) error {
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
