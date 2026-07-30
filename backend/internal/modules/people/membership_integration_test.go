// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// SetChannelIdentityBlocked over real migrated Postgres (design §4.2 D9):
// proves the write is idempotent both ways, and — the scenario the whole
// design turns on — that a block followed by an unblock never forks the
// returning customer's next message onto a second Person.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// channelIdentityBlockedAt reads the live identity's blocked_at column
// directly, so a test can assert on the exact value SetChannelIdentityBlocked
// left behind rather than only on whether dedupe still resolves it.
func (e *dedupeEnv) channelIdentityBlockedAt(ctx context.Context, t *testing.T, ci connector.ChannelIdentity) *time.Time {
	t.Helper()
	var blockedAt *time.Time
	err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT blocked_at FROM person_channel_identity
			WHERE provider = $1 AND channel_user_id = $2 AND archived_at IS NULL`,
			ci.Provider, ci.ChannelUserID).Scan(&blockedAt)
	})
	if err != nil {
		t.Fatalf("reading blocked_at for %s: %v", ci.ChannelUserID, err)
	}
	return blockedAt
}

func TestKickedStatusMarksTheIdentityBlocked(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	person := e.seedPerson(ctx, t, "Kickable Customer", nil, nil)
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "780001"}
	e.bindIdentity(ctx, t, person, ci)

	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return e.store.SetChannelIdentityBlocked(ctx, tx, ci, true)
	}); err != nil {
		t.Fatalf("SetChannelIdentityBlocked(blocked=true): %v", err)
	}

	if blockedAt := e.channelIdentityBlockedAt(ctx, t, ci); blockedAt == nil {
		t.Fatal("blocked_at is NULL after a kicked status; want it set")
	}
}

func TestMemberStatusClearsTheBlock(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	person := e.seedPerson(ctx, t, "Unblocking Customer", nil, nil)
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "780002"}
	e.bindIdentity(ctx, t, person, ci)
	e.blockIdentity(ctx, t, ci)

	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return e.store.SetChannelIdentityBlocked(ctx, tx, ci, false)
	}); err != nil {
		t.Fatalf("SetChannelIdentityBlocked(blocked=false): %v", err)
	}

	if blockedAt := e.channelIdentityBlockedAt(ctx, t, ci); blockedAt != nil {
		t.Fatalf("blocked_at = %v after a member status, want NULL", *blockedAt)
	}
}

// TestBlockThenUnblockKeepsOnePersonNotTwo is the test that only fails after
// a customer comes back (design §4.2 D9): if blocking had ever archived the
// identity row, the dedupe lane's archived_at IS NULL clause would miss on
// the post-unblock message below and mint a second Person for the same
// human — exactly what the partial unique index would happily admit.
func TestBlockThenUnblockKeepsOnePersonNotTwo(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	const name = "Comes Back"
	person := e.seedPerson(ctx, t, name, nil, nil)
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "780003"}
	e.bindIdentity(ctx, t, person, ci)

	block := func() {
		t.Helper()
		if err := e.store.tx(ctx, func(tx pgx.Tx) error {
			return e.store.SetChannelIdentityBlocked(ctx, tx, ci, true)
		}); err != nil {
			t.Fatalf("block: %v", err)
		}
	}
	block()
	firstBlock := e.channelIdentityBlockedAt(ctx, t, ci)
	if firstBlock == nil {
		t.Fatal("blocked_at is NULL after blocking; want it set")
	}

	// Telegram redelivers my_chat_member; a repeat block must be a no-op, not
	// move the timestamp forward.
	block()
	if redelivered := e.channelIdentityBlockedAt(ctx, t, ci); !redelivered.Equal(*firstBlock) {
		t.Fatalf("blocked_at moved from %v to %v on a redelivered block; blocking must be idempotent",
			*firstBlock, *redelivered)
	}

	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return e.store.SetChannelIdentityBlocked(ctx, tx, ci, false)
	}); err != nil {
		t.Fatalf("unblock: %v", err)
	}
	if blockedAt := e.channelIdentityBlockedAt(ctx, t, ci); blockedAt != nil {
		t.Fatalf("blocked_at = %v after unblocking, want NULL", *blockedAt)
	}

	// The customer writes again — routine, and the whole reason D9 exists.
	resolved, err := e.resolveOrCreatePersonForIdentity(ctx, name, ci)
	if err != nil {
		t.Fatalf("resolving after unblock: %v", err)
	}
	if resolved != person {
		t.Fatalf("resolved %s after unblock, want the original person %s — block/unblock must not fork a second person",
			resolved, person)
	}

	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM person WHERE full_name = $1 AND archived_at IS NULL`, name); n != 1 {
		t.Fatalf("%d person rows named %q, want exactly 1", n, name)
	}
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM person_channel_identity WHERE channel_user_id = $1 AND archived_at IS NULL`,
		ci.ChannelUserID); n != 1 {
		t.Fatalf("%d live channel identity rows for %s, want exactly 1", n, ci.ChannelUserID)
	}
}
