// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Who the recipient sees a message is from.
//
// Separate from the signature seam beside it, and deliberately: a signature is
// what the sender WROTE and this is who they ARE. They also come from different
// owners — the signature is the person's own text, the display name is the seat
// the identity module holds — and a message can honestly carry one without the
// other.
//
// A From header with no display name shows the address's local part in every
// mail client, so a message from lars@gradion.com arrives from "lars". The name
// exists in app_user; this seam is how it reaches the wire.

import "context"

// SenderNameReader answers what the human behind this call is called.
//
// It takes no user id, unlike SignatureFor. The identity module resolves the
// acting human itself — including the human an agent acts on behalf of — and
// that resolution is the one this send must not second-guess: the name on the
// envelope has to be the same human the audit log records.
type SenderNameReader interface {
	ActorIdentity(ctx context.Context) (name, email string, err error)
}

// WithSenderName wires the display name the From header carries. Compose calls
// this; the zero Store sends a bare address, which is what every message did
// before the name was available.
func (s *Store) WithSenderName(reader SenderNameReader) *Store {
	clone := *s
	clone.senderName = reader
	return &clone
}

// WithSenderName returns handlers whose send path names its sender.
func (h Handlers) WithSenderName(reader SenderNameReader) Handlers {
	h.store = h.store.WithSenderName(reader)
	return h
}

// senderDisplayName resolves the name for this send, or empty.
//
// Empty is never an error. A system principal, a seat this workspace no longer
// holds, and a member who has no display name on file all resolve to nothing —
// and a bare address is a correct From header. Refusing to send because a name
// could not be read would trade a cosmetic gap for a delivery failure.
func (s *Store) senderDisplayName(ctx context.Context) (string, error) {
	if s.senderName == nil {
		return "", nil
	}
	name, _, err := s.senderName.ActorIdentity(ctx)
	if err != nil {
		return "", err
	}
	return name, nil
}
