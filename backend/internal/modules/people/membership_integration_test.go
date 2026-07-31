// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// SetChannelIdentityBlocked over real migrated Postgres (design §4.2 D9):
// proves the write is idempotent both ways, that it carries the write shape
// (audit row + outbox event in the flip's own transaction), that two
// transitions reaching it out of order still settle on the one Telegram issued
// last, and — the scenario the whole design turns on — that a block followed by
// an unblock never forks the returning customer's next message onto a second
// Person.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// membershipBotID is the bot every delivery in this file is stamped with,
// except where a test deliberately replaces it: update_id is a per-bot
// sequence, so which bot a transition arrived through is part of its ordering.
const membershipBotID = "42"

// setMembership applies one my_chat_member transition through the real write
// path, carrying the bot and the update_id the delivery was stamped with.
func (e *dedupeEnv) setMembership(
	ctx context.Context, t *testing.T, ci connector.ChannelIdentity, blocked bool, botID string, updateID int64,
) {
	t.Helper()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return e.store.SetChannelIdentityBlocked(ctx, tx, ci, blocked, botID, updateID)
	}); err != nil {
		t.Fatalf("SetChannelIdentityBlocked(blocked=%t, bot=%s, update=%d): %v", blocked, botID, updateID, err)
	}
}

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

	e.setMembership(ctx, t, ci, true, membershipBotID, 100)

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

	e.setMembership(ctx, t, ci, false, membershipBotID, 100)

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

	// One delivery, then Telegram's redelivery of that same delivery: the
	// update_id is the same because it is the same update.
	e.setMembership(ctx, t, ci, true, membershipBotID, 100)

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
	e.setMembership(ctx, t, ci, true, membershipBotID, 100)
	if n := e.reachabilityAudits(ctx, t, person); n != 1 {
		t.Fatalf("%d reachability audit rows after a redelivered block, want still 1", n)
	}
	if n := e.personUpdatedEvents(ctx, t, person); n != 1 {
		t.Fatalf("%d person.updated events after a redelivered block, want still 1", n)
	}

	// The unblock is a real state change again, and records the reverse.
	e.setMembership(ctx, t, ci, false, membershipBotID, 101)
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

	e.setMembership(ctx, t, ci, true, membershipBotID, 200)
	firstBlock := e.channelIdentityBlockedAt(ctx, t, ci)
	if firstBlock == nil {
		t.Fatal("blocked_at is NULL after blocking; want it set")
	}

	// Telegram redelivers my_chat_member; a repeat block must be a no-op, not
	// move the timestamp forward.
	e.setMembership(ctx, t, ci, true, membershipBotID, 200)
	if redelivered := e.channelIdentityBlockedAt(ctx, t, ci); !redelivered.Equal(*firstBlock) {
		t.Fatalf("blocked_at moved from %v to %v on a redelivered block; blocking must be idempotent",
			*firstBlock, *redelivered)
	}

	e.setMembership(ctx, t, ci, false, membershipBotID, 201)
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

// Telegram numbers its updates but does not deliver them in order to a fleet
// of workers, so the block and the unblock that answers it can commit either
// way round. The transition that loses that race must not be applied: a stale
// block accepted here leaves a reachable customer suppressed for good, because
// only another genuine block/unblock cycle ever writes blocked_at again.
//
// The unblock below also changes no state, which is the second half of the
// case: a watermark that only advanced on a real state change would record
// nothing here and admit the stale block anyway.
func TestAStaleMembershipUpdateCannotSuppressAReachableCustomer(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	person := e.seedPerson(ctx, t, "Overtaken Customer", nil, nil)
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "780005"}
	e.bindIdentity(ctx, t, person, ci)

	e.setMembership(ctx, t, ci, false, membershipBotID, 501)
	e.setMembership(ctx, t, ci, true, membershipBotID, 500)

	if blockedAt := e.channelIdentityBlockedAt(ctx, t, ci); blockedAt != nil {
		t.Fatalf("blocked_at = %v after update 500 arrived behind update 501; a superseded block must not suppress a reachable customer",
			*blockedAt)
	}
	if n := e.reachabilityAudits(ctx, t, person); n != 0 {
		t.Fatalf("%d reachability audit rows, want 0 — this customer's reachability never changed", n)
	}
}

// A transition carries the bot that received it and that bot's update id, and
// those two are the only thing that orders it against the transition it may be
// racing. One that arrives without them cannot be ordered, and applying it
// anyway would write a watermark no later update could beat — the identity's
// reachability would then never change again, and nothing would say so. The
// refusal is loud, and it leaves the stored state exactly as it found it.
func TestAReachabilityChangeThatCannotBeOrderedIsRefused(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	person := e.seedPerson(ctx, t, "Unorderable Customer", nil, nil)
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "780007"}
	e.bindIdentity(ctx, t, person, ci)

	for _, tc := range []struct {
		name     string
		botID    string
		updateID int64
	}{
		{name: "no bot named", botID: "", updateID: 500},
		{name: "no update id", botID: membershipBotID, updateID: 0},
		{name: "a negative update id", botID: membershipBotID, updateID: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := e.store.tx(ctx, func(tx pgx.Tx) error {
				return e.store.SetChannelIdentityBlocked(ctx, tx, ci, true, tc.botID, tc.updateID)
			})
			if err == nil {
				t.Fatal("the write applied a reachability change it cannot order against a concurrent one")
			}
			if blockedAt := e.channelIdentityBlockedAt(ctx, t, ci); blockedAt != nil {
				t.Fatalf("blocked_at = %v after a refused change, want it untouched", *blockedAt)
			}
		})
	}
}

// Replacing the workspace's bot restarts update_id from that bot's own
// sequence, so the new bot's ids are routinely far below the retired bot's.
// A watermark that did not reset per bot would read every one of them as stale
// and wedge the identity's reachability permanently — no block and no unblock
// from the live bot would ever be applied again.
func TestAFreshBotResetsTheMembershipWatermark(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	person := e.seedPerson(ctx, t, "Rebotted Customer", nil, nil)
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "780006"}
	e.bindIdentity(ctx, t, person, ci)

	const replacementBotID = "77"
	e.setMembership(ctx, t, ci, true, membershipBotID, 900)
	if blockedAt := e.channelIdentityBlockedAt(ctx, t, ci); blockedAt == nil {
		t.Fatal("blocked_at is NULL after a block from the retired bot; want it set")
	}

	e.setMembership(ctx, t, ci, false, replacementBotID, 5)
	if blockedAt := e.channelIdentityBlockedAt(ctx, t, ci); blockedAt != nil {
		t.Fatalf("blocked_at = %v after the replacement bot's unblock; its low update ids are fresh, not stale",
			*blockedAt)
	}
}
