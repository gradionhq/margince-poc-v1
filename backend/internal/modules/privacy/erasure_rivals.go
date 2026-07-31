// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The precondition Art. 17 erasure runs before it destroys anything: no OTHER
// live Person may still hold one of this subject's identifiers.
//
// Both satellites that identify a human — person_email and
// person_channel_identity — carry a unique index that is PARTIAL on
// archived_at IS NULL, and archiving a Person archives its satellites
// (people/person.go). So the pair (archive the record, let the human write
// again) legitimately produces a SECOND Person holding the same address or the
// same channel account, with a live binding. Erasure resolves the subject by
// person_id, which is the right scope for the record but the wrong scope for
// the identifier: it arms the suppression list and purges the raw evidence for
// the ACCOUNT while anonymizing only ONE of the records that account reaches.
// What is left behind is the worst of both — the duplicate still names the
// human, still reads as reachable, and a rep can message them; while that
// duplicate's own evidence was destroyed by an erasure that was never about it.
//
// So the erasure refuses instead. The alternative — cascading into the rival
// Person — would make one Art. 17 request anonymize records it never named,
// which is unbounded in exactly the direction erasure must not be. A refusal
// leaves the installation intact and tells the operator the one thing that
// makes the request satisfiable: merge the duplicates, then erase the survivor.
//
// The rival must be a LIVE Person. An archived duplicate holds no live binding
// and is reachable by nobody; its stale identifier rows are removed by the
// account-scoped deletes in eraseChannelIdentities and anonymizeSubjectRows,
// which is why refusing on those too would deadlock — with no live record to
// merge into, there would be no order in which either could ever be erased.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// refuseRivalIdentifierHolders reports ErrConflict when a different, live
// Person still holds one of the subject's addresses or channel accounts.
//
// The messages name the KIND of identifier and the remedy, never the value or
// the rival's id: the operator asking for an erasure does not need the
// duplicate's identity to act on this, and an erasure refusal is not a place to
// disclose one record's contents to whoever can reach another.
func refuseRivalIdentifierHolders(ctx context.Context, tx pgx.Tx, subject ids.PersonID, emails []string, identities []channelIdentity) error {
	held, err := anotherLivePersonHoldsAnEmail(ctx, tx, subject, emails)
	if err != nil {
		return err
	}
	if held {
		return fmt.Errorf("another live person record still holds one of this subject's email addresses; "+
			"merge the duplicate records first, so the erasure covers the whole subject: %w", apperrors.ErrConflict)
	}
	held, err = anotherLivePersonHoldsAChannelAccount(ctx, tx, subject, identities)
	if err != nil {
		return err
	}
	if held {
		return fmt.Errorf("another live person record still holds one of this subject's messaging accounts; "+
			"merge the duplicate records first, so the erasure covers the whole subject: %w", apperrors.ErrConflict)
	}
	return nil
}

// anotherLivePersonHoldsAnEmail probes person_email for the subject's
// addresses under a different, un-archived Person. The satellite row's own
// archived_at is deliberately not filtered: a live Person holding the
// subject's address in an archived row still stores it.
func anotherLivePersonHoldsAnEmail(ctx context.Context, tx pgx.Tx, subject ids.PersonID, emails []string) (bool, error) {
	if len(emails) == 0 {
		return false, nil
	}
	var held bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM person_email pe
		    JOIN person p ON p.id = pe.person_id
		   WHERE pe.person_id <> $1 AND p.archived_at IS NULL AND pe.email = ANY($2))`,
		subject, emails).Scan(&held)
	return held, err
}

// anotherLivePersonHoldsAChannelAccount is the same probe over
// person_channel_identity. The (provider, account) pairs travel as two
// parallel arrays and are re-paired by unnest, so a subject with accounts on
// two providers cannot match a rival that merely holds one provider's name and
// the other provider's account id.
func anotherLivePersonHoldsAChannelAccount(ctx context.Context, tx pgx.Tx, subject ids.PersonID, identities []channelIdentity) (bool, error) {
	if len(identities) == 0 {
		return false, nil
	}
	providers := make([]string, 0, len(identities))
	accounts := make([]string, 0, len(identities))
	for _, identity := range identities {
		providers = append(providers, identity.Provider)
		accounts = append(accounts, identity.ChannelUserID)
	}
	var held bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM person_channel_identity pci
		    JOIN person p ON p.id = pci.person_id
		   WHERE pci.person_id <> $1 AND p.archived_at IS NULL
		     AND (pci.provider, pci.channel_user_id) IN (
		           SELECT provider, account FROM unnest($2::text[], $3::text[]) AS t(provider, account)))`,
		subject, providers, accounts).Scan(&held)
	return held, err
}
