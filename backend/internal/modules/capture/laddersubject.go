// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// Whom the creation ladder is ABOUT (ADR-0082/A127 §3).
//
// Its own file because it answers a question the ladder itself does not: the
// tiers below decide WHETHER to create a record, and this decides FOR WHOM.
// The two are separate from the message's AUTHOR, which is never changed here —
// authorship drives reply detection, so a party substituted into it would be
// reported as having written mail they did not.

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// ladderSubjectTx names the party the creation ladder is about.
//
// Ordinarily that is the message's derived counterparty. When the counterparty
// is a colleague, it is the first external address the message names — the
// prospect in an introduction — and when there is none it is nobody, which
// leaves the ladder with nothing to decide.
//
// The returned Counterparty carries the SUBJECT's address and domain but keeps
// the record's direction: direction describes the message, not the party.
func (s *Sink) ladderSubjectTx(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord) (connector.Counterparty, error) {
	cp := rec.Counterparty
	if cp.Email == "" {
		return connector.Counterparty{}, nil
	}
	internal, err := s.internalDomainTx(ctx, tx, cp.Domain)
	if err != nil {
		return connector.Counterparty{}, err
	}
	if !internal {
		return cp, nil
	}

	own, err := ownDomainsTx(ctx, tx)
	if err != nil {
		return connector.Counterparty{}, err
	}
	external := own.External(rec.Addresses)
	if len(external) == 0 {
		return connector.Counterparty{}, nil
	}
	// The first external address in header order: To before Cc before Bcc, as
	// the sender wrote them. Picking among several is a judgement no header
	// supports, and the ones not picked are still recorded as participants.
	stand := external[0]
	return connector.Counterparty{
		Email: stand,
		// No display name: MessageParticipant carries an address and a header
		// position, not a name, and inventing one from the local part would put
		// a guess where provenance belongs. Enrichment names them properly.
		DisplayName: "",
		Domain:      domainOfAddress(stand),
		Direction:   cp.Direction,
		// The owner attestation and the unsubscribe header belong to the
		// message's real counterparty, not to a party standing in for them:
		// the workspace has not written to this address just because a
		// colleague copied them.
	}, nil
}
