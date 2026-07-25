// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The capture disposition ledger (CAP-DDL-8, ADR-0072 §5). The tiered creation
// gate defers the ambiguous first-time sender instead of creating on sight: the
// Sink records what it decided about an address, IN the capture transaction, and
// the verdict engine resolves what it deferred. Suppressions record here too, so
// a wrong registry entry is queryable rather than only a log line.
//
// This file owns the ledger's SQL and nothing else — capture never touches
// person/organization tables, and the resolver seam stays the only way records
// come into being.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Disposition statuses. `pending` and `unsure` are the LIVE states the unique
// index keys on — one open question per address at a time.
const (
	PendingStatusPending    = "pending"
	PendingStatusUnsure     = "unsure"
	PendingStatusReal       = "real"
	PendingStatusNoise      = "noise"
	PendingStatusSuppressed = "suppressed"
	PendingStatusRejected   = "rejected"
)

// pendingMaxAttempts bounds the verdict retries (ADR-0072 §5: retries=2). A row
// that exhausts them is retired from the due-scan rather than retried forever.
const pendingMaxAttempts = 2

// pendingLease is how long a claimed row stays off other workers' scans. Longer
// than a batch takes, short enough that a worker that died mid-batch releases
// its rows by expiry instead of stranding them.
const pendingLease = 15 * time.Minute

// PendingCounterparty is one deferred disposition as the verdict engine reads
// it: the identity to judge and the message that raised the question.
type PendingCounterparty struct {
	ID          ids.UUID
	Email       string
	Domain      string
	DisplayName string // untrusted header text — for display, never matching
	ActivityID  ids.UUID
	OwnerID     ids.UUID
	Subject     string
	Body        string
	Attempts    int
}

// recordDisposition writes one ledger row inside the caller's capture
// transaction. Idempotent on the live-row index: a second message from the same
// stranger joins the open question instead of queuing a second verdict for it.
//
// status decides whether anything is deferred — a T2 suppression records its
// reason and retires immediately (no next_attempt_at), while a T4 ambiguous
// sender stays due for the verdict engine.
func recordDisposition(ctx context.Context, tx pgx.Tx, in dispositionRow) error {
	email := normalizeEmail(in.Email)
	if email == "" {
		return errors.New("capture: a disposition needs a normalized counterparty address")
	}
	var nextAttempt any
	if in.Status == PendingStatusPending {
		nextAttempt = time.Now()
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO capture_pending_counterparty
		  (workspace_id, email, domain, display_name, activity_id, owner_id, status, disposition_reason, next_attempt_at)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		        $1, NULLIF($2, ''), NULLIF($3, ''), $4, $5, $6, NULLIF($7, ''), $8)
		ON CONFLICT (workspace_id, email) WHERE status IN ('pending', 'unsure')
		DO NOTHING`,
		email, strings.ToLower(strings.TrimSpace(in.Domain)), in.DisplayName,
		in.ActivityID, in.OwnerID, in.Status, in.Reason, nextAttempt)
	if err != nil {
		return fmt.Errorf("capture: recording the counterparty disposition: %w", err)
	}
	return nil
}

// dispositionRow names one ledger write.
type dispositionRow struct {
	Email       string
	Domain      string
	DisplayName string
	ActivityID  ids.UUID
	OwnerID     ids.UUID
	Status      string
	Reason      string
}

// PendingStore reads and resolves the ledger. It is the verdict engine's seam
// into capture's own table; compose injects it.
type PendingStore struct{ pool *pgxpool.Pool }

// NewPendingStore builds the ledger store over the capture pool.
func NewPendingStore(pool *pgxpool.Pool) *PendingStore { return &PendingStore{pool: pool} }

// ClaimDue atomically leases up to limit due rows for this workspace. FOR UPDATE
// SKIP LOCKED lets several replicas drain the ledger without double-judging a
// row or serializing on each other; the lease is what a crashed worker releases
// by expiry.
//
// Claiming bumps attempts, so a row that keeps failing walks toward its bound
// rather than being retried forever.
func (s *PendingStore) ClaimDue(ctx context.Context, limit int) ([]PendingCounterparty, error) {
	var out []PendingCounterparty
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE capture_pending_counterparty p
			   SET attempts = p.attempts + 1,
			       claimed_until = now() + $2::interval,
			       updated_at = now()
			 WHERE p.id IN (
			   SELECT id FROM capture_pending_counterparty
			    WHERE status = 'pending'
			      AND next_attempt_at IS NOT NULL AND next_attempt_at <= now()
			      AND (claimed_until IS NULL OR claimed_until <= now())
			    ORDER BY next_attempt_at
			    LIMIT $1
			    FOR UPDATE SKIP LOCKED)
			RETURNING p.id, p.email, coalesce(p.domain, ''), coalesce(p.display_name, ''),
			          p.activity_id, p.owner_id, p.attempts,
			          coalesce((SELECT a.subject FROM activity a WHERE a.id = p.activity_id), ''),
			          coalesce((SELECT a.body FROM activity a WHERE a.id = p.activity_id), '')`,
			limit, pendingLease.String())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p PendingCounterparty
			if err := rows.Scan(&p.ID, &p.Email, &p.Domain, &p.DisplayName,
				&p.ActivityID, &p.OwnerID, &p.Attempts, &p.Subject, &p.Body); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("capture: claiming due dispositions: %w", err)
	}
	return out, nil
}

