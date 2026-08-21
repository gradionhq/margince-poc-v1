// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/password"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A signed-in human changing their own password.
//
// Possession of a LIVE SESSION is not the authority here; the current password
// is. A session is what a stolen laptop already has, and letting one set a new
// password would turn a borrowed browser into a permanent takeover. Everything
// else in this file follows from that: the verify happens before the KDF spend,
// the §27 lock binds the same way it binds login, and a wrong guess is counted
// and recorded rather than answered for free.

// ErrCurrentPasswordWrong marks a change whose current-password check failed.
// Distinct from ErrBadCredentials at the seam so the handler can answer the
// field rather than the session: the caller IS authenticated, and telling them
// "invalid email or password" would send them to the login screen they are
// already past.
var ErrCurrentPasswordWrong = errors.New("identity: the current password does not match")

// ErrPasswordUnchanged marks a change that sets the password it already had.
// Refused rather than accepted-as-a-no-op: the caller asked to rotate a
// credential, and reporting success without rotating anything tells them the
// old password has stopped working when it has not.
var ErrPasswordUnchanged = errors.New("identity: the new password is the current one")

// ChangePassword rotates the caller's own password.
//
// Every other credential that could act as this account ends with it —
// sessions, OAuth grants and their refresh chains, unconsumed authorization
// codes, locally minted passports — including the session making the call. A
// rotation exists to make what someone else may hold stop working, and a
// carve-out for "this browser" is a carve-out for whoever is sitting at it.
// The caller signs in again with the password they just chose.
func (s *Service) ChangePassword(ctx context.Context, current, next string) error {
	userID, ok := callerUserID(ctx)
	if !ok {
		return apperrors.ErrPermissionDenied
	}
	if err := passwordLengthError("new_password", next); err != nil {
		return err
	}
	wsID, ok := workspaceFrom(ctx)
	if !ok {
		return apperrors.ErrNotFound
	}

	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		if err := s.proveCurrentPassword(ctx, tx, userID, current); err != nil {
			return err
		}
		// Checked HERE, not before the verify. A caller who presents a wrong
		// current password that happens to equal their proposed new one would
		// otherwise be told "the new password must differ from the current
		// one" — false, and the one refusal that skipped both the lockout
		// counter and the evidence row.
		if current == next {
			return ErrPasswordUnchanged
		}
		// Hashed only now: the KDF is 19 MiB of work, and doing it before the
		// current password is verified sells every unauthorized caller a full
		// derivation.
		hash, err := password.Hash(next)
		if err != nil {
			return err
		}
		// Three things clear together, and each for its own reason. The lockout
		// state, because whoever did this just proved they hold the current
		// password, which outranks a stale brute-force streak against the
		// credential they have now replaced. And must_change_password, because
		// the account is no longer using a password somebody else chose — which
		// is the only question that flag answers.
		tag, err := tx.Exec(ctx,
			`UPDATE app_user
			    SET password_hash = $2, failed_login_count = 0, locked_until = NULL,
			        must_change_password = false
			  WHERE id = $1 AND status = 'active' AND archived_at IS NULL`, userID, hash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return apperrors.ErrNotFound
		}
		if err := endCredentialAuthority(passwordOwnerCtx(ctx, userID), tx, userID,
			passwordChangeRevokeReason); err != nil {
			return err
		}
		return logAuthEvent(ctx, tx, userID, "password_changed",
			"password changed by its owner; every borrowed credential revoked")
	})
	// Counted and recorded in the SERVICE, the way Login records its own
	// failures — not in the handler. A transport-only counter means every
	// non-HTTP caller guesses for free, and a test can prove the counting
	// works while the real path never calls it.
	//
	// Its own transaction, because the one above has already rolled back.
	if errors.Is(err, ErrCurrentPasswordWrong) {
		if recErr := s.recordFailedChange(ctx, wsID, userID); recErr != nil {
			slog.ErrorContext(ctx, "recording a failed password change",
				"user_id", userID.String(), "err", recErr)
		}
	}
	return err
}

