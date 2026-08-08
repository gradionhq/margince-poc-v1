// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// What the per-call Runtime does once it is live, over real migrated
// Postgres: the transaction it opens is already pinned to the invoking
// workspace, and the secret namespace it hands out belongs to the invoking
// unit and no other. Neither is checkable without a database — both are
// made of a transaction-local GUC and the policies that read it.
//
// Everything here rides the APP pool (integration.Setup's Env.Pool, off
// MARGINCE_TEST_APP_DSN). The integration cluster's owner role is BYPASSRLS
// and FORCE ROW LEVEL SECURITY does not override that, so an assertion about
// tenant filtering made over the owner connection proves nothing.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// extRuntimeEnv is the fixture: the shared migrated database, plus an
// in-memory custodian, plus the bound process-wide runtime dependencies a
// role's boot would have bound.
type extRuntimeEnv struct {
	*integration.Env
	vault keyvault.Vault
}

func setupExtRuntime(t *testing.T) *extRuntimeEnv {
	t.Helper()
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	bindRuntimeForTest(t, e.Pool, vault)
	return &extRuntimeEnv{Env: e, vault: vault}
}

// callCtx is what a governed tool call arrives with: a bound workspace, an
// actor, and a correlation id — the secret store writes a system_log row on
// every operation and needs all three.
func (e *extRuntimeEnv) callCtx(ws ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem,
		ID:   "system:extruntime-test",
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// runtime mints a Runtime the way the tool adapter does for an invocation
// arriving in the fixture workspace, and hands back that invocation's context
// alongside it. Keeping the two together is the point: the Runtime now
// derives its tenant from the context it was MINTED with, so a test that
// built one and then invented an unrelated context would be testing a shape
// the core never produces.
func (e *extRuntimeEnv) runtime(unit string) (*callRuntime, context.Context) {
	ctx := e.callCtx(e.WS)
	return runtimeFor(ctx, unit, e.Pool, e.vault), ctx
}

// TestRuntimeTxIsPinnedToTheInvokingWorkspace: the core binds the tenant GUC
// BEFORE the callback runs, so a handler's very first statement is already
// scoped — and there is no parameter through which it could ask for another
// workspace. The assertion reads the GUC the tenant policies read, which is
// the same fact from the same place rather than a restatement of the wiring.
func TestRuntimeTxIsPinnedToTheInvokingWorkspace(t *testing.T) {
	e := setupExtRuntime(t)
	rt, ctx := e.runtime("alpha")

	var pinned string
	if err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		return tx.QueryRow(ctx, `SELECT current_setting('app.workspace_id', true)`).Scan(&pinned)
	}); err != nil {
		t.Fatal(err)
	}
	if pinned != e.WS.String() {
		t.Fatalf("the callback ran pinned to workspace %q, want the invoking %q", pinned, e.WS)
	}
}

// TestRuntimeTxRefusesACallWithNoWorkspace: a Runtime is minted per
// invocation, and an invocation with no tenant bound has no workspace to pin
// to. Opening an unpinned transaction would hand a unit's SQL whatever the
// deny-on-unset policies happen to allow, so the seam refuses instead.
func TestRuntimeTxRefusesACallWithNoWorkspace(t *testing.T) {
	e := setupExtRuntime(t)
	// Minted from an INVOCATION with no tenant: that, and not the context the
	// handler passes, is where the workspace now comes from.
	rt := runtimeFor(context.Background(), "alpha", e.Pool, e.vault)

	ran := false
	err := rt.Tx(context.Background(), func(context.Context, extension.Tx) error {
		ran = true
		return nil
	})
	if err == nil {
		t.Fatal("an unpinned Tx opened anyway")
	}
	if ran {
		t.Fatal("the callback ran inside a transaction bound to no tenant")
	}
}

