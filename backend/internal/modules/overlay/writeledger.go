// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The echo-suppression our-write ledger (OVA-DDL-6 / OVA-AC-3): HubSpot webhooks
// carry no trustworthy source-app discriminator, so the echo of our OWN
// write-back is indistinguishable from a genuine third-party change by the
// signal alone. The write-back path opens one ledger entry per property it
// writes (OpenEntries); the webhook receiver classifies each inbound property
// change against the open entries (Classify) — our echo is suppressed (no sync
// loop), a genuine change is ingested, and a value-hash collision HALTS the
// mirror rather than silently mis-suppressing a real change. The open window is
// bounded (OVA-PARAM-3) and the value hash is SHA-256 (OVA-PARAM-4).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// DefaultLedgerWindow is OVA-PARAM-3: how long an our-write ledger entry stays
// open. An inbound change matching an entry inside this window is our echo;
// anything older is treated as a genuine change (the entry has expired).
const DefaultLedgerWindow = 24 * time.Hour

// Classification is the verdict Classify returns for one inbound property change.
type Classification int

const (
	// ClassGenuine is not our write (no open entry, an expired one, or a
	// different value) — ingest it as a real external change.
	ClassGenuine Classification = iota
	// ClassEcho is the echo of our own write-back — suppress it (no sync loop).
	ClassEcho
	// ClassCollision is an inbound value that HASHES like our write but is not
	// equal — a SHA-256 collision. The mirror is halted; never suppressed.
	ClassCollision
)

// WriteLedger is the our-write ledger store. now/window/hash are injectable so a
// test can exercise the window boundary and the (astronomically improbable in
// production) hash-collision path deterministically without a real collision.
type WriteLedger struct {
	pool   *pgxpool.Pool
	now    func() time.Time
	window time.Duration
	hash   func(string) string
}

// NewWriteLedger builds the production ledger: SHA-256 value hashing and the
// default 24h open window.
func NewWriteLedger(pool *pgxpool.Pool) *WriteLedger {
	return &WriteLedger{pool: pool, now: time.Now, window: DefaultLedgerWindow, hash: sha256Hex}
}

// sha256Hex is OVA-PARAM-4's pinned value hash: SHA-256 over the canonicalized
// value, hex-encoded.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// OpenEntries records one ledger entry per property actually written back
// (the producer half). props maps the written property name to its
// canonicalized value — exactly the properties the incumbent write sent, so an
// echo webhook carrying the same (object, external_id, property, value) is
// recognized. object_class + property naming is the adapter's own vocabulary
// (HubSpot property names for the real adapter); the ledger is agnostic to it
// as long as the producer and the consumer agree, which they do per adapter.
// Re-writing a property overwrites its entry (one open value per property).
func (l *WriteLedger) OpenEntries(ctx context.Context, objectClass, externalID string, props map[string]string) error {
	if len(props) == 0 {
		return nil
	}
	return database.WithWorkspaceTx(ctx, l.pool, func(tx pgx.Tx) error {
		for prop, val := range props {
			if _, err := tx.Exec(ctx, `
				INSERT INTO overlay_write_ledger
					(workspace_id, object_class, external_id, property, value_hash, value_canonical, opened_at)
				VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, $5, $6)
				ON CONFLICT (workspace_id, object_class, external_id, property)
				DO UPDATE SET value_hash = EXCLUDED.value_hash,
				              value_canonical = EXCLUDED.value_canonical,
				              opened_at = EXCLUDED.opened_at`,
				objectClass, externalID, prop, l.hash(val), val, l.now(),
			); err != nil {
				return fmt.Errorf("overlay: opening write-ledger entry for %s/%s.%s: %w", objectClass, externalID, prop, err)
			}
		}
		return nil
	})
}

// Classify decides whether an inbound property change is our own echo, a
// genuine external change, or a hash collision — the consumer half. An entry
// only counts when it is still inside the open window (OVA-PARAM-3). A hash
// match is CONFIRMED against the stored value: equal ⇒ echo (suppress);
// different ⇒ collision ⇒ the mirror is halted (OVA-AC-3 fail-safe) and
// ClassCollision returned, so the change is never silently dropped.
func (l *WriteLedger) Classify(ctx context.Context, objectClass, externalID, property, value string) (Classification, error) {
	incomingHash := l.hash(value)
	cutoff := l.now().Add(-l.window)
	var storedHash, storedValue string
	err := database.WithWorkspaceTx(ctx, l.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT value_hash, value_canonical FROM overlay_write_ledger
			WHERE object_class = $1 AND external_id = $2 AND property = $3 AND opened_at > $4`,
			objectClass, externalID, property, cutoff).Scan(&storedHash, &storedValue)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ClassGenuine, nil // no open entry (or expired) — a genuine change
	}
	if err != nil {
		return ClassGenuine, fmt.Errorf("overlay: classifying an inbound change against the write ledger: %w", err)
	}
	if storedHash != incomingHash {
		return ClassGenuine, nil // we wrote a different value — a genuine change
	}
	if storedValue == value {
		return ClassEcho, nil // our own write echoing back — suppress
	}
	// Hash matched but the value differs: a SHA-256 collision. Never suppress —
	// halt the mirror (fail-safe) and surface it.
	if haltErr := l.haltMirror(ctx, fmt.Sprintf("write-ledger value-hash collision on %s/%s.%s", objectClass, externalID, property)); haltErr != nil {
		return ClassCollision, fmt.Errorf("overlay: recording the mirror halt on a ledger collision failed: %w", haltErr)
	}
	return ClassCollision, nil
}

// haltMirror flags the workspace's mirror as halted (one row per workspace,
// upserted so a re-detection refreshes the reason/time).
func (l *WriteLedger) haltMirror(ctx context.Context, reason string) error {
	return database.WithWorkspaceTx(ctx, l.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO overlay_mirror_halt (workspace_id, reason, detected_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2)
			ON CONFLICT (workspace_id) DO UPDATE SET reason = EXCLUDED.reason, detected_at = EXCLUDED.detected_at`,
			reason, l.now())
		return err
	})
}

// Halted reports whether ctx's workspace mirror is halted — the receiver refuses
// to process further signals for a halted workspace until an operator clears the
// flag, so a collision-detected mirror never silently mis-suppresses.
func (l *WriteLedger) Halted(ctx context.Context) (bool, error) {
	var halted bool
	err := database.WithWorkspaceTx(ctx, l.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM overlay_mirror_halt)`).Scan(&halted)
	})
	if err != nil {
		return false, fmt.Errorf("overlay: reading the mirror-halt flag: %w", err)
	}
	return halted, nil
}
