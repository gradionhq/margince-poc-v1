// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The deals-store half of the custom-field VALUES coverage (CF-T05, arc
// 2a-ii T3): the same fieldcatalog seam wired into the deals store —
// active cf_* columns ride create/update writes and get/list reads like
// core fields, with the same drop-on-mismatch and workspace-isolation
// posture the person/organization suites prove.
//
// BOTH records this store writes are covered here, deal and project, and
// the second one is why the pairing matters. Person, organization, lead
// and deal each had a create-with-a-custom-field case; project had none,
// and project was the one whose INSERT numbered its first custom
// placeholder over its own last fixed bind — so every CreateProject
// carrying a custom-field value failed on a bind-count mismatch, and
// nothing said so. A record whose writer splices custom columns is not
// covered until something creates it WITH one.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/installseam"
	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// dealCFVPerms adds the grants the two round trips need on top of the
// catalog-admin posture: deal + pipeline for the deal, and project +
// organization for the project, which is always hung off a company.
var dealCFVPerms = principal.Permissions{
	RoleKeys: []string{"admin"},
	Objects: map[string]principal.ObjectGrant{
		"custom_field":          {Create: true, Read: true, Update: true, Delete: true},
		"deal":                  {Create: true, Read: true, Update: true, Delete: true},
		"pipeline":              {Create: true, Read: true, Update: true, Delete: true},
		"project":               {Create: true, Read: true, Update: true, Delete: true},
		"organization":          {Create: true, Read: true, Update: true, Delete: true},
		"installation_settings": {Read: true},
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
		store:    deals.NewStore(e.DB(), installseam.Deals()).WithFieldCatalog(svc),
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

// The project round trip, and the case the deal one above could not stand
// in for: the two records share a store and a seam but not a statement, and
// it was project's statement that mis-numbered its first custom placeholder.
// Create is the assertion that matters — the write itself used to fail — and
// the read-back is what proves the value reached its own column rather than a
// bind that happened to accept it.
func TestCustomFieldValues_ProjectRoundTrip(t *testing.T) {
	f := setupDealCFV(t)
	col := f.defineDealField(t, customfields.FieldSpec{Object: "project", Label: "Engagement Model", Type: customfields.TypeText, Source: "ui"})
	org := f.e.SeedOrg(t, "Northwind", nil)

	created, err := f.store.CreateProject(f.ctx, deals.CreateProjectInput{
		Name: "Rollout", OrganizationID: orgIDOf(org), Source: "ui",
		CustomFields: map[string]any{col: "retainer"},
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	assertCF(t, created.AdditionalProperties, col, "retainer")

	got, err := f.store.GetProject(f.ctx, projectIDOf(ids.UUID(created.Id)), storekit.LiveOnly)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	assertCF(t, got.AdditionalProperties, col, "retainer")

	updated, err := f.store.UpdateProject(f.ctx, projectIDOf(ids.UUID(created.Id)), deals.UpdateProjectInput{
		CustomFields: map[string]any{col: "fixed-price"},
	})
	if err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	assertCF(t, updated.AdditionalProperties, col, "fixed-price")
}

// dealIDOf mirrors PersonIDOf/orgIDOf for the deal suites.
func dealIDOf(u ids.UUID) ids.DealID { return ids.From[ids.DealKind](u) }
