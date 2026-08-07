// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A connector runs on borrowed authority, so the grant itself requires live
// authority: the registry resolves the granting human against identity at
// Connect, not only at every later sync. Enforced in the registry — the one
// place every transport goes through — so no transport can hand it a
// fabricated principal and have that stand as proof.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

func TestConnectRefusesAGrantFromADeactivatedHuman(t *testing.T) {
	e := SetupSearch(t)
	// The production resolver, not the always-live harness fake: liveness is
	// exactly what is under test.
	registry := capture.NewRegistry(e.Pool, capture.NewSink(e.Pool), identity.NewService(e.Pool), newTestKeyvault(t, e))
	registry.Register(&scopeFake{})
	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})

	if _, err := e.Owner.Exec(context.Background(),
		`UPDATE app_user SET status = 'deactivated' WHERE id = $1`, e.Rep1); err != nil {
		t.Fatal(err)
	}
	_, err := registry.Connect(grantCtx, "graph", connector.Auth("token"))
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("grant from a deactivated human → %v, want ErrNotFound", err)
	}
	if n := countRows(t, e, `SELECT count(*) FROM capture_connection`); n != 0 {
		t.Errorf("the refused grant persisted %d connections, want 0", n)
	}
	var sealed int
	if err := e.Owner.QueryRow(context.Background(), `SELECT count(*) FROM vault_secret`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if sealed != 0 {
		t.Errorf("the refused grant sealed %d credentials, want 0", sealed)
	}

	// The control: the same call from a live human still connects, so the
	// refusal above is about liveness and not about the wiring.
	if _, err := e.Owner.Exec(context.Background(),
		`UPDATE app_user SET status = 'active' WHERE id = $1`, e.Rep1); err != nil {
		t.Fatal(err)
	}
	connID, err := registry.Connect(grantCtx, "graph", connector.Auth("token"))
	if err != nil {
		t.Fatalf("grant from a live human: %v", err)
	}
	var status string
	if err := database.WithWorkspaceTx(grantCtx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT status FROM capture_connection WHERE id = $1`, connID).Scan(&status)
	}); err != nil {
		t.Fatal(err)
	}
	if status != "connected" {
		t.Errorf("connection status = %q, want connected", status)
	}
}
