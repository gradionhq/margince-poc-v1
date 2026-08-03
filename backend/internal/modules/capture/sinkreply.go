// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The reply half of the capture Sink (CAP-FORMULA-1): an INBOUND message in a
// thread we previously wrote OUTBOUND in is a reply, and the engagement signal
// scoring feeds on that fact.
//
// The formula keys on nothing but thread_key and direction, so it holds for any
// threaded medium — mail and the channel connectors alike. What differs between
// them is only how the record NAMES its human, and counterpartyShapeOf
// (sinkchannel.go) is already the one place capture asks that question. This
// file routes through it rather than assuming an answer.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// channelEmail is the medium every mail connector reports. It is the MEDIUM and
// not the source system on purpose: gmail, imap and graph are three ways of
// reaching one inbox, and a consumer routing a reply back has the same job for
// all three. A channel connector reports its provider instead, which is the
// same rule — the provider IS the medium there.
const channelEmail = "email"

// replyOrigin is the single answer to "what medium did this reply arrive on,
// and who is already on file for it". Both halves come out of ONE switch over
// counterpartyShapeOf, so a new channel provider is one arm rather than a grep
// across every site that had re-derived the answer for itself.
type replyOrigin struct {
	// channel is the medium a reply must be SENT BACK on, which is why it names
	// the provider rather than a medium class: an automation answering an
	// inbound reply routes on this value alone, and a class would make it
	// re-derive the provider from the activity row anyway.
	channel string
	// contactID is the counterparty when they already resolve to a person.
	// Nil is the ordinary first-contact case, not a fault: the ensure that
	// creates them runs AFTER this transaction commits, so a first-ever sender
	// has no person yet on either medium.
	contactID *ids.PersonID
}

// emitReply is CAP-FORMULA-1: an INBOUND message in a thread we previously
// wrote OUTBOUND in is a reply — the engagement signal scoring feeds on.
// Emitted only when the activity row is new, so the at-least-once sync loop
// cannot double-fire it; never a subject heuristic.
func (s *Sink) emitReply(ctx context.Context, tx pgx.Tx, auditID ids.UUID, id ids.ActivityID, rec connector.NormalizedRecord, fields ActivityFields) error {
	if fields.Direction != connector.DirectionInbound || rec.ThreadKey == "" {
		return nil
	}
	var matched ids.UUID
	// archived_at IS NULL is the formula's own prior-outbound scan. An archived
	// message is off the timeline, so naming one as the matched outbound would
	// point the reply fact at a row its consumers cannot read back.
	//
	// Archiving here is not only a human act: the retention evaluator
	// (privacy/retention.go) and the noise sweep (activities/capturenoise.go)
	// both archive activities by machine. So an installation running a retention
	// policy stops reply-detecting threads whose outbound legs have aged out,
	// even for a customer replying today. That follows the formula rather than
	// this file's choosing, and it is the behaviour the spec pins.
	err := tx.QueryRow(ctx, `
		SELECT id FROM activity
		WHERE thread_key = $1 AND direction = 'outbound' AND archived_at IS NULL AND id <> $2
		ORDER BY occurred_at DESC LIMIT 1`,
		rec.ThreadKey, id).Scan(&matched)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("capture: reply detection: %w", err)
	}
	origin, ok, err := s.replyOriginOf(ctx, tx, rec.Counterparty)
	if err != nil {
		return err
	}
	if !ok {
		// A thread-matched inbound whose record names no counterparty at all.
		// The thread match is real and the medium is arguably still derivable
		// from the record, so this is a DEVIATION from the formula, which emits
		// on the match alone: what it refuses to do is publish a reply fact
		// naming a medium nothing in the record actually attests to. Deviations
		// belong upstream, not in a comment — .tmp/reply-origin/UPSTREAM.md
		// raises it, and no live producer reaches this arm today (every mail
		// connector drops a From-less message before capture).
		return nil
	}
	idempotencyKey := rec.NaturalKey.SourceSystem + ":" + rec.NaturalKey.SourceID
	payload := engagementReplyPayload(matched, origin, defaultOccurredAt(fields.OccurredAt), idempotencyKey)
	return storekit.EmitEvent(ctx, tx, auditID, id.UUID, payload)
}

// replyOriginOf resolves the medium a reply arrived on and the person already
// on file for it, in one switch over the shape the record names its human by.
// It reports ok=false when there is no origin to publish at all.
//
// The two malformed shapes return their sentinel rather than a bare miss.
// Upsert refuses both before any of this runs (sink.go), so reaching them here
// means that guard has been bypassed — an invariant break the reply path must
// state, not absorb into a silent no-event.
func (s *Sink) replyOriginOf(ctx context.Context, tx pgx.Tx, cp connector.Counterparty) (replyOrigin, bool, error) {
	return s.replyOriginForShape(ctx, tx, cp, counterpartyShapeOf(cp))
}

