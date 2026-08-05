// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// admin user administration (§5.6a): invite / change-role / deactivate /
// reactivate. Every path is admin-only (the service methods re-check
// actor.hasRole("admin")); the handler resolves the acting Identity the
// middleware bound and returns the resulting member row.

// InviteUser (POST /users): provision a new member and mail the set-password link.
func (h Handlers) InviteUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var req crmcontracts.InviteUserRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	// The contract's format/length constraints are not enforced by the binding —
	// validate here so a malformed email or empty name can't create a member.
	email, perr := values.ParseEmail(string(req.Email))
	if perr != nil {
		httperr.Write(w, r, httperr.Validation("email", "invalid_email", "a valid email address is required"))
		return
	}
	name := strings.TrimSpace(req.DisplayName)
	if name == "" || utf8.RuneCountInString(name) > 255 {
		httperr.Write(w, r, httperr.Validation("display_name", "length", "a display name of 1–255 characters is required"))
		return
	}
	userID, rawToken, err := h.svc.InviteUser(r.Context(), actor, InviteUserInput{
		Email:       email.String(),
		DisplayName: name,
		Role:        string(req.Role),
	})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	h.sendInvite(r, email.String(), rawToken)
	h.writeUserByID(w, r, userID, http.StatusCreated)
}

// ChangeUserRole (PATCH /users/{id}/role).
func (h Handlers) ChangeUserRole(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var req crmcontracts.ChangeUserRoleRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	if err := h.svc.ChangeUserRole(r.Context(), actor, ids.UserID{UUID: ids.UUID(id)}, string(req.Role)); err != nil {
		httperr.Write(w, r, err)
		return
	}
	h.writeUserByID(w, r, ids.UserID{UUID: ids.UUID(id)}, http.StatusOK)
}

// DeactivateUser (POST /users/{id}/deactivate).
func (h Handlers) DeactivateUser(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	// The reason body is optional; an empty/absent body is a bare deactivate.
	req := crmcontracts.DeactivateUserRequest{}
	if r.ContentLength != 0 && !httperr.Decode(w, r, &req) {
		return
	}
	if req.Reason != nil && utf8.RuneCountInString(*req.Reason) > 500 {
		httperr.Write(w, r, httperr.Validation("reason", "length", "the reason must be 500 characters or fewer"))
		return
	}
	if err := h.svc.DeactivateUser(r.Context(), actor, DeactivateUserInput{
		UserID: ids.UserID{UUID: ids.UUID(id)},
		Reason: req.Reason,
	}); err != nil {
		httperr.Write(w, r, err)
		return
	}
	h.writeUserByID(w, r, ids.UserID{UUID: ids.UUID(id)}, http.StatusOK)
}

// ReactivateUser (POST /users/{id}/reactivate).
func (h Handlers) ReactivateUser(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	if err := h.svc.ReactivateUser(r.Context(), actor, ids.UserID{UUID: ids.UUID(id)}); err != nil {
		httperr.Write(w, r, err)
		return
	}
	h.writeUserByID(w, r, ids.UserID{UUID: ids.UUID(id)}, http.StatusOK)
}

// IssueUserPasswordLink (POST /users/{id}/password-link): mint a single-use
// set-password link for a member and return it once, for the admin to deliver
// out-of-band (ADR-0061 Amendment 1).
func (h Handlers) IssueUserPasswordLink(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	// Set before any branch below can answer: the success body is a live
	// credential, and the refusals disclose this installation's posture and
	// this caller's standing. Neither belongs in a shared proxy's cache, and a
	// header set on the success path alone would leave every error uncovered.
	w.Header().Set("Cache-Control", "no-store")
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	// Authorization FIRST, ahead of both the configuration gates and the rate
	// limiter. The service re-checks this and is the authority, but deferring
	// to it here would let any authenticated non-admin spend another member's
	// issuance budget — a denial-of-recovery primitive — and read the
	// installation's email posture off the 409 code, both before ever being
	// told they are not an admin.
	if !actor.hasRole(roleAdmin) {
		httperr.Write(w, r, apperrors.ErrPermissionDenied)
		return
	}
	// A request this installation can never serve must not consume the budget
	// that protects one it can, so the configuration gates precede the limiter.
	if refusal := h.passwordLinkRefusal(); refusal != nil {
		httperr.Write(w, r, refusal)
		return
	}
	target := ids.UserID{UUID: ids.UUID(id)}
	if !h.passwordLinkPerActor.Allow(actor.UserID.String()) || !h.passwordLinkPerTarget.Allow(target.String()) {
		// The generic budget sentinel renders as "budget exceeded", which tells
		// an admin neither what happened nor what to do. Say both: the ceiling
		// exists because each issue invalidates the last link, and the link they
		// were already given still works.
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusTooManyRequests, Code: "rate_limited",
			Detail: "too many set-password links issued recently; the last link issued for this member is still valid, and more can be issued within the hour",
		})
		return
	}
	rawToken, expiresAt, err := h.svc.IssuePasswordLink(r.Context(), actor, target)
	if err != nil {
		if errors.Is(err, ErrMemberNotActive) {
			httperr.Write(w, r, &httperr.DetailedError{
				Status: http.StatusConflict, Code: "member_not_active",
				Detail: "this member is not active; reactivate them before issuing a set-password link",
			})
			return
		}
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, crmcontracts.IssuePasswordLinkResponse{
		SetPasswordUrl: passwordLink(h.passwordLinkBaseURL, rawToken),
		ExpiresAt:      expiresAt,
	})
}

// passwordLinkRefusal reports why this installation cannot issue set-password
// links, or nil when it can. Both refusals are operator configuration states
// rather than anything about the request, which is why they are decided before
// the target is even resolved.
func (h Handlers) passwordLinkRefusal() error {
	if h.resetMailer != nil {
		return &httperr.DetailedError{
			Status: http.StatusConflict, Code: "email_channel_configured",
			Detail: "this installation delivers set-password links by email; invite the member instead",
		}
	}
	if h.passwordLinkBaseURL == "" {
		return &httperr.DetailedError{
			Status: http.StatusConflict, Code: "public_base_url_unset",
			Detail: "no public base URL is configured, so no set-password link can be built; ask the operator to set it",
		}
	}
	return nil
}

// actor resolves the acting Identity the middleware bound; on the (defensive,
// middleware-guaranteed) miss it writes 401 and reports ok=false.
func (h Handlers) actor(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	id, ok := identityFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
	}
	return id, ok
}

// writeUserByID reads the member back (any status) and writes it — the shared
// tail of every admin write, so the client always sees the resulting row.
func (h Handlers) writeUserByID(w http.ResponseWriter, r *http.Request, userID ids.UserID, status int) {
	row, err := h.svc.GetUser(r.Context(), userID)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, status, wireUser(row))
}

// sendInvite mails the single-use set-password link when a mailer is wired.
// Delivery is best-effort — the member and token already committed, so a mail
// failure is an operator incident (logged), never a failed invite.
func (h Handlers) sendInvite(r *http.Request, email, rawToken string) {
	if h.resetMailer == nil || rawToken == "" {
		return
	}
	link := passwordLink(h.passwordLinkBaseURL, rawToken)
	body := "You've been invited to Margince.\n\n" +
		"Set your password within seven days to sign in:\n\n  " + link + "\n\n" +
		"If you weren't expecting this, you can ignore this email."
	if err := h.resetMailer.Send(r.Context(), email, "You're invited to Margince", body); err != nil {
		slog.Error("invite email failed", "err", err)
	}
}
