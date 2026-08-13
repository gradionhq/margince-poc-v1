// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package integrations

// The claim hand-off (T4, PI-PARAM-10): a completed run's values cross into
// the owning domain in the DOMAIN's transaction, re-fenced immediately before
// the write, because a subject can be suppressed while a paid run is in
// flight (PI-AC-7). The pending marker (next_attempt_at on the run) is set by
// the terminal commit and cleared only in the same transaction as the claim
// write, so a crash anywhere between the two leaves the sweep something to
// find. Recovery re-reads the result from the provider by job id — no payload
// is ever parked in a queue between attempts.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// claimAttemptCap bounds the recovery ladder: five attempts over roughly
// fifteen minutes, then the run keeps its money truth (completed, paid) and
// claims_unwritten says the values never arrived.
const claimAttemptCap = 5

// ClaimWrite is one completed run's values, handed to the owning domain.
type ClaimWrite struct {
	RunID    string
	PersonID string
	Provider string
	Claims   []provider.Claim
	// RetrievedAt is when the provider answered — the run's completed_at, not
	// the moment of this write. A recovery hand-off runs minutes later and
	// must not restamp the values as fresher than they are.
	RetrievedAt time.Time
}

// retrievedAt reads when the provider actually answered this run. Taken from
// the row rather than from the clock, so the first hand-off attempt and a
// recovery five attempts later stamp the same claim identically.
func (s *Store) retrievedAt(ctx context.Context, tx pgx.Tx, runID string) (time.Time, error) {
	var at *time.Time
	if err := tx.QueryRow(ctx,
		`SELECT completed_at FROM provider_run WHERE id = $1`, runID).Scan(&at); err != nil {
		return time.Time{}, fmt.Errorf("integrations: reading the run's completion time: %w", err)
	}
	if at == nil {
		// The terminal write sets completed_at in the same statement as the
		// state, so a completed run always has one. A row that somehow does
		// not gets the clock rather than a zero time, which would render as
		// 1 January year 1 on the person page.
		return s.now(), nil
	}
	return *at, nil
}

// WriteClaimsFunc is the owning domain's idempotent claim upsert, run inside
// a transaction integrations opens for the hand-off. Declared here rather
// than in shared/ports/provider for the same reason DeleteClaimsFunc is: it
// needs a pgx.Tx and that package is stdlib-only.
type WriteClaimsFunc func(ctx context.Context, tx pgx.Tx, w ClaimWrite) error

// WithClaimWriter binds the owning domain's claim write. Compose supplies it;
// without it every hand-off attempt fails and the ladder exhausts into
// claims_unwritten — the honest record for a build with no domain bound.
func (s *Store) WithClaimWriter(fn WriteClaimsFunc) *Store {
	s.writeClaims = fn
	return s
}

// handoffClaims writes one run's claims in its own transaction: re-fence,
// write, clear the marker. An error leaves the marker standing for the sweep.
func (s *Store) handoffClaims(ctx context.Context, runID, personID, name string, claims []provider.Claim) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		return s.writeClaimsInline(ctx, tx, runID, personID, name, claims)
	})
}

// writeClaimsInline is the hand-off itself, inside a transaction the caller
// already holds — the polled path opens one for it, the synchronous path
// reuses its own terminal transaction because it has no handle to recover by.
func (s *Store) writeClaimsInline(ctx context.Context, tx pgx.Tx, runID, personID, name string, claims []provider.Claim) error {
	if s.writeClaims == nil || s.fence == nil {
		return errors.New("integrations: no claim writer is bound, so the hand-off must wait for the sweep")
	}
	verdict, err := s.fence(ctx, tx, personID)
	if err != nil {
		return err
	}
	if !verdict.Allowed {
		// Paid, but the subject may no longer receive the values (suppressed
		// or erased mid-flight). The values are discarded and the run says
		// so; the spend stands because the purchase happened.
		return s.discardClaims(ctx, tx, runID)
	}
	at, err := s.retrievedAt(ctx, tx, runID)
	if err != nil {
		return err
	}
	if err := s.writeClaims(ctx, tx, ClaimWrite{
		RunID: runID, PersonID: personID, Provider: name, Claims: claims, RetrievedAt: at,
	}); err != nil {
		return err
	}
	// Cleared with the write: a crash after the claims commit but before a
	// separate clear would merely re-run an idempotent upsert.
	if _, err := tx.Exec(ctx, `
		UPDATE provider_run SET next_attempt_at = NULL, updated_at = now()
		 WHERE id = $1`, runID); err != nil {
		return fmt.Errorf("integrations: clearing the claims-pending marker: %w", err)
	}
	return nil
}

