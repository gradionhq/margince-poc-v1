// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The org-360 evidence reads: facts and profile fields surface exactly
// what a human confirmed, row-scoped and ordered for a stable render, and
// an org outside the caller's scope is existence-hidden (404), never
// another workspace's evidence.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func seedOrgWithEvidence(ctx context.Context, t *testing.T, e *dedupeEnv) ids.OrganizationID {
	t.Helper()
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Voltaq Systems GmbH", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "voltaq.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("seed org: %v", err)
	}
	orgID := ids.From[ids.OrganizationKind](ids.UUID(org.Id))
	err = e.store.tx(ctx, func(tx pgx.Tx) error {
		// Two profile fields: a site-read icp (evidence-backed) and a
		// human-set legal_name (evidence columns null — the read must map
		// them to nil, not the zero string).
		if _, err := tx.Exec(ctx, `
			INSERT INTO organization_profile_field
			  (workspace_id, organization_id, field, value, evidence_snippet, source_url, confidence, source, captured_by)
			VALUES ($1,$2,'icp','Energy-intensive manufacturers','"…for energy-intensive manufacturers"','https://voltaq.test/about',0.9,'site_read','agent:deepread'),
			       ($1,$2,'legal_name','Voltaq Systems GmbH',NULL,NULL,NULL,'human','human:'||$3)`,
			e.ws, orgID, e.rep); err != nil {
			return err
		}
		// Two facts across two categories: a company/phone (value_key '')
		// and a signal/certification (value_key non-empty).
		if _, err := tx.Exec(ctx, `
			INSERT INTO organization_fact
			  (workspace_id, organization_id, category, field, value, value_key, evidence_snippet, source_url, confidence, source, captured_by)
			VALUES ($1,$2,'company','phone','+49 30 1234','','"+49 30 1234"','https://voltaq.test/impressum',0.95,'site_read','agent:deepread'),
			       ($1,$2,'signal','certification','ISO 9001','iso 9001','"ISO 9001 certified"','https://voltaq.test/quality',0.8,'site_read','agent:deepread')`,
			e.ws, orgID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed evidence: %v", err)
	}
	return orgID
}

func TestListOrganizationProfileFieldsReturnsConfirmedFieldsRowScoped(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := seedOrgWithEvidence(ctx, t, e)

	fields, err := e.store.ListOrganizationProfileFields(ctx, orgID)
	if err != nil {
		t.Fatalf("list profile fields: %v", err)
	}
	if len(fields) != 2 {
		t.Fatalf("got %d profile fields, want 2", len(fields))
	}
	// Ordered by field: icp before legal_name.
	if string(fields[0].Field) != "icp" || string(fields[1].Field) != "legal_name" {
		t.Fatalf("profile fields out of order: %q, %q", fields[0].Field, fields[1].Field)
	}
	if fields[0].EvidenceSnippet == nil || string(fields[0].Source) != "site_read" {
		t.Fatalf("icp lost its evidence/source: %+v", fields[0])
	}
	// The human-set legal_name has no evidence — the read must not invent it.
	if fields[1].EvidenceSnippet != nil || fields[1].Confidence != nil {
		t.Fatalf("legal_name fabricated evidence: %+v", fields[1])
	}

	// An org outside the caller's scope is existence-hidden, never leaked.
	if _, err := e.store.ListOrganizationProfileFields(ctx, ids.From[ids.OrganizationKind](ids.NewV7())); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("foreign org must 404, got %v", err)
	}
}

func TestListOrganizationFactsReturnsConfirmedFactsGroupedOrder(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := seedOrgWithEvidence(ctx, t, e)

	facts, err := e.store.ListOrganizationFacts(ctx, orgID)
	if err != nil {
		t.Fatalf("list facts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("got %d facts, want 2", len(facts))
	}
	// Ordered by category: company before signal.
	if string(facts[0].Category) != "company" || string(facts[1].Category) != "signal" {
		t.Fatalf("facts out of category order: %q, %q", facts[0].Category, facts[1].Category)
	}
	if string(facts[0].Field) != "phone" || facts[0].ValueKey != "" {
		t.Fatalf("company/phone mismapped: %+v", facts[0])
	}
	if string(facts[1].Field) != "certification" || facts[1].ValueKey != "iso 9001" {
		t.Fatalf("signal/certification mismapped: %+v", facts[1])
	}

	if _, err := e.store.ListOrganizationFacts(ctx, ids.From[ids.OrganizationKind](ids.NewV7())); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("foreign org must 404, got %v", err)
	}
}
