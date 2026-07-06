// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// RFC 8058 one-click unsubscribe wiring (B-E11.32, features/06 §1.2
// AC-D3): a bulk/marketing send carries a machine-readable
// List-Unsubscribe URL plus the List-Unsubscribe-Post one-click marker,
// and a visible link in the body. The URL points at the preference
// center's public POST endpoint, whose token the consent module mints per
// recipient. activities never imports consent — the composition root
// injects the linker.

import (
	"context"
	"net/url"
	"strings"
)

// UnsubscribeLinker resolves a recipient address to their preference-center
// token so the send path can build the List-Unsubscribe URL. ok is false
// when the address carries no unsubscribe surface — a locked
// (transactional) purpose, or an address no person holds — in which case
// the send carries no unsubscribe header.
type UnsubscribeLinker interface {
	UnsubscribeToken(ctx context.Context, recipientEmail, purposeKey string) (token string, ok bool, err error)
}

// WithUnsubscribe wires the RFC 8058 linker onto the send path. A send
// composed without one simply carries no unsubscribe header — a marketing
// send still requires granted consent at the gate, so the missing header
// is a wiring gap, never a suppression bypass.
func (h Handlers) WithUnsubscribe(linker UnsubscribeLinker) Handlers {
	h.unsubscribe = linker
	return h
}

// WithPublicBaseURL sets the canonical scheme+host the recipient's
// unsubscribe/preference links resolve to. It is configured at boot, NEVER
// derived from the inbound request: the link carries the recipient's
// unsubscribe token, so trusting a request Host/X-Forwarded-Proto header
// would let an attacker who controls it at send time point the tokenized
// link at their own domain and harvest the token.
func (h Handlers) WithPublicBaseURL(base string) Handlers {
	h.publicBaseURL = strings.TrimRight(strings.TrimSpace(base), "/")
	return h
}

// unsubscribeURL is the ONE spelling of the public one-click endpoint the
// header and the body footer both point at: token in the path, the
// message's purpose in the query so a per-purpose withdrawal targets
// exactly the list this message belonged to.
func unsubscribeURL(baseURL, token, purposeKey string) string {
	return baseURL + "/v1/public/preferences/" + url.PathEscape(token) +
		"/unsubscribe?purpose=" + url.QueryEscape(strings.ToLower(strings.TrimSpace(purposeKey)))
}

// listUnsubscribeHeaders returns the RFC 8058 header pair: the bracketed
// https one-click URL and the fixed one-click POST marker. Emitting
// List-Unsubscribe-Post is what promises the URL is a no-confirmation POST
// target — mail providers surface a one-click control off exactly this.
func listUnsubscribeHeaders(unsubURL string) (listUnsubscribe, listUnsubscribePost string) {
	return "<" + unsubURL + ">", "List-Unsubscribe=One-Click"
}

// appendUnsubscribeFooter adds the human-visible unsubscribe + manage-
// preferences links beneath the message body (AC-D3 "a visible unsubscribe
// link"), built from the same token as the machine header so the two can
// never diverge.
func appendUnsubscribeFooter(body, baseURL, token, unsubURL string) string {
	manageURL := baseURL + "/v1/public/preferences/" + url.PathEscape(token)
	return body + "\n\n---\nUnsubscribe: " + unsubURL + "\nManage your preferences: " + manageURL
}
