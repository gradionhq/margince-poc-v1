// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The pager half of the bounded backfill (ADR-0063, CAP-DDL-4): one provider
// page per step, its outcome committed with the cursor that resumes it, and
// the decision every failed page forces — wait the provider out, or end the
// run. The run's control surface (start, status, cancel) is backfill.go's;
// this file is what the worker calls until one of them says stop.

package capture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

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
// done answers the RUN's fate, not this call's outcome: a step that ENDS the
// run reports done=true alongside the err that ended it, so a caller reading
// done without err never pages a run that is already over.
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
		generation    int
	)
	err = database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT b.connection_id, b.after_date, b.cursor, b.status, c.provider, c.user_id, c.credential_ref, c.auth, c.generation
			FROM capture_backfill b JOIN capture_connection c ON c.id = b.connection_id
			WHERE b.id = $1`, backfillID).
			Scan(&connID, &after, &cursor, &status, &name, &grantedBy, &credentialRef, &authBytes, &generation)
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

	// The page's live tally: the connector reports what it has walked and the
	// Sink counts the counterparties it mints, both into this collector, which
	// persists them to the run's inflight_* columns as the page runs. The
	// commit below folds the yields into the run's own counter columns and
	// clears the transient copy.
	pageCtx, progress := withPageProgress(runCtx, r, backfillID)
	res, err := bf.BackfillPage(pageCtx, auth, after, pageToken, r.sink)
	if err != nil {
		return r.recordPageFault(ctx, backfillID, err)
	}
	done, completed, err = r.commitBackfillPage(ctx, backfillID, generation, res, progress)
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
//
// done answers the run's fate on its own, independent of err: every arm that
// ends the run reports done=true, and only the arm that leaves the run LIVE for
// a later retry reports done=false with the delay to wait. A caller consulting
// done without err must never page a run that has already ended.
func (r *Registry) recordPageFault(ctx context.Context, backfillID ids.UUID, cause error) (done, completed bool, retryAfter time.Duration, err error) {
	class := classifySyncError(cause)
	if class != classRateLimited && class != classUnreachable {
		return true, false, 0, errors.Join(cause, r.failBackfill(ctx, backfillID, cause))
	}
	failures, live, countErr := r.countBackfillFailure(ctx, backfillID, class)
	if countErr != nil {
		// The ladder write is what leaves a live run resumable, so a run whose
		// ladder cannot be written must not be left live: the caller stops paging
		// after this, and a live run nobody pages sits behind
		// uq_capture_backfill_live answering 409 to every later start, with nothing
		// a human can clear. End it on the class the page actually failed with.
		return true, false, 0, errors.Join(cause, countErr, r.failBackfill(ctx, backfillID, cause))
	}
	if !live {
		// The run reached a terminal state under us (a cancel, most likely).
		// There is nothing left to retry and nothing left to fail.
		return true, false, 0, cause
	}
	if failures >= backfillMaxConsecutiveFailures {
		return true, false, 0, errors.Join(cause, r.failBackfill(ctx, backfillID, cause))
	}
	return false, false, backfillRetryDelay(failures, cause), cause
}

// countBackfillFailure adds one to the run's consecutive-failure ladder and
// records the class, WITHOUT touching the cursor — the failed page never
// happened as far as the resume point is concerned. live=false means the row
// was no longer queued/running: the caller lost a race with a cancel.
//
// The write is DETACHED, for the same reason the terminal one is: the commonest
// reason a page fails is the job context dying, and the provider client reports
// that deadline as its own unreachable — so this write would fail on the very
// context that produced the fault it is recording, and the run would keep a
// ladder that never climbs and a retry nobody scheduled.
func (r *Registry) countBackfillFailure(ctx context.Context, backfillID ids.UUID, class errorClass) (failures int, live bool, err error) {
	countCtx, cancel := detachedWrite(ctx)
	defer cancel()
	err = database.WithWorkspaceTx(countCtx, r.pool, func(tx pgx.Tx) error {
		// A run whose FIRST page fails transiently is still a run that started:
		// leaving it 'queued' would misreport a live import as one never begun.
		scanErr := tx.QueryRow(countCtx, `
			UPDATE capture_backfill
			SET consecutive_failures = consecutive_failures + 1, last_error_class = $2`+resetInflightProgress+`,
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
// Two guards make the UPDATE conditional, and both mean the same thing: the run
// this page belongs to is no longer the run being written. `status IN
// ('queued','running')` catches a concurrent cancel or completion;
// `generation` catches a disconnect or reconnect of the underlying connection
// while the page was out at the provider — a page fetched under a grant its
// human has since withdrawn is not history this connection gets to keep.
// Either way the statement affects zero rows: completed is true ONLY when this
// step actually moved a live run to done, so a lost race is terminal, never a
// spurious completion (and so never a spurious digest).
//
// The counterparty yields commit in the SAME statement as scanned/captured, so
// a page that fails to commit has counted nothing. They are an honest
// undercount rather than an estimate: a sender the tier gate deferred is
// resolved by the verdict engine long after this page, and the person it may
// eventually mint is nobody's page to claim.
func (r *Registry) commitBackfillPage(ctx context.Context, backfillID ids.UUID, generation int, res connector.BackfillPageResult, progress *pageProgress) (done, completed bool, err error) {
	finishing := res.NextToken == ""
	peopleCreated, orgsCreated := progress.totals()
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
			    people_created = people_created + $6, organizations_created = organizations_created + $7,
			    consecutive_failures = 0`+resetInflightProgress+`,
			    status = `+statusExpr+terminal+`
			WHERE id = $1 AND status IN ('queued','running')
			  AND EXISTS (SELECT 1 FROM capture_connection c
			              WHERE c.id = capture_backfill.connection_id AND c.generation = $8)`,
			backfillID, cur, res.Scanned, res.Captured, res.Skipped, peopleCreated, orgsCreated, generation)
		if err != nil {
			return err
		}
		rowsAffected = tag.RowsAffected()
		if rowsAffected > 0 {
			return nil
		}
		// Zero rows is either a run someone already ended (a no-op below) or a
		// run whose connection changed under it. The second case has to end the
		// run here: every remaining page would be fenced off the same way, and
		// uq_capture_backfill_live would hold a run nothing can ever finish,
		// answering 409 to every later start for the connection. Cancelled is
		// what happened — the import stopped, and what it already captured is
		// kept.
		_, err = tx.Exec(ctx, `
			UPDATE capture_backfill SET status = 'cancelled', completed_at = now()`+resetInflightProgress+`
			WHERE id = $1 AND status IN ('queued','running')`, backfillID)
		return err
	})
	if err != nil {
		return false, false, err
	}
	return finishing || rowsAffected == 0, finishing && rowsAffected == 1, nil
}

// terminalWriteTimeout bounds a detached write. Detached from the caller's
// cancellation, it needs a deadline of its own or a stalled database would hang
// the worker that is already shutting down.
const terminalWriteTimeout = 5 * time.Second

// detachedWrite carries a write that decides a run's fate past the death of the
// job that triggered it. The commonest reason a page fails is the job context
// dying — a River timeout or a worker shutdown — and on that context every
// write fails too, leaving the run stuck queued/running forever behind
// uq_capture_backfill_live: no worker pages it, and every future StartBackfill
// for the connection answers 409 with no way for the user to clear it.
func detachedWrite(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), terminalWriteTimeout)
}

// failBackfill records a terminal failure class on the run (detail goes to
// the job log); captured rows are retained. Ending the run is the one write
// that must outlive the job, so it is detached.
func (r *Registry) failBackfill(ctx context.Context, backfillID ids.UUID, cause error) error {
	class := classifySyncError(cause)
	failCtx, cancel := detachedWrite(ctx)
	defer cancel()
	return database.WithWorkspaceTx(failCtx, r.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(failCtx, `
			UPDATE capture_backfill SET status = 'error', last_error_class = $2, completed_at = now()`+resetInflightProgress+`
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
