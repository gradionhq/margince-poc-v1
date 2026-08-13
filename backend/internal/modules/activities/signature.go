// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The sender's sign-off on an outbound message.
//
// The signature belongs to the identity module's world, not this one, so it
// arrives through a seam compose injects — this module may not import a
// sibling. Nil is a role wired without one, and a role that cannot read a
// signature sends unsigned mail rather than refusing to send: an unsigned
// message is what the product did for its whole life until now, and a rep
// blocked from replying because a settings row could not be read would be the
// worse failure.

import (
	"context"
	"html"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// SignatureReader answers what the SENDER signs their mail with. It is asked
// only about the authenticated caller: a send signs with its own sender's
// sign-off, and there is no call shape here that names anybody else.
type SignatureReader interface {
	SignatureFor(ctx context.Context, userID ids.UUID) (string, error)
}

// WithSignature wires the sign-off the send path appends. Compose calls this;
// the zero Store keeps sending unsigned.
func (s *Store) WithSignature(reader SignatureReader) *Store {
	clone := *s
	clone.signature = reader
	return &clone
}

// WithSignature returns handlers whose send path appends the sender's sign-off.
func (h Handlers) WithSignature(reader SignatureReader) Handlers {
	h.store = h.store.WithSignature(reader)
	return h
}

// signedBody returns the message with the sender's sign-off beneath it.
//
// The separator is a blank line rather than the "-- " sig-dash: this product's
// own reply parser treats that dash as a signature boundary and cuts everything
// below it (textlang.NewTextOnly), so writing one here would make our own
// captured copy of the thread end at the signature we just added.
//
// An agent caller has no signature of its own and signs nothing. It acts under
// a human's authority but it is not that human, and a tool-written message
// arriving under somebody's personal sign-off claims a hand that never touched
// it.
func (s *Store) signedBody(ctx context.Context, body string) (string, error) {
	sign, err := s.senderSignature(ctx)
	if err != nil {
		return "", err
	}
	if sign == "" {
		return body, nil
	}
	return strings.TrimRight(body, "\n") + "\n\n" + sign, nil
}

// senderSignature is the caller's own sign-off, trimmed, or empty when there is
// none to add — no reader wired, no human actor, or nothing written.
//
// One lookup for both renderings. Two would be two chances for the plain part
// and the markup part of one message to disagree about who signed it.
func (s *Store) senderSignature(ctx context.Context) (string, error) {
	if s.signature == nil {
		return "", nil
	}
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman || actor.UserID == ids.Nil {
		return "", nil
	}
	sign, err := s.signature.SignatureFor(ctx, actor.UserID)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sign), nil
}

// signedHTML is signedBody's markup twin: the same sign-off and the same
// unsubscribe footer, rendered as HTML.
//
// The signature is stored as PLAIN TEXT, so it is escaped before it reaches a
// markup document. A member whose sign-off contains "Weiß & Konrad <Recht>"
// must not have it silently become a broken tag, and one who typed a script tag
// must not have it run in the recipient's client.
//
// An empty markup body stays empty: a message with no HTML alternative is sent
// as a single text/plain part, and manufacturing markup here would make every
// plain send multipart for no reader's benefit.
func (s *Store) signedHTML(ctx context.Context, htmlBody string, derived sendDeliverability) (string, error) {
	if strings.TrimSpace(htmlBody) == "" {
		return "", nil
	}
	sign, err := s.senderSignature(ctx)
	if err != nil {
		return "", err
	}
	out := htmlBody
	if sign != "" {
		out += "\n<p>" + htmlLines(sign) + "</p>"
	}
	if footer := derived.htmlFooter(); footer != "" {
		out += "\n" + footer
	}
	return out, nil
}

// htmlLines escapes plain text for a markup document and keeps its line breaks,
// which a signature depends on: a name, a company and a phone number written on
// three lines are three lines to the person who wrote them.
func htmlLines(text string) string {
	return strings.ReplaceAll(html.EscapeString(text), "\n", "<br>")
}
