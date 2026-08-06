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
	// An invite creates an ACTIVE member with no password whose only way in is
	// the set-password token. With no mail channel AND no public base URL there
	// is nothing to send it over and no link to build, so the member would be
	// created unreachable and the 201 would be a lie — the exact silent failure
	// the admin-issued link exists to remove, surviving in the one posture that
	// link cannot serve. Refuse instead, and name the operator's missing
	// setting (ADR-0061 Amendment 1).
	if h.resetMailer == nil && h.passwordLinkBaseURL == "" {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusConflict, Code: "no_delivery_channel",
			Detail: "this installation can neither email a set-password link nor build one, " +
				"so an invited member could never sign in; ask the operator to configure " +
				"outbound email or a public base URL",
		})
		return
	}
	userID, rawToken, err := h.svc.InviteUser(r.Context(), actor, InviteUserInput{
		Email:       email.String(),
		DisplayName: name,
		Role:        string(req.Role),
	})
	if err != nil {
		httperr.Write(w, r, conflictIf(err, errEmailTaken, "email_taken",
			"a member with this email already exists in this organization; if they were "+
				"deactivated, reactivate them from the roster instead of inviting again"))
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
		err = conflictIf(err, errLastActiveAdmin, "last_active_admin",
			"this member is the organization's only active administrator; give another "+
				"member the admin role first, then change this one's")
		// Both refusals here are 404s, and an admin who mistyped a role would
		// otherwise be told the MEMBER was not found and go looking for the
		// wrong thing.
		err = refuseAs(err, errUnknownRole, http.StatusNotFound, "unknown_role",
			"this organization defines no role with that key; the built-in roles are "+
				"admin, manager, rep, read_only and ops")
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
		httperr.Write(w, r, conflictIf(err, errLastActiveAdmin, "last_active_admin",
			"this member is the organization's only active administrator; deactivating them "+
				"would leave nobody able to manage users — give another member the admin role first"))
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
		httperr.Write(w, r, conflictIf(err, errNotDeactivated, "not_deactivated",
			"this member is suspended, not deactivated; a suspension is cleared by resolving "+
				"what caused it, and reactivating would hide that"))
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
	// Both ceilings count the attempt, atomically, before the mint. Splitting
	// the check from the charge around a database round-trip would let racing
	// requests all observe room and all supersede — turning the control that
	// bounds denial-of-recovery into one that a concurrent caller walks past.
	// The cost is that a refusal is charged too; a mistyped id charges a key
	// nobody holds, and a repeatedly-refused inactive member cannot use a link
	// until they are reactivated anyway.
	if !h.passwordLinkPerActor.Allow(actor.UserID.String()) {
		httperr.Write(w, r, tooManyPasswordLinksBy("you have issued too many set-password links in the "+
			"last hour; wait, or ask another administrator to issue this one"))
		return
	}
	if !h.passwordLinkPerTarget.Allow(target.String()) {
		// Deliberately NOT "ask another administrator": every admin shares this
		// member's ceiling, so that advice would send the operator down a path
		// guaranteed to refuse.
		httperr.Write(w, r, tooManyPasswordLinksBy("too many set-password links have been issued for "+
			"this member in the last hour; wait before issuing another"))
		return
	}
	rawToken, expiresAt, err := h.svc.IssuePasswordLink(r.Context(), actor, target)
	if err != nil {
		if errors.Is(err, errMemberNotActive) {
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

// conflictIf renders cause as a 409 the operator can act on when err is that
// cause, and returns err untouched otherwise. The service says WHICH refusal
// happened; the handler knows which verb was attempted, so the next step is
// worded here. Without this the whole surface answers the bare word "conflict",
// which tells an admin neither what went wrong nor what to do — the sentinel's
// own text is a log line, not advice.
//
// code carries the same distinction to a CLIENT: detail is prose for a human,
// and a UI that has to branch on which refusal it hit would otherwise be left
// matching sentences.
func conflictIf(err, cause error, code, detail string) error {
	return refuseAs(err, cause, http.StatusConflict, code, detail)
}

// refuseAs is conflictIf's general form: two refusals on this surface share a
// STATUS but not a meaning — an unknown member and an unknown role are both
// 404 — so the status stays the caller's to state.
func refuseAs(err, cause error, status int, code, detail string) error {
	if !errors.Is(err, cause) {
		return err
	}
	return &httperr.DetailedError{Status: status, Code: code, Detail: detail}
}

// tooManyPasswordLinksBy renders an issuance-ceiling refusal. The generic
// budget sentinel reads "budget exceeded", which tells an admin neither what
// happened nor what to do, and the two ceilings need different advice: one is
// spent by the caller, the other is shared across every administrator.
func tooManyPasswordLinksBy(detail string) error {
	return &httperr.DetailedError{
		Status: http.StatusTooManyRequests, Code: "rate_limited", Detail: detail,
	}
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
// tail of every admin write, so the client always sees the resulting row. Every
// caller is admin-only (the service methods re-check it), so the row carries
// its role keys: an admin write answers the same shape the admin roster does,
// and a member row that dropped them here would mean the field's presence
// tracked the endpoint rather than the caller.
func (h Handlers) writeUserByID(w http.ResponseWriter, r *http.Request, userID ids.UserID, status int) {
	row, err := h.svc.GetUser(r.Context(), userID, true)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, status, wireUserWithRoles(row))
}

// sendInvite mails the single-use set-password link when a mailer is wired.
// Delivery is best-effort — the member and token already committed, so a mail
// failure is an operator incident (logged), never a failed invite.
func (h Handlers) sendInvite(r *http.Request, email, rawToken string) {
	if !h.canSendPasswordLink() || rawToken == "" {
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
