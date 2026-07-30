// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// SetChannelIdentityBlocked over real migrated Postgres (design §4.2 D9):
// proves the write is idempotent both ways, that it carries the write shape
// (audit row + outbox event in the flip's own transaction), and — the scenario
// the whole design turns on — that a block followed by an unblock never forks
// the returning customer's next message onto a second Person.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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

// reachabilityAudits counts the person-scoped audit rows a reachability flip
// leaves, and reachabilityImages reads the newest one's before/after pair.
// Together they say both "a change was recorded" and "it recorded the right
// change" — a count alone would pass on an audit row claiming the opposite.
func (e *dedupeEnv) reachabilityAudits(ctx context.Context, t *testing.T, personID ids.PersonID) int {
	t.Helper()
	return e.countInWorkspace(ctx, t, `
		SELECT count(*) FROM audit_log
		 WHERE entity_type = 'person' AND entity_id = $1 AND action = 'update'
		   AND after->'reachability' IS NOT NULL`, personID)
}

func (e *dedupeEnv) reachabilityImages(ctx context.Context, t *testing.T, personID ids.PersonID) (was, is bool) {
	t.Helper()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT (before->'reachability'->>'reachable')::bool,
			       (after->'reachability'->>'reachable')::bool
			  FROM audit_log
			 WHERE entity_type = 'person' AND entity_id = $1 AND action = 'update'
			   AND after->'reachability' IS NOT NULL
			 ORDER BY occurred_at DESC, id DESC
			 LIMIT 1`, personID).Scan(&was, &is)
	}); err != nil {
		t.Fatalf("reading the newest reachability audit images for %s: %v", personID, err)
	}
	return was, is
}

// personUpdatedEvents counts the outbox half of the write shape.
func (e *dedupeEnv) personUpdatedEvents(ctx context.Context, t *testing.T, personID ids.PersonID) int {
	t.Helper()
	return e.countInWorkspace(ctx, t, `
		SELECT count(*) FROM event_outbox
		 WHERE envelope->>'type' = 'person.updated'
		   AND envelope->'entity'->>'id' = $1`, personID.String())
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

// TestReachabilityFlipCarriesTheWriteShape holds the flip to domain row +
// audit row + outbox event in one transaction. Reachability is Person record
// state — it decides whether the timeline offers a reply box at all — so a
// flip with no trail changes what the record says with nothing saying who
// changed it or when. A redelivery changes no state and must therefore leave
// no second trail: an audit spine that grows one row per redelivered webhook
// is unreadable exactly when someone needs to read it.
func TestReachabilityFlipCarriesTheWriteShape(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	person := e.seedPerson(ctx, t, "Traceable Customer", nil, nil)
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "780004"}
	e.bindIdentity(ctx, t, person, ci)

	// The create itself emits person.created, not person.updated, so the
	// baseline for the outbox half is zero.
	if n := e.personUpdatedEvents(ctx, t, person); n != 0 {
		t.Fatalf("%d person.updated events before any reachability change, want 0", n)
	}

	block := func() {
		t.Helper()
		if err := e.store.tx(ctx, func(tx pgx.Tx) error {
			return e.store.SetChannelIdentityBlocked(ctx, tx, ci, true)
		}); err != nil {
			t.Fatalf("block: %v", err)
		}
	}
	block()

	if n := e.reachabilityAudits(ctx, t, person); n != 1 {
		t.Fatalf("%d reachability audit rows after a block, want exactly 1", n)
	}
	if was, is := e.reachabilityImages(ctx, t, person); !was || is {
		t.Fatalf("block recorded reachable %t → %t, want true → false", was, is)
	}
	if n := e.personUpdatedEvents(ctx, t, person); n != 1 {
		t.Fatalf("%d person.updated events after a block, want exactly 1", n)
	}

	// Telegram redelivers my_chat_member. The guarded UPDATE touches no row,
	// so neither half of the write shape may fire.
	block()
	if n := e.reachabilityAudits(ctx, t, person); n != 1 {
		t.Fatalf("%d reachability audit rows after a redelivered block, want still 1", n)
	}
	if n := e.personUpdatedEvents(ctx, t, person); n != 1 {
		t.Fatalf("%d person.updated events after a redelivered block, want still 1", n)
	}

	// The unblock is a real state change again, and records the reverse.
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return e.store.SetChannelIdentityBlocked(ctx, tx, ci, false)
	}); err != nil {
		t.Fatalf("unblock: %v", err)
	}
	if n := e.reachabilityAudits(ctx, t, person); n != 2 {
		t.Fatalf("%d reachability audit rows after the unblock, want 2", n)
	}
	if was, is := e.reachabilityImages(ctx, t, person); was || !is {
		t.Fatalf("unblock recorded reachable %t → %t, want false → true", was, is)
	}
	if n := e.personUpdatedEvents(ctx, t, person); n != 2 {
		t.Fatalf("%d person.updated events after the unblock, want 2", n)
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
