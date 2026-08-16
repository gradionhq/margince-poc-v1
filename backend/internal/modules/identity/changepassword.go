// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/password"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// A signed-in human changing their own password. The product had every other
// way to set one — a reset token mailed to the account, an admin minting a
// set-password link for someone else — and none for the ordinary case, which
// left an installation with no outbound email and one account unable to rotate
// its own credential at all.
//
// Possession of a LIVE SESSION is not the authority here; the current password
// is. A session is what a stolen laptop already has, and letting it set a new
// password would turn a borrowed browser into a permanent takeover.

// ErrCurrentPasswordWrong marks a change whose current-password check failed.
// Distinct from ErrBadCredentials at the seam so the handler can answer the
// field rather than the session: the caller IS authenticated, and telling them
// "invalid email or password" would send them to the login screen they are
// already past.
var ErrCurrentPasswordWrong = errors.New("identity: the current password does not match")

// ErrPasswordUnchanged marks a change that sets the password it already had.
// Refused rather than accepted-as-a-no-op: the caller asked to rotate a
// credential, and reporting success without rotating it is a lie that matters
// most on the forced path, where the whole point is that the old one stops
// working.
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
	if n := utf8.RuneCountInString(next); n < minPasswordLen || n > maxPasswordLen {
		return &values.ParseError{
			Field:   "new_password",
			Code:    "length",
			Message: fmt.Sprintf("the new password must be %d–%d characters", minPasswordLen, maxPasswordLen),
		}
	}
	if current == next {
		return ErrPasswordUnchanged
	}
	hash, err := password.Hash(next)
	if err != nil {
		return err
	}
	wsID, ok := workspaceFrom(ctx)
	if !ok {
		return apperrors.ErrNotFound
	}

	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		var stored string
		// FOR UPDATE so a second concurrent change cannot verify against a
		// password the first one has already replaced.
		lookupErr := tx.QueryRow(ctx,
			`SELECT coalesce(password_hash, '') FROM app_user
			  WHERE id = $1 AND status = 'active' AND archived_at IS NULL
			  FOR UPDATE`, userID).Scan(&stored)
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if lookupErr != nil {
			return lookupErr
		}
		// An account with no password (an invited member who never followed
		// their set-password link) has no current password to prove. It reads
		// as a wrong current password rather than as a way in.
		if stored == "" || password.Verify(current, stored) != nil {
			return ErrCurrentPasswordWrong
		}
		tag, err := tx.Exec(ctx,
			`UPDATE app_user
			    SET password_hash = $2, failed_login_count = 0, locked_until = NULL
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
		return logAuthEvent(ctx, tx, wsID, userID, "password_changed",
			"password changed by its owner; every borrowed credential revoked")
	})
}

// passwordChangeRevokeReason names why the credentials ended, for the row a
// reader finds later.
const passwordChangeRevokeReason = "password changed by its owner"

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
	err := h.svc.ChangePassword(r.Context(), req.CurrentPassword, req.NewPassword)
	switch {
	case errors.Is(err, ErrCurrentPasswordWrong):
		httperr.Unauthorized(w, r, "the current password does not match")
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
