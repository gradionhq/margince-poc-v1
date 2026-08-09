// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// A send starts either as a reply or as a new conversation, and ADR-0087
// makes that choice explicit rather than inferring it from a missing anchor.
// An accidentally absent anchor would otherwise become a new conversation
// silently, losing a reply's threading with no signal anyone could see.
//
// Both origins resolve to the SAME two facts — the threading chain the
// message carries and the record links its timeline row inherits — and
// everything after that resolution is one code path, which is what keeps
// the authorization order, the consent gate, deliverability derivation,
// identity minting and the single-transaction staging from forking.

import (
	"context"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// SendOrigin is where one outbound message starts. Exactly one of the two
// constructors below produces a usable value; the zero value refuses, so a
// caller that forgets to name an origin cannot accidentally send anything.
type SendOrigin struct {
	// anchor is the activity being replied to. Zero on an account-started
	// send, which is the only way the two are told apart.
	anchor ids.ActivityID
	// links are the record links an account-started message is filed under,
	// supplied explicitly because there is no anchor to inherit them from.
	// Each is row-scope probed at insert by insertActivityLinks.
	links []ActivityLinkInput
}

// FromActivity is the reply origin: the anchor is read, its threading chain
// is continued, and the new activity inherits its record links.
func FromActivity(anchor ids.ActivityID) SendOrigin {
	return SendOrigin{anchor: anchor}
}

// FromAccount is the account-started origin: no anchor, a fresh thread
// rooted at this message's own newly minted identity, and record links
// named by the caller.
func FromAccount(links []ActivityLinkInput) SendOrigin {
	return SendOrigin{links: links}
}

// isReply reports whether this origin continues an existing conversation.
func (o SendOrigin) isReply() bool { return o.anchor.UUID != ids.UUID{} }

// NoSendOriginError refuses a send whose origin was never named. It is a
// composition defect on any first-party transport — both handlers construct
// an origin — so it carries no FieldFault: there is no request field a
// caller could correct.
type NoSendOriginError struct{}

func (e *NoSendOriginError) Error() string {
	return "send has no origin: name the activity it replies to, or the records it starts from"
}

// resolve reads whatever the origin needs BEFORE the guard sequence runs,
// so an unreadable anchor answers with the row-scope verdict and nothing
// else. An account origin reads nothing here: its links are probed at
// insert, inside the transaction, where a link target that disappeared
// between the two is still caught.
func (o SendOrigin) resolve(ctx context.Context, s *Store) ([]ActivityLinkInput, error) {
	if !o.isReply() {
		if len(o.links) == 0 {
			return nil, &NoSendOriginError{}
		}
		return o.links, nil
	}
	anchor, err := s.GetActivity(ctx, o.anchor, storekit.LiveOnly)
	if err != nil {
		return nil, err
	}
	return inheritedLinks(anchor), nil
}

// threading derives the conversation chain this send carries, inside the
// staging transaction. An account-started message answers nothing, so it
// roots the thread at its own identity — the same key capture derives when
// it later reads that root message back out of the mailbox.
func (o SendOrigin) threading(ctx context.Context, tx pgx.Tx, messageID string) (threading, error) {
	if !o.isReply() {
		return threading{threadKey: messageID}, nil
	}
	return anchorThreading(ctx, tx, o.anchor, messageID)
}

// inheritedLinks carries the anchor's own links onto the reply, so the sent
// message lands on the same records' timelines as the conversation it
// answers. The links were already visibility-checked as part of reading the
// anchor, and each one is re-checked at insert.
func inheritedLinks(anchor crmcontracts.Activity) []ActivityLinkInput {
	if anchor.Links == nil {
		return nil
	}
	links := make([]ActivityLinkInput, 0, len(*anchor.Links))
	for _, l := range *anchor.Links {
		links = append(links, ActivityLinkInput{EntityType: string(l.EntityType), EntityID: ids.UUID(l.EntityId)})
	}
	return links
}
