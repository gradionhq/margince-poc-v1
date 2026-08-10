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

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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
	// Changing the company's domain is an admin act, even though editing the
	// rest of the company profile is not.
	//
	// This domain decides whether mail is STORED: correspondence among people
	// on it produces zero rows (ADR-0082/A127), and a connector never offers a
	// skipped message again. Leaving it on `organization.update` — which a rep
	// holds — would let any rep name a customer's domain here and permanently
	// stop the workspace keeping that customer's mail. The own-domain settings
	// surface gates the same decision on `capture_settings`; this is the other
	// writer of that set, and it takes the same grant.
	//
	// Only a CHANGE is gated: re-saving the company with its existing domain is
	// an ordinary profile edit and stays open to whoever may edit the profile.
	changing, err := companyDomainWouldChange(ctx, tx, orgID, host)
	if err != nil {
		return err
	}
	if changing {
		if err := auth.Require(ctx, "capture_settings", principal.ActionUpdate); err != nil {
			return err
		}
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
//
// It is the ONE reducer, and that is what the trailing dot is doing here. The
// FQDN root form — `acme.example.`, and `https://acme.example./about`, where the
// dot is inside the URL and trimming the string never reaches it — is the same
// name to DNS and a different string to an index. Left in on the write side, one
// company reachable by two spellings becomes two organizations, which is the
// duplicate this module exists to prevent; left in on one side only, a read and
// a write disagree about what they are looking at. The root dot is not part of
// the key.
func companyHost(website string) (string, error) {
	if !strings.Contains(website, "://") {
		website = "https://" + website
	}
	parsed, err := url.Parse(website)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("people: company website %q has no host", website)
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	host = strings.TrimPrefix(host, "www.")
	if host == "" {
		return "", fmt.Errorf("people: company website %q has no host", website)
	}
	return host, nil
}

// companyDomainWouldChange reports whether host differs from the anchor's
// current primary domain — the only case that alters what counts as internal.
//
// FOR UPDATE, because this read DECIDES a permission and the write it decides
// for happens later in the same transaction. Unlocked, at READ COMMITTED, a
// concurrent change to the primary domain lands in between: a caller
// re-submitting what WAS the domain reads "unchanged", skips the
// capture_settings grant, and then writes a value that is now a change. That
// is a rep silently reverting the installation's own domain — and the domain
// decides which mail is stored at all. The lock makes the decision and the
// write one unit; a competing writer waits and this one re-reads its result.
func companyDomainWouldChange(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, host string) (bool, error) {
	var current string
	err := tx.QueryRow(ctx,
		`SELECT domain FROM organization_domain
		  WHERE workspace_id = $1 AND organization_id = $2
		    AND is_primary AND archived_at IS NULL
		    FOR UPDATE`,
		workspaceID(ctx), orgID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		// No domain yet: the first one is a change from nothing.
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the company's current domain: %w", err)
	}
	return !strings.EqualFold(current, host), nil
}
