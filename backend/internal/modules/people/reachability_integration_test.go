// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The reachability projection (design §6.6) on the person read: a live,
// unblocked channel identity reads back as reachable; a blocked one stays
// visible — the record must keep showing that a conversation exists — but
// reports unreachable. Both assert through the real GetPerson path, the same
// one the HTTP handler calls, not the loader function in isolation.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// channelIdentityTimestamps reads created_at/blocked_at straight off the row
// so the test compares the projection against the same database's own
// values, not a host clock that can skew against the DB's.
func (e *dedupeEnv) channelIdentityTimestamps(ctx context.Context, t *testing.T, ci connector.ChannelIdentity) (createdAt time.Time, blockedAt *time.Time) {
	t.Helper()
	err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT created_at, blocked_at FROM person_channel_identity
			 WHERE provider = $1 AND channel_user_id = $2 AND archived_at IS NULL`,
			ci.Provider, ci.ChannelUserID).Scan(&createdAt, &blockedAt)
	})
	if err != nil {
		t.Fatalf("reading channel identity timestamps: %v", err)
	}
	return createdAt, blockedAt
}

func TestPersonReadReportsTelegramReachability(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	person := e.seedPerson(ctx, t, "Telegram Contact", nil, nil)
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "780001", Username: "tguser"}
	e.bindIdentity(ctx, t, person, ci)
	createdAt, blockedAt := e.channelIdentityTimestamps(ctx, t, ci)
	if blockedAt != nil {
		t.Fatalf("a freshly bound identity has blocked_at = %v, want nil", blockedAt)
	}

	got, err := e.store.GetPerson(ctx, person, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("GetPerson: %v", err)
	}
	if got.Reachability == nil || len(*got.Reachability) != 1 {
		t.Fatalf("Reachability = %v, want exactly one entry", got.Reachability)
	}
	r := (*got.Reachability)[0]
	if r.Provider != crmcontracts.PersonReachabilityProviderTelegram {
		t.Fatalf("Provider = %q, want telegram", r.Provider)
	}
	if !r.Reachable {
		t.Fatal("Reachable = false, want true — the identity is live and unblocked")
	}
	if !r.Since.Equal(createdAt) {
		t.Fatalf("Since = %v, want the identity's created_at %v", r.Since, createdAt)
	}
}

func TestBlockedIdentityReportsUnreachableButKeepsTheIdentity(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	person := e.seedPerson(ctx, t, "Blocked Telegram Contact", nil, nil)
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "780002"}
	e.bindIdentity(ctx, t, person, ci)
	e.blockIdentity(ctx, t, ci)
	_, blockedAt := e.channelIdentityTimestamps(ctx, t, ci)
	if blockedAt == nil {
		t.Fatal("blocked_at is nil after blocking — the fixture did not take effect")
	}

	got, err := e.store.GetPerson(ctx, person, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("GetPerson: %v", err)
	}
	// The identity is archived_at IS NULL (blocking never archives it, §4.2
	// D9) — it must still surface, just as unreachable, so the record shows a
	// Telegram conversation exists even though a reply cannot be delivered.
	if got.Reachability == nil || len(*got.Reachability) != 1 {
		t.Fatalf("Reachability = %v, want the blocked identity still present as exactly one entry", got.Reachability)
	}
	r := (*got.Reachability)[0]
	if r.Reachable {
		t.Fatal("Reachable = true for a blocked identity, want false")
	}
	if !r.Since.Equal(*blockedAt) {
		t.Fatalf("Since = %v, want the block timestamp %v", r.Since, *blockedAt)
	}
}
