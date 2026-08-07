// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The connector lifecycle's post-commit obligations, on real Postgres: the
// superseded credential is destroyed even when the client hangs up the instant
// it has its response, and every connect/disconnect leaves an attributable
// audit row.

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// hangUpVault reproduces "the client hung up the instant it had its response"
// deterministically: the moment the lifecycle change reaches for the vault to
// destroy the superseded credential, the request context dies. A delete that
// rides the request context never happens; a detached one still does.
type hangUpVault struct {
	keyvault.Vault
	hangUp context.CancelFunc
}

func (v *hangUpVault) Delete(ctx context.Context, ws ids.WorkspaceID, ref keyvault.Ref) error {
	if v.hangUp != nil {
		v.hangUp()
	}
	return v.Vault.Delete(ctx, ws, ref)
}

// connectionCredentialRef reads the ref a connection row currently names.
func connectionCredentialRef(t *testing.T, e *SearchEnv, connID ids.UUID) string {
	t.Helper()
	var ref *string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT credential_ref FROM capture_connection WHERE id = $1`, connID).Scan(&ref)
	}); err != nil {
		t.Fatalf("reading the connection's credential_ref: %v", err)
	}
	if ref == nil {
		t.Fatal("the connection carries no credential_ref")
	}
	return *ref
}

func TestReconnectDeletesSupersededCredentialWhenTheCallerHangsUp(t *testing.T) {
	e := SetupSearch(t)
	vault := newTestKeyvault(t, e)
	hangUp := &hangUpVault{Vault: vault}
	registry := newTestCaptureRegistry(e, hangUp)
	registry.Register(&authAssertingFake{})

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "graph", connector.Auth("first-token"))
	if err != nil {
		t.Fatalf("first connect: %v", err)
	}
	supersededRef := connectionCredentialRef(t, e, connID)

	// The reconnect runs on a request context the client abandons mid-cleanup.
	reqCtx, cancel := context.WithCancel(e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead}))
	defer cancel()
	hangUp.hangUp = cancel
	if _, err := registry.Connect(reqCtx, "graph", connector.Auth("second-token")); err != nil {
		t.Fatalf("reconnect: %v", err)
	}

	if _, err := vault.Get(context.Background(), ids.From[ids.WorkspaceKind](e.WS), keyvault.Ref(supersededRef)); !errors.Is(err, keyvault.ErrNotFound) {
		t.Fatalf("the superseded credential survived the reconnect: got %v, want ErrNotFound", err)
	}
}

func TestDisconnectDeletesTheCredentialWhenTheCallerHangsUp(t *testing.T) {
	e := SetupSearch(t)
	vault := newTestKeyvault(t, e)
	hangUp := &hangUpVault{Vault: vault}
	registry := newTestCaptureRegistry(e, hangUp)
	registry.Register(&authAssertingFake{})

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "graph", connector.Auth("granted-token"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	ref := connectionCredentialRef(t, e, connID)

	reqCtx, cancel := context.WithCancel(e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead}))
	defer cancel()
	hangUp.hangUp = cancel
	if err := registry.Disconnect(reqCtx, "graph"); err != nil {
		t.Fatalf("disconnect: %v", err)
	}

	if _, err := vault.Get(context.Background(), ids.From[ids.WorkspaceKind](e.WS), keyvault.Ref(ref)); !errors.Is(err, keyvault.ErrNotFound) {
		t.Fatalf("the revoked credential survived the disconnect: got %v, want ErrNotFound", err)
	}
	// The row must not keep pointing at a destroyed secret, hang-up or not.
	var stillRefs *string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT credential_ref FROM capture_connection WHERE id = $1`, connID).Scan(&stillRefs)
	}); err != nil {
		t.Fatal(err)
	}
	if stillRefs != nil {
		t.Fatalf("disconnect left credential_ref = %q pointing at a destroyed secret", *stillRefs)
	}
}

// lifecycleAudit is one audit_log row as the lifecycle tests read it back.
type lifecycleAudit struct {
	Action  string
	ActorID string
	After   []byte
}

// connectionAudits reads every audit_log row naming a capture_connection, in
// occurrence order.
func connectionAudits(t *testing.T, e *SearchEnv) []lifecycleAudit {
	t.Helper()
	var out []lifecycleAudit
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `
			SELECT action, actor_id, after FROM audit_log
			 WHERE entity_type = 'capture_connection'
			 ORDER BY occurred_at, id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a lifecycleAudit
			if err := rows.Scan(&a.Action, &a.ActorID, &a.After); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("reading the connection audit trail: %v", err)
	}
	return out
}

func TestConnectAndDisconnectLeaveAnAttributableAuditRow(t *testing.T) {
	e := SetupSearch(t)
	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	registry.Register(&authAssertingFake{})

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "graph", connector.Auth("granted-token"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	audits := connectionAudits(t, e)
	if len(audits) != 1 {
		t.Fatalf("connect wrote %d audit rows, want exactly 1: %+v", len(audits), audits)
	}
	if audits[0].Action != "create" {
		t.Errorf("connect audited as %q, want %q", audits[0].Action, "create")
	}
	wantActor := "human:" + e.Rep1.String()
	if audits[0].ActorID != wantActor {
		t.Errorf("connect audited actor %q, want the granting human %q", audits[0].ActorID, wantActor)
	}
	// The after-image describes the connection, never the credential: a
	// secret that reaches the audit trail is a secret in a second store.
	if bytes.Contains(audits[0].After, []byte("granted-token")) || bytes.Contains(audits[0].After, []byte("mgv.")) {
		t.Errorf("the connect after-image carries credential material: %s", audits[0].After)
	}

	if err := registry.Disconnect(grantCtx, "graph"); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	audits = connectionAudits(t, e)
	if len(audits) != 2 {
		t.Fatalf("disconnect wrote %d audit rows in total, want 2: %+v", len(audits), audits)
	}
	if audits[1].Action != "archive" {
		t.Errorf("disconnect audited as %q, want %q", audits[1].Action, "archive")
	}
	if audits[1].ActorID != wantActor {
		t.Errorf("disconnect audited actor %q, want %q", audits[1].ActorID, wantActor)
	}

	// The audited entity is the connection the caller was handed.
	var audited int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM audit_log WHERE entity_type = 'capture_connection' AND entity_id = $1`, connID).Scan(&audited)
	}); err != nil {
		t.Fatal(err)
	}
	if audited != 2 {
		t.Fatalf("%d audit rows name connection %s, want 2", audited, connID)
	}
}

