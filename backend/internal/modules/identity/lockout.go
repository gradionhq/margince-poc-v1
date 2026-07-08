// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Failed-login lockout (formulas-and-rules §27, knobs RC-17): a pure
// state machine over app_user.failed_login_count/locked_until, applied
// inside the failure transaction, so a fixed test clock reproduces every
// transition without a database.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/password"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// RC-17 knobs (formulas-and-rules §27: LOCKOUT_THRESHOLD / WINDOW_MIN /
// DURATION_MIN = 5 / 15 / 15).
const (
	lockoutThreshold = 5
	lockoutWindow    = 15 * time.Minute
	lockoutDuration  = 15 * time.Minute
)

// lockoutState mirrors the app_user lockout columns. LastFailure is the
// row's updated_at: while a failure streak runs, the counter update is
// the row's last write, so updated_at IS the last-failure stamp; an
// unrelated profile write between failures can only stretch the §27
// window (keeping stale failures countable longer), never unlock early
// or lock a clean account — the error stays on the cautious side.
type lockoutState struct {
	FailedCount int
	LastFailure time.Time
	LockedUntil *time.Time
}

// locked reports whether the account refuses login at now (§27: refuse
// while now < locked_until, whatever the password).
func (s lockoutState) locked(now time.Time) bool {
	return s.LockedUntil != nil && now.Before(*s.LockedUntil)
}

// fail folds one failed attempt into the state (§27.1). A failure older
// than the window restarts the streak at 1 — a slow drip never
// accumulates to a lock — and reaching the threshold sets locked_until.
func (s lockoutState) fail(now time.Time) lockoutState {
	count := s.FailedCount + 1
	if !s.LastFailure.IsZero() && now.Sub(s.LastFailure) > lockoutWindow {
		count = 1
	}
	next := lockoutState{FailedCount: count, LastFailure: now, LockedUntil: s.LockedUntil}
	if count >= lockoutThreshold {
		until := now.Add(lockoutDuration)
		next.LockedUntil = &until
	}
	return next
}

// accountLockedError maps to permission_denied (403): the sentinel
// registry (interfaces.md §0) has no 423 account_locked yet — it is a
// comment-registry future — and a locked account is a refusal of the
// credential holder, not a missing record or a rate limit.
type accountLockedError struct{}

func (accountLockedError) Error() string {
	return "crmauth: account locked after repeated failed logins; retry after the lockout window"
}

func (accountLockedError) Is(target error) bool {
	return target == apperrors.ErrPermissionDenied
}

// loginCredentials is the account a verified password attempt resolved.
type loginCredentials struct {
	UserID      ids.UserID
	DisplayName string
	SeatType    string
}

// checkCredentials resolves email+password to the account allowed to
// open a session, applying the login gates in refusal order: status,
// then the §27 lock, then the password itself. status = 'active' is the
// one gate for invited, suspended AND deactivated users — all three fall
// onto the decoy branch and read as bad credentials (an invited user has
// no usable password path until activation flips them to active). A
// verified login resets the §27 streak in the same transaction.
func (s *Service) checkCredentials(ctx context.Context, tx pgx.Tx, email, plaintext string) (loginCredentials, error) {
	var account loginCredentials
	var hash *string
	var lock lockoutState
	err := tx.QueryRow(ctx,
		`SELECT id, password_hash, display_name, seat_type, failed_login_count, locked_until, updated_at
		 FROM app_user
		 WHERE lower(email) = lower($1) AND status = 'active' AND archived_at IS NULL`,
		email).Scan(&account.UserID, &hash, &account.DisplayName, &account.SeatType,
		&lock.FailedCount, &lock.LockedUntil, &lock.LastFailure)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && hash == nil) {
		//craft:ignore swallowed-errors the decoy verification exists only to equalize timing; its result is meaningless by design
		_ = password.Verify(plaintext, decoyHash) // equal work on both paths
		return loginCredentials{}, ErrBadCredentials
	}
	if err != nil {
		return loginCredentials{}, err
	}
	// §27: while locked, even the correct password is refused — the
	// check sits before Verify so attempts during the lock neither
	// succeed nor extend the streak.
	if lock.locked(s.now()) {
		return loginCredentials{}, accountLockedError{}
	}
	if err := password.Verify(plaintext, *hash); err != nil {
		if errors.Is(err, password.ErrMismatch) {
			return loginCredentials{}, ErrBadCredentials
		}
		return loginCredentials{}, err
	}
	// §27: success resets the streak. Guarded so the common clean login
	// never churns the row (and its updated_at).
	if lock.FailedCount != 0 || lock.LockedUntil != nil {
		if _, err := tx.Exec(ctx,
			`UPDATE app_user SET failed_login_count = 0, locked_until = NULL WHERE id = $1`,
			account.UserID); err != nil {
			return loginCredentials{}, err
		}
	}
	return account, nil
}

// recordFailedLogin commits one failed password attempt: the §27 counter
// fold on the user row (locked FOR UPDATE — concurrent failures must not
// lose increments) plus the failure audit row, in their own transaction
// because the attempt's transaction rolled back with ErrBadCredentials.
// An unknown or non-active email still lands the audit row — an
// invisible brute-force is exactly what the trail exists to catch.
func (s *Service) recordFailedLogin(ctx context.Context, wsID ids.WorkspaceID, email string) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		outcome := "failed"
		var userID ids.UserID
		var state lockoutState
		err := tx.QueryRow(ctx,
			`SELECT id, failed_login_count, locked_until, updated_at FROM app_user
			 WHERE lower(email) = lower($1) AND status = 'active' AND archived_at IS NULL
			 FOR UPDATE`,
			email).Scan(&userID, &state.FailedCount, &state.LockedUntil, &state.LastFailure)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// no account to count against; only the audit row lands
		case err != nil:
			return err
		default:
			now := s.now()
			next := state.fail(now)
			if _, err := tx.Exec(ctx,
				`UPDATE app_user SET failed_login_count = $2, locked_until = $3 WHERE id = $1`,
				userID, next.FailedCount, next.LockedUntil); err != nil {
				return err
			}
			if next.locked(now) && !state.locked(now) {
				outcome = "lockout" // §27: the lock transition is its own audited fact
			}
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO audit_log (workspace_id, actor_type, actor_id, action, entity_type, evidence)
			 VALUES ($1, 'human', 'human:unauthenticated', 'login', 'session',
			         jsonb_build_object('outcome', $2::text, 'email', $3::text))`,
			wsID, outcome, email)
		return err
	})
}
