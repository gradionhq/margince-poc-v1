// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// The buyer's store: the credential exchange, session resolution and sign-out.
//
// EVERY method in the store_public*.go files carries the session's room and
// participant as a mandatory SQL predicate, and none of them calls auth.Require
// or EnsureVisible — those gates refuse a buyer by design, and a buyer's
// authority is the session row, nothing else. The three anonymous operations
// (peek, exchange, link request) share ONE query shape for every way a
// credential can be dead, so their refusals are indistinguishable from outside:
// an unknown string, a consumed one, a lapsed one, a superseded one and a
// revoked participant's all stop at the same indexed lookup.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// sessionObject is the table the session writes audit against.
const sessionObject = "deal_room_session"

// exchangeablePredicate is the one rule for "this credential admits its
// bearer right now", spelled once so peek and exchange cannot disagree. The
// room's state is deliberately NOT part of it beyond `archived`: a paused or
// closed room still exchanges, and the session then reports the state to the
// authenticated buyer. Refusing here instead would make the anonymous edge an
// oracle for whether a room is paused.
const exchangeablePredicate = `i.token_hash = $1
	  AND i.consumed_at IS NULL AND i.superseded_at IS NULL AND i.expires_at > now()
	  AND p.id = i.participant_id AND p.revoked_at IS NULL
	  AND r.id = p.room_id AND r.archived_at IS NULL AND r.state <> 'archived'`

