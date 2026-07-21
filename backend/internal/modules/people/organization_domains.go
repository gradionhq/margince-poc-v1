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

// ensureOrgDomainsUnclaimedExcept is ensureOrgDomainsUnclaimed for an edit:
// a domain already live on THIS org is not a conflict (keeping it is a
// no-op), only one owned by a DIFFERENT org is. The existing-id is
// disclosed under the same visibility gate as the create-path probe.
func ensureOrgDomainsUnclaimedExcept(ctx context.Context, tx pgx.Tx, self ids.OrganizationID, domains []OrgDomainInput) error {
	for _, d := range domains {
		var existing ids.OrganizationID
		err := tx.QueryRow(ctx,
			`SELECT organization_id FROM organization_domain WHERE domain = lower($1) AND archived_at IS NULL`,
			d.Domain).Scan(&existing)
		if errors.Is(err, pgx.ErrNoRows) || existing == self {
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

// readLiveDomains returns the org's live domains as a lookup set and as
// audit-before rows in one pass.
func readLiveDomains(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) (map[string]bool, []map[string]any, error) {
	rows, err := tx.Query(ctx,
		`SELECT domain, is_primary FROM organization_domain
		 WHERE organization_id = $1 AND archived_at IS NULL`, orgID)
	if err != nil {
		return nil, nil, fmt.Errorf("read current domains: %w", err)
	}
	defer rows.Close()
	live := map[string]bool{}
	before := []map[string]any{}
	for rows.Next() {
		var domain string
		var isPrimary bool
		if err := rows.Scan(&domain, &isPrimary); err != nil {
			return nil, nil, fmt.Errorf("scan current domain: %w", err)
		}
		live[domain] = true
		before = append(before, map[string]any{"domain": domain, "is_primary": isPrimary})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("read current domains: %w", err)
	}
	return live, before, nil
}

// singleDesiredPrimary returns the one domain marked primary, or "" for
// none. uq_org_domain_primary is a single-row invariant, so more than one
// is the typed 409 up front rather than a constraint failure mid-write.
func singleDesiredPrimary(desired []OrgDomainInput) (string, error) {
	primary := ""
	count := 0
	for _, d := range desired {
		if d.IsPrimary {
			primary = d.Domain
			count++
		}
	}
	if count > 1 {
		return "", apperrors.ErrConflict
	}
	return primary, nil
}

// reconcileOrgDomains makes the org's live domain set equal `desired`
// (add missing, archive removed, set the single primary). It returns the
// prior live set as audit-before rows. Primaries are cleared before the new
// one is set so the transient state never trips uq_org_domain_primary, and
// adds reuse insertOrgDomains so the uniqueness→409 mapping stays one
// spelling. Callers validate the domains (parse + unclaimed) first.
func reconcileOrgDomains(ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, orgID ids.OrganizationID, by string, desired []OrgDomainInput) ([]map[string]any, error) {
	live, before, err := readLiveDomains(ctx, tx, orgID)
	if err != nil {
		return nil, err
	}
	primary, err := singleDesiredPrimary(desired)
	if err != nil {
		return nil, err
	}

	// Clear every current primary first so setting the new one never races
	// the unique index against a still-primary row.
	if _, err := tx.Exec(ctx,
		`UPDATE organization_domain SET is_primary = false
		 WHERE organization_id = $1 AND archived_at IS NULL AND is_primary`, orgID); err != nil {
		return nil, fmt.Errorf("clear domain primaries: %w", err)
	}

	desiredSet := map[string]bool{}
	var adds []OrgDomainInput
	for _, d := range desired {
		desiredSet[d.Domain] = true
		if !live[d.Domain] {
			adds = append(adds, OrgDomainInput{Domain: d.Domain, IsPrimary: false})
		}
	}
	if err := archiveRemovedDomains(ctx, tx, orgID, live, desiredSet); err != nil {
		return nil, err
	}
	if len(adds) > 0 {
		if err := insertOrgDomains(ctx, tx, wsID, orgID, "manual", by, adds); err != nil {
			return nil, err
		}
	}
	if primary != "" {
		if _, err := tx.Exec(ctx,
			`UPDATE organization_domain SET is_primary = true
			 WHERE organization_id = $1 AND domain = lower($2) AND archived_at IS NULL`,
			orgID, primary); err != nil {
			return nil, fmt.Errorf("set primary domain: %w", err)
		}
	}
	return before, nil
}

// archiveRemovedDomains soft-deletes the org's live domains absent from the
// desired set.
func archiveRemovedDomains(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, live, desiredSet map[string]bool) error {
	for domain := range live {
		if desiredSet[domain] {
			continue
		}
		if _, err := tx.Exec(ctx,
			`UPDATE organization_domain SET archived_at = now()
			 WHERE organization_id = $1 AND domain = lower($2) AND archived_at IS NULL`,
			orgID, domain); err != nil {
			return fmt.Errorf("archive removed domain: %w", err)
		}
	}
	return nil
}

// domainSummaries renders the desired set as audit-after rows.
func domainSummaries(domains []OrgDomainInput) []map[string]any {
	out := make([]map[string]any, 0, len(domains))
	for _, d := range domains {
		out = append(out, map[string]any{"domain": d.Domain, "is_primary": d.IsPrimary})
	}
	return out
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
