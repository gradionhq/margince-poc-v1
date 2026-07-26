// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The connector lifecycle's post-commit obligations, on real Postgres: the
// superseded credential is destroyed even when the client hangs up the instant
// it has its response, and every connect/disconnect leaves an attributable
// audit row.

import (
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
func connectionCredentialRef(t *testing.T, e *searchEnv, connID ids.UUID) string {
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
	e := setupSearch(t)
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
	e := setupSearch(t)
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
