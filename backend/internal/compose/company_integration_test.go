// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The installation's own company: the anchor pointer (0083) is what makes
// "has this installation described itself yet?" answerable, the form's save is
// the human's confirm-first write, and a value a human has saved is theirs —
// a later agent read-back of the same site leaves it alone.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func strptr(s string) *string { return &s }

func TestCompanyIsUnsetUntilAHumanSavesIt(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.Pool)
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)

	// A freshly bootstrapped installation (ADR-0061) has an organization row
	// for nobody: the anchor is unset, and that IS the onboarding signal.
	if _, err := store.GetCompany(ctx); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("GetCompany on a bare installation → %v, want ErrNotFound", err)
	}

	saved, err := store.SaveCompany(ctx, people.SaveCompanyInput{
		DisplayName: "Acme GmbH",
		Website:     strptr("https://www.acme.example/about"),
		Fields: map[string]*string{
			"legal_name":    strptr("Acme Gesellschaft mit beschränkter Haftung"),
			"offer_summary": strptr("Revenue operations software"),
			"icp":           strptr("RevOps at SaaS scale-ups"),
			// A field nobody filled stays absent rather than becoming "".
			"usp": nil,
		},
	})
	if err != nil {
		t.Fatalf("SaveCompany: %v", err)
	}
	if saved.DisplayName != "Acme GmbH" {
		t.Fatalf("saved name = %q", saved.DisplayName)
	}
	if !saved.MinimumComplete {
		t.Fatal("the three semantic fields did not make the company minimum-complete")
	}
	// The website is stored as the bare domain — the same handle a read-back
	// resolves organizations by — so a full URL normalises on the way in.
	if saved.Website == nil || *saved.Website != "acme.example" {
		t.Fatalf("saved website = %v, want acme.example", saved.Website)
	}
	if _, filled := saved.Fields["usp"]; filled {
		t.Fatalf("an unsent field was written: %+v", saved.Fields)
	}

	// The mark is what makes the company findable; without it the row is just
	// another organization.
	var anchors int
	err = database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM organization
			  WHERE id = $1 AND workspace_id = $2 AND is_anchor AND archived_at IS NULL`,
			saved.OrganizationID, e.WS).Scan(&anchors)
	})
	if err != nil {
		t.Fatal(err)
	}
	if anchors != 1 {
		t.Fatalf("the saved company is not marked as the installation's own (%d anchor rows)", anchors)
	}
	if audits := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'organization' AND entity_id = $1 AND action = 'create'`,
		saved.OrganizationID.UUID); audits != 1 {
		t.Fatalf("company save wrote %d create audits, want 1", audits)
	}
	if outbox := e.WsCount(t,
		`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'organization.created' AND envelope#>>'{entity,id}' = $1`,
		saved.OrganizationID.String()); outbox != 1 {
		t.Fatalf("company save wrote %d organization.created events, want 1", outbox)
	}

	// Re-reading is the form's own round-trip.
	got, err := store.GetCompany(ctx)
	if err != nil {
		t.Fatalf("GetCompany after save: %v", err)
	}
	if got.OrganizationID != saved.OrganizationID || got.Fields["icp"] != "RevOps at SaaS scale-ups" {
		t.Fatalf("GetCompany = %+v, want the saved company", got)
	}

	// A second save updates the anchor rather than minting a rival company.
	if _, err := store.SaveCompany(ctx, people.SaveCompanyInput{
		DisplayName: "Acme SE",
		Fields:      map[string]*string{"icp": strptr("RevOps at enterprise")},
	}); err != nil {
		t.Fatalf("second SaveCompany: %v", err)
	}
	var orgs int
	err = database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM organization`).Scan(&orgs)
	})
	if err != nil {
		t.Fatal(err)
	}
	if orgs != 1 {
		t.Fatalf("saving twice created %d organizations, want the one anchor", orgs)
	}

	// A field sent empty is cleared, not stored as the empty answer.
	cleared, err := store.SaveCompany(ctx, people.SaveCompanyInput{
		DisplayName: "Acme SE",
		Fields:      map[string]*string{"icp": strptr("")},
	})
	if err != nil {
		t.Fatalf("clearing SaveCompany: %v", err)
	}
	if _, filled := cleared.Fields["icp"]; filled {
		t.Fatalf("cleared field is still present: %+v", cleared.Fields)
	}
}

// Editing the website has to actually move the primary domain. An organization
// has at most one (uq_org_domain_primary), so a naive insert collides with the
// old one — and a swallowed collision means the human changed their website,
// saw a 200, and kept the old site.
func TestCompanyWebsiteCanBeChangedAfterTheFirstSave(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.Pool)
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)

	base := people.SaveCompanyInput{
		DisplayName: "Acme GmbH",
		Fields: map[string]*string{
			"legal_name": strptr("Acme GmbH"), "registered_address": strptr("Berlin"),
			"register_vat": strptr("DE123"), "industry": strptr("Software"),
		},
	}
	first := base
	first.Website = strptr("https://old.example")
	if _, err := store.SaveCompany(ctx, first); err != nil {
		t.Fatalf("first SaveCompany: %v", err)
	}

	moved := base
	moved.Website = strptr("https://new.example")
	got, err := store.SaveCompany(ctx, moved)
	if err != nil {
		t.Fatalf("changing the website: %v", err)
	}
	if got.Website == nil {
		t.Fatal("the saved company has no website at all after the change")
	}
	if *got.Website != "new.example" {
		t.Fatalf("the saved website is %q, want new.example — the edit was lost", *got.Website)
	}

	// Exactly one primary, and it is the new site: the old row must be demoted,
	// not left alongside as a rival primary.
	var primaries int
	var primary string
	err = database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(),
			`SELECT count(*) FROM organization_domain
			  WHERE organization_id = $1 AND is_primary AND archived_at IS NULL`,
			got.OrganizationID).Scan(&primaries); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(),
			`SELECT domain FROM organization_domain
			  WHERE organization_id = $1 AND is_primary AND archived_at IS NULL`,
			got.OrganizationID).Scan(&primary)
	})
	if err != nil {
		t.Fatal(err)
	}
	if primaries != 1 || primary != "new.example" {
		t.Fatalf("primary domains = %d (%q), want exactly 1 as new.example", primaries, primary)
	}

	// Re-saving the SAME site is idempotent, not a conflict with itself.
	if _, err := store.SaveCompany(ctx, moved); err != nil {
		t.Fatalf("re-saving the same website: %v", err)
	}
}

func TestCompanySavedByAHumanSurvivesALaterReadBack(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.Pool)
	human := e.As(e.Rep1, nil, integration.AdminPerms)

	saved, err := store.SaveCompany(human, people.SaveCompanyInput{
		DisplayName: "Acme GmbH",
		Website:     strptr("https://acme.example"),
		Fields:      map[string]*string{"icp": strptr("What the human says we sell to")},
	})
	if err != nil {
		t.Fatalf("SaveCompany: %v", err)
	}

	// The human's own words carry human provenance — which is exactly what
	// the read-back's upsert refuses to overwrite.
	var capturedBy, source string
	err = database.WithWorkspaceTx(human, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT captured_by, source FROM organization_profile_field
			  WHERE organization_id = $1 AND field = 'icp'`, saved.OrganizationID).Scan(&capturedBy, &source)
	})
	if err != nil {
		t.Fatal(err)
	}
	if capturedBy != "human:"+e.Rep1.String() || source != "human" {
		t.Fatalf("human-typed field stamped captured_by=%q source=%q, want human:<id> / human", capturedBy, source)
	}

	// Now an agent reads the same site back and its accept lands on the same
	// organization (resolved by the domain the form recorded).
	agent := principal.WithActor(human, principal.Principal{
		Type: principal.PrincipalSystem, ID: "agent:coldstart",
		UserID: e.Rep1, OnBehalfOf: e.Rep1, Permissions: integration.AdminPerms,
	})
	orgID, err := store.ApplyColdStartProfile(agent, people.ApplyColdStartProfileInput{
		SourceURL: "https://acme.example",
		Fields: []people.ColdStartFieldInput{{
			Field: "icp", Value: "What the website says", EvidenceSnippet: "Built for RevOps",
			SourceURL: "https://acme.example", Confidence: 0.9,
		}},
	})
	if err != nil {
		t.Fatalf("ApplyColdStartProfile: %v", err)
	}
	if orgID != saved.OrganizationID {
		t.Fatalf("the read-back landed on %s, not the anchor %s — the form's domain should resolve to the company",
			orgID, saved.OrganizationID)
	}

	got, err := store.GetCompany(human)
	if err != nil {
		t.Fatal(err)
	}
	if got.Fields["icp"] != "What the human says we sell to" {
		t.Fatalf("an agent read-back overwrote the human's own value: %q", got.Fields["icp"])
	}
}

