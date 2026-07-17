// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The per-connection scheduling state machine (ADR-0063, CAP-DDL-5): a
// transient failure never kills a connection. Rate limits honor Retry-After,
// other transient errors back off exponentially, persistent failure degrades
// the connection to a daily probe — and one success heals everything. Error
// DETAIL goes to system_log; the sidecar row carries only the class.

package capture

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

const (
	// backoffBase..backoffCap bound the transient-failure retry ladder:
	// 2min·2^n capped at 4h, jittered ±20% so a fleet that failed together
	// does not retry together.
	backoffBase = 2 * time.Minute
	backoffCap  = 4 * time.Hour

	// degradeAfterFailures flips a connection to status 'error' — which means
	// "degraded, probed daily", never a tombstone: the due-scan keeps
	// selecting it at errProbeInterval and one success flips it back.
	degradeAfterFailures = 20
	errProbeInterval     = 24 * time.Hour
)

// errorClass is the CAP-DDL-5 vocabulary. The class is schedulable
// information; the underlying detail is system_log's.
type errorClass string

const (
	classRateLimited errorClass = "rate_limited"
	classUnreachable errorClass = "unreachable"
	classAuth        errorClass = "auth"
	classHistoryGone errorClass = "history_gone"
	classInternal    errorClass = "internal"
)

// classifySyncError maps a connector failure onto the shared vocabulary. Any
// error outside it is internal: our bug, not the provider's weather.
func classifySyncError(err error) errorClass {
	switch {
	case errors.Is(err, connector.ErrRateLimited):
		return classRateLimited
	case errors.Is(err, connector.ErrAuthRejected):
		return classAuth
	case errors.Is(err, connector.ErrUnreachable):
		return classUnreachable
	case errors.Is(err, connector.ErrCursorGone):
		return classHistoryGone
	default:
		return classInternal
	}
}

// backoffDelay is the transient-failure ladder: 2min·2^n capped at 4h, with
// ±20% jitter.
func backoffDelay(consecutiveFailures int) time.Duration {
	d := backoffBase
	for i := 0; i < consecutiveFailures && d < backoffCap; i++ {
		d *= 2
	}
	if d > backoffCap {
		d = backoffCap
	}
	jitter := 0.8 + 0.4*rand.Float64() //nolint:gosec // G404: scheduling jitter, not key material — de-syncs a fleet that failed together
	return time.Duration(float64(d) * jitter)
}

// syncStateWorkspace names the workspace the sidecar row belongs to; the
// scheduling calls only run under the fleet walk's per-workspace ctx.
func syncStateWorkspace(ctx context.Context) (ids.UUID, error) {
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return ids.Nil, errors.New("capture: sync-state recording outside workspace context")
	}
	return ws, nil
}

// recordSyncSuccess resets the ladder, paces the next sync one interval out,
// and — the auto-recovery path — flips a degraded connection back to
// connected. One success heals everything.
func (r *Registry) recordSyncSuccess(ctx context.Context, connectionID ids.UUID) error {
	ws, err := syncStateWorkspace(ctx)
	if err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		now := r.now()
		if _, err := tx.Exec(ctx, `
			INSERT INTO capture_sync_state (connection_id, workspace_id, next_sync_at,
			                                consecutive_failures, last_synced_at, last_success_at, last_error_class)
			VALUES ($1, $2, $3, 0, $4, $4, NULL)
			ON CONFLICT (connection_id) DO UPDATE SET
			  next_sync_at = EXCLUDED.next_sync_at,
			  consecutive_failures = 0,
			  last_synced_at = EXCLUDED.last_synced_at,
			  last_success_at = EXCLUDED.last_success_at,
			  last_error_class = NULL`,
			connectionID, ws, now.Add(r.syncInterval), now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE capture_connection SET status = 'connected'
			WHERE id = $1 AND status = 'error' AND archived_at IS NULL`, connectionID)
		return err
	})
}

// recordSyncFailure classifies, schedules the retry, and degrades — never
// tombstones. Auth parks the connection as reauth_required until its human
// reconnects (the OAuth callback resets both rows).
func (r *Registry) recordSyncFailure(ctx context.Context, connectionID ids.UUID, syncErr error) error {
	class := classifySyncError(syncErr)
	ws, err := syncStateWorkspace(ctx)
	if err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		now := r.now()

		var failures int
		if err := tx.QueryRow(ctx, `
			INSERT INTO capture_sync_state (connection_id, workspace_id, next_sync_at,
			                                consecutive_failures, last_synced_at, last_error_class)
			VALUES ($1, $2, $3, 1, $4, $5)
			ON CONFLICT (connection_id) DO UPDATE SET
			  consecutive_failures = capture_sync_state.consecutive_failures + 1,
			  last_synced_at = EXCLUDED.last_synced_at,
			  last_error_class = EXCLUDED.last_error_class
			RETURNING consecutive_failures`,
			connectionID, ws, now.Add(backoffDelay(0)), now, string(class)).Scan(&failures); err != nil {
			return err
		}

		next := now.Add(backoffDelay(failures))
		switch class {
		case classAuth:
			// The connection needs its human, not a retry: park it. The
			// due-scan only selects connected/error, so no next_sync_at
			// gymnastics are needed.
			if _, err := tx.Exec(ctx, `
				UPDATE capture_connection SET status = 'reauth_required'
				WHERE id = $1 AND status IN ('connected','error') AND archived_at IS NULL`, connectionID); err != nil {
				return err
			}
		case classRateLimited:
			var rl *connector.RateLimitedError
			if errors.As(syncErr, &rl) && rl.RetryAfter > next.Sub(now) {
				next = now.Add(rl.RetryAfter)
			}
		default:
		}

		if failures >= degradeAfterFailures {
			// Degraded, probed daily — never a tombstone. One success in the
			// daily probe flips the status back (recordSyncSuccess).
			next = now.Add(errProbeInterval)
			if _, err := tx.Exec(ctx, `
				UPDATE capture_connection SET status = 'error'
				WHERE id = $1 AND status = 'connected' AND archived_at IS NULL`, connectionID); err != nil {
				return err
			}
		}

		if _, err := tx.Exec(ctx, `
			UPDATE capture_sync_state SET next_sync_at = $2 WHERE connection_id = $1`,
			connectionID, next); err != nil {
			return err
		}

		// The class is on the row; the detail belongs to the operational
		// ledger (0078's rationale, kept).
		if _, err := storekit.LogSystem(ctx, tx, "capture_sync_error", map[string]any{
			"connection_id": connectionID.String(),
			"class":         string(class),
			"failures":      failures,
			"detail":        syncErr.Error(),
		}); err != nil {
			return err
		}
		return nil
	})
}
