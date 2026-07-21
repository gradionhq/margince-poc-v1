// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// Editing an org's domains is a replace-set that rides the org's own write
// shape: one version bump, one audit row, one organization.updated event,
// every added domain human-stamped. A domain owned by another org is the
// typed 409; keeping a domain the org already owns is never a false
// conflict.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func liveDomainsOf(ctx context.Context, t *testing.T, e *dedupeEnv, orgID ids.OrganizationID) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT domain, is_primary FROM organization_domain
			  WHERE organization_id = $1 AND archived_at IS NULL`, orgID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d string
			var p bool
			if err := rows.Scan(&d, &p); err != nil {
				return err
			}
			out[d] = p
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("read live domains: %v", err)
	}
	return out
}

func orgVersion(ctx context.Context, t *testing.T, e *dedupeEnv, orgID ids.OrganizationID) int64 {
	t.Helper()
	var v int64
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT version FROM organization WHERE id = $1`, orgID).Scan(&v)
	}); err != nil {
		t.Fatalf("read org version: %v", err)
	}
	return v
}

func countOrgUpdatedEvents(ctx context.Context, t *testing.T, e *dedupeEnv, orgID ids.OrganizationID) int {
	t.Helper()
	var n int
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM event_outbox
			  WHERE envelope->>'type' = 'organization.updated'
			    AND envelope->'entity'->>'id' = $1`, orgID.String()).Scan(&n)
	}); err != nil {
		t.Fatalf("count organization.updated events: %v", err)
	}
	return n
}

func domainCapturedBy(ctx context.Context, t *testing.T, e *dedupeEnv, orgID ids.OrganizationID, domain string) string {
	t.Helper()
	var by string
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT captured_by FROM organization_domain
			  WHERE organization_id = $1 AND domain = $2 AND archived_at IS NULL`, orgID, domain).Scan(&by)
	}); err != nil {
		t.Fatalf("read domain captured_by: %v", err)
	}
	return by
}

func TestUpdateOrganizationDomainsReplaceSet(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Voltaq Systems GmbH", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "voltaq.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	orgID := ids.From[ids.OrganizationKind](ids.UUID(org.Id))
	v0 := orgVersion(ctx, t, e, orgID)

	// Add a second domain and move the primary onto it; the first stays but
	// is demoted.
	if _, err := e.store.UpdateOrganization(ctx, orgID, UpdateOrganizationInput{
		Domains: &[]OrgDomainInput{
			{Domain: "voltaq.test", IsPrimary: false},
			{Domain: "voltaq-systems.test", IsPrimary: true},
		},
	}); err != nil {
		t.Fatalf("replace-set update: %v", err)
	}

	live := liveDomainsOf(ctx, t, e, orgID)
	if len(live) != 2 || live["voltaq.test"] != false || live["voltaq-systems.test"] != true {
		t.Fatalf("live domains after replace-set = %+v, want {voltaq.test:false, voltaq-systems.test:true}", live)
	}
	if by := domainCapturedBy(ctx, t, e, orgID, "voltaq-systems.test"); len(by) < 6 || by[:6] != "human:" {
		t.Fatalf("added domain captured_by = %q, want human:*", by)
	}
	if v1 := orgVersion(ctx, t, e, orgID); v1 <= v0 {
		t.Fatalf("org version did not bump: %d -> %d", v0, v1)
	}
	if n := countOrgUpdatedEvents(ctx, t, e, orgID); n != 1 {
		t.Fatalf("got %d organization.updated events, want exactly 1", n)
	}

	// Remove the demoted domain: the replace-set archives it.
	if _, err := e.store.UpdateOrganization(ctx, orgID, UpdateOrganizationInput{
		Domains: &[]OrgDomainInput{{Domain: "voltaq-systems.test", IsPrimary: true}},
	}); err != nil {
		t.Fatalf("remove update: %v", err)
	}
	live = liveDomainsOf(ctx, t, e, orgID)
	if len(live) != 1 || live["voltaq-systems.test"] != true {
		t.Fatalf("live domains after removal = %+v, want {voltaq-systems.test:true}", live)
	}
}

func TestUpdateOrganizationDomainConflictIsTyped409(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	owner, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Owner GmbH", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "claimed.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Other GmbH", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "other.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = e.store.UpdateOrganization(ctx, ids.From[ids.OrganizationKind](ids.UUID(other.Id)),
		UpdateOrganizationInput{Domains: &[]OrgDomainInput{{Domain: "claimed.test", IsPrimary: true}}})
	var dup *DuplicateDomainError
	if !errors.As(err, &dup) {
		t.Fatalf("claiming another org's domain must be the typed 409, got %v", err)
	}
	if dup.ExistingID != ids.From[ids.OrganizationKind](ids.UUID(owner.Id)) {
		t.Fatalf("409 discloses %s, want the owner %s", dup.ExistingID, owner.Id)
	}
}

func TestUpdateOrganizationKeepingOwnDomainIsNoFalseConflict(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Keep GmbH", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "keep.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	orgID := ids.From[ids.OrganizationKind](ids.UUID(org.Id))

	// Re-submitting the org's own live domain must not read as a dedupe hit.
	if _, err := e.store.UpdateOrganization(ctx, orgID, UpdateOrganizationInput{
		DisplayName: strPtr("Keep GmbH (edited)"),
		Domains:     &[]OrgDomainInput{{Domain: "keep.test", IsPrimary: true}},
	}); err != nil {
		t.Fatalf("keeping own domain must not conflict: %v", err)
	}
	live := liveDomainsOf(ctx, t, e, orgID)
	if len(live) != 1 || !live["keep.test"] {
		t.Fatalf("live domains = %+v, want {keep.test:true}", live)
	}
}
