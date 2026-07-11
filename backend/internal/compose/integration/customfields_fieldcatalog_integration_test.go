// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The fieldcatalog cross-module seam (shared/ports/fieldcatalog): proves
// modules/customfields' Service satisfies the port a record store
// depends on, and exercises the three invariants that seam promises a
// record store — active-only, per-object, and workspace-scoped — over a
// real migrated Postgres. The Create/Retire/atomicity mechanics
// themselves are customfields_integration_test.go's charter; this suite
// only drives the read side compose will inject into people/deals.

import (
	"sort"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// var _ fieldcatalog.Reader documents the seam at its call site: the
// compile-time proof that *customfields.Service satisfies the port
// people/deals will depend on instead of the concrete module (T2 wires
// the injection; this line is what would fail to compile first if the
// two drifted apart).
var _ fieldcatalog.Reader = (*customfields.Service)(nil)

func columnNames(cols []fieldcatalog.Column) []string {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	sort.Strings(names)
	return names
}

func TestActiveColumns_ActiveOnly_ExcludesRetired(t *testing.T) {
	e := Setup(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))
	ctx := e.As(e.Rep1, nil, cfAdminPerms)

	stayer, err := svc.Create(ctx, customfields.FieldSpec{
		Object: "person", Label: "Preferred greeting", Type: customfields.TypeText, Source: "ui",
	})
	if err != nil {
		t.Fatal(err)
	}
	toRetire, err := svc.Create(ctx, customfields.FieldSpec{
		Object: "person", Label: "Legacy note", Type: customfields.TypeText, Source: "ui",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Retire(ctx, ids.UUID(toRetire.Id)); err != nil {
		t.Fatalf("Retire: %v", err)
	}

	cols, err := svc.ActiveColumns(ctx, "person")
	if err != nil {
		t.Fatalf("ActiveColumns: %v", err)
	}
	got := columnNames(cols)
	if len(got) != 1 || got[0] != *stayer.ColumnName {
		t.Fatalf("ActiveColumns(person) = %v, want only %q (retired field must be excluded)", got, *stayer.ColumnName)
	}
	for _, c := range cols {
		if c.Type != customfields.TypeText {
			t.Fatalf("Column.Type = %q, want %q", c.Type, customfields.TypeText)
		}
	}
}

func TestActiveColumns_PerObject_DoesNotLeakAcrossObjects(t *testing.T) {
	e := Setup(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))
	ctx := e.As(e.Rep1, nil, cfAdminPerms)

	personField, err := svc.Create(ctx, customfields.FieldSpec{
		Object: "person", Label: "Person only", Type: customfields.TypeBoolean, Source: "ui",
	})
	if err != nil {
		t.Fatal(err)
	}
	dealField, err := svc.Create(ctx, dateSpec("Deal only"))
	if err != nil {
		t.Fatal(err)
	}

	personCols, err := svc.ActiveColumns(ctx, "person")
	if err != nil {
		t.Fatalf("ActiveColumns(person): %v", err)
	}
	if got := columnNames(personCols); len(got) != 1 || got[0] != *personField.ColumnName {
		t.Fatalf("ActiveColumns(person) = %v, want only %q — a deal field must never leak into person's columns", got, *personField.ColumnName)
	}

	dealCols, err := svc.ActiveColumns(ctx, "deal")
	if err != nil {
		t.Fatalf("ActiveColumns(deal): %v", err)
	}
	if got := columnNames(dealCols); len(got) != 1 || got[0] != *dealField.ColumnName {
		t.Fatalf("ActiveColumns(deal) = %v, want only %q — a person field must never leak into deal's columns", got, *dealField.ColumnName)
	}
}

func TestActiveColumns_WorkspaceScoped_TenantBSeesNoneOfTenantAs(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))
	ctxA := e.As(e.Rep1, nil, cfAdminPerms)

	if _, err := svc.Create(ctxA, customfields.FieldSpec{
		Object: "person", Label: "Tenant A field", Type: customfields.TypeText, Source: "ui",
	}); err != nil {
		t.Fatal(err)
	}

	_, ctxB := seedSecondWorkspace(t, owner)
	colsB, err := svc.ActiveColumns(ctxB, "person")
	if err != nil {
		t.Fatalf("ActiveColumns as tenant B: %v", err)
	}
	if len(colsB) != 0 {
		t.Fatalf("tenant B's identical object query must carry none of tenant A's columns, got %v", columnNames(colsB))
	}

	// Sanity: tenant A still sees its own column — the empty result above
	// is RLS scoping, not an empty catalog.
	colsA, err := svc.ActiveColumns(ctxA, "person")
	if err != nil {
		t.Fatalf("ActiveColumns as tenant A: %v", err)
	}
	if len(colsA) != 1 {
		t.Fatalf("tenant A must still see its own column, got %v", columnNames(colsA))
	}
}

func TestActiveColumns_NoActiveFields_ReturnsEmptyNotError(t *testing.T) {
	e := Setup(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))
	ctx := e.As(e.Rep1, nil, cfAdminPerms)

	cols, err := svc.ActiveColumns(ctx, "activity")
	if err != nil {
		t.Fatalf("ActiveColumns with no fields defined: %v", err)
	}
	if len(cols) != 0 {
		t.Fatalf("got %v, want empty", cols)
	}
}
