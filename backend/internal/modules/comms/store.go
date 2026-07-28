// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Delivery status. There is no in-flight status on purpose: a crash mid-send
// would strand a row in it, and a guard keyed on that status would then turn
// River's redelivery into a silent skip — disabling the connector's
// retransmission check in exactly the crash it exists for. River serializes one
// job per delivery; terminal status plus that check is what makes a retry safe.
const (
	StatusPending = "pending"
	StatusSent    = "sent"
	StatusParked  = "parked"
)

// ErrTerminal marks a delivery that is already finished (or was never staged).
// A redelivered job hits this, and it is a normal at-least-once outcome rather
// than a failure.
var ErrTerminal = errors.New("comms: delivery is already terminal")

// Store is the comms_outbound seam: staging, loading with attempt-counting,
// and the three terminal/retry transitions. It carries no RBAC gate of its
// own — see the internal/modules/comms waivers in backend/rbacgate_test.go
// for why.
type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewStore builds the store. The clock is injected so age arithmetic is asserted
// by advancing time, never by sleeping.
func NewStore(pool *pgxpool.Pool, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{pool: pool, now: now}
}

// StageInput is one message staged for transmission, written in the caller's
// transaction so the delivery and the activity it belongs to commit together.
//
// There is deliberately no UserID field: the sending identity — whose Gmail
// credential eventually transmits the message — is stamped by StageTx from
// the authenticated principal, never taken from a caller-supplied value.
// This matches storekit.CapturedBy's provenance rule ("stamped from the
// authenticated principal, never from the request body"): a caller that
// could name an arbitrary user_id could stage a delivery that later sends
// through someone else's mailbox.
type StageInput struct {
	ActivityID      ids.ActivityID
	Provider        string
	MessageID       string // unbracketed
	Recipients      []string
	Cc              []string
	Subject         string
	Body            string // unsubscribe footer already applied
	ConsentPurpose  string
	InReplyTo       string   // unbracketed; empty starts a thread
	References      []string // unbracketed ancestry, oldest first
	ThreadKey       string
	ListUnsubscribe string // the Post header is derived from this, never stored
}

// Delivery is one staged message as the dispatcher sees it.
type Delivery struct {
	ID              ids.UUID
	ActivityID      ids.ActivityID
	UserID          ids.UserID
	Provider        string
	MessageID       string
	Recipients      []string
	Cc              []string
	Subject         string
	Body            string
	ConsentPurpose  string
	InReplyTo       string
	References      []string
	ListUnsubscribe string
	Status          string
	Attempts        int
	CreatedAt       time.Time
}

// StageTx records one delivery inside the caller's transaction. user_id —
// whose mailbox eventually transmits the message — is derived from the
// authenticated principal on ctx, exactly as storekit.CapturedBy stamps
// captured_by everywhere else; no caller input can put a different user's
// id in that column. A principal with no app_user identity (system,
// connector) cannot stage a delivery at all: sending is a human act.
func (s *Store) StageTx(ctx context.Context, tx pgx.Tx, in StageInput) (ids.UUID, error) {
	actor, err := storekit.Actor(ctx)
	if err != nil {
		return ids.UUID{}, err
	}
	if actor.UserID.IsZero() {
		return ids.UUID{}, fmt.Errorf("comms: staging a delivery requires an authenticated app_user identity, got principal type %q", actor.Type)
	}
	userID := ids.From[ids.UserKind](actor.UserID)

	recipients, err := json.Marshal(in.Recipients)
	if err != nil {
		return ids.UUID{}, fmt.Errorf("comms: encoding recipients: %w", err)
	}
	cc, err := json.Marshal(in.Cc)
	if err != nil {
		return ids.UUID{}, fmt.Errorf("comms: encoding cc: %w", err)
	}
	refs, err := json.Marshal(in.References)
	if err != nil {
		return ids.UUID{}, fmt.Errorf("comms: encoding references: %w", err)
	}
	id := ids.NewV7()
	if _, err := tx.Exec(ctx, `
		INSERT INTO comms_outbound
		  (id, workspace_id, activity_id, user_id, provider, message_id,
		   recipients, cc, subject, body, consent_purpose, in_reply_to,
		   references_chain, thread_key, list_unsubscribe, status, created_at)
		VALUES ($1, current_setting('app.workspace_id')::uuid, $2, $3, $4, $5,
		        $6, $7, $8, $9, $10, NULLIF($11,''), $12, NULLIF($13,''),
		        NULLIF($14,''), 'pending', $15)`,
		id, in.ActivityID, userID, in.Provider, in.MessageID,
		recipients, cc, in.Subject, in.Body, in.ConsentPurpose,
		in.InReplyTo, refs, in.ThreadKey, in.ListUnsubscribe, s.now().UTC()); err != nil {
		return ids.UUID{}, fmt.Errorf("comms: staging delivery: %w", err)
	}
	return id, nil
}

