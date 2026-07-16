// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// storekit.LogSystem over a real migrated schema: the append-only ledger
// for SYSTEM / non-entity operational events (login, bulk export, capture
// skip). It stamps actor + workspace from the principal exactly like Audit,
// writes no entity, and returns the row id an entity-less pipeline event
// carries as its ledger trace link. The row is append-only at the DB layer.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// connectorCtx binds a connector principal acting for a granting human —
// the capture-skip shape (the harness seeds no connector helper).
func connectorCtx(e *Env, onBehalf ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type:       principal.PrincipalConnector,
		ID:         "connector:gmail",
		OnBehalfOf: onBehalf,
	})
}

func TestLogSystem_writesConnectorRowWithDerivedActor(t *testing.T) {
	e := Setup(t)
	ctx := connectorCtx(e, e.Rep1)
	detail := map[string]any{"reason": "personal_exclusion", "source_system": "gmail", "source_id": "msg-1"}

	var id ids.UUID
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		var err error
		id, err = storekit.LogSystem(ctx, tx, "capture_skip", detail)
		return err
	}); err != nil {
		t.Fatalf("LogSystem: %v", err)
	}
	if id.IsZero() {
		t.Fatal("LogSystem returned a zero id — the ledger link would be unroutable")
	}

	var (
		actorType, actorID, action string
		onBehalf                   *ids.UUID
		entityAbsent               bool
		detailJSON                 []byte
	)
	rowCtx := principal.WithWorkspaceID(context.Background(), e.WS)
	if err := database.WithWorkspaceTx(rowCtx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT actor_type, actor_id, on_behalf_of, action, detail,
			        NOT EXISTS (SELECT 1 FROM information_schema.columns
			                    WHERE table_schema = current_schema() AND table_name='system_log' AND column_name IN ('entity_type','entity_id'))
			 FROM system_log WHERE id = $1`, id).
			Scan(&actorType, &actorID, &onBehalf, &action, &detailJSON, &entityAbsent)
	}); err != nil {
		t.Fatalf("reading back the row: %v", err)
	}

	if actorType != "connector" || actorID != "connector:gmail" {
		t.Errorf("actor = %s/%s, want connector/connector:gmail", actorType, actorID)
	}
	if onBehalf == nil || *onBehalf != e.Rep1 {
		t.Errorf("on_behalf_of = %v, want %s (the granting human)", onBehalf, e.Rep1)
	}
	if action != "capture_skip" {
		t.Errorf("action = %q, want capture_skip", action)
	}
	if !entityAbsent {
		t.Error("system_log must be LEAN — no entity_type/entity_id columns")
	}
	var got map[string]any
	if err := json.Unmarshal(detailJSON, &got); err != nil {
		t.Fatalf("detail is not valid json: %v", err)
	}
	if got["reason"] != "personal_exclusion" || got["source_system"] != "gmail" {
		t.Errorf("detail = %v, want the excluded-message context", got)
	}
}

func TestLogSystem_isAppendOnly(t *testing.T) {
	e := Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)

	var id ids.UUID
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		var err error
		id, err = storekit.LogSystem(ctx, tx, "export", map[string]any{"kind": "filtered"})
		return err
	}); err != nil {
		t.Fatalf("LogSystem: %v", err)
	}

	// UPDATE and DELETE must both raise the immutability trigger.
	for _, stmt := range []string{
		`UPDATE system_log SET action = 'tampered' WHERE id = $1`,
		`DELETE FROM system_log WHERE id = $1`,
	} {
		err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt, id)
			return err
		})
		if err == nil {
			t.Errorf("statement %q succeeded — system_log must be append-only", stmt)
		}
	}
}
