// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The installation's own company: the anchor pointer (0082) is what makes
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
			"legal_name": strptr("Acme Gesellschaft mit beschränkter Haftung"),
			"icp":        strptr("RevOps at SaaS scale-ups"),
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
	if capturedBy != "human:"+e.Rep1.String() || source != "manual" {
		t.Fatalf("human-typed field stamped captured_by=%q source=%q, want human:<id> / manual", capturedBy, source)
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
