// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The customfields engine suite (decisions/0024): the one-transaction
// schema-pool dance — real ALTER TABLE + catalog INSERT + audit row
// landing or rolling back together over a real migrated Postgres — plus
// the catalog lifecycle, the cross-workspace column-namespace answer,
// RBAC, and RLS isolation.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// CFAdminPerms is the decisions/0006 admin posture narrowed to what this
// suite exercises: full custom_field config authority plus the person
// grants the value-preservation assertions need.
var cfAdminPerms = principal.Permissions{
	RoleKeys: []string{"admin"},
	Objects: map[string]principal.ObjectGrant{
		"custom_field": {Create: true, Read: true, Update: true, Delete: true},
		"person":       {Create: true, Read: true, Update: true, Delete: true},
	},
	RowScope: principal.RowScopeAll,
}

// cfReadPerms mirrors the rep/manager/read_only posture: catalog read
// only, never a schema change.
var cfReadPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"custom_field": {Read: true},
	},
	RowScope: principal.RowScopeTeam,
}

// columnOnTable reports whether the physical column exists — the
// information_schema probe both the atomicity and rollback proofs read.
func columnOnTable(t *testing.T, owner *pgx.Conn, table, column string) bool {
	t.Helper()
	var exists bool
	if err := owner.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2)`,
		table, column).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

// wsExecErr runs one statement workspace-bound and returns its error —
// for assertions that a write is REFUSED by the database (WsExec fatals).
func wsExecErr(e *Env, ws ids.UUID, sql string, args ...any) error {
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	return database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sql, args...)
		return err
	})
}

// seedSecondWorkspace provisions a second tenant (workspace + one user)
// and returns an admin-shaped context bound to it, for the cross-tenant
// suites.
func seedSecondWorkspace(t *testing.T, owner *pgx.Conn) (ids.UUID, context.Context) {
	t.Helper()
	ws, user := ids.NewV7(), ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Tenant B', $2, 'EUR')`,
		ws, "tenant-b-"+ws.String()[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'B Admin')`,
		user, ws, "b@tenant-b.test"); err != nil {
		t.Fatal(err)
	}
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(),
		UserID: user, Permissions: cfAdminPerms,
	})
	return ws, ctx
}

func dateSpec(label string) customfields.FieldSpec {
	return customfields.FieldSpec{Object: "deal", Label: label, Type: customfields.TypeDate, Source: "ui"}
}

func TestCustomFieldCreate_ColumnCatalogAndAuditLandTogether(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))
	ctx := e.As(e.Rep1, nil, cfAdminPerms)

	created, err := svc.Create(ctx, dateSpec("Renewal date"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ColumnName == nil || *created.ColumnName != "cf_renewal_date" {
		t.Fatalf("column_name = %v, want the slug-derived cf_renewal_date", created.ColumnName)
	}
	if string(created.Status) != "active" || created.ArchivedAt != nil {
		t.Fatalf("a fresh field must be active with archived_at null, got %+v", created)
	}
	if created.Version == nil || *created.Version != 1 {
		t.Fatalf("a fresh field must carry version 1, got %v", created.Version)
	}
	if !columnOnTable(t, owner, "deal", "cf_renewal_date") {
		t.Fatal("the physical column must exist after a committed create")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM custom_field WHERE id = $1`, ids.UUID(created.Id)); n != 1 {
		t.Fatalf("catalog rows = %d, want 1", n)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'custom_field' AND entity_id = $1 AND action = 'create'`,
		ids.UUID(created.Id)); n != 1 {
		t.Fatalf("audit rows = %d, want exactly 1", n)
	}
}

// TestCustomFieldCreate_AtomicRollback_OnCatalogConflict proves the
// three-way atomicity (CUSTOM-FIELDS-AC-2/AC-10): a catalog row is
// pre-seeded claiming the SLUG under a different column name, so the
// engine's column-namespace pre-check passes, the ALTER runs, and the
// catalog INSERT then fails on the per-workspace unique index — the
// whole transaction, physical column included, must roll back. Postgres
// DDL is transactional; this is the real proof, not a mock.
func TestCustomFieldCreate_AtomicRollback_OnCatalogConflict(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))
	ctx := e.As(e.Rep1, nil, cfAdminPerms)

	// Same (workspace, object, slug), different column_name: invisible to
	// the pre-check, fatal to the INSERT.
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO custom_field (workspace_id, object, slug, label, type, column_name, created_by)
		 VALUES ($1, 'deal', 'renewal_date', 'Pre-existing', 'text', 'cf_renewal_date_other', $2)`,
		e.WS, e.Rep1); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Create(ctx, dateSpec("Renewal date"))
	if !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("a catalog collision must answer the 409 conflict sentinel, got %v", err)
	}
	if columnOnTable(t, owner, "deal", "cf_renewal_date") {
		t.Fatal("the ALTER TABLE must roll back with the failed catalog insert — the column survived")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM custom_field WHERE object = 'deal' AND slug = 'renewal_date'`); n != 1 {
		t.Fatalf("only the pre-seeded catalog row may remain, got %d", n)
	}
}

func TestCustomFieldCreate_CrossWorkspaceCollision_AnswersColumnTaken(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))

	if _, err := svc.Create(e.As(e.Rep1, nil, cfAdminPerms), dateSpec("Renewal date")); err != nil {
		t.Fatalf("first workspace's create: %v", err)
	}
	wsB, ctxB := seedSecondWorkspace(t, owner)

	_, err := svc.Create(ctxB, dateSpec("Renewal date"))
	var taken *customfields.ColumnTakenError
	if !errors.As(err, &taken) {
		t.Fatalf("the second workspace's identical slug must answer ColumnTakenError, got %v", err)
	}
	if !errors.Is(err, apperrors.ErrConflict) {
		t.Fatal("ColumnTakenError must read as the 409 conflict sentinel")
	}
	if !columnOnTable(t, owner, "deal", "cf_renewal_date") {
		t.Fatal("the FIRST workspace's column must survive the refused second claim")
	}
	var n int
	if err := owner.QueryRow(context.Background(),
		`SELECT count(*) FROM custom_field WHERE workspace_id = $1`, wsB).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the refused workspace must gain no catalog row, got %d", n)
	}
}

func TestCustomFieldCreate_RefusalsWriteNothing(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))
	ctx := e.As(e.Rep1, nil, cfAdminPerms)

	if _, err := svc.Create(ctx, customfields.FieldSpec{
		Object: "deal", Label: "Link to invoice system", Type: customfields.TypeText, Source: "ui",
	}); !errors.Is(err, customfields.ErrStructural) {
		t.Fatalf("structural label must refuse with ErrStructural, got %v", err)
	}
	var verr *customfields.ValidationError
	if _, err := svc.Create(ctx, customfields.FieldSpec{
		Object: "deal", Label: "Budget", Type: "money", Source: "ui",
	}); !errors.As(err, &verr) {
		t.Fatalf("unknown type must refuse with ValidationError, got %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM custom_field`); n != 0 {
		t.Fatalf("refusals must write no catalog row, got %d", n)
	}
	if columnOnTable(t, owner, "deal", "cf_link_to_invoice_system") || columnOnTable(t, owner, "deal", "cf_budget") {
		t.Fatal("refusals must add no physical column")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM audit_log WHERE entity_type = 'custom_field'`); n != 0 {
		t.Fatalf("refusals must write no audit row, got %d", n)
	}
}

func TestCustomFieldCreate_UnwiredSchemaPool_Answers501Sentinel(t *testing.T) {
	e := Setup(t)
	svc := customfields.NewService(e.Pool, nil)
	ctx := e.As(e.Rep1, nil, cfAdminPerms)

	if _, err := svc.Create(ctx, dateSpec("Renewal date")); !errors.Is(err, customfields.ErrSchemaChangesUnavailable) {
		t.Fatalf("an unwired schema pool must refuse with ErrSchemaChangesUnavailable, got %v", err)
	}
	if _, err := svc.SetOptions(ctx, ids.NewV7(), []string{"a"}); !errors.Is(err, customfields.ErrSchemaChangesUnavailable) {
		t.Fatalf("SetOptions on an unwired schema pool must refuse too, got %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM custom_field`); n != 0 {
		t.Fatalf("the unwired refusal must write nothing, got %d", n)
	}
}
