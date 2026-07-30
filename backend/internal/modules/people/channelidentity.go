// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The channel-identity binding: the one write path for
// person_channel_identity rows, and the one place the identity race between
// two simultaneous first messages is settled.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// fieldReachability names the channel-reachability image in an audit row and
// in the person.updated event's changed fields — the same word the person
// read projects it under (person_children.go's attachPersonReachability), so
// a human reading the trail and a human reading the record see one name.
const fieldReachability = "reachability"

// ResolveOrCreateChannelIdentity binds ci to personID and returns the person
// the identity ACTUALLY belongs to once the dust settles — personID when this
// call created the binding, the incumbent's person when one already existed.
//
// It is an insert-then-adopt, not a read-then-insert, because the check and
// the act cannot be one statement: two first messages from the same Telegram
// user arrive concurrently, both lanes miss, and both callers would insert.
// Here the loser blocks on Postgres' speculative-insert lock until the winner
// commits, then reads the winner out and adopts it. The database is the
// arbiter, so there is no window to lose.
//
// A caller that had to CREATE a person before it could offer one must treat a
// returned id different from personID as having lost the race: its own person
// row is speculative and must not survive the transaction, or the human ends
// up on two records with the conversation on one of them.
//
// The row carries no audit entry of its own, by the same rule as person_email
// and person_phone: it is a satellite of the person write that encloses it,
// and that write's audit row is the one an auditor reads.
func ResolveOrCreateChannelIdentity(ctx context.Context, tx pgx.Tx, personID ids.PersonID, ci connector.ChannelIdentity) (ids.PersonID, error) {
	if ci.Provider == "" || ci.ChannelUserID == "" {
		return ids.PersonID{}, errors.New("people: a channel identity needs both a provider and a channel user id")
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return ids.PersonID{}, err
	}
	// source names the channel the binding came from; the provider IS that
	// channel, and unlike a mail message there is no per-record id worth
	// stamping — the binding outlives every message that refreshed it.
	tag, err := tx.Exec(ctx, `
		INSERT INTO person_channel_identity
			(workspace_id, person_id, provider, channel_user_id, username, source, captured_by)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $3, $6)
		ON CONFLICT (workspace_id, provider, channel_user_id) WHERE archived_at IS NULL
		DO NOTHING`,
		workspaceID(ctx), personID, ci.Provider, ci.ChannelUserID, ci.Username, by)
	if err != nil {
		return ids.PersonID{}, fmt.Errorf("people: binding channel identity: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return personID, nil
	}

	// Zero rows means a live binding already exists — either it predates this
	// call or the concurrent winner has now committed. A fresh statement takes
	// a fresh snapshot under READ COMMITTED, so this read sees it.
	var winner ids.PersonID
	err = tx.QueryRow(ctx, `
		SELECT person_id FROM person_channel_identity
		WHERE provider = $1 AND channel_user_id = $2 AND archived_at IS NULL`,
		ci.Provider, ci.ChannelUserID).Scan(&winner)
	if err != nil {
		return ids.PersonID{}, fmt.Errorf("people: reading the channel identity that won the bind: %w", err)
	}
	return winner, nil
}

// SetChannelIdentityBlocked applies a my_chat_member reachability change
// (design §4.2 D9): a block sets blocked_at, an unblock clears it. It NEVER
// touches archived_at — archiving is the same trap this file's package
// comment warns against for the bind path: the dedupe lane and the unique
// index both read archived_at IS NULL, so archiving on block would fork a
// returning customer into a second Person the moment their next message
// misses the lane.
//
// The WHERE clause guards on the identity's CURRENT blocked state rather
// than issuing an unconditional SET, which is what makes this idempotent:
// Telegram redelivers my_chat_member, and a repeat delivery must touch zero
// rows — not move blocked_at's timestamp forward, not re-fire the
// updated_at/version trigger, and not leave a second audit row for a state
// that did not change.
//
// A my_chat_member naming an identity nobody has bound yet (blocked before
// ever messaging the bot) matches no row. That is not a fault: there is no
// reachability state yet to correct.
func (s *Store) SetChannelIdentityBlocked(ctx context.Context, tx pgx.Tx, ci connector.ChannelIdentity, blocked bool) error {
	if ci.Provider == "" || ci.ChannelUserID == "" {
		return errors.New("people: a channel identity needs both a provider and a channel user id")
	}
	query := `
		UPDATE person_channel_identity SET blocked_at = now()
		WHERE provider = $1 AND channel_user_id = $2
		  AND archived_at IS NULL AND blocked_at IS NULL
		RETURNING person_id`
	if !blocked {
		query = `
			UPDATE person_channel_identity SET blocked_at = NULL
			WHERE provider = $1 AND channel_user_id = $2
			  AND archived_at IS NULL AND blocked_at IS NOT NULL
			RETURNING person_id`
	}
	// At most one row can come back: the partial unique index admits one live
	// binding per (workspace, provider, channel_user_id).
	var personID ids.PersonID
	err := tx.QueryRow(ctx, query, ci.Provider, ci.ChannelUserID).Scan(&personID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("people: setting channel identity blocked=%t: %w", blocked, err)
	}
	// The flip really happened, so the guard above proves what the prior
	// state was: a row only matched because it held the opposite of `blocked`.
	return auditChannelIdentityChange(ctx, tx, personID,
		reachabilityImage(ci.Provider, blocked), reachabilityImage(ci.Provider, !blocked))
}

// reachabilityImage is the audit/event field image for a reachability flip.
// The provider travels with it because a person can hold identities on
// several channels, and "reachable: false" alone would not say on which.
func reachabilityImage(provider string, reachable bool) map[string]any {
	return map[string]any{fieldReachability: map[string]any{
		"provider": provider, "reachable": reachable,
	}}
}

// auditChannelIdentityChange puts a channel-identity mutation on the PERSON's
// write trail: domain row + audit row + outbox event in the caller's one
// transaction.
//
// The person is the right subject, not the satellite. Both mutations this
// serves change what the RECORD says — reachability is projected onto the
// person read (person_children.go), and it decides whether a rep is offered
// the reply box at all — so an auditor asking "why can this person no longer
// be messaged, and since when" must find the answer on the person's history,
// exactly as a person_profile_field write is audited as a person update
// (enrichsignature.go).
//
// The bind path needs none of this: it is enclosed by the person create whose
// audit row already covers it. These two writes have no enclosing person
// mutation — they reach a Person who already exists.
//
// Every genuine change gets a row, and there is no per-identity budget on top
// of that. The counterpart decides how often they flip, so the question is what
// bounds the trail, and the answer is where the bound belongs rather than here:
//
//   - Repetition costs nothing. Both callers guard on the CURRENT stored state,
//     so a redelivered my_chat_member and a message repeating the handle we
//     already hold reach no row and write nothing — which covers everything
//     Telegram generates on its own.
//   - What is left is one row per state a human actually changed, and a human
//     who can change state can already send messages, each of which costs an
//     activity, an audit row and an event of its own. A budget here would cap a
//     constant factor on a path that is unbounded by design, and the real bound
//     on both is the same one: the workspace disconnects the bot.
//   - A capped trail would be worse than a long one. Reachability decides
//     whether a rep is offered the reply box at all, so a flip recorded nowhere
//     leaves an auditor asking "since when can we not message this person" with
//     a record that changed and a history that does not say so — and a trail
//     silently truncated by a budget reads exactly like a complete one.
func auditChannelIdentityChange(ctx context.Context, tx pgx.Tx, personID ids.PersonID, before, after map[string]any) error {
	auditID, err := storekit.Audit(ctx, tx, actionUpdate, entityPerson, personID.UUID, before, after)
	if err != nil {
		return err
	}
	return storekit.EmitEvent(ctx, tx, auditID, personID.UUID,
		crmcontracts.PublicEventPersonUpdated{ChangedFields: after})
}

// ReachableChannelIdentities returns every identity personID can currently be
// REACHED at on provider — a live row with blocked_at IS NULL (design §6.6).
//
// It returns a list rather than one identity because a person may hold more than
// one account on the same channel: the unique key is
// (workspace, provider, channel_user_id), which binds an account to one person
// and not a person to one account. Handing the caller the first row would reply
// to whichever account the planner returned, so the choice is theirs to refuse.
//
// An EMPTY list is an answer, not a fault: a person who never messaged the
// workspace's bot and one who blocked it are both simply unreachable, and the
// caller owes the rep that sentence rather than an error. Only a failure to ASK
// is an error.
//
// The username travels because a caller may show it; nothing routes on it, since
// a handle can be released and re-claimed while the account id cannot.
func ReachableChannelIdentities(ctx context.Context, tx pgx.Tx, personID ids.PersonID, provider string) ([]connector.ChannelIdentity, error) {
	rows, err := tx.Query(ctx, `
		SELECT channel_user_id, coalesce(username, '')
		  FROM person_channel_identity
		 WHERE person_id = $1 AND provider = $2
		   AND archived_at IS NULL AND blocked_at IS NULL
		 ORDER BY created_at`, personID, provider)
	if err != nil {
		return nil, fmt.Errorf("people: reading reachable channel identities: %w", err)
	}
	defer rows.Close()
	var out []connector.ChannelIdentity
	for rows.Next() {
		identity := connector.ChannelIdentity{Provider: provider}
		if err := rows.Scan(&identity.ChannelUserID, &identity.Username); err != nil {
			return nil, fmt.Errorf("people: reading reachable channel identities: %w", err)
		}
		out = append(out, identity)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("people: reading reachable channel identities: %w", err)
	}
	return out, nil
}