// replyOriginForShape is replyOriginOf with the classification handed in, so the
// arms can be walked across the whole enum rather than only the shapes a
// Counterparty can be built to produce. Nothing but the seam above and that walk
// calls it: a caller choosing its own shape would be re-deciding the one
// question counterpartyShapeOf exists to answer.
func (s *Sink) replyOriginForShape(ctx context.Context, tx pgx.Tx, cp connector.Counterparty, shape counterpartyShape) (replyOrigin, bool, error) {
	switch shape {
	case shapeMail:
		contact, found, err := mailReplyContact(ctx, tx, cp.Email)
		if err != nil {
			return replyOrigin{}, false, err
		}
		return withContact(replyOrigin{channel: channelEmail}, contact, found), true, nil
	case shapeChannel:
		contact, found, err := channelReplyContact(ctx, tx, cp.ChannelIdentity)
		if err != nil {
			return replyOrigin{}, false, err
		}
		return withContact(replyOrigin{channel: cp.ChannelIdentity.Provider}, contact, found), true, nil
	case shapeNone:
		return replyOrigin{}, false, nil
	case shapeAmbiguous:
		return replyOrigin{}, false, ErrCounterpartyNamedTwice
	case shapeHalfChannel:
		return replyOrigin{}, false, ErrChannelIdentityIncomplete
	default:
		// A shape added to the enum without an arm here. Naming it is the whole
		// value of this arm: the alternative is a new medium silently publishing
		// replies with an empty channel.
		return replyOrigin{}, false, fmt.Errorf("capture: reply origin: unhandled counterparty shape %d", shape)
	}
}

// withContact carries a resolved person onto the origin, and leaves it absent
// when the lookup found none. It exists so both arms above spell "found or not"
// the same way rather than each re-deriving a pointer from a flag.
func withContact(origin replyOrigin, contact ids.PersonID, found bool) replyOrigin {
	if found {
		origin.contactID = &contact
	}
	return origin
}

// mailReplyContact resolves a mail counterparty to a person already on file.
// A miss is not an error: the ensure that would create them runs after this
// transaction commits, so the normal first-contact reply simply has no contact
// to name yet.
func mailReplyContact(ctx context.Context, tx pgx.Tx, email string) (ids.PersonID, bool, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return ids.PersonID{}, false, nil
	}
	var personID ids.PersonID
	err := tx.QueryRow(ctx, `
		SELECT person_id FROM person_email WHERE email = $1 AND archived_at IS NULL
		ORDER BY is_primary DESC LIMIT 1`, normalized).Scan(&personID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.PersonID{}, false, nil
	}
	if err != nil {
		return ids.PersonID{}, false, fmt.Errorf("capture: reply contact lookup: %w", err)
	}
	return personID, true, nil
}

// channelReplyContact is the same resolution on a channel identity — the key a
// messaging connector names its human by, where mail names an address. The
// binding is unique per live identity (uq_person_channel_identity is partial on
// archived_at IS NULL), so this needs no ordering tiebreak the way the mail
// lookup needs is_primary.
func channelReplyContact(ctx context.Context, tx pgx.Tx, ci connector.ChannelIdentity) (ids.PersonID, bool, error) {
	if ci.Provider == "" || ci.ChannelUserID == "" {
		// shapeChannel already guarantees both halves, exactly as shapeMail
		// guarantees a non-empty address for the sibling lookup. Both refuse an
		// empty key anyway rather than probing on one: a half key matches by
		// accident or matches nothing, and neither is an answer.
		return ids.PersonID{}, false, nil
	}
	var personID ids.PersonID
	err := tx.QueryRow(ctx, `
		SELECT person_id FROM person_channel_identity
		WHERE provider = $1 AND channel_user_id = $2 AND archived_at IS NULL`,
		ci.Provider, ci.ChannelUserID).Scan(&personID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.PersonID{}, false, nil
	}
	if err != nil {
		return ids.PersonID{}, false, fmt.Errorf("capture: reply channel contact lookup: %w", err)
	}
	return personID, true, nil
}

// engagementReplyPayload builds the engagement.reply event from the origin the
// switch above resolved — the reply's channel and, when the counterparty is
// already a known person, their id (absent, not null, otherwise).
func engagementReplyPayload(matched ids.UUID, origin replyOrigin, occurredAt time.Time, idempotencyKey string) crmcontracts.PublicEventEngagementReply {
	payload := crmcontracts.PublicEventEngagementReply{
		MatchedOutboundActivityId: openapi_types.UUID(matched),
		Channel:                   origin.channel,
		OccurredAt:                occurredAt,
		IdempotencyKey:            idempotencyKey,
	}
	if origin.contactID != nil {
		id := openapi_types.UUID(origin.contactID.UUID)
		payload.ContactId = &id
	}
	return payload
}
