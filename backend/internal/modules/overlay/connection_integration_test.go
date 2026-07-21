// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

// Connect is security-relevant (token vaulting) and a genuine
// system-of-record mutation (the write shape), so it gets its own
// real-Postgres coverage rather than riding piggyback on a later task's
// end-to-end test.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// queryRowWS runs a single-row read inside database.WithWorkspaceTx —
// every overlay table carries FORCE RLS keyed off the app.workspace_id
// GUC, so a bare pool.QueryRow (no GUC bound) would see zero rows rather
// than the fixture the test just wrote. This is the assertion-side
// mirror of seedTombstone (mirrorstore_integration_test.go): the same
// tenant-scoped transaction helper the store itself uses.
func queryRowWS(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sql string, args []any, dest ...any) {
	t.Helper()
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, sql, args...).Scan(dest...)
	}); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
}

func TestConnectSealsTheTokenAndFlipsTheWorkspaceToOverlay(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	vault := keyvault.NewMemory()
	svc := NewService(pool, vault, NewMirrorStore(pool, noOwnerEmails{}))

	const token = "pat-super-secret-hubspot-token"
	conn, err := svc.Connect(ctx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: token})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if conn.Incumbent != "hubspot" || conn.Region != "eu1" || conn.Status != "active" {
		t.Errorf("Connect returned %+v, want incumbent=hubspot region=eu1 status=active", conn)
	}
	if conn.ConnectedAt.IsZero() {
		t.Error("Connect returned a zero ConnectedAt")
	}
	found := false
	for _, scope := range conn.Scopes {
		if scope == "crm.objects.owners.read" {
			found = true
		}
	}
	if !found {
		t.Errorf("Connect recorded scopes %v, want crm.objects.owners.read included (design.md §4.3/§7)", conn.Scopes)
	}

	// The plaintext token must NEVER land in the incumbent_connection
	// column — only the opaque vault ref. Reading every text column back
	// (via the owner connection, not RLS-gated) and asserting the raw
	// token substring is absent is the load-bearing security proof here.
	var incumbent, region, status, credentialRef string
	queryRowWS(ctx, t, pool,
		`SELECT incumbent, region, status, credential_ref FROM incumbent_connection WHERE workspace_id = $1`,
		[]any{ws}, &incumbent, &region, &status, &credentialRef)
	if strings.Contains(credentialRef, token) {
		t.Fatalf("credential_ref %q embeds the plaintext token — it must carry only the opaque vault ref", credentialRef)
	}
	if credentialRef == "" {
		t.Fatal("credential_ref is empty — Connect must persist the vault ref")
	}
	for _, col := range []string{incumbent, region, status, credentialRef} {
		if strings.Contains(col, token) {
			t.Fatalf("column value %q contains the plaintext token", col)
		}
	}

	// The sealed secret really is retrievable under the workspace's own
	// ref — proving Connect used the vault, not just minted an opaque
	// string that happens to look like one.
	sealed, getErr := vault.Get(ctx, ids.From[ids.WorkspaceKind](ws), keyvault.Ref(credentialRef))
	if getErr != nil {
		t.Fatalf("resolving the sealed token from the vault: %v", getErr)
	}
	if string(sealed) != token {
		t.Errorf("vault returned %q, want the original token", sealed)
	}

	// The workspace flip: x_sor_mode/x_incumbent change together (the
	// x_overlay_iff_incumbent CHECK).
	var sorMode string
	var incumbentCol *string
	queryRowWS(ctx, t, pool,
		`SELECT x_sor_mode, x_incumbent FROM workspace WHERE id = $1`, []any{ws}, &sorMode, &incumbentCol)
	if sorMode != "overlay" || incumbentCol == nil || *incumbentCol != "hubspot" {
		t.Errorf("workspace mode = (%s, %v), want (overlay, hubspot)", sorMode, incumbentCol)
	}
}

