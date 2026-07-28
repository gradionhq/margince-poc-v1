// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Retention for the transport-idempotency claim table.
//
// settleClaim records the VERBATIM 2xx response body of every replayable
// mutation, and for the replayable set that body is the whole record: a person
// with their names, e-mails, phones and custom fields; a deal with its amount;
// a DSR case with the data subject's identity. Nothing removed those rows. The
// 24h window in claimKey only makes a claim RE-CLAIMABLE in place by the exact
// same (workspace, principal, key, endpoint) tuple — a recurrence that in
// practice never happens — so the body sat in the table for the lifetime of the
// installation, under a role holding table-wide DML and inside every database
// backup, while the Art. 17 eraser and the Art. 15 export both had no idea the
// table existed.
//
// The answer is not to teach the eraser about a transport table (it belongs to
// this package; ADR-0054 keeps that boundary). It is for the snapshot not to
// outlive the retry it exists to make safe: past the replay window the row can
// never be replayed again, so keeping it stores subject data for no purpose at
// all — which is exactly what Art. 5(1)(e) storage limitation forbids. Inside
// the window, ensureReplayVisible is what keeps an erased record from being
// served (replayscope.go).

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// idempotencyRetentionActor is the principal the sweep runs as.
const idempotencyRetentionActor = "agent:idempotency_retention"

// IdempotencyRetentionSweeper drops claim rows the replay window has closed on.
type IdempotencyRetentionSweeper struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// NewIdempotencyRetentionSweeper builds the sweep over the pool.
func NewIdempotencyRetentionSweeper(pool *pgxpool.Pool, log *slog.Logger) *IdempotencyRetentionSweeper {
	return &IdempotencyRetentionSweeper{pool: pool, log: log}
}

// Sweep deletes every claim older than the replay window, in every workspace.
// It reports how many rows went, because a retention pass that says nothing
// reads exactly like one that had nothing to do.
func (s *IdempotencyRetentionSweeper) Sweep(ctx context.Context) error {
	workspaces, err := allWorkspaceIDs(ctx, s.pool)
	if err != nil {
		return err
	}
	var purged int64
	for _, ws := range workspaces {
		wsCtx := principal.WithActor(principal.WithWorkspaceID(ctx, ws), principal.Principal{
			Type: principal.PrincipalSystem,
			ID:   idempotencyRetentionActor,
		})
		n, err := s.sweepWorkspace(wsCtx)
		if err != nil {
			return err
		}
		purged += n
	}
	if purged > 0 {
		s.log.InfoContext(ctx, "idempotency retention: expired claims purged",
			"rows", purged, "window", replayWindow.String())
	}
	return nil
}

// allWorkspaceIDs lists EVERY workspace, archived ones included — unlike the
// sweeps that do work on behalf of a live tenant. Archiving a workspace does
// not un-store the snapshots inside it: skipping those rows would keep subject
// data forever in exactly the workspaces nobody looks at any more, and
// idempotency_key.workspace_id is ON DELETE RESTRICT, so the leftovers would
// also refuse the eventual hard delete.
func allWorkspaceIDs(ctx context.Context, pool *pgxpool.Pool) ([]ids.UUID, error) {
	rows, err := pool.Query(ctx, `SELECT id FROM workspace ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("compose: listing workspaces for idempotency retention: %w", err)
	}
	defer rows.Close()
	var out []ids.UUID
	for rows.Next() {
		var id ids.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *IdempotencyRetentionSweeper) sweepWorkspace(ctx context.Context) (int64, error) {
	var purged int64
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// idx_idempotency_key_created (migration 0033) is what keeps this cheap;
		// the migration named a future cleanup pass as its reason to exist.
		tag, err := tx.Exec(ctx, `
			DELETE FROM idempotency_key
			WHERE created_at < now() - make_interval(secs => $1)`, replayWindow.Seconds())
		if err != nil {
			return fmt.Errorf("compose: purging expired idempotency claims: %w", err)
		}
		purged = tag.RowsAffected()
		return nil
	})
	return purged, err
}
