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
//
// The ledger key is (object_class, external_id, property, value-hash) exactly as
// the spec pins it: multiple values for one property coexist, so a rapid A→B
// write-back keeps A's entry open until A's (possibly delayed) echo is
// recognized rather than being clobbered by B's. The window is measured against
// the DATABASE clock for both the entry's opened_at and the expiry comparison,
// so a producer process and a receiver process can never disagree on the
// boundary by their wall-clock skew.

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

// WriteLedger is the our-write ledger store. window and hash are injectable so a
// test can exercise the window boundary and the (astronomically improbable in
// production) hash-collision path deterministically without a real collision;
// the open/expiry clock itself is always the database's, never a wall clock.
type WriteLedger struct {
	pool   *pgxpool.Pool
	window time.Duration
	hash   func(string) string
}

// NewWriteLedger builds the production ledger: SHA-256 value hashing and the
// default 24h open window.
func NewWriteLedger(pool *pgxpool.Pool) *WriteLedger {
	return &WriteLedger{pool: pool, window: DefaultLedgerWindow, hash: sha256Hex}
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
// Distinct values for one property coexist (keyed by value-hash); re-writing
// the SAME value refreshes its open window. The write is fenced against a
// racing disconnect (assertActiveConnection) so a write landing after teardown
// cannot repopulate a purged ledger.
func (l *WriteLedger) OpenEntries(ctx context.Context, objectClass, externalID string, props map[string]string) error {
	if len(props) == 0 {
		return nil
	}
	return database.WithWorkspaceTx(ctx, l.pool, func(tx pgx.Tx) error {
		if err := assertActiveConnection(ctx, tx); err != nil {
			return err
		}
		for prop, val := range props {
			if _, err := tx.Exec(ctx, `
				INSERT INTO overlay_write_ledger
					(workspace_id, object_class, external_id, property, value_hash, value_canonical)
				VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, $5)
				ON CONFLICT (workspace_id, object_class, external_id, property, value_hash)
				DO UPDATE SET value_canonical = EXCLUDED.value_canonical, opened_at = now()`,
				objectClass, externalID, prop, l.hash(val), val,
			); err != nil {
				return fmt.Errorf("overlay: opening write-ledger entry for %s/%s.%s: %w", objectClass, externalID, prop, err)
			}
		}
		return nil
	})
}

// Classify decides whether an inbound property change is our own echo, a
// genuine external change, or a hash collision — the consumer half. It looks up
// the entry keyed by the inbound value's hash within the open window (DB clock).
// A hash hit is CONFIRMED against the stored value: equal ⇒ echo (suppress);
// different ⇒ collision ⇒ the mirror is halted (OVA-AC-3 fail-safe) and
// ClassCollision returned, so the change is never silently dropped. On a
// genuine change (no live entry for the inbound value) every open entry for that
// property is invalidated: the incumbent now holds a value we did not write, so
// our earlier written values are superseded and must not later suppress a
// genuine change back to one of them.
func (l *WriteLedger) Classify(ctx context.Context, objectClass, externalID, property, value string) (Classification, error) {
	incomingHash := l.hash(value)
	var result Classification
	err := database.WithWorkspaceTx(ctx, l.pool, func(tx pgx.Tx) error {
		var storedValue string
		scanErr := tx.QueryRow(ctx, `
			SELECT value_canonical FROM overlay_write_ledger
			WHERE object_class = $1 AND external_id = $2 AND property = $3 AND value_hash = $4
			  AND opened_at > now() - make_interval(secs => $5)`,
			objectClass, externalID, property, incomingHash, l.window.Seconds()).Scan(&storedValue)
		switch {
		case errors.Is(scanErr, pgx.ErrNoRows):
			// No live entry for this value: a genuine change. Invalidate our
			// now-superseded entries for this property so a later change back to
			// a previously-written value is not mis-suppressed as an echo.
			result = ClassGenuine
			_, delErr := tx.Exec(ctx, `
				DELETE FROM overlay_write_ledger
				WHERE object_class = $1 AND external_id = $2 AND property = $3`,
				objectClass, externalID, property)
			return delErr
		case scanErr != nil:
			return scanErr
		case storedValue == value:
			result = ClassEcho // our own write echoing back — suppress
			return nil
		default:
			// Hash matched but the value differs: a SHA-256 collision. Never
			// suppress — halt the mirror (fail-safe) in this same transaction.
			result = ClassCollision
			return haltMirrorTx(ctx, tx, fmt.Sprintf("write-ledger value-hash collision on %s/%s.%s", objectClass, externalID, property))
		}
	})
	if err != nil {
		return ClassGenuine, fmt.Errorf("overlay: classifying an inbound change against the write ledger: %w", err)
	}
	return result, nil
}

// haltMirrorTx flags the workspace's mirror as halted within tx (one row per
// workspace, upserted so a re-detection refreshes the reason/time).
func haltMirrorTx(ctx context.Context, tx pgx.Tx, reason string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO overlay_mirror_halt (workspace_id, reason)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1)
		ON CONFLICT (workspace_id) DO UPDATE SET reason = EXCLUDED.reason, detected_at = now()`,
		reason); err != nil {
		return fmt.Errorf("overlay: recording the mirror halt: %w", err)
	}
	return nil
}

// Halted reports whether ctx's workspace mirror is halted — a ledger collision
// tripped the fail-safe. The receiver refuses to enqueue re-fetches and the
// re-fetch worker refuses to read/ingest for a halted workspace until an
// operator clears the flag, so a mirror we no longer trust never
// mis-suppresses or serves through.
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
