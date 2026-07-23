// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

// Integration-test scaffold for the overlay module against a real,
// migrated Postgres — modeled on
// internal/modules/consent/dsr_integration_test.go's setupDSR: read the
// owner/app DSNs, seed a workspace (+ human user) with the owner
// connection, open an app-role pool, and bind the workspace/actor context
// every store call needs (database.WithWorkspaceTx reads it off the ctx).

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget/budgettest"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// testBudgetMeter builds a Redis-backed OVB meter with a small,
// fast-to-exhaust budget for each named incumbent (budgettest.SmallConfig).
// The names must match the test incumbent's Name() — the meter selects
// config by incumbent, and an unconfigured name fails closed (which would
// silently no-op a sweep). The raw-Redis dependency lives in budgettest
// (platform tier), never in this module.
func testBudgetMeter(t *testing.T, incumbents ...string) *overlaybudget.Meter {
	t.Helper()
	return budgettest.Meter(t, budgettest.SmallConfig(incumbents...))
}

// testWorkspaceCtx mints a fresh workspace + one human app_user over the
// real integration Postgres, and returns a context bound to both (the
// shape database.WithWorkspaceTx and every RBAC-aware store call needs)
// plus the app-role pool the test's store under test is constructed with,
// and the workspace id itself (the multi-actor visibility tests seed
// further app_users into the SAME workspace via testWorkspaceCtxAsUser,
// which needs it). It fails loudly rather than skipping: a missing DSN
// means the dependency (`make db-up`) was never provisioned, and a
// silently skipped test looks exactly like a passing one.
func testWorkspaceCtx(t *testing.T) (context.Context, *pgxpool.Pool, ids.UUID) {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("connecting the owner DSN: %v", err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})

	ws := ids.NewV7()
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Overlay', $2, 'EUR')`,
		ws, "overlay-"+ws.String()); err != nil {
		t.Fatalf("seeding workspace: %v", err)
	}

	user := ids.New[ids.UserKind]()
	if _, err := owner.Exec(ctx,
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Overlay Test User')`,
		user, ws, "overlay-user-"+user.String()+"@overlay.test"); err != nil {
		t.Fatalf("seeding app_user: %v", err)
	}

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatalf("opening the app pool: %v", err)
	}
	t.Cleanup(pool.Close)

	opCtx := principal.WithWorkspaceID(context.Background(), ws)
	opCtx = principal.WithCorrelationID(opCtx, ids.NewV7())
	opCtx = principal.WithActor(opCtx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user.UUID,
		SeatType: principal.SeatFull,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			// admin's identity/internal/policy default: full CRUD on
			// overlay_connection AND on the mirrored entities (the entity
			// write grants are what let the write-back verb tests exercise
			// Create/Update/Archive) — hand-built here (see
			// overlayAdminObjectGrants) the same way
			// internal/modules/consent/dsr_integration_test.go's setupDSR
			// declares its own object's grant, since this fixture builds a
			// Principal directly rather than running it through policy.Merge.
			Objects:  overlayAdminObjectGrants(),
			RowScope: principal.RowScopeAll,
		},
	})
	return opCtx, pool, ws
}

// overlayAdminObjectGrants is the object-capability set the overlay
// integration fixtures bind for an admin actor: full CRUD on
// overlay_connection (Connect/Disconnect are admin/ops-only) plus full CRUD
// on every mirrored entity type. The entity grants are required now that
// the overlay Provider object-gates BOTH its reads and its write-back verbs
// (Create/Update/Archive) like the native stores — without them the fixture
// actor would be denied the very mirror rows the read tests assert on and
// the write-back the write tests exercise. The mirrored-entity set is
// derived from knownEntityTypes (the provider's own list), so a new mirrored
// type can never silently lack its fixture grant. RowScope stays all;
// overlay row scope is the store's mirror_visibility deny-join, not the RBAC
// owner predicate.
func overlayAdminObjectGrants() map[string]principal.ObjectGrant {
	grants := map[string]principal.ObjectGrant{
		overlayConnectionObject: {Create: true, Read: true, Update: true, Delete: true},
	}
	for _, et := range knownEntityTypes {
		grants[string(et)] = principal.ObjectGrant{Create: true, Read: true, Update: true, Delete: true}
	}
	return grants
}

// testWorkspaceCtxAsUser seeds one more human app_user into ws (an
// existing workspace testWorkspaceCtx already created) and returns a
// context acting as that new user, with the email given — the shape
// the fail-closed visibility tests need: several distinct human
// actors sharing one workspace, so "mapped" vs "unmapped" vs "mapped but
// not this record's owner" are genuinely different principals, not the
// same one reused. It opens its own short-lived owner connection rather
// than threading testWorkspaceCtx's through every call site.
func testWorkspaceCtxAsUser(t *testing.T, ws ids.UUID, email string) (context.Context, ids.UUID) {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	if ownerDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("connecting the owner DSN: %v", err)
	}
	defer func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	}()

	user := ids.New[ids.UserKind]()
	if _, err := owner.Exec(ctx,
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Overlay Test User')`,
		user, ws, email); err != nil {
		t.Fatalf("seeding app_user: %v", err)
	}

	opCtx := principal.WithWorkspaceID(context.Background(), ws)
	opCtx = principal.WithCorrelationID(opCtx, ids.NewV7())
	opCtx = principal.WithActor(opCtx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user.UUID,
		SeatType: principal.SeatFull,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects:  overlayAdminObjectGrants(),
			RowScope: principal.RowScopeAll,
		},
	})
	return opCtx, user.UUID
}

// testMemberCtx returns a context acting as userID with a non-admin,
// read-only grant on overlay_connection (identity/internal/policy's
// manager/rep/read_only posture: connecting/disconnecting is admin/ops
// only; every other role reads). Used by the RBAC deny-path test —
// Connect/Disconnect must refuse this actor with ErrPermissionDenied
// while Get still succeeds.
func testMemberCtx(ws, userID ids.UUID) context.Context {
	opCtx := principal.WithWorkspaceID(context.Background(), ws)
	opCtx = principal.WithCorrelationID(opCtx, ids.NewV7())
	return principal.WithActor(opCtx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + userID.String(), UserID: userID,
		SeatType: principal.SeatFull,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects: map[string]principal.ObjectGrant{
				overlayConnectionObject: {Read: true},
			},
			RowScope: principal.RowScopeTeam,
		},
	})
}

// noOwnerEmails is an OwnerEmailResolver that never resolves any owner —
// the tests that only exercise the tombstone/staleness/dirty guards (not
// email matching) have no owner-email fixture to seed, and a resolver that
// visibly errors rather than fabricating an email keeps that honest.
type noOwnerEmails struct{}

func (noOwnerEmails) OwnerEmail(_ context.Context, ownerExternalID string) (string, error) {
	return "", fmt.Errorf("test: no owner with external id %s", ownerExternalID)
}

// stubOwnerEmails is an OwnerEmailResolver over a fixed id→email table —
// the email-match tests fixture a known set of incumbent owners without
// reaching a real (or even fake) HubSpot client.
type stubOwnerEmails map[string]string

func (s stubOwnerEmails) OwnerEmail(_ context.Context, ownerExternalID string) (string, error) {
	email, ok := s[ownerExternalID]
	if !ok {
		return "", fmt.Errorf("test: no owner with external id %s", ownerExternalID)
	}
	return email, nil
}