// PeekCredential answers whether a credential can be exchanged, and nothing
// else. Never an error for a dead credential: absence is a false.
func (s *Store) PeekCredential(ctx context.Context, raw string) (bool, error) {
	var ok bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM deal_room_invitation i, deal_room_participant p, deal_room r
			                 WHERE `+exchangeablePredicate+`)`,
			digestOfCredential(raw)).Scan(&ok)
	})
	if err != nil {
		return false, fmt.Errorf("peek deal room credential: %w", err)
	}
	return ok, nil
}

// IssuedSession is the token a buyer presents from now on, returned once.
type IssuedSession struct {
	Token     string
	ExpiresAt time.Time
}

// ExchangeCredential consumes a one-time credential and opens a session.
//
// Consuming and checking are ONE statement: an UPDATE whose WHERE is the
// exchangeable predicate, so two concurrent exchanges of the same credential
// cannot both win — the second finds consumed_at already set and matches no
// row. Every failure is apperrors.ErrNotFound, whatever the cause.
func (s *Store) ExchangeCredential(ctx context.Context, raw string) (IssuedSession, error) {
	token, digest, err := mintSessionToken()
	if err != nil {
		return IssuedSession{}, err
	}
	expiresAt := time.Now().UTC().Add(sessionTTL)
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var participantID ids.DealRoomParticipantID
		var roomID ids.DealRoomID
		err := tx.QueryRow(ctx,
			`UPDATE deal_room_invitation i SET consumed_at = now()
			   FROM deal_room_participant p, deal_room r
			  WHERE `+exchangeablePredicate+`
			  RETURNING p.id, p.room_id`,
			digestOfCredential(raw)).Scan(&participantID, &roomID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("consume deal room credential: %w", err)
		}
		// The actor is known only now: the buyer the credential named. Bound
		// here so the audit row and captured_by attribute the session to them.
		ctx := principal.WithActor(ctx, BuyerPrincipal(participantID))
		return openSession(ctx, tx, participantID, roomID, digest, expiresAt)
	})
	if err != nil {
		return IssuedSession{}, err
	}
	return IssuedSession{Token: token, ExpiresAt: expiresAt}, nil
}

// openSession inserts the session row and records that somebody signed in.
func openSession(ctx context.Context, tx pgx.Tx, participantID ids.DealRoomParticipantID, roomID ids.DealRoomID, digest []byte, expiresAt time.Time) error {
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return err
	}
	id := ids.NewV7()
	if _, err := tx.Exec(ctx,
		`INSERT INTO deal_room_session (id, participant_id, room_id, token_hash, expires_at, last_seen_at, source, captured_by)
		 VALUES ($1, $2, $3, $4, $5, now(), $6, $7)`,
		id, participantID, roomID, digest, expiresAt, sourceCredential, by); err != nil {
		return fmt.Errorf("open deal room session: %w", err)
	}
	// The audit row names the session, not the token: nothing in the image
	// could re-admit anyone.
	if _, err := storekit.Audit(ctx, tx, "create", sessionObject, id, nil,
		map[string]any{fieldRoomID: roomID.UUID, "participant_id": participantID.UUID, "expires_at": expiresAt}); err != nil {
		return fmt.Errorf("audit deal room sign-in: %w", err)
	}
	return nil
}

// mintSessionToken is mintCredential with the session prefix. The strength and
// the digest-only rule are the same; only the spelling differs, so a session
// token presented where a credential is expected fails the lookup by prefix
// before it fails by entropy.
func mintSessionToken() (raw string, digest []byte, err error) {
	raw, _, err = mintCredential()
	if err != nil {
		return "", nil, err
	}
	raw = sessionPrefix + raw[len(credentialPrefix):]
	return raw, digestOfCredential(raw), nil
}

// ErrSessionRefused is the one answer for every way a presented token can fail
// to resolve — unknown, revoked, lapsed, or its participant revoked. The public
// edge turns it into a 401 with no further detail.
var ErrSessionRefused = errors.New("dealrooms: the room session admits nobody")

// ResolveSession turns a presented token into a Session, or refuses.
//
// Resolved fresh on EVERY request — this read is the revocation guarantee.
// The participant is joined on (id, room_id), so a session can only ever name
// a participant of its own room; a revoked participant, a revoked session and
// a lapsed session all refuse on one path with ErrSessionRefused.
func (s *Store) ResolveSession(ctx context.Context, token string) (Session, error) {
	var out Session
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var lastSeen *time.Time
		err := tx.QueryRow(ctx,
			`SELECT s.id, s.participant_id, s.room_id, p.capability, s.last_seen_at
			   FROM deal_room_session s
			   JOIN deal_room_participant p ON p.id = s.participant_id AND p.room_id = s.room_id
			  WHERE s.token_hash = $1 AND s.revoked_at IS NULL AND s.expires_at > now()
			    AND p.revoked_at IS NULL`,
			digestOfCredential(token)).Scan(&out.ID, &out.ParticipantID, &out.RoomID, &out.Capability, &lastSeen)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSessionRefused
		}
		if err != nil {
			return fmt.Errorf("resolve deal room session: %w", err)
		}
		return touchSession(ctx, tx, out, lastSeen)
	})
	if err != nil {
		return Session{}, err
	}
	return out, nil
}

// touchSession moves last_seen_at forward, at most once a minute. Not audited:
// nothing about the record changed and nobody gained or lost access — this is
// the roster's "last here" column being kept honest.
func touchSession(ctx context.Context, tx pgx.Tx, sess Session, lastSeen *time.Time) error {
	if lastSeen != nil && time.Since(*lastSeen) < lastSeenGranularity {
		return nil
	}
	if _, err := tx.Exec(ctx,
		`UPDATE deal_room_session SET last_seen_at = now() WHERE id = $1 AND room_id = $2`, sess.ID, sess.RoomID); err != nil {
		return fmt.Errorf("touch deal room session: %w", err)
	}
	return nil
}

// SignOut ends this one session. Permitted in every room state: leaving is an
// access act, and a buyer may always stop holding a token.
func (s *Store) SignOut(ctx context.Context, sess Session) error {
	if sess.ID == ids.Nil {
		return apperrors.ErrPermissionDenied
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE deal_room_session SET revoked_at = now()
			  WHERE id = $1 AND participant_id = $2 AND room_id = $3 AND revoked_at IS NULL`,
			sess.ID, sess.ParticipantID, sess.RoomID)
		if err != nil {
			return fmt.Errorf("sign out of deal room: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Already ended, by the buyer or by a revoke. Nothing to record.
			return nil
		}
		if _, err := storekit.Audit(ctx, tx, "revoke", sessionObject, sess.ID,
			map[string]any{"revoked_at": nil}, map[string]any{fieldRoomID: sess.RoomID.UUID, "by": "buyer"}); err != nil {
			return fmt.Errorf("audit deal room sign-out: %w", err)
		}
		return nil
	})
}