// connectionGeneration reads the lifecycle fence a deferred sync or backfill
// page commits its work against.
func connectionGeneration(t *testing.T, e *SearchEnv, connID ids.UUID) int {
	t.Helper()
	var generation int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT generation FROM capture_connection WHERE id = $1`, connID).Scan(&generation)
	}); err != nil {
		t.Fatalf("reading the connection's generation: %v", err)
	}
	return generation
}

// A disconnect whose phase 1 committed and whose credential destruction then
// failed leaves the row disconnected with its credential_ref intact, and every
// later call re-enters that state to finish the cleanup. Re-driving cleanup is
// not a second withdrawal: the human withdrew once, so the trail must say so
// once, and there is no new cycle out at the provider left to fence.
func TestARetriedDisconnectDoesNotReAuditTheWithdrawal(t *testing.T) {
	e := SetupSearch(t)
	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	registry.Register(&authAssertingFake{})

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "graph", connector.Auth("granted-token"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	// A composition with no vault cannot destroy the sealed credential, so its
	// disconnect fails permanently AFTER phase 1 has committed — the half-finished
	// state every subsequent call converges from.
	stranded := newTestCaptureRegistry(e, nil)
	if err := stranded.Disconnect(grantCtx, "graph"); err == nil {
		t.Fatal("a disconnect that cannot destroy the credential must not report success")
	}
	audits := connectionAudits(t, e)
	if len(audits) != 2 || audits[1].Action != "archive" {
		t.Fatalf("the withdrawal did not commit its own audit row: %+v", audits)
	}
	fenced := connectionGeneration(t, e, connID)

	if err := stranded.Disconnect(grantCtx, "graph"); err == nil {
		t.Fatal("the retry still cannot destroy the credential and must still say so")
	}
	if retried := connectionAudits(t, e); len(retried) != 2 {
		t.Fatalf("the retry wrote %d audit rows in total, want 2 — a re-driven cleanup is not a second withdrawal: %+v",
			len(retried), retried)
	}
	if got := connectionGeneration(t, e, connID); got != fenced {
		t.Fatalf("generation = %d, want %d — the retry fenced a cycle the first withdrawal already ended", got, fenced)
	}
}

// refuseSyncStateUpdates installs a statement-level trigger that makes every
// UPDATE on capture_sync_state raise — the cheapest way to fail Connect AFTER
// its audit row is written. It is statement-level on purpose: a fresh
// connection has no sidecar row yet, so a row-level trigger would never fire.
func refuseSyncStateUpdates(t *testing.T, e *SearchEnv) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.Owner.Exec(ctx, `
		CREATE FUNCTION refuse_sync_state_update() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'sync state refused by test'; END $$;
		CREATE TRIGGER refuse_sync_state BEFORE UPDATE ON capture_sync_state
			FOR EACH STATEMENT EXECUTE FUNCTION refuse_sync_state_update();`); err != nil {
		t.Fatalf("installing the sync-state failure trigger: %v", err)
	}
	t.Cleanup(func() {
		if _, err := e.Owner.Exec(context.Background(), `
			DROP TRIGGER refuse_sync_state ON capture_sync_state;
			DROP FUNCTION refuse_sync_state_update();`); err != nil {
			t.Errorf("removing the sync-state failure trigger: %v", err)
		}
	})
}

// The audit row must be part of the connect, not a second write that happens
// to follow it: a connect that fails anywhere in its transaction must leave no
// trace of a connection that never existed.
func TestAConnectThatFailsLeavesNoAuditRow(t *testing.T) {
	e := SetupSearch(t)
	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	registry.Register(&authAssertingFake{})
	refuseSyncStateUpdates(t, e)

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	if _, err := registry.Connect(grantCtx, "graph", connector.Auth("granted-token")); err == nil {
		t.Fatal("connect must fail while the sync-state update is refused")
	}

	if audits := connectionAudits(t, e); len(audits) != 0 {
		t.Fatalf("a rolled-back connect left %d audit rows behind: %+v", len(audits), audits)
	}
	var connections int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM capture_connection`).Scan(&connections)
	}); err != nil {
		t.Fatal(err)
	}
	if connections != 0 {
		t.Fatalf("a rolled-back connect left %d connection rows behind", connections)
	}
}
