// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The overlay poller's per-connection scheduling backoff (branch 1b,
// mirroring capture's ADR-0063 sync-state): a sweep that fails at the
// CONNECTION level — a revoked token, a rate-limit, an unreachable
// incumbent — must not be re-swept hot every tick. RecordSweepFailure
// backs the next sweep off (2min·2^n capped at 4h, jittered; a rate-limit
// honors a longer floor), and RecordSweepSuccess resets it. The row lives
// in overlay_sync_state and is read by DueOverlayConnections' due-scan;
// error DETAIL goes to system_log, the row carries only the class.

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

const (
	// sweepBackoffBase..sweepBackoffCap bound the transient-failure ladder:
	// 2min·2^n capped at 4h, jittered ±20% so a fleet that failed together
	// (one HubSpot outage) does not retry in lockstep.
	sweepBackoffBase = 2 * time.Minute
	sweepBackoffCap  = 4 * time.Hour

	// rateLimitedFloor is the minimum backoff for a rate-limited sweep — a
	// 429 means "you are already over quota", so retrying on the short
	// transient ladder would just burn more of the same budget. A precise
	// Retry-After (surfaced by the incumbent client) would refine this; the
	// floor is the conservative default until then.
	rateLimitedFloor = 10 * time.Minute
)

// sweepErrorClass is the schedulable classification overlay can derive from
// the apperrors sentinels a sweep surfaces (see the migration's CHECK on
// why transport-unreachable collapses into internal here).
type sweepErrorClass string

const (
	classSweepRateLimited sweepErrorClass = "rate_limited"
	classSweepAuth        sweepErrorClass = "auth"
	classSweepInternal    sweepErrorClass = "internal"
)

// classifySweepError maps a sweep failure onto the schedulable vocabulary.
// A rate-limit and an auth denial are the two the scheduler treats
// specially; anything else is internal (a transient the ladder retries).
func classifySweepError(err error) sweepErrorClass {
	switch {
	case errors.Is(err, apperrors.ErrIncumbentBudgetExhausted):
		return classSweepRateLimited
	case errors.Is(err, apperrors.ErrPermissionDenied):
		return classSweepAuth
	default:
		return classSweepInternal
	}
}

// sweepBackoffDelay is the transient-failure ladder for consecutiveFailures
// prior failures: 2min·2^n capped at 4h, with ±20% jitter.
func sweepBackoffDelay(consecutiveFailures int) time.Duration {
	d := sweepBackoffBase
	for i := 0; i < consecutiveFailures && d < sweepBackoffCap; i++ {
		d *= 2
	}
	if d > sweepBackoffCap {
		d = sweepBackoffCap
	}
	jitter := 0.8 + 0.4*rand.Float64() //nolint:gosec // G404: scheduling jitter, not key material — de-syncs a fleet that failed together
	return time.Duration(float64(d) * jitter)
}

// RecordSweepSuccess resets the backoff for ctx's workspace: the next
// sweep is due immediately and the failure ladder is cleared. One clean
// sweep heals a backed-off connection.
func (s *MirrorStore) RecordSweepSuccess(ctx context.Context, now time.Time) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if s.fenced {
			if err := assertActiveConnection(ctx, tx); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO overlay_sync_state (workspace_id, next_sweep_at, consecutive_failures, last_success_at, last_error_class, updated_at)
			VALUES (NULLIF(current_setting('app.workspace_id',true),'')::uuid, $1, 0, $1, NULL, now())
			ON CONFLICT (workspace_id) DO UPDATE SET
			  next_sweep_at = EXCLUDED.next_sweep_at,
			  consecutive_failures = 0,
			  last_success_at = EXCLUDED.last_success_at,
			  last_error_class = NULL,
			  updated_at = now()`,
			now)
		return err
	})
}

// RecordSweepFailure classifies sweepErr, increments the failure ladder,
// and pushes the next sweep out by the backoff (a rate-limit honors the
// longer floor) — so a failing connection stops re-sweeping hot. It never
// tombstones: the connection stays selectable, just paced. The error
// detail is logged to system_log; the sidecar row carries only the class.
func (s *MirrorStore) RecordSweepFailure(ctx context.Context, sweepErr error, now time.Time) error {
	class := classifySweepError(sweepErr)
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if s.fenced {
			if err := assertActiveConnection(ctx, tx); err != nil {
				return err
			}
		}
		var failures int
		if err := tx.QueryRow(ctx, `
			INSERT INTO overlay_sync_state (workspace_id, next_sweep_at, consecutive_failures, last_error_class, last_failure_at, updated_at)
			VALUES (NULLIF(current_setting('app.workspace_id',true),'')::uuid, $1, 1, $2, $1, now())
			ON CONFLICT (workspace_id) DO UPDATE SET
			  consecutive_failures = overlay_sync_state.consecutive_failures + 1,
			  last_error_class = EXCLUDED.last_error_class,
			  last_failure_at = EXCLUDED.last_failure_at,
			  updated_at = now()
			RETURNING consecutive_failures`,
			now, string(class)).Scan(&failures); err != nil {
			return fmt.Errorf("overlay: recording the sweep failure: %w", err)
		}

		next := now.Add(sweepBackoffDelay(failures))
		if class == classSweepRateLimited && next.Sub(now) < rateLimitedFloor {
			next = now.Add(rateLimitedFloor)
		}
		if _, err := tx.Exec(ctx, `UPDATE overlay_sync_state SET next_sweep_at = $1 WHERE workspace_id = NULLIF(current_setting('app.workspace_id',true),'')::uuid`,
			next); err != nil {
			return fmt.Errorf("overlay: pacing the next sweep after failure: %w", err)
		}

		if _, err := storekit.LogSystem(ctx, tx, "overlay.sweep_error", map[string]any{
			"class":    string(class),
			"failures": failures,
			"detail":   sweepErr.Error(),
		}); err != nil {
			return fmt.Errorf("overlay: logging the sweep-error system event: %w", err)
		}
		return nil
	})
}
