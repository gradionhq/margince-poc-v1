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
		run, err = latestBackfill(ctx, tx, connID, provider)
		return err
	})
	return run, err
}

// latestBackfill reads one connection's newest backfill run within the
// caller's transaction; no run at all is (nil, nil) — the contract's state
// "none". The connection-list read shares this with BackfillStatus so the
// two surfaces cannot drift.
func latestBackfill(ctx context.Context, tx pgx.Tx, connID ids.UUID, provider string) (*BackfillRun, error) {
	// People and organizations are LIVE counts of the counterparties THIS
	// connector created since the run began — not the capture_backfill
	// counters, which the page-commit path never fills (the counterparty
	// auto-create runs in its own transaction, decoupled from the page
	// result) and so always read zero. Scoped to `connector:<provider>` so a
	// second connector's captures (e.g. Calendar alongside Gmail) never
	// inflate this run's count. Both tables are RLS-scoped to the workspace
	// inside this transaction; the run's started_at bounds the window.
	row := tx.QueryRow(ctx, `
		SELECT b.id, b.connection_id, b.window_months, b.after_date, b.status, b.cursor, b.total_estimate,
		       b.scanned, b.captured, b.skipped,
		       (SELECT count(*) FROM person
		          WHERE captured_by = 'connector:' || $2 AND created_at >= b.started_at),
		       (SELECT count(*) FROM organization
		          WHERE captured_by = 'connector:' || $2 AND created_at >= b.started_at),
		       b.dedupe_candidates,
		       b.started_at, b.completed_at, b.updated_at, b.last_error_class
		FROM capture_backfill b WHERE b.connection_id = $1
		ORDER BY b.created_at DESC LIMIT 1`, connID, provider)
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
//
// retryAfter > 0 says the page failed on something a delay repairs (a rate
// limit, an unreachable provider) and the run is still LIVE: the caller must
// come back after that delay rather than treat err as the end of the import.
// It is always 0 alongside a terminal outcome. err carries the fault detail
// either way — the run row records only its class, the caller's log owns the
// rest.
func (r *Registry) RunBackfillStep(ctx context.Context, backfillID ids.UUID) (done, completed bool, retryAfter time.Duration, err error) {
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
		return true, false, 0, fmt.Errorf("capture: backfill %s: %w", backfillID, apperrors.ErrNotFound)
	}
	if err != nil {
		return false, false, 0, err
	}
	if status == "cancelled" || status == "done" || status == "error" {
		return true, false, 0, nil
	}

	c, err := r.connector(name)
	if err != nil {
		// Terminally fail the run like every sibling execution-phase error —
		// returning bare would strand it queued/running, blocking every future
		// StartBackfill for the connection and never surfacing as failed.
		return true, false, 0, r.failBackfill(ctx, backfillID, err)
	}
	bf, ok := c.(connector.Backfiller)
	if !ok {
		return true, false, 0, r.failBackfill(ctx, backfillID, ErrBackfillUnsupported)
	}
	runCtx, err := r.connectorContext(ctx, name, grantedBy)
	if err != nil {
		return true, false, 0, r.failBackfill(ctx, backfillID, err)
	}
	auth, err := r.resolveCredential(ctx, credentialRef, authBytes)
	if err != nil {
		return true, false, 0, r.failBackfill(ctx, backfillID, err)
	}

	pageToken, err := backfillPageCursor(cursor)
	if err != nil {
		return true, false, 0, errors.Join(err, r.failBackfill(ctx, backfillID, err))
	}

	res, err := bf.BackfillPage(runCtx, auth, after, pageToken, r.sink)
	if err != nil {
		return r.recordPageFault(ctx, backfillID, err)
	}
	done, completed, err = r.commitBackfillPage(ctx, backfillID, res)
	return done, completed, 0, err
}