// discardClaims closes a hand-off that will never deliver: the run keeps its
// money truth (completed, paid) and claims_unwritten says the values never
// reached the subject.
func (s *Store) discardClaims(ctx context.Context, tx pgx.Tx, runID string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE provider_run SET claims_unwritten = true, next_attempt_at = NULL, updated_at = now()
		 WHERE id = $1`, runID); err != nil {
		return fmt.Errorf("integrations: recording the discarded claims: %w", err)
	}
	return nil
}

// recoverClaims re-attempts one pending hand-off: bump the ladder, re-read
// the result by provider job id, hand off.
//
// The ladder advances on EVERY due pass, before and independently of the
// lease. A recovery that cannot proceed — the connection was withdrawn, or
// the run carries no provider job id to re-read — is still an attempt spent,
// and it must reach claims_unwritten like any other exhaustion. Bumping only
// after a successful lease left those runs re-selected by the sweep forever:
// marker set, counter frozen, and a paid result reported as a clean
// `completed` while its values never arrived.
func (s *Store) recoverClaims(ctx context.Context, runID string) error {
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
		exhausted, err := s.bumpClaimAttempt(ctx, tx, runID)
		if err != nil || exhausted {
			return err
		}
		l, ok, err := s.leaseForPoll(ctx, tx, name, runID, string(provider.RunCompleted))
		if err != nil || !ok {
			return err
		}
		lease, leased = l, true
		return nil
	})
	if err != nil || !leased {
		return err
	}
	status, err := adapter.Poll(ctx, lease.cred, lease.jobID)
	if err != nil {
		return fmt.Errorf("integrations: re-reading the completed result: %w", err)
	}
	if status.Outcome != provider.OutcomeCompleted || status.Result == nil {
		return fmt.Errorf("integrations: the provider no longer serves run %s's completed result (%s)", runID, status.Outcome)
	}
	return s.handoffClaims(ctx, runID, lease.person, name, status.Result.Claims)
}

// bumpClaimAttempt advances the ladder, exhausting it at the cap. Reports
// exhausted=true when this run just crossed into claims_unwritten. It runs on
// every due pass, before the lease, so a recovery that cannot proceed still
// spends an attempt and reaches its end rather than repeating forever.
func (s *Store) bumpClaimAttempt(ctx context.Context, tx pgx.Tx, runID string) (bool, error) {
	var attempts int
	if err := tx.QueryRow(ctx, `
		UPDATE provider_run SET attempt_count = attempt_count + 1, updated_at = now()
		 WHERE id = $1 RETURNING attempt_count`, runID).Scan(&attempts); err != nil {
		return false, fmt.Errorf("integrations: advancing the hand-off ladder: %w", err)
	}
	if attempts >= claimAttemptCap {
		if err := s.discardClaims(ctx, tx, runID); err != nil {
			return false, err
		}
		return true, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE provider_run SET next_attempt_at = now() + $2::interval
		 WHERE id = $1`, runID, claimBackoff(attempts).String()); err != nil {
		return false, fmt.Errorf("integrations: scheduling the next hand-off attempt: %w", err)
	}
	return false, nil
}

// claimBackoff is PI-PARAM-10's exponential ladder: one minute doubling per
// attempt, so five attempts span ~15 minutes (1 + 2 + 4 + 8). The spec pins
// both the shape and the window, and the shape is the half that matters when
// the failure being ridden out is the domain's write path rather than a rate
// limit — a flat interval retries hardest exactly when a slow recovery is
// least likely to be ready.
func claimBackoff(attempt int) time.Duration {
	return time.Minute << (attempt - 1)
}
