// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// setCompanyDomain records the company's own website as its primary domain —
// the same handle the cold-start read-back resolves organizations by, so a
// company saved by hand is findable exactly like one read from a site.
//
// An organization has at most ONE primary domain (uq_org_domain_primary), so a
// later save naming a new site must demote the old one first: inserting a
// second primary would collide, and a swallowed collision would mean the human
// edited their website, got a 200, and kept the old one.
func setCompanyDomain(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, website, by string) error {
	host, err := companyHost(website)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE organization_domain SET is_primary = false
		  WHERE workspace_id = $1 AND organization_id = $2
		    AND is_primary AND archived_at IS NULL AND domain <> lower($3)`,
		workspaceID(ctx), orgID, host); err != nil {
		return fmt.Errorf("demote previous company domain: %w", err)
	}

	// A domain is unique per workspace: re-saving the same site re-primaries the
	// row we already have, and a domain some CUSTOMER org already owns is a
	// conflict — claiming it here would silently move a record off its company.
	var owner ids.OrganizationID
	err = tx.QueryRow(ctx,
		`INSERT INTO organization_domain (workspace_id, organization_id, domain, is_primary, source, captured_by)
		 VALUES ($1, $2, lower($3), true, 'manual', $4)
		 ON CONFLICT (workspace_id, domain) WHERE archived_at IS NULL
		 DO UPDATE SET is_primary = true
		 WHERE organization_domain.organization_id = EXCLUDED.organization_id
		 RETURNING organization_id`,
		workspaceID(ctx), orgID, host, by).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("the domain %s already belongs to another organization: %w", host, apperrors.ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("set company domain: %w", err)
	}
	return nil
}

// companyHost reduces a website to the bare domain the organization_domain
// index keys on, accepting what a human actually types: "acme.com" as readily
// as "https://www.acme.com/about". The transport rejects an unparseable
// website before it gets here; this guard keeps a malformed one out of the
// domain index rather than storing it.
func companyHost(website string) (string, error) {
	if !strings.Contains(website, "://") {
		website = "https://" + website
	}
	parsed, err := url.Parse(website)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("people: company website %q has no host", website)
	}
	return strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www."), nil
}
