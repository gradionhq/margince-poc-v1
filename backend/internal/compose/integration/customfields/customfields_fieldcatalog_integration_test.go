// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package customfields

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

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	customfieldsmod "github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// var _ fieldcatalog.Reader documents the seam at its call site: the
// compile-time proof that *customfieldsmod.Service satisfies the port
// people/deals will depend on instead of the concrete module (T2 wires
// the injection; this line is what would fail to compile first if the
// two drifted apart).
var _ fieldcatalog.Reader = (*customfieldsmod.Service)(nil)

// var _ fieldcatalog.FilterableReader documents the same seam for the FILTER
// vocabulary — the columns a filter may name, retired ones included, which is
// a different question from the ones a record store may write. collections
// consumes it through WithFieldCatalog, so a drift would surface there too;
// this line names the obligation at the implementation rather than leaving it
// to whichever consumer happens to compile first.
var _ fieldcatalog.FilterableReader = (*customfieldsmod.Service)(nil)

func columnNames(cols []fieldcatalog.Column) []string {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	sort.Strings(names)
	return names
}

func TestActiveColumns_ActiveOnly_ExcludesRetired(t *testing.T) {
	e := integration.Setup(t)
	svc := customfieldsmod.NewService(e.Pool, integration.SchemaPool(t))
	ctx := e.As(e.Rep1, nil, integration.CustomFieldAdminPerms)

	stayer, err := svc.Create(ctx, customfieldsmod.FieldSpec{
		Object: "person", Label: "Preferred greeting", Type: customfieldsmod.TypeText, Source: "ui",
	})
	if err != nil {
		t.Fatal(err)
	}
	toRetire, err := svc.Create(ctx, customfieldsmod.FieldSpec{
		Object: "person", Label: "Legacy note", Type: customfieldsmod.TypeText, Source: "ui",
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
		if c.Type != customfieldsmod.TypeText {
			t.Fatalf("Column.Type = %q, want %q", c.Type, customfieldsmod.TypeText)
		}
	}
}

func TestActiveColumns_PerObject_DoesNotLeakAcrossObjects(t *testing.T) {
	e := integration.Setup(t)
	svc := customfieldsmod.NewService(e.Pool, integration.SchemaPool(t))
	ctx := e.As(e.Rep1, nil, integration.CustomFieldAdminPerms)

	personField, err := svc.Create(ctx, customfieldsmod.FieldSpec{
		Object: "person", Label: "Person only", Type: customfieldsmod.TypeBoolean, Source: "ui",
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

func TestActiveColumns_NoActiveFields_ReturnsEmptyNotError(t *testing.T) {
	e := integration.Setup(t)
	svc := customfieldsmod.NewService(e.Pool, integration.SchemaPool(t))
	ctx := e.As(e.Rep1, nil, integration.CustomFieldAdminPerms)

	cols, err := svc.ActiveColumns(ctx, "activity")
	if err != nil {
		t.Fatalf("ActiveColumns with no fields defined: %v", err)
	}
	if len(cols) != 0 {
		t.Fatalf("got %v, want empty", cols)
	}
}

func hasColumn(cols []fieldcatalog.Column, name string) bool {
	for _, c := range cols {
		if c.Name == name {
			return true
		}
	}
	return false
}

// A retired field's column survives retirement (retirement is a status change,
// not a DROP), and a saved segment filtering on it must keep evaluating. So the
// FILTER vocabulary sees it while the WRITE vocabulary does not: two questions,
// two methods, and conflating them would let a retired field become writable.
func TestFilterableColumnsSeesRetiredFieldsAndActiveColumnsDoesNot(t *testing.T) {
	e := integration.Setup(t)
	svc := customfieldsmod.NewService(e.Pool, integration.SchemaPool(t))
	ctx := e.As(e.Rep1, nil, integration.CustomFieldAdminPerms)

	live, err := svc.Create(ctx, customfieldsmod.FieldSpec{
		Object: "person", Label: "Still live", Type: customfieldsmod.TypeText, Source: "ui",
	})
	if err != nil {
		t.Fatal(err)
	}
	gone, err := svc.Create(ctx, customfieldsmod.FieldSpec{
		Object: "person", Label: "Long gone", Type: customfieldsmod.TypeText, Source: "ui",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Retire(ctx, ids.UUID(gone.Id)); err != nil {
		t.Fatalf("Retire: %v", err)
	}

	active, err := svc.ActiveColumns(ctx, "person")
	if err != nil {
		t.Fatalf("ActiveColumns: %v", err)
	}
	filterable, err := svc.FilterableColumns(ctx, "person")
	if err != nil {
		t.Fatalf("FilterableColumns: %v", err)
	}

	if hasColumn(active, *gone.ColumnName) {
		t.Error("ActiveColumns returned a retired column; a retired field must not become writable")
	}
	if !hasColumn(filterable, *gone.ColumnName) {
		t.Error("FilterableColumns omitted a retired column, so a saved segment on it would 422")
	}
	if !hasColumn(filterable, *live.ColumnName) {
		t.Error("FilterableColumns omitted an active column")
	}
	if !sort.SliceIsSorted(filterable, func(i, j int) bool {
		return filterable[i].Name < filterable[j].Name
	}) {
		t.Error("FilterableColumns is unordered; the vocabulary must be deterministic")
	}
}
