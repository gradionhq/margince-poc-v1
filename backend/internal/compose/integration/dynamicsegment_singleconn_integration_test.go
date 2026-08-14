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
