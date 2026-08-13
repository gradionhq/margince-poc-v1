// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package integrations

// The polling half of run execution (T3): drain one workspace's due runs.
// Four kinds of due-ness resolve through the same partial index
// (provider_run_due): in-progress runs to poll, completed runs whose claim
// hand-off is still pending, queued runs whose submit job was lost, and
// submitting runs whose worker died mid-flight.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// RunDueSweep drains this workspace's due runs once. One run's failure does
// not stop the others; the joined error surfaces them all so the job retries.
func (s *Store) RunDueSweep(ctx context.Context) error {
	if err := s.expireDeadInflight(ctx); err != nil {
		return err
	}
	due, err := s.dueRuns(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, d := range due {
		var err error
		switch d.state {
		case string(provider.RunQueued):
			err = s.ExecuteSubmit(ctx, d.id)
		case string(provider.RunInProgress):
			err = s.pollOne(ctx, d.id)
		case string(provider.RunCompleted):
			err = s.recoverClaims(ctx, d.id)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("run %s: %w", d.id, err))
		}
	}
	return errors.Join(errs...)
}

// expireDeadInflight calls time on submissions whose worker died: past the
// expiry nobody will ever settle them, and the only honest terminal state is
// the one that says the outcome was never learned. Never a resubmit — the
// request may have landed, and a retry is how one ambiguous charge becomes
// two certain ones (PI-AC-4). inflight_at deliberately stands.
func (s *Store) expireDeadInflight(ctx context.Context) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE provider_run
			   SET state = 'submission_unknown', completed_at = now(),
			       last_safe_status_code = 'submission_expired', updated_at = now()
			 WHERE state = 'submitting' AND inflight_at < now() - $1::interval`,
			inflightExpiry.String()); err != nil {
			return fmt.Errorf("integrations: expiring dead in-flight submissions: %w", err)
		}
		return nil
	})
}

type dueRun struct {
	id    string
	state string
}

// dueRuns reads the due-scan in one indexed pass, oldest first so a backlog
// drains in arrival order.
func (s *Store) dueRuns(ctx context.Context) ([]dueRun, error) {
	var out []dueRun
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, state FROM provider_run
			 WHERE (state = 'in_progress')
			    OR (state = 'completed' AND NOT claims_unwritten
			        AND next_attempt_at IS NOT NULL AND next_attempt_at <= now())
			    OR (state = 'queued' AND updated_at < now() - $1::interval)
			 ORDER BY created_at`, strandedSubmitAge.String())
		if err != nil {
			return fmt.Errorf("integrations: scanning the due runs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var d dueRun
			if err := rows.Scan(&d.id, &d.state); err != nil {
				return fmt.Errorf("integrations: scanning a due run: %w", err)
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// pollLease is what the poll lease hands the provider call.
type pollLease struct {
	cred   provider.Credential
	epoch  int64
	jobID  string
	person string
}

// pollOne advances one in-progress run: lease, poll outside any transaction,
// then settle. A pending answer costs nothing — the next sweep asks again.
func (s *Store) pollOne(ctx context.Context, runID string) error {
	name, err := s.runProviderName(ctx, runID)
	if errors.Is(err, errRunVanished) {
		return nil
	}
	if err != nil {
		return err
	}
	adapter, err := s.registry.Adapter(name)
	if err != nil {
		return fmt.Errorf("integrations: run %s names a provider this build does not carry: %w", runID, err)
	}
	var lease pollLease
	var leased bool
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		l, ok, err := s.leaseForPoll(ctx, tx, name, runID, string(provider.RunInProgress))
		lease, leased = l, ok
		return err
	})
	if err != nil || !leased {
		return err
	}
	status, err := adapter.Poll(ctx, lease.cred, lease.jobID)
	if err != nil {
		return fmt.Errorf("integrations: polling the provider: %w", err)
	}
	if !status.Outcome.Terminal() {
		return nil
	}
	return s.settlePoll(ctx, adapter.Descriptor(), name, runID, lease, status)
}

// leaseForPoll re-authorizes egress for an existing run under the same lock
// discipline the submit lease uses. ok=false with a nil error means there is
// nothing to poll — and a withdrawn connection commits the park to
// submission_unknown on the way out: the outcome exists but may no longer be
// fetched, and storing a result obtained after a disconnect is the thing
// PI-AC-5 forbids.
func (s *Store) leaseForPoll(ctx context.Context, tx pgx.Tx, name, runID, wantState string) (pollLease, bool, error) {
	none := pollLease{}
	if err := storekit.LockWriteIdentity(ctx, tx, "provider_connection", name); err != nil {
		return none, false, err
	}
	var state string
	var person, jobID *string
	var runEpoch int64
	err := tx.QueryRow(ctx, `
		SELECT state, person_id::text, connection_epoch, provider_job_id
		  FROM provider_run WHERE id = $1 FOR UPDATE`, runID).
		Scan(&state, &person, &runEpoch, &jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return none, false, nil
	}
	if err != nil {
		return none, false, fmt.Errorf("integrations: locking the run: %w", err)
	}
	if state != wantState || jobID == nil || person == nil {
		return none, false, nil
	}
	conn, err := s.readLiveConnection(ctx, tx, name)
	withdrawn := errors.Is(err, errNoLiveConnection)
	if err != nil && !withdrawn {
		return none, false, err
	}
	if withdrawn || conn.epoch != runEpoch {
		if wantState == string(provider.RunInProgress) {
			if err := s.parkUnknown(ctx, tx, runID, "disconnected_in_flight"); err != nil {
				return none, false, err
			}
		}
		// A completed run recovering its claims stays completed: the purchase
		// already happened, and the marker stands until the attempts exhaust
		// into claims_unwritten.
		return none, false, nil
	}
	cred, err := s.unseal(ctx, tx, conn.credentialRef)
	if err != nil {
		return none, false, err
	}
	return pollLease{cred: cred, epoch: runEpoch, jobID: *jobID, person: *person}, true, nil
}

// settlePoll writes a poll's terminal outcome in one transaction, then hands
// completed claims off outside it. The epoch is re-read under the lock: a
// disconnect that landed while the poll was out means the result is not ours
// to keep.
func (s *Store) settlePoll(ctx context.Context, desc provider.Descriptor, name, runID string, lease pollLease, status provider.PollStatus) error {
	var completed *provider.Result
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		if err := storekit.LockWriteIdentity(ctx, tx, "provider_connection", name); err != nil {
			return err
		}
		state, err := s.lockRunState(ctx, tx, runID)
		if err != nil || state != string(provider.RunInProgress) {
			return err
		}
		conn, err := s.readLiveConnection(ctx, tx, name)
		withdrawn := errors.Is(err, errNoLiveConnection)
		if err != nil && !withdrawn {
			return err
		}
		if withdrawn || conn.epoch != lease.epoch {
			return s.parkUnknown(ctx, tx, runID, "disconnected_in_flight")
		}
		if status.Outcome == provider.OutcomeCompleted {
			completed = status.Result
		}
		return s.terminalize(ctx, tx, desc, name, runID, status)
	})
	if err != nil {
		return err
	}
	if completed != nil {
		return s.handoffClaims(ctx, runID, lease.person, name, completed.Claims)
	}
	return nil
}