func TestConnectTwiceAnswersAlreadyConnected(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	vault := keyvault.NewMemory()
	svc := NewService(pool, vault, NewMirrorStore(pool, noOwnerEmails{}))

	if _, err := svc.Connect(ctx, ConnectInput{Incumbent: "hubspot", Region: "us1", Token: "first-token"}); err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	_, err := svc.Connect(ctx, ConnectInput{Incumbent: "hubspot", Region: "us1", Token: "second-token"})
	if err == nil {
		t.Fatal("second Connect succeeded, want apperrors.ErrIncumbentAlreadyConnected")
	}
	if !errors.Is(err, apperrors.ErrIncumbentAlreadyConnected) {
		t.Errorf("second Connect error = %v, want apperrors.ErrIncumbentAlreadyConnected", err)
	}
}

func TestGetAnswersNotFoundBeforeConnectAndTheConnectionAfter(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	vault := keyvault.NewMemory()
	svc := NewService(pool, vault, NewMirrorStore(pool, noOwnerEmails{}))

	if _, err := svc.Get(ctx); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("Get before Connect = %v, want apperrors.ErrNotFound", err)
	}

	if _, err := svc.Connect(ctx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "a-token"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	conn, err := svc.Get(ctx)
	if err != nil {
		t.Fatalf("Get after Connect: %v", err)
	}
	if conn.Incumbent != "hubspot" || conn.Status != "active" {
		t.Errorf("Get returned %+v, want incumbent=hubspot status=active", conn)
	}
}

// TestConnectionLifecycleObjectRBACDeniesMemberAllowsAdmin is the
// deny/allow proof for the object-RBAC gate Connect/Get/Disconnect carry
// (identity/internal/policy: overlay_connection is admin/ops-only for
// create/update/delete, every role reads) — without it, any authenticated
// workspace member, even a read-only viewer, could DELETE
// /v1/overlay/connection and purge every mirror row + revoke the
// credential + flip sor_mode.
func TestConnectionLifecycleObjectRBACDeniesMemberAllowsAdmin(t *testing.T) {
	adminCtx, pool, ws := testWorkspaceCtx(t)
	_, memberUserID := testWorkspaceCtxAsUser(t, ws, "member@overlay.test")
	memberCtx := testMemberCtx(ws, memberUserID)
	vault := keyvault.NewMemory()
	svc := NewService(pool, vault, NewMirrorStore(pool, noOwnerEmails{}))

	// A member (read-only on overlay_connection) is denied Connect...
	if _, err := svc.Connect(memberCtx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "member-attempt"}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("member Connect = %v, want apperrors.ErrPermissionDenied", err)
	}
	// ...but a read is allowed (every role reads; ErrNotFound because
	// nothing is connected yet — the object gate let the call THROUGH to
	// the row-existence check, which is the point of this half of the
	// assertion).
	if _, err := svc.Get(memberCtx); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("member Get = %v, want apperrors.ErrNotFound (object gate must pass; only the row lookup should fail)", err)
	}

	// An admin IS allowed to Connect...
	if _, err := svc.Connect(adminCtx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "admin-token"}); err != nil {
		t.Fatalf("admin Connect: %v", err)
	}

	// ...and the same member is denied Disconnect on the now-live connection.
	if err := svc.Disconnect(memberCtx); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("member Disconnect = %v, want apperrors.ErrPermissionDenied", err)
	}
	// The connection must still be untouched: the denial happened before
	// any row was ever read or purged.
	var status string
	queryRowWS(adminCtx, t, pool, `SELECT status FROM incumbent_connection WHERE workspace_id = $1`, []any{ws}, &status)
	if status != statusActive {
		t.Errorf("connection status = %q after a denied member Disconnect, want %q (untouched)", status, statusActive)
	}

	// An admin IS allowed to Disconnect.
	if err := svc.Disconnect(adminCtx); err != nil {
		t.Fatalf("admin Disconnect: %v", err)
	}
}