// recordPageFault decides what a failed page means for the run. A rate limit or
// an unreachable provider is the provider's weather: the run keeps its
// committed token, counts the failure, and the caller comes back after the
// ladder's delay — a mailbox import that spans hours must survive the outages
// that span minutes. Every other class is a fault no delay repairs (a rejected
// credential needs its human, a vanished history needs a fresh window, an
// internal error needs us), so the run ends and the class says why.
//
// The cap is the honest end of the ladder: a provider still refusing after
// backfillMaxConsecutiveFailures consecutive pages is not going to relent
// because we asked once more.
func (r *Registry) recordPageFault(ctx context.Context, backfillID ids.UUID, cause error) (done, completed bool, retryAfter time.Duration, err error) {
	class := classifySyncError(cause)
	if class != classRateLimited && class != classUnreachable {
		return false, false, 0, errors.Join(cause, r.failBackfill(ctx, backfillID, cause))
	}
	failures, live, countErr := r.countBackfillFailure(ctx, backfillID, class)
	if countErr != nil {
		return false, false, 0, errors.Join(cause, countErr)
	}
	if !live {
		// The run reached a terminal state under us (a cancel, most likely).
		// There is nothing left to retry and nothing left to fail.
		return true, false, 0, cause
	}
	if failures >= backfillMaxConsecutiveFailures {
		return false, false, 0, errors.Join(cause, r.failBackfill(ctx, backfillID, cause))
	}
	return false, false, backfillRetryDelay(failures, cause), cause
}

// countBackfillFailure adds one to the run's consecutive-failure ladder and
// records the class, WITHOUT touching the cursor — the failed page never
// happened as far as the resume point is concerned. live=false means the row
// was no longer queued/running: the caller lost a race with a cancel.
func (r *Registry) countBackfillFailure(ctx context.Context, backfillID ids.UUID, class errorClass) (failures int, live bool, err error) {
	err = database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		// A run whose FIRST page fails transiently is still a run that started:
		// leaving it 'queued' would misreport a live import as one never begun.
		scanErr := tx.QueryRow(ctx, `
			UPDATE capture_backfill
			SET consecutive_failures = consecutive_failures + 1, last_error_class = $2,
			    status = CASE WHEN status = 'queued' THEN 'running' ELSE status END
			WHERE id = $1 AND status IN ('queued','running')
			RETURNING consecutive_failures`, backfillID, string(class)).Scan(&failures)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil
		}
		if scanErr != nil {
			return scanErr
		}
		live = true
		return nil
	})
	return failures, live, err
}

// backfillRetryDelay is the shared transient ladder, with the provider's own
// Retry-After honoured whenever it asks for longer: coming back earlier than a
// rate limiter told us to only spends the next refusal.
func backfillRetryDelay(failures int, cause error) time.Duration {
	delay := backoffDelay(failures)
	var limited *connector.RateLimitedError
	if errors.As(cause, &limited) && limited.RetryAfter > delay {
		return limited.RetryAfter
	}
	return delay
}

// commitBackfillPage records one page's counters and the run's status
// transition, returning whether the run is now terminal (done) and whether
// THIS call is the edge that closed a live run successfully (completed).
//
// The `WHERE status IN ('queued','running')` guard means a run cancelled or
// completed concurrently between the caller's read and this UPDATE affects
// zero rows: completed is true ONLY when this step actually moved a live run
// to done, so a lost race is terminal, never a spurious completion (and so
// never a spurious digest). done stops the pager either way — the run
// finished, or someone else already ended it.
func (r *Registry) commitBackfillPage(ctx context.Context, backfillID ids.UUID, res connector.BackfillPageResult) (done, completed bool, err error) {
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
		// A committed page clears the transient ladder, which is what makes the
		// cap measure CONSECUTIVE failure: an import that limps through a flaky
		// morning must not be ended by faults it already recovered from.
		tag, err := tx.Exec(ctx, `
			UPDATE capture_backfill
			SET cursor = $2, scanned = scanned + $3, captured = captured + $4, skipped = skipped + $5,
			    consecutive_failures = 0,
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
	return finishing || rowsAffected == 0, finishing && rowsAffected == 1, nil
}

// terminalWriteTimeout bounds the detached write that ends a run. Detached
// from the caller's cancellation, it needs a deadline of its own or a stalled
// database would hang the worker that is already shutting down.
const terminalWriteTimeout = 5 * time.Second

// failBackfill records a terminal failure class on the run (detail goes to
// the job log); captured rows are retained.
//
// The write is DETACHED from the caller's context. The commonest reason a page
// fails is the job context dying — a River timeout or a worker shutdown — and
// on that context this write would fail too, leaving the run stuck 'running'
// forever behind uq_capture_backfill_live: no worker pages it, and every future
// StartBackfill for the connection answers 409 with no way for the user to
// clear it. Ending the run is the one write that must outlive the job.
func (r *Registry) failBackfill(ctx context.Context, backfillID ids.UUID, cause error) error {
	class := classifySyncError(cause)
	failCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalWriteTimeout)
	defer cancel()
	return database.WithWorkspaceTx(failCtx, r.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(failCtx, `
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
