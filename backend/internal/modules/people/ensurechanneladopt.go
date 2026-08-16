// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Adopting the human an ADDRESS found, when a channel message names them by an
// account nothing is bound to yet.
//
// It sits beside the minting half rather than inside it because the two settle
// different questions. Minting decides who a stranger is; this decides that a
// human already on the books is the same human, and then attaches the account
// they can be answered at to the record that already describes them. The second
// is the one that writes onto a row somebody else created, which is why it
// carries the settle, the lock and the audit the first does not need.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// correspondingEmails is the address list the subject lock covers: the
// corroborating address when there is one, and nothing otherwise. A record that
// names its human by account alone can reach no address-keyed subject, so it
// takes no address lock and never stalls an unrelated erasure.
func correspondingEmails(email string) []string {
	if email == "" {
		return nil
	}
	return []string{email}
}

// channelCandidate is what the ladder is asked about: the account that NAMES the
// human, and the address that merely corroborates them.
//
// Both go in together because the ladder's precedence is the answer to the
// question a caller would otherwise have to ask itself — an established channel
// binding outranks a shared address (dedupe.go) — and because a later lane
// naming a different person is a report only the ladder can produce.
func channelCandidate(in EnsureChannelCounterpartyInput, name string) PersonCandidate {
	c := PersonCandidate{
		FullName:          name,
		ChannelIdentities: []connector.ChannelIdentity{in.Identity},
	}
	if in.CorroboratingEmail != "" {
		c.Emails = []string{in.CorroboratingEmail}
	}
	return c
}

// adoptEmailRoutedIncumbent binds this account to the human the ADDRESS found —
// someone already captured from mail, who holds no binding for this account yet.
//
// It is a sibling of offerChannelPerson rather than a branch inside the
// resolver: both settle who the person is and bind the account to them, and the
// difference — one mints, one adopts — is exactly what a reader needs held
// apart.
//
// Nothing writes the address here. The email lane MATCHED, so it is already on
// the incumbent by definition; an insert would collide with uq_person_email_dedupe
// on every message from this sender, and an audit row would claim a change that
// did not happen.
//
// Visibility is deliberately left alone. A mail-captured incumbent is
// owner-visible, and adopting does not widen that: a stranger's direct message
// must not be able to publish a rep's privately captured contact to the whole
// workspace.
func (s *Store) adoptEmailRoutedIncumbent(
	ctx context.Context, tx pgx.Tx, in EnsureChannelCounterpartyInput,
	match PersonResolution, res *EnsureChannelCounterpartyResult,
) error {
	// The ladder read is not the last word on WHICH row this is: a merge
	// committing between that read and this write retires the incumbent, and a
	// binding left on the retired row is a reply route nobody opens while the
	// activity link — settled separately by linkActivityToPerson — points at the
	// survivor. One hop is enough; a merge repoints rather than chains.
	var canonical ids.PersonID
	if err := tx.QueryRow(ctx,
		`SELECT coalesce(merged_into_id, id) FROM person WHERE id = $1 FOR UPDATE`,
		match.PersonID).Scan(&canonical); err != nil {
		return fmt.Errorf("people: settling the person this channel account belongs to: %w", err)
	}
	bound, err := ResolveOrCreateChannelIdentity(ctx, tx, canonical, in.Identity)
	if err != nil {
		return err
	}
	// The account may already belong to somebody else. Not to a concurrent
	// message from this same sender — those serialize on the subject lock above,
	// so a second one sees the winner's binding in the ladder and never reaches
	// here — but to any writer that binds outside that lock. The database is the
	// arbiter, so the account's real owner is what came back, never the row this
	// call proposed.
	res.PersonID = bound
	res.Conflict = match.Conflict
	if bound != canonical {
		// Losing that race leaves the committed state as a disagreement the
		// ladder could not have reported: its channel lane read BEFORE the
		// rival's binding existed, so match.Conflict is nil and nothing would
		// raise an identity review. Two records now describe one human — the
		// address found one, the account belongs to the other — which is the
		// duplicate this whole path exists to prevent, arriving by the one route
		// the resolver cannot see. Naming it here is what puts it in front of
		// somebody.
		//
		// The binding routes, per ladder precedence: an established channel
		// binding outranks a shared address.
		res.Conflict = &LaneConflict{
			RoutedTo: bound, Rival: canonical,
			RoutedLane: laneChannelIdentity, RivalLane: LaneEmail,
		}
		return nil
	}
	// The image names the ACCOUNT, not just its provider. This is the one record
	// that a channel account was attached to a person who already existed, and
	// "a dispact identity was bound" cannot answer the question an auditor
	// actually brings to it — which account now reaches this human.
	return auditChannelIdentityChange(ctx, tx, canonical,
		nil, map[string]any{fieldChannelIdentity: channelIdentityKey(in.Identity)})
}
