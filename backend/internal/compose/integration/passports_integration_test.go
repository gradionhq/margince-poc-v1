// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The passport list surface (GET /passports, feedback/13): metadata
// only, own rows for a regular user, workspace-wide for the admin role
// — the same authority split RevokePassport enforces. The HTTP-level
// token-never-re-disclosed assertion rides the e2e agent suite; this
// lane pins the service-layer scoping against the real migrated schema.

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/testdb"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type passportsEnv struct {
	svc   *identity.Service
	WS    ids.UUID
	alice ids.UUID
	bob   ids.UUID
}

func setupPassports(t *testing.T) *passportsEnv {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()

	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	if err := testdb.EnsureSchema(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := testdb.Truncate(ctx, owner); err != nil {
		t.Fatal(err)
	}

	e := &passportsEnv{WS: ids.NewV7(), alice: ids.NewV7(), bob: ids.NewV7()}
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Passports', 'passports', 'EUR')`, e.WS); err != nil {
		t.Fatal(err)
	}
	for i, user := range []ids.UUID{e.alice, e.bob} {
		if _, err := owner.Exec(ctx,
			`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'User')`,
			user, e.WS, string(rune('a'+i))+"@passports.test"); err != nil {
			t.Fatal(err)
		}
	}

	var pool *pgxpool.Pool
	pool, err = database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e.svc = identity.NewService(pool)
	return e
}

func (e *passportsEnv) identityFor(user ids.UUID, roles []string) identity.Identity {
	return identity.Identity{UserID: ids.From[ids.UserKind](user), WorkspaceID: ids.From[ids.WorkspaceKind](e.WS), Roles: roles}
}

func (e *passportsEnv) ctx() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// A user lists exactly their own passports; the admin role sees the
// workspace's; the rows are metadata only.
func TestListPassportsScopesToOwnerUnlessAdmin(t *testing.T) {
	e := setupPassports(t)
	ctx := e.ctx()

	label := func(s string) *string { return &s }
	aliceIssued, err := e.svc.IssuePassport(ctx, e.identityFor(e.alice, []string{"rep"}),
		identity.IssuePassportInput{Label: label("alice claude"), Scopes: []string{"read"}})
	if err != nil {
		t.Fatalf("alice mint: %v", err)
	}
	if _, err := e.svc.IssuePassport(ctx, e.identityFor(e.bob, []string{"rep"}),
		identity.IssuePassportInput{Label: label("bob cursor"), Scopes: []string{"read", "draft"}}); err != nil {
		t.Fatalf("bob mint: %v", err)
	}

	aliceRows, err := e.svc.ListPassports(ctx, e.identityFor(e.alice, []string{"rep"}))
	if err != nil {
		t.Fatalf("alice list: %v", err)
	}
	if len(aliceRows) != 1 {
		t.Fatalf("alice sees %d passports, want exactly her own 1", len(aliceRows))
	}
	if aliceRows[0].ID != aliceIssued.ID {
		t.Fatalf("alice sees passport %s, want her minted %s", aliceRows[0].ID, aliceIssued.ID)
	}
	if aliceRows[0].Label == nil || *aliceRows[0].Label != "alice claude" {
		t.Fatalf("label = %v, want alice claude", aliceRows[0].Label)
	}

	adminRows, err := e.svc.ListPassports(ctx, e.identityFor(e.alice, []string{"admin"}))
	if err != nil {
		t.Fatalf("admin list: %v", err)
	}
	if len(adminRows) != 2 {
		t.Fatalf("admin sees %d passports, want the workspace's 2", len(adminRows))
	}
}

// A revoked passport stays listed with its revoked_at stamped — the
// Settings surface shows the kill switch took, it does not hide history.
func TestListPassportsShowsRevocation(t *testing.T) {
	e := setupPassports(t)
	ctx := e.ctx()

	id := e.identityFor(e.alice, []string{"rep"})
	issued, err := e.svc.IssuePassport(ctx, id, identity.IssuePassportInput{Scopes: []string{"read"}})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := e.svc.RevokePassport(ctx, id, issued.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	rows, err := e.svc.ListPassports(ctx, id)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("listed %d rows, want 1", len(rows))
	}
	if rows[0].RevokedAt == nil {
		t.Fatal("revoked passport lists without revoked_at")
	}
}
