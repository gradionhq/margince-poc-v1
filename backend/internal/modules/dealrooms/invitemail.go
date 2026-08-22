// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/mailer"
)

// WithInviteMailer wires the operator's outbound mail relay. Nil means the
// installation cannot deliver an invitation, which this module reports rather
// than fails on.
func (h Handlers) WithInviteMailer(m mailer.Mailer) Handlers {
	h.inviteMailer = m
	return h
}

// WithInviteLinkBase wires the canonical origin a buyer link is built on. The
// trailing slash is trimmed HERE and only here, so every caller may pass a base
// with or without one.
func (h Handlers) WithInviteLinkBase(base string) Handlers {
	h.publicBaseURL = strings.TrimRight(base, "/")
	return h
}

// canSendInvite reports whether an invitation can reach anybody.
//
// BOTH halves are required. A relay with no base URL would mail a link built on
// an empty origin — an unusable URL that still consumed the one credential the
// recipient was issued.
func (h Handlers) canSendInvite() bool {
	return h.inviteMailer != nil && h.publicBaseURL != ""
}

// buyerRoute is the SPA route a buyer link points at. The credential rides the
// query of the FRAGMENT, so the server never sees it in a path.
const buyerRoute = "/#/room?c="

// buyerLink puts the credential in the URL's FRAGMENT, never its path.
//
// A browser does not put a fragment on the wire, so the credential stays out of
// access logs, out of the Referer header a click sends onward, and out of any
// cache key. The server therefore never sees it in a URL at all — it arrives in
// a POST body when the buyer's browser exchanges it.
//
// This is containment, not a guarantee, and the exception is routine rather than
// hypothetical: click-tracking mail gateways rewrite whole URLs into their own
// query strings, fragment included, so the credential lands in a third party's
// request line whichever form we choose. The identity module says the same about
// its set-password links. What actually bounds the exposure is that a credential
// is single-use and short-lived, not the shape of the URL carrying it.
func (h Handlers) buyerLink(credential string) string {
	return h.publicBaseURL + buyerRoute + credential
}

// sendInvite hands the invitation to the relay.
//
// The message names the room and nothing about the deal — its value, its stage,
// its other participants. An invitation lands in a mailbox we do not control and
// may be forwarded onward, so it carries only what a recipient needs to act on.
func (h Handlers) sendInvite(r *http.Request, issued IssuedInvitation) error {
	body := "You have been given access to a Deal Room.\n\n" +
		"Open it here:\n\n  " + h.buyerLink(issued.Credential) + "\n\n" +
		"This link is personal to you and works once, until " +
		issued.ExpiresAt.Format("2 January 2006") + ".\n" +
		"If you were not expecting this, you can ignore this message."

	if err := h.inviteMailer.Send(r.Context(), string(issued.Participant.Email),
		"You have been given access to a Deal Room", body); err != nil {
		return fmt.Errorf("send deal room invitation: %w", err)
	}
	return nil
}
