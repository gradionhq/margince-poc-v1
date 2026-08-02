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

// SharedUnsubscribeTokenError refuses a send that would put ONE recipient's
// preference token in front of the others.
//
// The token is a bearer credential over that person's consent record: it reads
// their per-purpose state, withdraws, and GRANTS — a forged grant re-opens
// mail to someone who never consented, with a proof row attributing the
// decision to them. One rendered message carries one token, so a message with
// a second addressee hands the first recipient's credential to a third party.
//
// V1 refuses rather than rendering per addressee, because one token per
// recipient means one message per recipient and that is a different delivery
// model. Nothing legitimate is lost: a transactional send carries no token and
// is unaffected, and a marketing blast addressed To+Cc behind a shared
// unsubscribe link is not a shape this product supports.
//
// It maps to 422 with the fix in the message: the caller re-issues one send
// per recipient.
type SharedUnsubscribeTokenError struct{}

func (e *SharedUnsubscribeTokenError) Error() string {
	return "a send that carries an unsubscribe link reaches one addressee at a time — re-issue it once per recipient, with no cc"
}

// FieldFault refuses a second addressee: one rendered message carries one recipient's token.
func (e *SharedUnsubscribeTokenError) FieldFault() (field, code, message string) {
	return "recipients", "shared_unsubscribe_token", e.Error()
}

// redactedToken stands in for the recipient's preference token in the copy of
// the message the workspace RECORDS. The token is a bearer credential over
// that person's consent record — on the anonymous public edge it reads their
// per-purpose state, withdraws, and grants, under a system principal that
// short-circuits every RBAC gate — so it belongs on the mail and nowhere
// else: not in the durable activity body, which any seat holding
// activity:read serves back (the seeded read_only role reads every one of
// them), and not in the 202 the API caller reads.
//
// The record keeps the footer's SHAPE, built by the same two functions from
// this stand-in, so a reader still sees that the send carried a working
// one-click link and which purpose it pointed at, and loses only the value
// that would let them use it. It is path-safe on purpose: url.PathEscape
// leaves it alone, so the recorded URL reads as a URL and can never be
// mistaken for the pref_-prefixed credential it replaces.
const redactedToken = "token-redacted"

// sendDeliverability is what a send must carry for a mailbox provider to
// accept it as bulk mail, in the three renderings that must not be confused
// for one another.
type sendDeliverability struct {
	// listUnsubscribe is the RFC 8058 header value.
	listUnsubscribe string
	// transmitted is the body that goes on the wire. It carries the live
	// token, because the recipient's one-click link IS that token.
	transmitted string
	// recorded is the body the timeline row keeps and every authenticated
	// read of it serves: the same message with the capability redacted.
	recorded string
}

// deliverability derives what a send must carry for a mailbox provider to
// accept it as bulk mail: the RFC 8058 List-Unsubscribe header value and the
// human-visible footer, both built from ONE token so they cannot diverge.
// It returns both bodies, footer already applied.
//
// ok is false when the address carries no unsubscribe surface — a locked
// (transactional) purpose, or an address no person holds — in which case a
// transactional message has nothing to unsubscribe from and an address the
// consent gate would refuse discloses nothing. Those sends carry no token, so
// both bodies are the one the caller wrote.
//
// recipients is the MERGED addressee list, every To and Cc address, because
// the refusal above counts who RECEIVES the rendered message rather than how
// they were addressed.
func (s *Store) deliverability(ctx context.Context, body string, recipients []string, purposeKey string) (sendDeliverability, error) {
	untokenized := sendDeliverability{transmitted: body, recorded: body}
	if s.unsubscribe == nil || len(recipients) == 0 {
		return untokenized, nil
	}
	token, ok, err := s.unsubscribe.UnsubscribeToken(ctx, recipients[0], purposeKey)
	if err != nil {
		return sendDeliverability{}, err
	}
	if !ok {
		return untokenized, nil
	}
	// The refusal is tested HERE, after the linker has answered, because that
	// answer is what says this purpose carries an unsubscribe surface at all —
	// a multi-recipient transactional send reached the branch above and left
	// already, carrying no token to leak.
	if distinctAddresses(recipients) > 1 {
		return sendDeliverability{}, &SharedUnsubscribeTokenError{}
	}
	if s.publicBaseURL == "" {
		// Fail loudly rather than derive the base from the request: the link
		// carries the recipient's unsubscribe token, and a marketing send
		// may not go out without a working, non-forgeable List-Unsubscribe
		// URL (features/06 §1.2).
		return sendDeliverability{}, fmt.Errorf("send: public base URL is not configured; a marketing send must carry a working List-Unsubscribe URL")
	}
	unsubURL := unsubscribeURL(s.publicBaseURL, token, purposeKey)
	return sendDeliverability{
		listUnsubscribe: listUnsubscribeHeader(unsubURL),
		transmitted:     appendUnsubscribeFooter(body, s.publicBaseURL, token, unsubURL),
		recorded: appendUnsubscribeFooter(body, s.publicBaseURL, redactedToken,
			unsubscribeURL(s.publicBaseURL, redactedToken, purposeKey)),
	}, nil
}

// distinctAddresses counts who a rendered message actually reaches. Addresses
// are compared case- and space-insensitively, the way a mail server treats
// them, so the same person listed twice — once in To and once in Cc, or with
// different capitalisation — is one addressee and not a refusal.
func distinctAddresses(recipients []string) int {
	seen := make(map[string]bool, len(recipients))
	for _, addr := range recipients {
		key := normalizeAddress(addr)
		if key == "" {
			continue
		}
		seen[key] = true
	}
	return len(seen)
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