// proveCurrentPassword is the authorization this route runs on: the caller must
// hold the password they are replacing. It reads the row FOR UPDATE, so it
// serializes against recordFailedLogin and against a second concurrent change
// on the same account.
//
// The §27 lock binds here too. Without it an account locked out of the login
// path could still have its password verified — and changed — through this one,
// which is the same secret behind a different door.
func (s *Service) proveCurrentPassword(ctx context.Context, tx pgx.Tx, userID ids.UserID, current string) error {
	var stored string
	var lock lockoutState
	err := tx.QueryRow(ctx,
		`SELECT coalesce(password_hash, ''), failed_login_count, locked_until, updated_at
		   FROM app_user
		  WHERE id = $1 AND status = 'active' AND archived_at IS NULL
		  FOR UPDATE`, userID).
		Scan(&stored, &lock.FailedCount, &lock.LockedUntil, &lock.LastFailure)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperrors.ErrNotFound
	}
	if err != nil {
		return err
	}
	if lock.locked(s.now()) {
		return errAccountLocked
	}
	// An account with no password (an invited member who never followed their
	// set-password link) has no current password to prove. The decoy keeps this
	// branch costing what a real verification costs, so the shape of the
	// refusal says nothing the answer does not.
	if stored == "" {
		//craft:ignore swallowed-errors the decoy exists to spend the time a real verify spends; its verdict is meaningless by construction
		_ = password.Verify(current, decoyHash)
		return ErrCurrentPasswordWrong
	}
	if password.Verify(current, stored) != nil {
		return ErrCurrentPasswordWrong
	}
	return nil
}

// passwordChangeRevokeReason names why the credentials ended, for the row a
// reader finds later.
const passwordChangeRevokeReason = "password changed by its owner"

// recordFailedChange folds a wrong current password into the SAME §27 lockout
// the login path counts against, and leaves the evidence.
//
// In its OWN transaction, because the caller's has already rolled back by the
// time this runs — that is the whole reason recordFailedLogin does the same.
// Without it an attacker with a borrowed session could guess forever: no
// counter, no lock, and an audit trail showing nothing happened at all.
func (s *Service) recordFailedChange(ctx context.Context, wsID ids.WorkspaceID, userID ids.UserID) error {
	// Detached from the request: a caller who aborts the connection the moment
	// the verify fails must not thereby skip the counter and the evidence.
	ctx, cancel := detachedForFailure(ctx)
	defer cancel()
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		var state lockoutState
		err := tx.QueryRow(ctx,
			`SELECT failed_login_count, locked_until, updated_at FROM app_user
			  WHERE id = $1 AND status = 'active' AND archived_at IS NULL
			  FOR UPDATE`, userID).
			Scan(&state.FailedCount, &state.LockedUntil, &state.LastFailure)
		if errors.Is(err, pgx.ErrNoRows) {
			// The row went away between the refusal and this write. Nothing to
			// count against; the evidence below still lands.
			return logAuthEvent(ctx, tx, userID, "password_change_failed",
				"wrong current password; the account was no longer active")
		}
		if err != nil {
			return err
		}
		now := s.now()
		next := state.fail(now)
		if _, err := tx.Exec(ctx,
			`UPDATE app_user SET failed_login_count = $2, locked_until = $3 WHERE id = $1`,
			userID, next.FailedCount, next.LockedUntil); err != nil {
			return err
		}
		detail := "wrong current password"
		if next.locked(now) && !state.locked(now) {
			// §27: the lock transition is its own audited fact, not a footnote
			// on the attempt that caused it.
			detail = "wrong current password; the account is now locked"
		}
		return logAuthEvent(ctx, tx, userID, "password_change_failed", detail)
	})
}

// callerUserID narrows the bound principal to the user it names. A caller with
// no user behind it — an agent seat, a system principal — has no own password
// to change.
func callerUserID(ctx context.Context) (ids.UserID, bool) {
	id, ok := identityFrom(ctx)
	if !ok {
		return ids.UserID{}, false
	}
	return id.UserID, true
}

