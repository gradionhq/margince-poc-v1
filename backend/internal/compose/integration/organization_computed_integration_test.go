// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The RD-T08 formula-field display rows on GET /organizations/{id}
// (arc 2b Task 3) exercised over a real migrated Postgres: the gated
// 5-row assembly with a real computed open_pipeline value, the two
// honest-floor states (no view row at all vs. a row whose aggregate is
// itself NULL), the STATE-4 absent-key proof, and the security_invoker
// proof that RLS — not organization_id happening to be unique — is what
// keeps one workspace's deals out of another's roll-up.
//
// Deals never carry fx_rate_to_base while status='open' through any
// real write path (deal_closed_fx only requires it once a deal leaves
// 'open', and no code path sets it early) — so a genuinely computable
// open_pipeline figure is fabricated here via the owner connection, the
// same "seed what the write paths cannot produce" pattern
// dealhealth_integration_test.go uses for its stage-history timestamps.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// freezeDealFX sets fx_rate_to_base = 1 (identity conversion — every
// fixture in this suite deals in the workspace's own EUR base currency)
// directly through the owner connection: the one state no application
// write path reaches for an OPEN deal, so amount_minor_base (0065's
// GENERATED column) becomes a real, non-NULL figure for these tests to
// sum.
func freezeDealFX(t *testing.T, owner *pgx.Conn, dealID ids.UUID) {
	t.Helper()
	if _, err := owner.Exec(context.Background(),
		`UPDATE deal SET fx_rate_to_base = 1 WHERE id = $1`, dealID); err != nil {
		t.Fatal(err)
	}
}