// terminalize writes a terminal provider answer: state, reconciliation, the
// claims-pending marker, and the connection's last_used_at. The marker is set
// in the SAME statement as the terminal state — a crash between the two would
// otherwise lose a paid result with nothing left for the sweep to find
// (PI-PARAM-10/PI-AC-12).
func (s *Store) terminalize(ctx context.Context, tx pgx.Tx, desc provider.Descriptor, name, runID string, status provider.PollStatus) error {
	switch status.Outcome {
	case provider.OutcomeCompleted:
		if _, err := tx.Exec(ctx, `
			UPDATE provider_run SET state = 'completed', completed_at = now(),
			       next_attempt_at = now(), inflight_at = NULL, updated_at = now()
			 WHERE id = $1`, runID); err != nil {
			return fmt.Errorf("integrations: recording the completed run: %w", err)
		}
		var spend map[provider.Pool]int
		if status.Result != nil {
			spend = status.Result.PoolSpend
		}
		if err := s.reconcile(ctx, tx, desc, runID, spend, true); err != nil {
			return err
		}
	case provider.OutcomeNoMatch:
		if _, err := tx.Exec(ctx, `
			UPDATE provider_run SET state = 'no_match', completed_at = now(),
			       last_safe_status_code = $2, inflight_at = NULL, updated_at = now()
			 WHERE id = $1`, runID, status.SafeStatusCode); err != nil {
			return fmt.Errorf("integrations: recording the no-match run: %w", err)
		}
		if err := s.reconcile(ctx, tx, desc, runID, nil, false); err != nil {
			return err
		}
	default:
		return s.recordRefusal(ctx, tx, desc, name, runID, status.Outcome, status.SafeStatusCode)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE provider_connection SET last_used_at = now(), updated_at = now()
		 WHERE provider = $1`, name); err != nil {
		return fmt.Errorf("integrations: stamping the connection's last use: %w", err)
	}
	return nil
}

// pollFrom lifts a synchronous Submission into the poll shape terminalize
// takes, so the one function owns every terminal write.
func pollFrom(sub provider.Submission) provider.PollStatus {
	return provider.PollStatus{Outcome: sub.Outcome, Result: sub.Result, SafeStatusCode: sub.SafeStatusCode}
}
