// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The bounded connect-time backfill (ADR-0063, CAP-DDL-4), as the user drives
// it: pick a window, preview the scope, and an explicit start creates ONE
// resumable run per connection; status is a single indexed row and cancel
// retains everything captured. The run pages backward on its own provider
// token — never sync_cursor, so backfill and incremental interleave without
// conflict — and backfillpager.go is what walks it.

package capture

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// The CAP-PARAM-4 window set. "none" is expressed by never starting a run.
var backfillWindows = map[int]bool{3: true, 6: true, 12: true}

// ErrWindowInvalid marks a window outside the offered set (422).
var ErrWindowInvalid = errors.New("capture: the backfill window is not in the offered set")

// ErrBackfillRunning marks a start while a run is live (409 backfill_running).
var ErrBackfillRunning = errors.New("capture: a backfill is already running for this connection")

// ErrWindowNarrowing marks a re-invoke with a smaller window than a prior
// run (widen-only; 409 window_narrowing).
var ErrWindowNarrowing = errors.New("capture: the backfill window can only widen")

// ErrBackfillUnsupported marks a provider whose connector cannot enumerate
// backward from a date (not a Backfiller).
var ErrBackfillUnsupported = errors.New("capture: this provider does not support backfill")

// BackfillRun is the CAP-DDL-4 row — the single-row activation read.
type BackfillRun struct {
	ID            ids.UUID
	ConnectionID  ids.UUID
	WindowMonths  int
	AfterDate     time.Time
	Status        string
	Cursor        []byte
	Estimate      *int
	Scanned       int
	Captured      int
	Skipped       int
	People        int
	Organizations int
	DedupeCands   int
	StartedAt     *time.Time
	CompletedAt   *time.Time
	UpdatedAt     time.Time
	ErrorClass    *string
}