// directOpenPipelineRead is the test's own ground truth: the exact
// query organization_computed.go's openPipelineRollup runs, executed
// independently here so the assertions below prove the store's
// assembled figure against the view, not against itself. found is false
// for the view's honest "nothing to sum" case (no row at all).
func directOpenPipelineRead(ctx context.Context, t *testing.T, e *Env, orgID ids.UUID) (minor *int64, count int, found bool) {
	t.Helper()
	err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT open_pipeline_minor_base, open_deal_count
			 FROM organization_open_pipeline_rollup WHERE organization_id = $1`,
			orgID).Scan(&minor, &count)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return minor, count, true
}

// computedFieldByKey indexes the assembled rows for the reason/floor
// assertions that don't care about row order.
func computedFieldByKey(rows []crmcontracts.ComputedField, key string) crmcontracts.ComputedField {
	for _, r := range rows {
		if r.Key == key {
			return r
		}
	}
	return crmcontracts.ComputedField{}
}

// assertHonestFloors checks the four non-computable rows: weighted_pipeline
// names the read that actually serves it (poc-v1 HAS that read, unlike
// the poc-1 reference this ports), the other three are genuinely unbuilt.
func assertHonestFloors(t *testing.T, rows []crmcontracts.ComputedField) {
	t.Helper()
	want := map[string]string{
		"weighted_pipeline":     "served_by_hierarchy_rollup",
		"customer_age":          "not_yet_built",
		"net_revenue_retention": "not_yet_built",
		"blended_gross_margin":  "not_yet_built",
	}
	for key, reason := range want {
		row := computedFieldByKey(rows, key)
		if row.Key == "" {
			t.Fatalf("missing floor row %q", key)
		}
		if row.Computable {
			t.Fatalf("%s must be computable=false, got %+v", key, row)
		}
		if row.Reason == nil || *row.Reason != reason {
			t.Fatalf("%s.reason = %v, want %q", key, row.Reason, reason)
		}
		if row.ValueMinor != nil || row.Value != nil {
			t.Fatalf("%s must carry no value while computable=false, got %+v", key, row)
		}
	}
}

// pipelineFixtureFor is DealFixture's body, parameterized over ctx so a
// second workspace (the cross-tenant suite below) can seed its own
// default pipeline — DealFixture itself is hard-wired to e.Admin(),
// which is always bound to the harness's primary workspace.
func pipelineFixtureFor(ctx context.Context, t *testing.T, e *Env) (pipeline ids.PipelineID, open ids.StageID) {
	t.Helper()
	if err := e.Deals.SeedDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := e.Deals.DefaultPipeline(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range *p.Stages {
		if st.Semantic == "open" {
			open = ids.From[ids.StageKind](ids.UUID(st.Id))
			break
		}
	}
	return ids.From[ids.PipelineKind](ids.UUID(p.Id)), open
}

// computedFieldNoGrantPerms mirrors a real role's organization:read grant
// with the computed_field object simply absent from the policy document
// — the STATE-4 shape every non-admin custom role predates 0066's
// backfill would have had, and exactly what the plan asks be minted by
// hand since every one of poc-v1's five SEEDED system roles already
// carries computed_field:read (0066/policy.go).
var computedFieldNoGrantPerms = principal.Permissions{
	RoleKeys: []string{"custom-no-computed-field"},
	Objects: map[string]principal.ObjectGrant{
		"organization": {Read: true},
	},
	RowScope: principal.RowScopeAll,
}

// computedFieldWorkspaceBPerms grants workspace B's synthetic admin
// exactly what this suite's cross-tenant scenario needs — organization
// and deal writes plus computed_field:read — narrower than
// AdminPerms/cfAdminPerms because neither existing fixture carries the
// organization+deal+computed_field combination this suite exercises.
var computedFieldWorkspaceBPerms = principal.Permissions{
	RoleKeys: []string{"admin"},
	Objects: map[string]principal.ObjectGrant{
		"organization":   {Create: true, Read: true},
		"deal":           {Create: true, Read: true},
		"pipeline":       {Create: true, Read: true},
		"computed_field": {Read: true},
	},
	RowScope: principal.RowScopeAll,
}

// seedComputedFieldsWorkspaceB provisions a second tenant (own workspace
// + one user) and returns an admin-shaped context scoped to it — the
// customfields suite's seedSecondWorkspace grants only
// custom_field+person, which doesn't cover this suite's organization/
// deal/computed_field needs, so this is its own local variant rather
// than a shared-fixture edit that would ripple into that suite.
func seedComputedFieldsWorkspaceB(t *testing.T, owner *pgx.Conn) (ws ids.UUID, ctx context.Context) {
	t.Helper()
	ws, user := ids.NewV7(), ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Tenant B Computed', $2, 'EUR')`,
		ws, "computed-b-"+ws.String()[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'B Admin')`,
		user, ws, "b@computed-b.test"); err != nil {
		t.Fatal(err)
	}
	ctx = principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(),
		UserID: user, Permissions: computedFieldWorkspaceBPerms,
	})
	return ws, ctx
}

// TestOrganizationComputed_GatedVisible_RealValueMatchesDirectViewRead is
// the happy path: two open deals with their FX frozen (the owner-conn
// fixture above) sum to a known figure that must match both the view
// read directly AND the assembled open_pipeline row, and the four floor
// rows must carry their exact honest reasons.
func TestOrganizationComputed_GatedVisible_RealValueMatchesDirectViewRead(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	pipeline, open := pipelineFixtureFor(e.Admin(), t, e)
	orgID := e.SeedOrg(t, "Acme Corp", nil)

	d1, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
		Name: "D1", AmountMinor: int64Ptr(100000), Currency: strPtr("EUR"),
		PipelineID: pipeline, StageID: open, OrganizationID: orgIDPtr(orgIDOf(orgID)), Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	d2, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
		Name: "D2", AmountMinor: int64Ptr(250000), Currency: strPtr("EUR"),
		PipelineID: pipeline, StageID: open, OrganizationID: orgIDPtr(orgIDOf(orgID)), Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	freezeDealFX(t, owner, ids.UUID(d1.Id))
	freezeDealFX(t, owner, ids.UUID(d2.Id))

	wantMinor, wantCount, found := directOpenPipelineRead(e.Admin(), t, e, orgID)
	if !found || wantMinor == nil || *wantMinor != 350000 || wantCount != 2 {
		t.Fatalf("test fixture: direct view read = %v/%d/%v, want 350000/2/true", wantMinor, wantCount, found)
	}

	org, err := e.People.GetOrganization(e.Admin(), orgIDOf(orgID), storekit.IncludeArchived)
	if err != nil {
		t.Fatal(err)
	}
	if org.ComputedFields == nil || len(*org.ComputedFields) != 5 {
		t.Fatalf("want exactly 5 computed_fields rows, got %v", org.ComputedFields)
	}
	rows := *org.ComputedFields
	open0 := computedFieldByKey(rows, "open_pipeline")
	if !open0.Computable || open0.Reason != nil {
		t.Fatalf("open_pipeline must be computable with no floor reason, got %+v", open0)
	}
	if open0.ValueMinor == nil || *open0.ValueMinor != *wantMinor {
		t.Fatalf("open_pipeline.value_minor = %v, want %d (the direct view read)", open0.ValueMinor, *wantMinor)
	}
	if open0.Kind != crmcontracts.ComputedFieldKindCurrencyMinor {
		t.Fatalf("open_pipeline.kind = %q, want currency_minor", open0.Kind)
	}
	if open0.FormulaSql == "" {
		t.Fatal("open_pipeline.formula_sql must be non-empty")
	}
	assertHonestFloors(t, rows)
}

// TestOrganizationComputed_NoOpenDeals_FloorsToZero is the view's honest
// "nothing to sum" state: an organization with no open deals produces no
// view row at all, and the assembler floors that to a real 0 — the
// poc-1-tested behaviour, since a tile has no way to render "unknown".
func TestOrganizationComputed_NoOpenDeals_FloorsToZero(t *testing.T) {
	e := Setup(t)
	orgID := e.SeedOrg(t, "No Deals Inc", nil)

	if _, _, found := directOpenPipelineRead(e.Admin(), t, e, orgID); found {
		t.Fatal("test fixture: expected NO view row for an org with no open deals")
	}

	org, err := e.People.GetOrganization(e.Admin(), orgIDOf(orgID), storekit.IncludeArchived)
	if err != nil {
		t.Fatal(err)
	}
	rows := *org.ComputedFields
	open0 := computedFieldByKey(rows, "open_pipeline")
	if !open0.Computable {
		t.Fatal("the zero floor is still computable=true — a real (zero) sum, not a missing one")
	}
	if open0.ValueMinor == nil || *open0.ValueMinor != 0 {
		t.Fatalf("open_pipeline.value_minor = %v, want 0", open0.ValueMinor)
	}
}

// TestOrganizationComputed_OpenDealsWithoutFrozenFX_AwaitingFX is the
// OTHER honest "not computable yet" state 0065 documents: open deals
// exist (the view row IS present, open_deal_count > 0) but every one is
// still missing fx_rate_to_base — the ordinary state for an open deal
// through any real write path — so amount_minor_base is NULL for every
// summand and SUM ignores them all, leaving the row's aggregate itself
// NULL. Flooring this to a real 0 would be dishonest: it would sit
// beside a non-zero weighted_pipeline (arc 1b's hierarchy-rollup) as a
// fabricated "no pipeline" figure. The assembler instead floors it to
// computable:false, reason:"awaiting_fx", with no value_minor on the
// wire — the row EXISTS with a NULL aggregate, distinct from the
// genuine-zero no-row case the next test covers.
func TestOrganizationComputed_OpenDealsWithoutFrozenFX_AwaitingFX(t *testing.T) {
	e := Setup(t)
	pipeline, open := pipelineFixtureFor(e.Admin(), t, e)
	orgID := e.SeedOrg(t, "Unpriced Pipeline LLC", nil)

	for _, amount := range []int64{75000, 125000} {
		if _, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
			Name: "Unpriced deal", AmountMinor: int64Ptr(amount), Currency: strPtr("EUR"),
			PipelineID: pipeline, StageID: open, OrganizationID: orgIDPtr(orgIDOf(orgID)), Source: "manual",
		}); err != nil {
			t.Fatal(err)
		}
	}

	minor, count, found := directOpenPipelineRead(e.Admin(), t, e, orgID)
	if !found {
		t.Fatal("test fixture: expected a view row (2 open deals reference this org)")
	}
	if minor != nil {
		t.Fatalf("test fixture: expected a NULL aggregate (neither deal has fx_rate_to_base), got %d", *minor)
	}
	if count != 2 {
		t.Fatalf("test fixture: open_deal_count = %d, want 2", count)
	}

	org, err := e.People.GetOrganization(e.Admin(), orgIDOf(orgID), storekit.IncludeArchived)
	if err != nil {
		t.Fatal(err)
	}
	open0 := computedFieldByKey(*org.ComputedFields, "open_pipeline")
	if open0.Computable {
		t.Fatalf("a NULL-aggregate row with open deals present must be computable=false, got %+v", open0)
	}
	if open0.Reason == nil || *open0.Reason != "awaiting_fx" {
		t.Fatalf("open_pipeline.reason = %v, want \"awaiting_fx\"", open0.Reason)
	}
	if open0.ValueMinor != nil {
		t.Fatalf("open_pipeline.value_minor = %v, want absent (awaiting_fx carries no value)", open0.ValueMinor)
	}
	if open0.FormulaSql == "" {
		t.Fatal("open_pipeline.formula_sql must stay populated: the formula exists, only its FX input doesn't yet")
	}

	raw, err := json.Marshal(org)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	fields, ok := wire["computed_fields"].([]any)
	if !ok {
		t.Fatalf("computed_fields not a JSON array in the wire payload: %v", wire["computed_fields"])
	}
	for _, f := range fields {
		row, ok := f.(map[string]any)
		if !ok || row["key"] != "open_pipeline" {
			continue
		}
		if _, present := row["value_minor"]; present {
			t.Fatalf("open_pipeline.value_minor key must be entirely absent from the wire for awaiting_fx, got %v", row["value_minor"])
		}
	}
}

// TestOrganizationComputed_UngatedPrincipal_ComputedFieldsKeyAbsentFromWire
// is the STATE-4 proof: every one of poc-v1's five seeded system roles
// already carries computed_field:read (0066's backfill + policy.go), so
// this mints a custom permission set — organization:read without
// computed_field — the shape a bespoke pre-0066 role's policy document
// would have had. The raw-map decode (not a struct field check) proves
// the key is absent from the wire entirely, not merely nil in Go.
func TestOrganizationComputed_UngatedPrincipal_ComputedFieldsKeyAbsentFromWire(t *testing.T) {
	e := Setup(t)
	orgID := e.SeedOrg(t, "Gated Org", nil)
	ctx := e.As(e.Rep1, nil, computedFieldNoGrantPerms)

	org, err := e.People.GetOrganization(ctx, orgIDOf(orgID), storekit.IncludeArchived)
	if err != nil {
		t.Fatal(err)
	}
	if org.ComputedFields != nil {
		t.Fatalf("want a nil ComputedFields pointer for an ungated viewer, got %v", org.ComputedFields)
	}

	raw, err := json.Marshal(org)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if _, present := wire["computed_fields"]; present {
		t.Fatalf("want the computed_fields KEY entirely absent from the wire, got %v", wire["computed_fields"])
	}
}

// TestOrganizationComputed_SecurityInvokerNeverLeaksAcrossWorkspaces is
// the RLS proof T2's notes ask for: the view is security_invoker=true
// (0065), so it runs with the CALLING role's privileges and RLS — this
// seeds two workspaces with SAME-NAMED organizations, each with its own
// real (FX-frozen) open pipeline total, and proves two things a broken
// or missing RLS policy would fail: (1) probing the OTHER workspace's
// real organization id from THIS workspace's GUC-bound transaction finds
// NO view row at all (deal rows exist in the database, they are just
// invisible under the wrong tenant context — organization_id uniqueness
// alone would never catch a regression here, only RLS does); (2) each
// workspace's own GET reflects its own total, never the other's.
func TestOrganizationComputed_SecurityInvokerNeverLeaksAcrossWorkspaces(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)

	pipelineA, openA := pipelineFixtureFor(e.Admin(), t, e)
	orgA := e.SeedOrg(t, "Acme", nil)
	dealA, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
		Name: "A deal", AmountMinor: int64Ptr(500000), Currency: strPtr("EUR"),
		PipelineID: pipelineA, StageID: openA, OrganizationID: orgIDPtr(orgIDOf(orgA)), Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	freezeDealFX(t, owner, ids.UUID(dealA.Id))

	_, ctxB := seedComputedFieldsWorkspaceB(t, owner)
	pipelineB, openB := pipelineFixtureFor(ctxB, t, e)
	orgB := e.SeedOrgAs(ctxB, t, "Acme")
	dealB, err := e.Deals.CreateDeal(ctxB, deals.CreateDealInput{
		Name: "B deal", AmountMinor: int64Ptr(999000), Currency: strPtr("EUR"),
		PipelineID: pipelineB, StageID: openB, OrganizationID: orgIDPtr(orgIDOf(orgB)), Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	freezeDealFX(t, owner, ids.UUID(dealB.Id))

	// The cross-context probe: workspace A's GUC bound, workspace B's
	// real organization id — a leak would show B's 999000, not nothing.
	if _, _, found := directOpenPipelineRead(e.Admin(), t, e, orgB); found {
		t.Fatal("workspace A's context found a view row for workspace B's organization — RLS leak")
	}
	if _, _, found := directOpenPipelineRead(ctxB, t, e, orgA); found {
		t.Fatal("workspace B's context found a view row for workspace A's organization — RLS leak")
	}

	orgAGet, err := e.People.GetOrganization(e.Admin(), orgIDOf(orgA), storekit.IncludeArchived)
	if err != nil {
		t.Fatal(err)
	}
	orgBGet, err := e.People.GetOrganization(ctxB, orgIDOf(orgB), storekit.IncludeArchived)
	if err != nil {
		t.Fatal(err)
	}
	aOpen := computedFieldByKey(*orgAGet.ComputedFields, "open_pipeline")
	bOpen := computedFieldByKey(*orgBGet.ComputedFields, "open_pipeline")
	if aOpen.ValueMinor == nil || *aOpen.ValueMinor != 500000 {
		t.Fatalf("workspace A's own org.open_pipeline = %v, want 500000", aOpen.ValueMinor)
	}
	if bOpen.ValueMinor == nil || *bOpen.ValueMinor != 999000 {
		t.Fatalf("workspace B's own org.open_pipeline = %v, want 999000", bOpen.ValueMinor)
	}
}

// orgIDPtr matches int64Ptr/strPtr's convention (orgrollup_integration_test.go
// / authz_integration_test.go): the *ids.OrganizationID CreateDealInput wants.
func orgIDPtr(id ids.OrganizationID) *ids.OrganizationID { return &id }