// TestRuntimeTxCommitsAndRollsBack walks the seam's own contract: the three
// verbs work, fn returning nil commits, fn returning an error rolls back.
//
// It runs against `workspace` — a CORE table — because the extension's own
// ext_* tables arrive with the demo unit and this seam has to be correct
// before there is one. That it succeeds is also the honest demonstration of
// what the containment wall is today: the tenant pin and the RLS policies,
// and NOT a per-unit database role. runtime.go used to claim the latter; it
// does not exist, and issue #628 tracks building it. When it does, this test
// is expected to need a table the unit actually owns — and the fact that it
// currently does not is the point being recorded here rather than hidden.
func TestRuntimeTxCommitsAndRollsBack(t *testing.T) {
	e := setupExtRuntime(t)
	rt, ctx := e.runtime("alpha")

	// Commit: rename the fixture workspace, then read it back in a SECOND
	// transaction, so what is asserted is durability rather than the write's
	// own snapshot.
	if err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		n, err := tx.Exec(ctx, `UPDATE workspace SET name = $1 WHERE id = $2`, "committed", e.WS)
		if err != nil {
			return err
		}
		if n != 1 {
			t.Errorf("Exec reported %d rows affected, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := e.workspaceName(ctx, t, rt); got != "committed" {
		t.Fatalf("after a committing Tx the row reads %q, want committed", got)
	}

	// Rollback: the same write, abandoned. The error the callback returns is
	// the one the caller sees — not a wrapped commit failure.
	sentinel := errors.New("the handler changed its mind")
	if err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE workspace SET name = $1 WHERE id = $2`, "rolled-back", e.WS); err != nil {
			return err
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("Tx returned %v, want the callback's own error", err)
	}
	if got := e.workspaceName(ctx, t, rt); got != "committed" {
		t.Fatalf("after a rolled-back Tx the row reads %q, want the committed value to have survived", got)
	}
}

// workspaceName reads the fixture workspace's name back through the seam,
// exercising Query/Rows on the way — the cursor idiom the published Rows
// documents, iterated to exhaustion and checked with Err.
func (e *extRuntimeEnv) workspaceName(ctx context.Context, t *testing.T, rt *callRuntime) string {
	t.Helper()
	var name string
	seen := 0
	if err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		rows, err := tx.Query(ctx, `SELECT name FROM workspace`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := rows.Scan(&name); err != nil {
				return err
			}
			seen++
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	// The fixture seeds exactly one workspace, so a second row would mean the
	// cursor is reading something other than what this test writes. It is NOT
	// a proof of the tenant pin — `workspace` is the tenant row itself and
	// carries no deny-on-unset policy of its own, so an unpinned read would
	// see it too. The pin is proved against the GUC the policies read, in
	// TestRuntimeTxIsPinnedToTheInvokingWorkspace.
	if seen != 1 {
		t.Fatalf("the read saw %d workspace rows, want exactly the seeded one", seen)
	}
	return name
}

// TestRuntimeQueryRowReportsAnEmptyMatchAsErrNoRows: the published Row.Scan
// promises the extension's own sentinel, not pgx's — a unit that matched on
// pgx.ErrNoRows would be binding a driver the surface never published.
func TestRuntimeQueryRowReportsAnEmptyMatchAsErrNoRows(t *testing.T) {
	e := setupExtRuntime(t)
	rt, ctx := e.runtime("alpha")

	if err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		var name string
		err := tx.QueryRow(ctx, `SELECT name FROM workspace WHERE id = $1`, ids.NewV7()).Scan(&name)
		if !errors.Is(err, extension.ErrNoRows) {
			t.Errorf("Scan on an empty match = %v, want extension.ErrNoRows", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestRuntimeSecretsReachTheUserScopedNamespace: the port's other three
// verbs, which are a SEPARATE namespace rather than a variation on the first
// — a unit may hold an installation credential and a per-member one under
// one key name, and the workspace read must not answer with the member's.
func TestRuntimeSecretsReachTheUserScopedNamespace(t *testing.T) {
	e := setupExtRuntime(t)
	rt, ctx := e.runtime("alpha")
	user := extension.UserID(e.Rep1.String())

	if err := rt.Secrets().PutUser(ctx, user, "token", []byte("rep1's token")); err != nil {
		t.Fatal(err)
	}
	got, err := rt.Secrets().GetUser(ctx, user, "token")
	if err != nil || string(got) != "rep1's token" {
		t.Fatalf("GetUser = %q, %v; want the stored token", got, err)
	}
	// Same key, other namespace: absent, not the member's value.
	if _, err := rt.Secrets().Get(ctx, "token"); !errors.Is(err, extension.ErrSecretNotFound) {
		t.Fatalf("the workspace scope answered from the user scope: err=%v", err)
	}
	if err := rt.Secrets().DeleteUser(ctx, user, "token"); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Secrets().GetUser(ctx, user, "token"); !errors.Is(err, extension.ErrSecretNotFound) {
		t.Fatalf("GetUser after DeleteUser = %v, want ErrSecretNotFound", err)
	}
}

// TestRuntimeTxSurfacesTheDatabasesOwnRefusal: the seam does not parse,
// rewrite or interpret a unit's SQL, so a statement the database refuses
// comes back as the database's own error and the transaction is abandoned.
// That matters because the containment wall IS the database's — the tenant
// pin and the policies that read it — so what it refuses must reach the unit
// unedited, with its SQLSTATE intact.
//
// The SQLSTATE is what is asserted, not merely that an error came back: a
// transaction that failed to open at all would also be non-nil, and would
// prove nothing about the statement ever reaching Postgres.
func TestRuntimeTxSurfacesTheDatabasesOwnRefusal(t *testing.T) {
	e := setupExtRuntime(t)
	rt, ctx := e.runtime("alpha")

	// 42P01 is undefined_table. The statement reached the server, the server
	// judged it, and its verdict is what the unit holds.
	const undefinedTable = "42P01"

	execErr := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO ext_no_such_table (id) VALUES (1)`)
		return err
	})
	if got := sqlState(execErr); got != undefinedTable {
		t.Fatalf("Exec against a missing table gave SQLSTATE %q (err=%v), want %s", got, execErr, undefinedTable)
	}
	queryErr := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		_, err := tx.Query(ctx, `SELECT * FROM ext_no_such_table`)
		return err
	})
	if got := sqlState(queryErr); got != undefinedTable {
		t.Fatalf("Query against a missing table gave SQLSTATE %q (err=%v), want %s", got, queryErr, undefinedTable)
	}
}

