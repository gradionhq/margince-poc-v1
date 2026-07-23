// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The bounded connect-time backfill (ADR-0063, CAP-DDL-4): the user picks a
// window, previews the scope, and an explicit start creates ONE resumable
// run per connection. The run pages backward on its own provider token —
// never sync_cursor, so backfill and incremental interleave without
// conflict — and commits cursor+counters per page, which makes the
// activation read a single row and a worker death resumable from the last
// committed page. Cancel stops the job and retains everything captured.

package capture

import (
	"context"
	"encoding/json"
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

// StartBackfill creates the run (widen-only versus any prior) and returns
// it; the caller enqueues the job. The unique live-run index is the race
// guard — two concurrent starts resolve to one row and one ErrBackfillRunning.
func (r *Registry) StartBackfill(ctx context.Context, provider string, userID ids.UserID, windowMonths int, estimate int) (BackfillRun, error) {
	if !backfillWindows[windowMonths] {
		return BackfillRun{}, fmt.Errorf("%w: %d months", ErrWindowInvalid, windowMonths)
	}
	var run BackfillRun
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		connID, err := r.connectionForUser(ctx, tx, provider, userID)
		if err != nil {
			return err
		}
		var widest *int
		if err := tx.QueryRow(ctx, `
			SELECT max(window_months) FROM capture_backfill WHERE connection_id = $1`, connID).Scan(&widest); err != nil {
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
func latestBackfill(ctx context.Context, tx pgx.Tx, connID ids.UUID) (*BackfillRun, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, connection_id, window_months, after_date, status, cursor, total_estimate,
		       scanned, captured, skipped, people_created, organizations_created, dedupe_candidates,
		       started_at, completed_at, updated_at, last_error_class
		FROM capture_backfill WHERE connection_id = $1
		ORDER BY created_at DESC LIMIT 1`, connID)
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
			UPDATE capture_backfill SET status = 'cancelled', completed_at = now()
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

// RunBackfillStep executes ONE provider page of a run and commits its
// outcome. It returns done=true when the run reached a terminal state (so the
// job stops), and completed=true ONLY on the single step that transitions a
// live run to a successful `done` — the caller uses that edge to fire the
// same-day digest so a freshly-imported mailbox surfaces on the morning
// screen without waiting for the nightly pass. An already-terminal or
// cancelled run returns done=true, completed=false (nothing new arrived). It
// never advances the cursor on a failed page — the retry resumes from the
// committed token. The sink counts land via the page-scoped stats snapshot
// the connector maintains.
func (r *Registry) RunBackfillStep(ctx context.Context, backfillID ids.UUID) (done, completed bool, err error) {
	var (
		connID        ids.UUID
		name          string
		grantedBy     ids.UserID
		credentialRef *string
		authBytes     []byte
		after         time.Time
		cursor        []byte
		status        string
	)
	err = database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT b.connection_id, b.after_date, b.cursor, b.status, c.provider, c.user_id, c.credential_ref, c.auth
			FROM capture_backfill b JOIN capture_connection c ON c.id = b.connection_id
			WHERE b.id = $1`, backfillID).
			Scan(&connID, &after, &cursor, &status, &name, &grantedBy, &credentialRef, &authBytes)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return true, false, fmt.Errorf("capture: backfill %s: %w", backfillID, apperrors.ErrNotFound)
	}
	if err != nil {
		return false, false, err
	}
	if status == "cancelled" || status == "done" || status == "error" {
		return true, false, nil
	}

	c, err := r.connector(name)
	if err != nil {
		// Terminally fail the run like every sibling execution-phase error —
		// returning bare would strand it queued/running, blocking every future
		// StartBackfill for the connection and never surfacing as failed.
		return true, false, r.failBackfill(ctx, backfillID, err)
	}
	bf, ok := c.(connector.Backfiller)
	if !ok {
		return true, false, r.failBackfill(ctx, backfillID, ErrBackfillUnsupported)
	}
	runCtx, err := r.connectorContext(ctx, name, grantedBy)
	if err != nil {
		return true, false, r.failBackfill(ctx, backfillID, err)
	}
	auth, err := r.resolveCredential(ctx, credentialRef, authBytes)
	if err != nil {
		return true, false, r.failBackfill(ctx, backfillID, err)
	}

	pageToken, err := backfillPageCursor(cursor)
	if err != nil {
		return true, false, errors.Join(err, r.failBackfill(ctx, backfillID, err))
	}

	res, err := bf.BackfillPage(runCtx, auth, after, pageToken, r.sink)
	if err != nil {
		// The page failed without advancing: record the class and let the
		// job's retry ladder decide; the committed token is the resume point.
		return false, false, errors.Join(err, r.failBackfill(ctx, backfillID, err))
	}

	finishing := res.NextToken == ""
	var rowsAffected int64
	err = database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		var cur []byte
		statusExpr := "CASE WHEN status = 'queued' THEN 'running' ELSE status END"
		terminal := ""
		if finishing {
			statusExpr = "'done'"
			terminal = ", completed_at = now()"
		} else {
			cur = []byte(fmt.Sprintf(`{"page_token":%q}`, res.NextToken))
		}
		tag, err := tx.Exec(ctx, `
			UPDATE capture_backfill
			SET cursor = $2, scanned = scanned + $3, captured = captured + $4, skipped = skipped + $5,
			    status = `+statusExpr+terminal+`
			WHERE id = $1 AND status IN ('queued','running')`,
			backfillID, cur, res.Scanned, res.Captured, res.Skipped)
		if err != nil {
			return err
		}
		rowsAffected = tag.RowsAffected()
		return nil
	})
	if err != nil {
		return false, false, err
	}
	// The `WHERE status IN ('queued','running')` guard means a run cancelled or
	// completed concurrently between the read above and this UPDATE affects
	// zero rows. completed is the transition edge — true ONLY when this step
	// actually moved a live run to done — so a lost race is terminal, never a
	// spurious completion (and so never a spurious digest). done stops the
	// loop either way: the run finished, or someone else already ended it.
	completed = finishing && rowsAffected == 1
	return finishing || rowsAffected == 0, completed, nil
}

// failBackfill records a terminal failure class on the run (detail goes to
// the job log); captured rows are retained.
func (r *Registry) failBackfill(ctx context.Context, backfillID ids.UUID, cause error) error {
	class := classifySyncError(cause)
	return database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE capture_backfill SET status = 'error', last_error_class = $2, completed_at = now()
			WHERE id = $1 AND status IN ('queued','running')`, backfillID, string(class))
		return err
	})
}

// backfillPageCursor extracts the provider token from the stored cursor.
// An absent cursor is the window's first page; a NON-empty but unreadable
// one is an error, not a silent restart — re-paging from the top would
// inflate the run's counters, so the caller fails the run instead.
func backfillPageCursor(cursor []byte) (string, error) {
	if len(cursor) == 0 {
		return "", nil
	}
	var c struct {
		PageToken string `json:"page_token"`
	}
	if err := json.Unmarshal(cursor, &c); err != nil {
		return "", fmt.Errorf("capture: unreadable backfill cursor %q: %w", cursor, err)
	}
	return c.PageToken, nil
}
