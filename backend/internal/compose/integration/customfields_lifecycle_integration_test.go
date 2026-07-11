// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The customfields catalog-lifecycle suite: rename/retire (app-pool,
// catalog-only), the picklist CHECK regeneration, the admin list, and
// the RBAC/RLS boundaries. The schema-pool tx dance itself is proven in
// customfields_integration_test.go, which also owns the shared fixtures
// (cfAdminPerms, columnOnTable, seedSecondWorkspace, ...).

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestCustomFieldRename_LabelOnly_ColumnIdentityStable(t *testing.T) {
	e := Setup(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))
	ctx := e.As(e.Rep1, nil, cfAdminPerms)

	created, err := svc.Create(ctx, dateSpec("Renewal date"))
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := svc.Rename(ctx, ids.UUID(created.Id), "Contract renewal", nil)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if renamed.Label != "Contract renewal" {
		t.Fatalf("label = %q, want the new label", renamed.Label)
	}
	if *renamed.ColumnName != *created.ColumnName || renamed.Slug != created.Slug {
		t.Fatalf("rename must never move column_name/slug: %q/%q -> %q/%q",
			*created.ColumnName, created.Slug, *renamed.ColumnName, renamed.Slug)
	}
	if *renamed.Version != *created.Version+1 {
		t.Fatalf("version = %d, want the touch-trigger bump to %d", *renamed.Version, *created.Version+1)
	}

	stale := *created.Version // one behind after the rename
	if _, err := svc.Rename(ctx, ids.UUID(created.Id), "Another", &stale); !errors.Is(err, apperrors.ErrVersionSkew) {
		t.Fatalf("a stale If-Match must answer version skew, got %v", err)
	}
	if _, err := svc.Rename(ctx, ids.NewV7(), "Ghost", nil); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("renaming a missing field must answer not-found, got %v", err)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'custom_field' AND entity_id = $1 AND action = 'update'`,
		ids.UUID(created.Id)); n != 1 {
		t.Fatalf("exactly the one successful rename may audit, got %d", n)
	}
}

func TestCustomFieldRetire_PreservesColumnAndValues(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))
	ctx := e.As(e.Rep1, nil, cfAdminPerms)

	created, err := svc.Create(ctx, customfields.FieldSpec{
		Object: "person", Label: "Preferred greeting", Type: customfields.TypeText, Source: "ui",
	})
	if err != nil {
		t.Fatal(err)
	}
	person := e.SeedPerson(t, "Ada", &e.Rep1)
	e.WsExec(t, `UPDATE person SET cf_preferred_greeting = 'Servus' WHERE id = $1`, person)

	retired, err := svc.Retire(ctx, ids.UUID(created.Id))
	if err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if string(retired.Status) != "retired" || retired.ArchivedAt != nil {
		t.Fatalf("retire is a status flip with archived_at untouched, got status=%s archived_at=%v",
			retired.Status, retired.ArchivedAt)
	}
	if !columnOnTable(t, owner, "person", "cf_preferred_greeting") {
		t.Fatal("retire must never drop the physical column")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM person WHERE cf_preferred_greeting = 'Servus'`); n != 1 {
		t.Fatal("retire must preserve every stored value")
	}

	// Retiring again is a no-op: same terminal row back, no second audit.
	again, err := svc.Retire(ctx, ids.UUID(created.Id))
	if err != nil {
		t.Fatalf("second Retire: %v", err)
	}
	if *again.Version != *retired.Version {
		t.Fatal("an idempotent retire must not bump the version")
	}

	// Retirement is terminal: the label is frozen, and the refusal writes
	// nothing — the audit trail still carries only the one retire.
	if _, err := svc.Rename(ctx, ids.UUID(created.Id), "Reopened greeting", nil); !errors.Is(err, customfields.ErrFieldRetired) {
		t.Fatalf("renaming a retired field must refuse with ErrFieldRetired, got %v", err)
	}
	if !errors.Is(customfields.ErrFieldRetired, apperrors.ErrConflict) {
		t.Fatal("ErrFieldRetired must read as the 409 conflict sentinel")
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'custom_field' AND entity_id = $1 AND action = 'update'`,
		ids.UUID(created.Id)); n != 1 {
		t.Fatalf("only the first retire may audit, got %d", n)
	}
}

func TestCustomFieldSetOptions_RegeneratesTheCheck(t *testing.T) {
	e := Setup(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))
	ctx := e.As(e.Rep1, nil, cfAdminPerms)

	created, err := svc.Create(ctx, customfields.FieldSpec{
		Object: "person", Label: "Procurement route", Type: customfields.TypePicklist,
		Options: []string{"direct", "reseller"}, Source: "ui",
	})
	if err != nil {
		t.Fatal(err)
	}
	person := e.SeedPerson(t, "Ada", &e.Rep1)
	e.WsExec(t, `UPDATE person SET cf_procurement_route = 'direct' WHERE id = $1`, person)

	updated, err := svc.SetOptions(ctx, ids.UUID(created.Id), []string{"direct", "marketplace"})
	if err != nil {
		t.Fatalf("SetOptions: %v", err)
	}
	if updated.Options == nil || len(*updated.Options) != 2 || (*updated.Options)[1] != "marketplace" {
		t.Fatalf("catalog options = %v, want the replacement set", updated.Options)
	}
	// The regenerated CHECK admits the new value and refuses the removed one.
	if err := wsExecErr(e, e.WS, `UPDATE person SET cf_procurement_route = 'marketplace' WHERE id = $1`, person); err != nil {
		t.Fatalf("a newly allowed option must be writable, got %v", err)
	}
	if err := wsExecErr(e, e.WS, `UPDATE person SET cf_procurement_route = 'reseller' WHERE id = $1`, person); err == nil {
		t.Fatal("a removed option must be rejected by the regenerated CHECK")
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'custom_field' AND entity_id = $1 AND action = 'update'`,
		ids.UUID(created.Id)); n != 1 {
		t.Fatalf("the options edit must write exactly one audit row, got %d", n)
	}
}