// isOwnCredentialRequest reports the one mutating call a READ seat may still
// make: changing its own password.
//
// The seat ceiling is a licensing bound on what a seat may do to the BUSINESS —
// it exists so a read seat cannot write records it was not paid for. A person's
// own credential is not business data, and the ceiling has no interest in it.
// Left inside the cap, a read seat could never rotate its own password at all,
// which is worst on exactly the installations this route was added for: the
// ones with no outbound email, where the reset flow is not a fallback.
// The method is part of the test for the reason publicRequests (middleware.go)
// gives for keying on it: this exemption punches through BOTH the seat ceiling
// and the forced rotation, and a future mutation added at this path must not
// inherit that by sharing an address with it.
func isOwnCredentialRequest(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == changePasswordPath
}

// changePasswordPath is the mounted address of the route above: the router's
// base plus the contract's path. Spelled from its two halves because the
// exemption, the router and the contract have to agree on it, and a drift
// between them strands an account behind a gate whose one exit moved.
const changePasswordPath = httpserver.BaseURL + "/auth/change-password"

// forcedRotationRefusal is the answer every admission door gives an account
// still holding a password somebody else chose. One spelling, because a client
// branches on the code and two doors that worded it differently would make the
// same situation look like two.
func forcedRotationRefusal() *httperr.DetailedError {
	return &httperr.DetailedError{
		Status: http.StatusForbidden, Code: "password_change_required",
		Detail: "this account must set its own password before it can be used",
	}
}

// ChangePassword is the HTTP half. The session admits the request; the current
// password authorizes the change (see the file comment), so a wrong one is a
// 401 naming the field rather than the neutral login refusal — the caller is
// already past the login screen and sending them back there would be a lie
// about what went wrong.
func (h Handlers) ChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !httperr.Decode(w, r, &req) {
		return
	}
	if req.CurrentPassword == "" {
		httperr.Write(w, r, httperr.Validation("current_password", "required",
			"the current password is required"))
		return
	}
	// Capped per ACCOUNT before the verify runs. This route tests the same
	// secret the login route does, so an uncapped one is a guessing oracle
	// behind any borrowed session — and each attempt costs an Argon2
	// verification, which is the second reason login caps it.
	caller, hasCaller := callerUserID(r.Context())
	if hasCaller && h.changeFailures.Blocked(caller.String()) {
		httperr.Write(w, r, apperrors.ErrBudgetExceeded)
		return
	}

	err := h.svc.ChangePassword(r.Context(), req.CurrentPassword, req.NewPassword)
	switch {
	case errors.Is(err, errAccountLocked):
		// Same answer the login path gives a locked account, for the same
		// reason: the lock is the fact, and the caller's next step is to wait.
		// A machine code, not just English: all three 401s this handler can
		// write would otherwise carry code "unauthorized", and the caller —
		// the settings card — reads the answer to tell a wrong password from
		// an expired session. Prose alone makes an expired session mid-form
		// render as a password error.
		//
		// Disclosing the lock is safe HERE, unlike on login: the caller already
		// holds a session for this account, so the fact discloses nothing they
		// could not already learn, and withholding it would leave them
		// retyping a password that is correct.
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnauthorized, Code: "account_locked",
			Detail: "the account is temporarily locked; try again later",
		})
		return
	case errors.Is(err, ErrCurrentPasswordWrong):
		// Only a FAILURE spends a token: the bucket caps wrong guesses, and
		// charging a successful rotation for one would throttle the very thing
		// the route exists to allow. The §27 counter and the evidence row are
		// the service's job (ChangePassword records both).
		if hasCaller {
			h.changeFailures.Record(caller.String())
		}
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnauthorized, Code: "current_password_invalid",
			Detail: "the current password does not match",
		})
		return
	case errors.Is(err, ErrPasswordUnchanged):
		httperr.Write(w, r, httperr.Validation("new_password", "unchanged",
			"the new password must differ from the current one"))
		return
	case err != nil:
		httperr.Write(w, r, err)
		return
	}
	// The session that made this call is gone with every other credential, so
	// the cookie is cleared here too — leaving it would hand the browser a
	// token that now authenticates nothing and read as a broken session
	// rather than a completed rotation.
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