// Resolve closes a claimed row with its verdict. The CAS on `pending` is what
// makes a racing second worker — or a replayed job — a no-op rather than a
// second creation: reports whether THIS call was the one that resolved it, and
// only that caller may act on the verdict.
func (s *PendingStore) Resolve(ctx context.Context, tx pgx.Tx, id ids.UUID, status, reason string) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE capture_pending_counterparty
		   SET status = $2, disposition_reason = NULLIF($3, ''),
		       resolved_at = now(), next_attempt_at = NULL, claimed_until = NULL, updated_at = now()
		 WHERE id = $1 AND status = 'pending'`, id, status, reason)
	if err != nil {
		return false, fmt.Errorf("capture: resolving disposition %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// Defer returns a claimed row to the queue for a later pass, or retires it when
// it has used its attempts. A row that never gets a usable verdict must stop
// costing model calls; retiring leaves the record and its reason, so the
// question is visibly unanswered rather than silently dropped.
func (s *PendingStore) Defer(ctx context.Context, id ids.UUID, attempts int, backoff time.Duration, reason string) error {
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if attempts >= pendingMaxAttempts {
			_, err := tx.Exec(ctx, `
				UPDATE capture_pending_counterparty
				   SET status = 'unsure', disposition_reason = NULLIF($2, ''),
				       next_attempt_at = NULL, claimed_until = NULL, updated_at = now()
				 WHERE id = $1 AND status = 'pending'`, id, reason)
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE capture_pending_counterparty
			   SET next_attempt_at = now() + $2::interval, claimed_until = NULL, updated_at = now()
			 WHERE id = $1 AND status = 'pending'`, id, backoff.String())
		return err
	})
	if err != nil {
		return fmt.Errorf("capture: deferring disposition %s: %w", id, err)
	}
	return nil
}

// HasLivePending reports whether an address currently has an open question.
// Attention-classify and the digest read this to leave a deferred sender's mail
// out of the population they act on (ADR-0072 §5).
func (s *PendingStore) HasLivePending(ctx context.Context, email string) (bool, error) {
	normalized := normalizeEmail(email)
	if normalized == "" {
		return false, nil
	}
	var live bool
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
			  SELECT 1 FROM capture_pending_counterparty
			   WHERE email = $1 AND status IN ('pending', 'unsure'))`, normalized).Scan(&live)
	})
	if err != nil {
		return false, fmt.Errorf("capture: reading the disposition ledger: %w", err)
	}
	return live, nil
}

// normalizeEmail is the ONE spelling of the ledger's identity: lowercased and
// trimmed, matching activity.counterparty_email and person_email so the verdict,
// the correspondence gate, and the dedupe chokepoint agree on what the same
// address is.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
