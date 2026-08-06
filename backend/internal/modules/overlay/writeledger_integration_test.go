// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

// The echo-suppression ledger's real-Postgres proof (OVA-AC-3 / OVA-DDL-6 /
// OVA-PARAM-3/4): an entry opened by a write-back suppresses that write's echo,
// a different or expired value is a genuine change (ingested, not dropped), a
// superseding genuine change invalidates the stale entry, and a value-hash
// collision halts the mirror rather than mis-suppressing.
//
// The open/expiry clock is the database's; only the window duration is a test
// seam, so the boundary is exercised by a zero window (every entry immediately
// expired) rather than by faking wall-clock time.

import (
	"testing"
)

// TestWriteLedgerClassifiesEchoGenuineAndWindow drives the echo, non-echo, and
// window-boundary arms with the production SHA-256 hash.
func TestWriteLedgerClassifiesEchoGenuineAndWindow(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	seedActiveConnection(ctx, t, pool) // OpenEntries is disconnect-fenced
	l := NewWriteLedger(pool)

	// The producer opened an entry for contacts/42.firstname = "Ada".
	if err := l.OpenEntries(ctx, "contacts", "42", map[string]string{"firstname": "Ada"}); err != nil {
		t.Fatalf("OpenEntries: %v", err)
	}

	// Echo: the same value inside the window is our own write-back.
	if c, err := l.Classify(ctx, "contacts", "42", "firstname", "Ada"); err != nil || c != ClassEcho {
		t.Errorf("echo: got (%v, %v), want ClassEcho", c, err)
	}
	// Genuine (no live entry for this value / property).
	if c, err := l.Classify(ctx, "contacts", "42", "lastname", "Lovelace"); err != nil || c != ClassGenuine {
		t.Errorf("no entry: got (%v, %v), want ClassGenuine", c, err)
	}

	// Window boundary (OVA-PARAM-3): a zero window means the entry — opened an
	// instant ago against the DB clock — is already outside the strict
	// opened_at > now()-window comparison, so the SAME value is now genuine.
	expired := &WriteLedger{pool: pool, window: 0, hash: sha256Hex}
	if err := expired.OpenEntries(ctx, "contacts", "77", map[string]string{"firstname": "Grace"}); err != nil {
		t.Fatalf("OpenEntries (expired case): %v", err)
	}
	if c, err := expired.Classify(ctx, "contacts", "77", "firstname", "Grace"); err != nil || c != ClassGenuine {
		t.Errorf("zero-window entry: got (%v, %v), want ClassGenuine (outside the open window)", c, err)
	}
}

// TestWriteLedgerGenuineChangeInvalidatesStaleEntry (OVA-AC-3 / E4): after the
// incumbent genuinely moves off our written value, a later change BACK to that
// value must not be mis-suppressed — observing the genuine change invalidates
// our now-superseded entry.
func TestWriteLedgerGenuineChangeInvalidatesStaleEntry(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	seedActiveConnection(ctx, t, pool)
	l := NewWriteLedger(pool)

	// We wrote firstname="Ada".
	if err := l.OpenEntries(ctx, "contacts", "42", map[string]string{"firstname": "Ada"}); err != nil {
		t.Fatalf("OpenEntries: %v", err)
	}
	// A third party changes it to "Bob": genuine, and it supersedes our entry.
	if c, err := l.Classify(ctx, "contacts", "42", "firstname", "Bob"); err != nil || c != ClassGenuine {
		t.Fatalf("third-party change: got (%v, %v), want ClassGenuine", c, err)
	}
	// A change back to "Ada" is now a GENUINE change too — our stale entry was
	// invalidated, so it is ingested, not wrongly suppressed as our echo.
	if c, err := l.Classify(ctx, "contacts", "42", "firstname", "Ada"); err != nil || c != ClassGenuine {
		t.Errorf("change back to Ada: got (%v, %v), want ClassGenuine (stale entry invalidated)", c, err)
	}
}

