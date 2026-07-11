// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The deal half of the custom-field VALUES coverage (CF-T05, arc 2a-ii
// T3): the same fieldcatalog seam wired into the deals store — active
// cf_* deal columns ride create/update writes and get/list reads like
// core fields, with the same drop-on-mismatch and workspace-isolation
// posture the person/organization suites prove.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// dealCFVPerms adds the deal + pipeline grants the deal round trip needs
// on top of the catalog-admin posture.
var dealCFVPerms = principal.Permissions{
	RoleKeys: []string{"admin"},
	Objects: map[string]principal.ObjectGrant{
		"custom_field": {Create: true, Read: true, Update: true, Delete: true},
		"deal":         {Create: true, Read: true, Update: true, Delete: true},
		"pipeline":     {Create: true, Read: true, Update: true, Delete: true},
	},
	RowScope: principal.RowScopeAll,
}

// dealCFVFixture is the deal store-level fixture: one Env plus a
// catalog-wired deals store, the schema-pool-backed customfields service
// that defines the fields, and the seeded default pipeline.
type dealCFVFixture struct {
	e        *Env
	svc      *customfields.Service
	store    *deals.Store
	ctx      context.Context
	pipeline ids.PipelineID
	stage    ids.StageID
}

func setupDealCFV(t *testing.T) dealCFVFixture {
	t.Helper()
	e := Setup(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))
	pipeline, open, _ := DealFixture(t, e)
	return dealCFVFixture{
		e:        e,
		svc:      svc,
		store:    deals.NewStore(e.Pool).WithFieldCatalog(svc),
		ctx:      e.As(e.Rep1, nil, dealCFVPerms),
		pipeline: pipeline,
		stage:    open,
	}
}

// defineDealField creates one active deal custom field and returns its
// physical column name.
func (f dealCFVFixture) defineDealField(t *testing.T, spec customfields.FieldSpec) string {
	t.Helper()
	field, err := f.svc.Create(f.ctx, spec)
	if err != nil {
		t.Fatalf("defining %s field %q: %v", spec.Type, spec.Label, err)
	}
	if field.ColumnName == nil {
		t.Fatalf("defined field %q carries no column_name", spec.Label)
	}
	return *field.ColumnName
}

func TestCustomFieldValues_DealRoundTrip(t *testing.T) {
	f := setupDealCFV(t)
	col := f.defineDealField(t, customfields.FieldSpec{Object: "deal", Label: "Segment", Type: customfields.TypeText, Source: "ui"})

	created, err := f.store.CreateDeal(f.ctx, deals.CreateDealInput{
		Name: "Acme Renewal", PipelineID: f.pipeline, StageID: f.stage, Source: "ui",
		CustomFields: map[string]any{col: "enterprise"},
	})
	if err != nil {
		t.Fatalf("CreateDeal: %v", err)
	}
	assertCF(t, created.AdditionalProperties, col, "enterprise")

	got, err := f.store.GetDeal(f.ctx, dealIDOf(ids.UUID(created.Id)), storekit.LiveOnly)
	if err != nil {
		t.Fatalf("GetDeal: %v", err)
	}
	assertCF(t, got.AdditionalProperties, col, "enterprise")

	updated, err := f.store.UpdateDeal(f.ctx, dealIDOf(ids.UUID(created.Id)), deals.UpdateDealInput{
		CustomFields: map[string]any{col: "mid-market"},
	})
	if err != nil {
		t.Fatalf("UpdateDeal: %v", err)
	}
	assertCF(t, updated.AdditionalProperties, col, "mid-market")

	list, _, err := f.store.ListDeals(f.ctx, deals.ListDealsInput{})
	if err != nil {
		t.Fatalf("ListDeals: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListDeals returned %d rows, want 1", len(list))
	}
	assertCF(t, list[0].AdditionalProperties, col, "mid-market")
}

// TestCustomFieldValues_DealWorkspaceIsolation: the physical cf_ column
// on deal is shared across tenants, but the catalog is workspace-scoped
// — a workspace that never defined the field neither writes nor reads it.
func TestCustomFieldValues_DealWorkspaceIsolation(t *testing.T) {
	f := setupDealCFV(t)
	col := f.defineDealField(t, customfields.FieldSpec{Object: "deal", Label: "Segment", Type: customfields.TypeText, Source: "ui"})

	inA, err := f.store.CreateDeal(f.ctx, deals.CreateDealInput{
		Name: "Tenant A Deal", PipelineID: f.pipeline, StageID: f.stage, Source: "ui",
		CustomFields: map[string]any{col: "enterprise"},
	})
	if err != nil {
		t.Fatalf("CreateDeal (tenant A): %v", err)
	}
	assertCF(t, inA.AdditionalProperties, col, "enterprise")

	wsB, ctxB := seedSecondWorkspace(t, OwnerConn(t))
	ctxB = withPerms(ctxB, wsB, dealCFVPerms)
	pipelineB, stageB := seedDealFixtureIn(ctxB, t, f.store)
	inB, err := f.store.CreateDeal(ctxB, deals.CreateDealInput{
		Name: "Tenant B Deal", PipelineID: pipelineB, StageID: stageB, Source: "ui",
		CustomFields: map[string]any{col: "enterprise"},
	})
	if err != nil {
		t.Fatalf("CreateDeal (tenant B): %v", err)
	}
	assertNoCF(t, inB.AdditionalProperties, col)

	// The dropped write really never landed: B's row holds NULL in the
	// shared physical column.
	var stored *string
	err = database.WithWorkspaceTx(ctxB, f.e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT `+col+` FROM deal WHERE id = $1`, ids.UUID(inB.Id)).Scan(&stored)
	})
	if err != nil {
		t.Fatalf("reading tenant B's column directly: %v", err)
	}
	if stored != nil {
		t.Fatalf("tenant B's %s = %q, want NULL (write must be dropped)", col, *stored)
	}

	// Tenant A still reads its value.
	gotA, err := f.store.GetDeal(f.ctx, dealIDOf(ids.UUID(inA.Id)), storekit.LiveOnly)
	if err != nil {
		t.Fatalf("GetDeal (tenant A): %v", err)
	}
	assertCF(t, gotA.AdditionalProperties, col, "enterprise")
}

// withPerms rebinds a second-tenant context under the given permission
// set (seedSecondWorkspace fixes catalog-admin perms; the deal suites
// need the deal + pipeline grants too).
func withPerms(ctx context.Context, ws ids.UUID, perms principal.Permissions) context.Context {
	rebound := principal.WithWorkspaceID(context.Background(), ws)
	rebound = principal.WithCorrelationID(rebound, ids.NewV7())
	actor, _ := principal.Actor(ctx)
	actor.Permissions = perms
	return principal.WithActor(rebound, actor)
}

// seedDealFixtureIn provisions the default pipeline in the context's
// workspace and returns its pipeline id plus the first open stage.
func seedDealFixtureIn(ctx context.Context, t *testing.T, store *deals.Store) (ids.PipelineID, ids.StageID) {
	t.Helper()
	if err := store.SeedDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := store.DefaultPipeline(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range *p.Stages {
		if st.Semantic == "open" {
			return ids.From[ids.PipelineKind](ids.UUID(p.Id)), ids.From[ids.StageKind](ids.UUID(st.Id))
		}
	}
	t.Fatal("default pipeline has no open stage")
	return ids.PipelineID{}, ids.StageID{}
}

// dealIDOf mirrors personIDOf/orgIDOf for the deal suites.
func dealIDOf(u ids.UUID) ids.DealID { return ids.From[ids.DealKind](u) }