// sqlState digs the server's own error code out of whatever the seam wrapped
// it in, or returns "" when the error never came from Postgres at all.
func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// TestRuntimeTxIgnoresAWorkspaceTheHandlerSuppliesItself is the design's
// load-bearing claim, made structural. A handler holds a context and can
// build another; what it must not be able to do is make the transaction run
// somewhere else. The core re-binds the tenant from the invocation every
// time, so a context naming a different workspace is simply overwritten —
// and the assertion reads the GUC the policies read, so this is the pin
// itself and not a restatement of the wiring.
func TestRuntimeTxIgnoresAWorkspaceTheHandlerSuppliesItself(t *testing.T) {
	e := setupExtRuntime(t)
	rt, _ := e.runtime("alpha")

	// The handler's own context, pointed at a tenant this invocation has no
	// business in — exactly what a unit reaching for its neighbour's rows
	// would write.
	elsewhere := ids.NewV7()
	hostile := principal.WithWorkspaceID(e.callCtx(e.WS), elsewhere)

	var pinned string
	if err := rt.Tx(hostile, func(ctx context.Context, tx extension.Tx) error {
		return tx.QueryRow(ctx, `SELECT current_setting('app.workspace_id', true)`).Scan(&pinned)
	}); err != nil {
		t.Fatal(err)
	}
	if pinned == elsewhere.String() {
		t.Fatalf("the handler re-pointed its own transaction at workspace %q", pinned)
	}
	if pinned != e.WS.String() {
		t.Fatalf("the transaction ran pinned to %q, want the invoking %q", pinned, e.WS)
	}

	// The same for the secret namespace, which resolves its tenant from the
	// context it is handed too: a hostile context must not move the namespace.
	if err := rt.Secrets().Put(hostile, "signing", []byte("alpha's key")); err != nil {
		t.Fatal(err)
	}
	// Read it back under the honest invocation context. If Put had honoured
	// the hostile tenant, this read would find nothing.
	honest, _ := e.runtime("alpha")
	if got, err := honest.Secrets().Get(e.callCtx(e.WS), "signing"); err != nil || string(got) != "alpha's key" {
		t.Fatalf("the secret landed in the workspace the handler named, not the invoking one: %q, %v", got, err)
	}
}

// TestRuntimeSecretsCannotReachAnotherUnitsNamespace is the wall itself: two
// units, one key name, two Runtimes the core built. Beta's Runtime is not
// beta's to point elsewhere — it closes over "beta" and every statement it
// issues carries it — so alpha's secret is not merely denied, it is absent.
func TestRuntimeSecretsCannotReachAnotherUnitsNamespace(t *testing.T) {
	e := setupExtRuntime(t)
	alpha, ctx := e.runtime("alpha")
	if err := alpha.Secrets().Put(ctx, "signing", []byte("alpha's key")); err != nil {
		t.Fatal(err)
	}
	if got, err := alpha.Secrets().Get(ctx, "signing"); err != nil || string(got) != "alpha's key" {
		t.Fatalf("alpha cannot read its own secret back: %q, %v", got, err)
	}

	beta, _ := e.runtime("beta")
	if _, err := beta.Secrets().Get(ctx, "signing"); !errors.Is(err, extension.ErrSecretNotFound) {
		t.Fatalf("beta's Runtime read across the namespace wall: err=%v", err)
	}
	// A delete is the destructive half of the same wall: beta must not be able
	// to revoke a credential alpha depends on.
	if err := beta.Secrets().Delete(ctx, "signing"); !errors.Is(err, extension.ErrSecretNotFound) {
		t.Fatalf("beta's Runtime deleted across the namespace wall: err=%v", err)
	}
	if got, err := alpha.Secrets().Get(ctx, "signing"); err != nil || string(got) != "alpha's key" {
		t.Fatalf("alpha's secret did not survive beta's delete: %q, %v", got, err)
	}
}