// TestWriteLedgerKeepsDistinctValuesPerProperty (OVA-DDL-6 key): a rapid A→B
// write-back keeps BOTH entries open, so A's (delayed) echo is still recognized
// rather than clobbered by B's entry.
func TestWriteLedgerKeepsDistinctValuesPerProperty(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	seedActiveConnection(ctx, t, pool)
	l := NewWriteLedger(pool)

	if err := l.OpenEntries(ctx, "contacts", "42", map[string]string{"firstname": "Ada"}); err != nil {
		t.Fatalf("OpenEntries A: %v", err)
	}
	if err := l.OpenEntries(ctx, "contacts", "42", map[string]string{"firstname": "Bob"}); err != nil {
		t.Fatalf("OpenEntries B: %v", err)
	}
	// Both our writes' echoes are recognized, in either order.
	if c, err := l.Classify(ctx, "contacts", "42", "firstname", "Bob"); err != nil || c != ClassEcho {
		t.Errorf("echo B: got (%v, %v), want ClassEcho", c, err)
	}
	if c, err := l.Classify(ctx, "contacts", "42", "firstname", "Ada"); err != nil || c != ClassEcho {
		t.Errorf("echo A (not clobbered by B): got (%v, %v), want ClassEcho", c, err)
	}
}

// TestWriteLedgerPruneExpired proves the hygiene prune: a fresh entry survives a
// prune at its own window, while a zero-window prune (everything expired)
// removes it — after which the same value is a genuine change, not an echo.
func TestWriteLedgerPruneExpired(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	seedActiveConnection(ctx, t, pool)

	l := NewWriteLedger(pool)
	if err := l.OpenEntries(ctx, "contacts", "42", map[string]string{"firstname": "Ada"}); err != nil {
		t.Fatalf("OpenEntries: %v", err)
	}
	// A prune at the open window removes nothing, and the entry still suppresses.
	if n, err := l.PruneExpired(ctx); err != nil || n != 0 {
		t.Errorf("prune of a fresh entry removed %d (err %v), want 0", n, err)
	}
	if c, err := l.Classify(ctx, "contacts", "42", "firstname", "Ada"); err != nil || c != ClassEcho {
		t.Errorf("fresh entry after prune: got (%v, %v), want ClassEcho", c, err)
	}

	// A zero-window prune treats every entry as expired and reclaims it.
	expiring := &WriteLedger{pool: pool, window: 0, hash: sha256Hex}
	if n, err := expiring.PruneExpired(ctx); err != nil || n != 1 {
		t.Errorf("zero-window prune removed %d (err %v), want 1", n, err)
	}
	if c, err := l.Classify(ctx, "contacts", "42", "firstname", "Ada"); err != nil || c != ClassGenuine {
		t.Errorf("after prune: got (%v, %v), want ClassGenuine (entry reclaimed)", c, err)
	}
}

// TestWriteLedgerCollisionHaltsTheMirror drives the F2 collision arm: a value
// that HASHES like our write but differs is never suppressed — the mirror is
// flagged and halted. A forced colliding hasher stands in for the
// astronomically improbable real SHA-256 collision (production keeps sha256Hex).
func TestWriteLedgerCollisionHaltsTheMirror(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	seedActiveConnection(ctx, t, pool)
	l := &WriteLedger{pool: pool, window: DefaultLedgerWindow, hash: func(string) string { return "COLLIDE" }}

	if err := l.OpenEntries(ctx, "contacts", "42", map[string]string{"firstname": "Ada"}); err != nil {
		t.Fatalf("OpenEntries: %v", err)
	}
	if halted, err := l.Halted(ctx); err != nil || halted {
		t.Fatalf("mirror must not be halted before any collision: halted=%v err=%v", halted, err)
	}

	// Inbound "Bob" hashes to the same "COLLIDE" but is not "Ada": a collision.
	// It must NOT be suppressed as an echo — the mirror halts instead.
	if c, err := l.Classify(ctx, "contacts", "42", "firstname", "Bob"); err != nil || c != ClassCollision {
		t.Errorf("collision: got (%v, %v), want ClassCollision", c, err)
	}
	if halted, err := l.Halted(ctx); err != nil || !halted {
		t.Errorf("the mirror must be halted after a collision: halted=%v err=%v", halted, err)
	}
}
