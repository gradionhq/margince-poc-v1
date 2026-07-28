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
	"fmt"
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
	h.store = h.store.WithUnsubscribe(linker)
	return h
}

// WithUnsubscribe is the store-level wiring the handler option delegates to:
// the derivation belongs to the send path, which the MCP tool surface enters
// without passing through any handler.
func (s *Store) WithUnsubscribe(linker UnsubscribeLinker) *Store {
	clone := *s
	clone.unsubscribe = linker
	return &clone
}

// WithPublicBaseURL sets the canonical scheme+host the recipient's
// unsubscribe/preference links resolve to. It is configured at boot, NEVER
// derived from the inbound request: the link carries the recipient's
// unsubscribe token, so trusting a request Host/X-Forwarded-Proto header
// would let an attacker who controls it at send time point the tokenized
// link at their own domain and harvest the token.
func (h Handlers) WithPublicBaseURL(base string) Handlers {
	h.store = h.store.WithPublicBaseURL(base)
	return h
}

// WithPublicBaseURL is the store-level half of the same wiring; it also
// supplies the domain every minted Message-ID is qualified by.
func (s *Store) WithPublicBaseURL(base string) *Store {
	clone := *s
	clone.publicBaseURL = strings.TrimRight(strings.TrimSpace(base), "/")
	return &clone
}

// deliverability derives what a send must carry for a mailbox provider to
// accept it as bulk mail: the RFC 8058 List-Unsubscribe header value and the
// human-visible footer, both built from ONE token so they cannot diverge.
// It returns the body to transmit, footer already applied.
//
// ok is false when the address carries no unsubscribe surface — a locked
// (transactional) purpose, or an address no person holds — in which case a
// transactional message has nothing to unsubscribe from and an address the
// consent gate would refuse discloses nothing.
func (s *Store) deliverability(ctx context.Context, body string, recipients []string, purposeKey string) (listUnsubscribe, outBody string, err error) {
	if s.unsubscribe == nil || len(recipients) == 0 {
		return "", body, nil
	}
	token, ok, err := s.unsubscribe.UnsubscribeToken(ctx, recipients[0], purposeKey)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", body, nil
	}
	if s.publicBaseURL == "" {
		// Fail loudly rather than derive the base from the request: the link
		// carries the recipient's unsubscribe token, and a marketing send
		// may not go out without a working, non-forgeable List-Unsubscribe
		// URL (features/06 §1.2).
		return "", "", fmt.Errorf("send: public base URL is not configured; a marketing send must carry a working List-Unsubscribe URL")
	}
	unsubURL := unsubscribeURL(s.publicBaseURL, token, purposeKey)
	return listUnsubscribeHeader(unsubURL), appendUnsubscribeFooter(body, s.publicBaseURL, token, unsubURL), nil
}

// unsubscribeURL is the ONE spelling of the public one-click endpoint the
// header and the body footer both point at: token in the path, the
// message's purpose in the query so a per-purpose withdrawal targets
// exactly the list this message belonged to.
func unsubscribeURL(baseURL, token, purposeKey string) string {
	return baseURL + "/v1/public/preferences/" + url.PathEscape(token) +
		"/unsubscribe?purpose=" + url.QueryEscape(strings.ToLower(strings.TrimSpace(purposeKey)))
}

// listUnsubscribeHeader returns the RFC 8058 List-Unsubscribe value: the
// bracketed https one-click URL. Its companion List-Unsubscribe-Post value
// is fixed by the RFC at "List-Unsubscribe=One-Click" and is therefore
// rendered at the wire from this value being present, never carried or
// stored beside it — one value cannot drift out of step with itself.
func listUnsubscribeHeader(unsubURL string) string {
	return "<" + unsubURL + ">"
}

// appendUnsubscribeFooter adds the human-visible unsubscribe + manage-
// preferences links beneath the message body (AC-D3 "a visible unsubscribe
// link"), built from the same token as the machine header so the two can
// never diverge.
func appendUnsubscribeFooter(body, baseURL, token, unsubURL string) string {
	manageURL := baseURL + "/v1/public/preferences/" + url.PathEscape(token)
	return body + "\n\n---\nUnsubscribe: " + unsubURL + "\nManage your preferences: " + manageURL
}