func TestCompanyContextIsScopedProvenanceBearingAndChangesWithTheProfile(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.Pool)
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)

	saved, err := store.SaveCompany(ctx, people.SaveCompanyInput{
		DisplayName: "Acme GmbH",
		Website:     strptr("https://acme.example"),
		Fields: map[string]*string{
			"offer_summary": strptr("Revenue operations software"),
			"icp":           strptr("Mid-market manufacturers"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.WsExec(t, `
		INSERT INTO organization_fact
		  (workspace_id, organization_id, category, field, value, value_key,
		   evidence_snippet, source_url, confidence, source, captured_by)
		VALUES ($1, $2, 'offering', 'service', 'CRM rollout', 'crm rollout',
		        '', '', 1, 'human', $3)`, e.WS, saved.OrganizationID, "human:"+e.Rep1.String())

	foreignWS, foreignOrg := ids.NewV7(), ids.NewV7()
	owner := integration.OwnerConn(t)
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Foreign', $2, 'EUR')`,
		foreignWS, "foreign-"+foreignWS.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO organization (id, workspace_id, display_name, is_anchor, source, captured_by)
		VALUES ($1, $2, 'Foreign Secret', true, 'manual', 'human:foreign')`,
		foreignOrg, foreignWS); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO organization_profile_field
		  (workspace_id, organization_id, field, value, evidence_snippet, source_url, confidence, source, captured_by)
		VALUES ($1, $2, 'offer_summary', 'Secret foreign offer', '', '', 1, 'human', 'human:foreign')`,
		foreignWS, foreignOrg); err != nil {
		t.Fatal(err)
	}

	first, err := store.GetCompanyContext(ctx, []people.CompanyContextScope{
		people.CompanyContextOffer, people.CompanyContextPositioning,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Scopes) != 2 || first.Scopes[0].Scope != people.CompanyContextPositioning || first.Scopes[1].Scope != people.CompanyContextOffer {
		t.Fatalf("context scopes = %#v, want canonical positioning then offer", first.Scopes)
	}
	if len(first.Scopes[0].Items) != 1 || first.Scopes[0].Items[0].Key != "icp" || first.Scopes[0].Items[0].Source != "human" {
		t.Fatalf("positioning context = %#v, want human-provenance ICP", first.Scopes[0].Items)
	}
	if len(first.Scopes[1].Items) != 2 {
		t.Fatalf("offer context = %#v, want summary and repeatable service", first.Scopes[1].Items)
	}
	foreignCtx := principal.WithWorkspaceID(context.Background(), foreignWS)
	foreignCtx = principal.WithCorrelationID(foreignCtx, ids.NewV7())
	foreignCtx = principal.WithActor(foreignCtx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:foreign", UserID: ids.NewV7(), Permissions: integration.AdminPerms,
	})
	foreign, err := store.GetCompanyContext(foreignCtx, []people.CompanyContextScope{people.CompanyContextOffer})
	if err != nil {
		t.Fatal(err)
	}
	if len(foreign.Scopes) != 1 || len(foreign.Scopes[0].Items) != 1 || foreign.Scopes[0].Items[0].Value != "Secret foreign offer" {
		t.Fatalf("foreign workspace context = %#v", foreign.Scopes)
	}
	again, err := store.GetCompanyContext(ctx, []people.CompanyContextScope{
		people.CompanyContextPositioning, people.CompanyContextOffer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint != again.Fingerprint {
		t.Fatalf("unchanged context fingerprint moved from %q to %q", first.Fingerprint, again.Fingerprint)
	}

	if _, err := store.SaveCompany(ctx, people.SaveCompanyInput{
		DisplayName: saved.DisplayName,
		Fields: map[string]*string{
			"offer_summary": strptr("Revenue intelligence software"),
		},
	}); err != nil {
		t.Fatal(err)
	}
	changed, err := store.GetCompanyContext(ctx, []people.CompanyContextScope{
		people.CompanyContextOffer, people.CompanyContextPositioning,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint == changed.Fingerprint {
		t.Fatal("editing a contributing profile value did not change the context fingerprint")
	}
}
