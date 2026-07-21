// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"log/slog"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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
	userID, rawToken, err := h.svc.InviteUser(r.Context(), actor, InviteUserInput{
		Email:       string(req.Email),
		DisplayName: req.DisplayName,
		Role:        string(req.Role),
	})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	h.sendInvite(r, string(req.Email), rawToken)
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
	link := h.resetBaseURL + "/reset-password?token=" + rawToken
	body := "You've been invited to Margince.\n\n" +
		"Set your password within seven days to sign in:\n\n  " + link + "\n\n" +
		"If you weren't expecting this, you can ignore this email."
	if err := h.resetMailer.Send(r.Context(), email, "You're invited to Margince", body); err != nil {
		slog.Error("invite email failed", "err", err)
	}
}
