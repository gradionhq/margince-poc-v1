// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// deliveryFacts are the timestamps the standing invitation carries. Every one is
// nullable, and which of them are set is the whole of what delivery state means.
type deliveryFacts struct {
	expiresAt   *time.Time
	sentAt      *time.Time
	deliveredAt *time.Time
	failedAt    *time.Time
	consumedAt  *time.Time
}

// state reads the facts as one word a seller can act on.
//
// The order matters and is not arbitrary — it runs from the most decided outcome
// backwards. A consumed credential is the end of the story whatever happened on
// the way, and a bounce outranks the send that preceded it, because "we sent it"
// is not what the seller needs to hear when it came straight back.
//
// This exists because delivery and access are separate questions. A participant
// whose invitation bounced and one whose access was revoked are in completely
// different situations — one needs a corrected address, the other was removed on
// purpose — and a single "not in yet" would collapse them into one.
func (d deliveryFacts) state(revoked bool) crmcontracts.DealRoomDeliveryState {
	// Revocation answers first: whatever the credential's own history, it no
	// longer stands, and reporting `sent` for somebody who was removed would
	// read as though the invitation were still working its way to them.
	if revoked {
		return crmcontracts.DealRoomDeliveryStateNone
	}
	switch {
	case d.consumedAt != nil:
		return crmcontracts.DealRoomDeliveryStateConsumed
	case d.failedAt != nil:
		return crmcontracts.DealRoomDeliveryStateFailed
	case d.deliveredAt != nil:
		return crmcontracts.DealRoomDeliveryStateDelivered
	case d.sentAt != nil:
		return crmcontracts.DealRoomDeliveryStateSent
	case d.expiresAt != nil:
		// A credential exists but nothing has been handed to a mail relay —
		// either the installation has none configured, or the caller is
		// delivering the link itself.
		return crmcontracts.DealRoomDeliveryStatePending
	}
	return crmcontracts.DealRoomDeliveryStateNone
}
