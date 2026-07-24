// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

// The echo-suppression ledger's real-Postgres proof (OVA-AC-3 / OVA-DDL-6 /
// OVA-PARAM-3/4): an entry opened by a write-back suppresses that write's echo,
// a different or expired value is a genuine change (ingested, not dropped), and
// a value-hash collision halts the mirror rather than mis-suppressing.

import (
	"testing"
	"time"
)

// TestWriteLedgerClassifiesEchoGenuineAndWindow drives the echo / non-echo /
// window-boundary arms with the production SHA-256 hash.
func TestWriteLedgerClassifiesEchoGenuineAndWindow(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	l := NewWriteLedger(pool)
	l.now = func() time.Time { return base } // deterministic window, never the wall clock

	// The producer opened an entry for contacts/42.firstname = "Ada".
	if err := l.OpenEntries(ctx, "contacts", "42", map[string]string{"firstname": "Ada"}); err != nil {
		t.Fatalf("OpenEntries: %v", err)
	}

	// Echo: the same value inside the window is our own write-back.
	if c, err := l.Classify(ctx, "contacts", "42", "firstname", "Ada"); err != nil || c != ClassEcho {
		t.Errorf("echo: got (%v, %v), want ClassEcho", c, err)
	}
	// Genuine (different value): we wrote "Ada"; an inbound "Bob" is a real change.
	if c, err := l.Classify(ctx, "contacts", "42", "firstname", "Bob"); err != nil || c != ClassGenuine {
		t.Errorf("different value: got (%v, %v), want ClassGenuine", c, err)
	}
	// Genuine (no entry for this property).
	if c, err := l.Classify(ctx, "contacts", "42", "lastname", "Lovelace"); err != nil || c != ClassGenuine {
		t.Errorf("no entry: got (%v, %v), want ClassGenuine", c, err)
	}

	// Window boundary (OVA-PARAM-3): advance now past the open window so the
	// entry has expired — the SAME value is now a genuine change, not an echo.
	l.now = func() time.Time { return base.Add(DefaultLedgerWindow + time.Minute) }
	if c, err := l.Classify(ctx, "contacts", "42", "firstname", "Ada"); err != nil || c != ClassGenuine {
		t.Errorf("expired entry: got (%v, %v), want ClassGenuine (outside the open window)", c, err)
	}
}

// TestWriteLedgerCollisionHaltsTheMirror drives the F2 collision arm: a value
// that HASHES like our write but differs is never suppressed — the mirror is
// flagged and halted. A forced colliding hasher stands in for the
// astronomically improbable real SHA-256 collision (production keeps sha256Hex).
func TestWriteLedgerCollisionHaltsTheMirror(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	l := NewWriteLedger(pool)
	l.now = func() time.Time { return base }
	l.hash = func(string) string { return "COLLIDE" } // every value hashes the same

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
