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

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// SenderNameReader answers what the human behind this call is called.
//
// It takes no user id, unlike SignatureFor: the identity module resolves the
// acting human itself. Note that it resolves an AGENT to the human it acts for,
// which is right for a draft and wrong for an envelope — senderDisplayName
// below refuses that case rather than passing it through.
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
// An agent send carries NO name, for the same reason it carries no signature:
// it acts under a human's authority but it is not that human, and a message
// arriving under somebody's name claims a hand that never touched it. The
// approval authorizes the sending; it does not make the approver the author.
// ActorIdentity resolves an agent to the human it acts for — which is right for
// a draft, where the model needs to know who it writes AS — so the refusal has
// to be made here rather than left to that resolution.
//
// The From header is the stronger claim of the two: a signature sits at the
// bottom of a message somebody may not read, and this is the line the inbox
// shows before anything is opened. Naming a human there while deliberately
// withholding their sign-off below would be the same lie told louder.
//
// Empty is never an error otherwise. A system principal, a seat this workspace
// no longer holds, and a member with no display name on file all resolve to
// nothing — and a bare address is a correct From header. Refusing to send
// because a name could not be read would trade a cosmetic gap for a delivery
// failure.
func (s *Store) senderDisplayName(ctx context.Context) (string, error) {
	if s.senderName == nil {
		return "", nil
	}
	if actor, ok := principal.Actor(ctx); !ok || actor.Type != principal.PrincipalHuman {
		return "", nil
	}
	name, _, err := s.senderName.ActorIdentity(ctx)
	if err != nil {
		return "", err
	}
	return name, nil
}
