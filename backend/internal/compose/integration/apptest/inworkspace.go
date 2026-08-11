// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package apptest

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// InWorkspace runs fn on the owner connection under the bootstrapped workspace's
// GUC. FORCE RLS applies to the owner too, so a tenant table is unreachable
// without it.
//
// It lives in apptest rather than beside the suites that call it because it takes
// an AppEnv: the parent integration package's ordinary files cannot import
// apptest without closing an import cycle through compose, so a fixture keyed on
// this type has exactly one home that every suite package can reach.
func InWorkspace(e *AppEnv, t *testing.T, slug string, fn func(pgx.Tx) error) error {
	t.Helper()
	ctx := context.Background()
	tx, err := e.Owner.Begin(ctx)
	if err != nil {
		return err
	}
	//craft:ignore swallowed-errors error-path safety net only — the Commit below is asserted, after which this rollback is a designed no-op
	defer func() { _ = tx.Rollback(ctx) }()
	var wsID string
	if err := tx.QueryRow(ctx, `SELECT id FROM workspace WHERE slug = $1`, slug).Scan(&wsID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, wsID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