// Load reads one delivery and counts the attempt about to be made. It returns
// ErrTerminal for a delivery that already finished — or was never staged in
// this workspace — which is how a redelivered job stops without transmitting,
// rather than dereferencing a row that is not there.
func (s *Store) Load(ctx context.Context, id ids.UUID) (Delivery, error) {
	var d Delivery
	var recipients, cc, refs []byte
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			UPDATE comms_outbound
			   SET attempts = attempts + 1
			 WHERE id = $1 AND status = 'pending'
			RETURNING id, activity_id, user_id, provider, message_id, recipients, cc,
			          subject, body, consent_purpose, coalesce(in_reply_to, ''),
			          references_chain, coalesce(list_unsubscribe, ''), status,
			          attempts, created_at`,
			id).Scan(&d.ID, &d.ActivityID, &d.UserID, &d.Provider, &d.MessageID,
			&recipients, &cc, &d.Subject, &d.Body, &d.ConsentPurpose, &d.InReplyTo,
			&refs, &d.ListUnsubscribe, &d.Status, &d.Attempts, &d.CreatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, ErrTerminal
	}
	if err != nil {
		return Delivery{}, fmt.Errorf("comms: loading delivery: %w", err)
	}
	if err := json.Unmarshal(recipients, &d.Recipients); err != nil {
		return Delivery{}, fmt.Errorf("comms: decoding recipients: %w", err)
	}
	if err := json.Unmarshal(cc, &d.Cc); err != nil {
		return Delivery{}, fmt.Errorf("comms: decoding cc: %w", err)
	}
	if err := json.Unmarshal(refs, &d.References); err != nil {
		return Delivery{}, fmt.Errorf("comms: decoding references: %w", err)
	}
	return d, nil
}

// RecordSent closes a delivery against the provider's receipt. Guarded on
// status = 'pending': a stale attempt (network partition, GC pause — the
// crash class R3 already assumes possible) can lose a race against a newer
// attempt that already closed the same row. Rather than clobber a 'sent' or
// 'parked' row — a real receipt overwritten, or worse, un-sent by a stale
// park — a delivery that is no longer pending reports ErrTerminal. That is
// a benign no-op, the same fact Load already reports the same way: Task 6's
// dispatcher must treat it as "already handled," never as retryable.
func (s *Store) RecordSent(ctx context.Context, id ids.UUID, providerMessageID string) error {
	return s.update(ctx, `
		UPDATE comms_outbound
		   SET status = 'sent', provider_message_id = $2, sent_at = $3, reason = NULL
		 WHERE id = $1 AND status = 'pending'`, id, providerMessageID, s.now().UTC())
}

// Park ends a delivery that no retry repairs, recording why in words an
// operator can act on. Guarded on status = 'pending' for the same reason as
// RecordSent — a stale attempt losing a race against a newer one that
// already closed the row reports ErrTerminal, a benign no-op the dispatcher
// must not treat as retryable.
func (s *Store) Park(ctx context.Context, id ids.UUID, reason string) error {
	return s.update(ctx, `UPDATE comms_outbound SET status = 'parked', reason = $2 WHERE id = $1 AND status = 'pending'`, id, reason)
}

// RecordFailure notes a transient fault and leaves the delivery pending so
// River's ladder brings it back. Same race as RecordSent/Park: a delivery
// a newer attempt already closed reports ErrTerminal rather than being
// silently reopened or dropped.
func (s *Store) RecordFailure(ctx context.Context, id ids.UUID, reason string) error {
	return s.update(ctx, `UPDATE comms_outbound SET reason = $2 WHERE id = $1 AND status = 'pending'`, id, reason)
}

// update runs a status-guarded transition and reports ErrTerminal — never
// apperrors.ErrNotFound — when it touches zero rows. Every caller's SQL
// already scopes to `status = 'pending'`, so zero rows means either the
// delivery does not exist in this workspace or it is already closed; Load
// answers both the same way, and these transitions do too.
func (s *Store) update(ctx context.Context, sql string, args ...any) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("comms: updating delivery: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrTerminal
		}
		return nil
	})
}
