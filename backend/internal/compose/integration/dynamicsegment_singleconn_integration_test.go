// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A dynamic list's membership evaluation resolves its filter vocabulary
// through SegmentEngine, which reaches the field catalog — a transaction
// of its own against this store's pool. Run against a pool capped at one
// connection, the sequencing is what decides whether that read can ever
// acquire: reached while ListMembers already holds the pool's only
// connection open, it waits on a connection its own caller holds — a
// deadlock Postgres cannot break because it sees two sessions rather than
// one goroutine waiting on itself. Reached with nothing held, it acquires
// immediately. This is the integration half proving the sequencing fix
// ("the catalogue is read before a transaction opens, never inside one")
// actually holds, the way txseam_singleconn_integration_test.go proves the
// analogous shape for the record-store seams — same pool, same deadline
// mechanic, same reason one connection is what makes the defect
// deterministic instead of a timing accident that only shows up under load.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

var dynamicSegmentSingleConnPerms = principal.Permissions{
	RoleKeys: []string{"admin"},
	Objects: map[string]principal.ObjectGrant{
		"custom_field": {Create: true, Read: true, Update: true, Delete: true},
		"person":       {Create: true, Read: true, Update: true, Delete: true},
		"list":         {Create: true, Read: true, Update: true, Delete: true},
	},
	RowScope: principal.RowScopeAll,
}

// savedViewSingleConnPerms is the posture the saved-view arm needs. A view is
// per-user state gated on ownership, so the row scope here is not what decides
// visibility — it is set wide so a refusal in this test can only be the pool.
var savedViewSingleConnPerms = principal.Permissions{
	RoleKeys: []string{"admin"},
	Objects: map[string]principal.ObjectGrant{
		"custom_field": {Create: true, Read: true, Update: true, Delete: true},
		"saved_view":   {Create: true, Read: true, Update: true, Delete: true},
	},
	RowScope: principal.RowScopeAll,
}

// TestListMembersEvaluatesADynamicSegmentOnTheCallersOnlyConnection proves
// the sequencing: on a single-connection pool, with a real field catalog
// wired to that same pool, a dynamic list's ListMembers call must resolve
// SegmentEngine (and therefore the catalog's own transaction) without a
// transaction of ListMembers's own already open — otherwise this call
// times out rather than completing. A timeout here would be exactly the
// deadlock shape the fixed sequencing exists to remove.
func TestListMembersEvaluatesADynamicSegmentOnTheCallersOnlyConnection(t *testing.T) {
	e := Setup(t)
	pool := singleConnPool(t)
	svc := customfields.NewService(pool, SchemaPool(t))
	ctx, cancel := context.WithTimeout(e.As(e.Rep1, nil, dynamicSegmentSingleConnPerms), txSeamBudget)
	t.Cleanup(cancel)

	peopleStore := people.NewStore(harnessDB(pool, e.WS)).WithFieldCatalog(svc)
	lists := collections.NewStore(harnessDB(pool, e.WS)).WithFieldCatalog(svc)

	field, err := svc.Create(ctx, customfields.FieldSpec{
		Object: "person", Label: "Segment Budget", Type: customfields.TypeText, Source: "ui",
	})
	if err != nil {
		t.Fatalf("defining the field: %v", err)
	}
	if field.ColumnName == nil {
		t.Fatal("defined field carries no column_name")
	}
	column := *field.ColumnName

	created, err := peopleStore.CreatePerson(ctx, people.CreatePersonInput{FullName: "Match", Source: "ui"})
	if err != nil {
		t.Fatalf("creating the person: %v", err)
	}
	if _, err := peopleStore.UpdatePerson(ctx, ids.From[ids.PersonKind](ids.UUID(created.Id)), people.UpdatePersonInput{
		CustomFields: map[string]any{column: "gold"},
	}); err != nil {
		t.Fatalf("setting the custom field: %v", err)
	}

	listRow, err := lists.CreateList(ctx, collections.CreateListInput{
		Name: "Gold segment", EntityType: "person", ListType: "dynamic",
		Definition: map[string]any{"field": column, "op": "eq", "value": "gold"},
	})
	if err != nil {
		t.Fatalf("creating the dynamic list: %v", err)
	}

	rows, _, err := lists.ListMembers(ctx, listRow.ID, 50, "")
	if err != nil {
		t.Fatalf("evaluating the segment on the caller's only connection: %v — a timeout here "+
			"is the catalog read waiting for a second connection the membership evaluation's "+
			"own transaction holds", err)
	}
	if len(rows) != 1 || rows[0].EntityID != ids.UUID(created.Id) {
		t.Fatalf("members = %v, want exactly [%s]", rows, created.Id)
	}
}

// The saved-view half of the same obligation. Both write paths resolve the
// filter vocabulary before opening their own transaction, and both are held to
// it here: move either resolution inside its write transaction and this times
// out.
//
// The update arm needs its own case rather than trusting the create arm,
// because it carries a sequencing decision the create path does not — it reads
// the stored view (one transaction) to learn the resource before resolving the
// vocabulary (another) and then writing (a third). Three sequential
// acquisitions on one connection pass; any nesting among them does not.
func TestSavedViewValidatesItsFilterOnTheCallersOnlyConnection(t *testing.T) {
	e := Setup(t)
	pool := singleConnPool(t)
	svc := customfields.NewService(pool, SchemaPool(t))
	ctx, cancel := context.WithTimeout(e.As(e.Rep1, nil, savedViewSingleConnPerms), txSeamBudget)
	t.Cleanup(cancel)

	views := collections.NewStore(harnessDB(pool, e.WS)).WithFieldCatalog(svc)

	// A cf_ column NAMED IN THE FILTER is what makes the catalogue read happen
	// at all: a core-field filter resolves from the static vocabulary, the
	// second acquisition never occurs, and the case would pass proving nothing.
	field, err := svc.Create(ctx, customfields.FieldSpec{
		Object: "person", Label: "View Budget", Type: customfields.TypeText, Source: "ui",
	})
	if err != nil {
		t.Fatalf("defining the field: %v", err)
	}
	if field.ColumnName == nil {
		t.Fatal("defined field carries no column_name")
	}
	filter := map[string]any{
		"filter": map[string]any{"field": *field.ColumnName, "op": "eq", "value": "gold"},
	}

	view, err := views.CreateSavedView(ctx, collections.CreateSavedViewInput{
		Resource: "people", Name: "Gold accounts", Query: filter,
	})
	if err != nil {
		t.Fatalf("creating the view on the caller's only connection: %v — a timeout here is the "+
			"catalogue read waiting for a second connection the write transaction holds", err)
	}

	replacement := map[string]any{
		"filter": map[string]any{"field": *field.ColumnName, "op": "eq", "value": "silver"},
	}
	if _, err := views.UpdateSavedView(ctx, view.ID, collections.UpdateSavedViewInput{Query: &replacement}); err != nil {
		t.Fatalf("updating the view on the caller's only connection: %v — a timeout here is the "+
			"stored-view read, the catalogue read, or the write nesting inside one another", err)
	}
}