func TestCustomFieldSetOptions_Refusals(t *testing.T) {
	e := Setup(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))
	ctx := e.As(e.Rep1, nil, cfAdminPerms)

	picklist, err := svc.Create(ctx, customfields.FieldSpec{
		Object: "person", Label: "Procurement route", Type: customfields.TypePicklist,
		Options: []string{"direct", "reseller"}, Source: "ui",
	})
	if err != nil {
		t.Fatal(err)
	}
	date, err := svc.Create(ctx, customfields.FieldSpec{
		Object: "person", Label: "Onboarding date", Type: customfields.TypeDate, Source: "ui",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.SetOptions(ctx, ids.UUID(date.Id), []string{"a"}); !errors.Is(err, customfields.ErrNotPicklist) {
		t.Fatalf("options on a non-picklist must refuse with ErrNotPicklist, got %v", err)
	}
	if _, err := svc.SetOptions(ctx, ids.UUID(picklist.Id), nil); !errors.Is(err, customfields.ErrLastOption) {
		t.Fatalf("an empty option set must refuse with ErrLastOption, got %v", err)
	}
	if _, err := svc.SetOptions(ctx, ids.NewV7(), []string{"a"}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("a missing field must answer not-found, got %v", err)
	}

	// Removing an option that stored values still use refuses honestly:
	// ADD CONSTRAINT validates existing rows.
	person := e.SeedPerson(t, "Ada", &e.Rep1)
	e.WsExec(t, `UPDATE person SET cf_procurement_route = 'reseller' WHERE id = $1`, person)
	if _, err := svc.SetOptions(ctx, ids.UUID(picklist.Id), []string{"direct"}); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("removing an in-use option must answer the conflict sentinel, got %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM person WHERE cf_procurement_route = 'reseller'`); n != 1 {
		t.Fatal("the refused edit must leave stored values untouched")
	}

	// A retired picklist's options are frozen: retirement is terminal.
	if _, err := svc.Retire(ctx, ids.UUID(picklist.Id)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetOptions(ctx, ids.UUID(picklist.Id), []string{"direct", "reseller"}); !errors.Is(err, customfields.ErrFieldRetired) {
		t.Fatalf("options on a retired field must refuse with ErrFieldRetired, got %v", err)
	}
}

func TestCustomFieldList_IncludesRetiredByDefault_AndPagesKeyset(t *testing.T) {
	e := Setup(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))
	ctx := e.As(e.Rep1, nil, cfAdminPerms)

	var created []ids.UUID
	for _, label := range []string{"Alpha", "Beta", "Gamma"} {
		f, err := svc.Create(ctx, customfields.FieldSpec{
			Object: "deal", Label: label, Type: customfields.TypeText, Source: "ui",
		})
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, ids.UUID(f.Id))
	}
	if _, err := svc.Retire(ctx, created[1]); err != nil {
		t.Fatal(err)
	}

	all, _, err := svc.List(ctx, customfields.ListInput{Object: "deal"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("the admin list includes retired rows by default: got %d, want 3", len(all))
	}
	active := "active"
	activeOnly, _, err := svc.List(ctx, customfields.ListInput{Object: "deal", Status: &active})
	if err != nil {
		t.Fatal(err)
	}
	if len(activeOnly) != 2 {
		t.Fatalf("status=active must narrow to 2, got %d", len(activeOnly))
	}

	// Keyset walk at page size 1: every row exactly once, then the end.
	seen := map[string]bool{}
	limit := 1
	var cursor *string
	for range 3 {
		page, info, err := svc.List(ctx, customfields.ListInput{Object: "deal", Limit: &limit, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) != 1 {
			t.Fatalf("page of limit 1 returned %d rows", len(page))
		}
		id := page[0].Id.String()
		if seen[id] {
			t.Fatalf("keyset walk repeated row %s", id)
		}
		seen[id] = true
		if !info.HasMore {
			break
		}
		cursor = &info.NextCursor
	}
	if len(seen) != 3 {
		t.Fatalf("keyset walk visited %d of 3 rows", len(seen))
	}
}

func TestCustomFieldRBAC_ReadGrantCannotChangeSchema(t *testing.T) {
	e := Setup(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))
	admin := e.As(e.Rep1, nil, cfAdminPerms)
	reader := e.As(e.Rep2, nil, cfReadPerms)
	ungranted := e.As(e.Rep3, nil, RepPerms) // no custom_field object at all

	created, err := svc.Create(admin, dateSpec("Renewal date"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Create(reader, dateSpec("Another field")); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("a read grant must not create, got %v", err)
	}
	if _, err := svc.Rename(reader, ids.UUID(created.Id), "X", nil); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("a read grant must not rename, got %v", err)
	}
	if _, err := svc.Retire(reader, ids.UUID(created.Id)); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("a read grant must not retire, got %v", err)
	}
	if _, _, err := svc.List(ungranted, customfields.ListInput{Object: "deal"}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("no custom_field grant at all must not even list, got %v", err)
	}
	if fields, _, err := svc.List(reader, customfields.ListInput{Object: "deal"}); err != nil || len(fields) != 1 {
		t.Fatalf("the read grant lists the catalog: %v / %d rows", err, len(fields))
	}
}

func TestCustomFieldRLS_IsolatesCatalogsAcrossWorkspaces(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))

	created, err := svc.Create(e.As(e.Rep1, nil, cfAdminPerms), dateSpec("Renewal date"))
	if err != nil {
		t.Fatal(err)
	}
	_, ctxB := seedSecondWorkspace(t, owner)

	fields, _, err := svc.List(ctxB, customfields.ListInput{Object: "deal"})
	if err != nil {
		t.Fatalf("List in workspace B: %v", err)
	}
	if len(fields) != 0 {
		t.Fatalf("workspace B must see nothing of A's catalog, got %d rows", len(fields))
	}
	if _, err := svc.Rename(ctxB, ids.UUID(created.Id), "Hijack", nil); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("A's row must read as nonexistent from B, got %v", err)
	}
	if _, err := svc.SetOptions(ctxB, ids.UUID(created.Id), []string{"a"}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("A's row must be untouchable from B's options path, got %v", err)
	}
}