// connectionForUser resolves the calling user's connection for provider.
func (r *Registry) connectionForUser(ctx context.Context, tx pgx.Tx, provider string, userID ids.UserID) (ids.UUID, error) {
	var id ids.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM capture_connection
		WHERE provider = $1 AND user_id = $2 AND status IN ('connected','error') AND archived_at IS NULL`,
		provider, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.Nil, apperrors.ErrNotFound
	}
	return id, err
}

// EstimateBackfill previews a window's scope: the provider-side message count
// newer than the window boundary. The consent number (preview before spend,
// ADR-0020). Pricing the projected spend is the estimator's job now (ADR-0068),
// so this returns the raw message count only.
func (r *Registry) EstimateBackfill(ctx context.Context, provider string, userID ids.UserID, windowMonths int) (messages int, err error) {
	if !backfillWindows[windowMonths] {
		return 0, fmt.Errorf("%w: %d months", ErrWindowInvalid, windowMonths)
	}
	var connID ids.UUID
	var name string
	var credentialRef *string
	var authBytes []byte
	err = database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		id, err := r.connectionForUser(ctx, tx, provider, userID)
		if err != nil {
			return err
		}
		connID = id
		return tx.QueryRow(ctx, `
			SELECT provider, credential_ref, auth FROM capture_connection WHERE id = $1`, connID).
			Scan(&name, &credentialRef, &authBytes)
	})
	if err != nil {
		return 0, err
	}
	c, err := r.connector(name)
	if err != nil {
		return 0, err
	}
	bf, ok := c.(connector.Backfiller)
	if !ok {
		return 0, ErrBackfillUnsupported
	}
	auth, err := r.resolveCredential(ctx, credentialRef, authBytes)
	if err != nil {
		return 0, err
	}
	messages, err = bf.EstimateBackfill(ctx, auth, r.now().AddDate(0, -windowMonths, 0))
	if err != nil {
		return 0, err
	}
	return messages, nil
}

// EnqueueBackfill schedules the worker job that will page a run. It runs
// INSIDE the run's own transaction, which is the whole point: the row and the
// job that claims it commit together, so a queue that refuses the insert leaves
// nothing behind.
//
// It takes the run's id rather than closing over it because the id is minted by
// the INSERT the enqueue joins. The job's args type belongs to the composition
// layer — this module cannot see River — so the caller supplies the closure and
// this module owns only when it runs.
type EnqueueBackfill func(ctx context.Context, tx pgx.Tx, backfillID ids.UUID) error

// StartBackfill creates the run (widen-only versus any prior) and schedules it
// in the same transaction. The unique live-run index is the race guard — two
// concurrent starts resolve to one row and one ErrBackfillRunning.
//
// enqueue is required. A run with no job is not a run: uq_capture_backfill_live
// keeps the queued row forever, nothing pages it, and every later start for that
// connection answers 409 backfill_running.
func (r *Registry) StartBackfill(ctx context.Context, provider string, userID ids.UserID, windowMonths int, estimate int, enqueue EnqueueBackfill) (BackfillRun, error) {
	if !backfillWindows[windowMonths] {
		return BackfillRun{}, fmt.Errorf("%w: %d months", ErrWindowInvalid, windowMonths)
	}
	if enqueue == nil {
		return BackfillRun{}, errors.New("capture: starting a backfill needs a scheduler — an unpaged run blocks its connection permanently")
	}
	var run BackfillRun
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		connID, err := r.connectionForUser(ctx, tx, provider, userID)
		if err != nil {
			return err
		}
		// Widen-only protects a mailbox from re-importing a window narrower than
		// one it already has: the narrower run would look like a fresh import and
		// end with less history than before. That reasoning is about the ACCOUNT,
		// so the runs it consults stop at the connection's last account rebind —
		// a mailbox connected today has imported nothing, and holding it to the
		// previous account's window leaves its human no way to import it at all
		// short of a year of a mailbox they just connected. A connection that
		// never changed account (account_bound_at IS NULL) consults every run.
		var widest *int
		if err := tx.QueryRow(ctx, `
			SELECT max(b.window_months)
			FROM capture_backfill b JOIN capture_connection c ON c.id = b.connection_id
			WHERE b.connection_id = $1
			  AND (c.account_bound_at IS NULL OR b.created_at >= c.account_bound_at)`, connID).Scan(&widest); err != nil {
			return err
		}
		if widest != nil && windowMonths < *widest {
			return ErrWindowNarrowing
		}
		after := r.now().AddDate(0, -windowMonths, 0)
		err = tx.QueryRow(ctx, `
			INSERT INTO capture_backfill (workspace_id, connection_id, window_months, after_date, total_estimate, status, started_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, NULLIF($4, 0), 'queued', now())
			RETURNING id`, connID, windowMonths, after, estimate).Scan(&run.ID)
		if err != nil {
			if storekit.IsUniqueViolation(err) {
				return ErrBackfillRunning
			}
			return err
		}
		if err := enqueue(ctx, tx, run.ID); err != nil {
			return fmt.Errorf("capture: scheduling the backfill: %w", err)
		}
		run.ConnectionID = connID
		run.WindowMonths = windowMonths
		run.AfterDate = after
		run.Status = "queued"
		if estimate > 0 {
			// The previewed estimate rides the returned run exactly as the row
			// stores it (NULLIF above): the start response's progress denominator.
			run.Estimate = &estimate
		}
		return nil
	})
	return run, err
}

// BackfillStatus reads the latest run for the user's connection — the
// activation view's single-row read. No run at all is (nil, nil): the
// contract's state "none".
func (r *Registry) BackfillStatus(ctx context.Context, provider string, userID ids.UserID) (*BackfillRun, error) {
	var run *BackfillRun
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		connID, err := r.connectionForUser(ctx, tx, provider, userID)
		if err != nil {
			return err
		}
		run, err = latestBackfill(ctx, tx, connID)
		return err
	})
	return run, err
}

// latestBackfill reads one connection's newest backfill run within the
// caller's transaction; no run at all is (nil, nil) — the contract's state
// "none". The connection-list read shares this with BackfillStatus so the
// two surfaces cannot drift.
//
// The counters it returns are the committed columns PLUS the running page's
// live tally (backfillprogress.go), which is what makes progress visible
// during a page rather than only between pages. The two can never
// double-count: a page's inflight_* columns are cleared by the same statement
// that folds its work into the committed ones.
func latestBackfill(ctx context.Context, tx pgx.Tx, connID ids.UUID) (*BackfillRun, error) {
	row := tx.QueryRow(ctx, `
		SELECT b.id, b.connection_id, b.window_months, b.after_date, b.status, b.cursor, b.total_estimate,
		       b.scanned + b.inflight_scanned, b.captured + b.inflight_captured, b.skipped + b.inflight_skipped,
		       b.people_created + b.inflight_people, b.organizations_created + b.inflight_organizations,
		       b.dedupe_candidates,
		       b.started_at, b.completed_at, b.updated_at, b.last_error_class
		FROM capture_backfill b WHERE b.connection_id = $1
		ORDER BY b.created_at DESC LIMIT 1`, connID)
	var b BackfillRun
	err := row.Scan(&b.ID, &b.ConnectionID, &b.WindowMonths, &b.AfterDate, &b.Status, &b.Cursor, &b.Estimate,
		&b.Scanned, &b.Captured, &b.Skipped, &b.People, &b.Organizations, &b.DedupeCands,
		&b.StartedAt, &b.CompletedAt, &b.UpdatedAt, &b.ErrorClass)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil //nolint:nilnil // absence IS the answer: the contract's state "none", not an error
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// CancelBackfill stops a live run; captured rows are retained (real
// history). No live run → apperrors.ErrConflict (409 not_running).
func (r *Registry) CancelBackfill(ctx context.Context, provider string, userID ids.UserID) (*BackfillRun, error) {
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		connID, err := r.connectionForUser(ctx, tx, provider, userID)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE capture_backfill SET status = 'cancelled', completed_at = now()`+settleInflightProgress+`
			WHERE connection_id = $1 AND status IN ('queued','running')`, connID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("capture: no running backfill to cancel: %w", apperrors.ErrConflict)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r.BackfillStatus(ctx, provider, userID)
}
