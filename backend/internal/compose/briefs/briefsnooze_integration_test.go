// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package briefs

// The snooze half of the per-rep queue state (A77/AC-home-6): a snoozed
// item hides from the home read and suppresses its deal from fresh runs
// while `snoozed_until` lies ahead, then re-surfaces as actionable on
// time alone — unlike acted/dismissed, whose return needs a material
// change. All instants are injected; nothing here reads the wall clock.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/integration"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestBriefSnoozeIsOwnerOnlyStampedAndConflictSafe(t *testing.T) {
	b := setupBrief(t)
	owner := integration.OwnerConn(t)

	run, err := b.engine.SnapshotRun(b.repCtx, briefClock)
	if err != nil {
		t.Fatal(err)
	}
	itemA := run.Items[0]
	snoozeAt := briefClock.Add(2 * time.Hour)
	until := briefClock.Add(48 * time.Hour)

	// Only the run's owner may snooze: another rep sees not-found.
	rep2 := b.As(b.Rep2, []ids.UUID{b.Team1}, integration.AdminPerms)
	if _, err := b.engine.MarkSnoozed(rep2, itemA.ID, until, snoozeAt); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("foreign snooze → %v, want ErrNotFound (existence-hiding)", err)
	}

	snoozed, err := b.engine.MarkSnoozed(b.repCtx, itemA.ID, until, snoozeAt)
	if err != nil {
		t.Fatal(err)
	}
	if snoozed.State != briefStateSnoozed ||
		snoozed.SnoozedUntil == nil || !snoozed.SnoozedUntil.Equal(until) ||
		snoozed.StateAt == nil || !snoozed.StateAt.Equal(snoozeAt) {
		t.Fatalf("snoozed item = %+v, want state snoozed until %s stamped at %s", snoozed, until, snoozeAt)
	}

	// While the snooze runs, the item is not re-markable — a second mark is
	// a conflict, never a silent overwrite.
	if _, err := b.engine.MarkActed(b.repCtx, itemA.ID, snoozeAt.Add(time.Minute)); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("mark during snooze → %v, want ErrConflict", err)
	}

	// Once snoozed_until passes, the item is actionable again even without
	// a home read in between — a rep marking from a stale screen must not
	// read differently from one who re-opened the brief first.
	acted, err := b.engine.MarkActed(b.repCtx, itemA.ID, until.Add(time.Minute))
	if err != nil {
		t.Fatalf("mark after snooze expiry: %v", err)
	}
	if acted.State != briefStateActed || acted.SnoozedUntil != nil {
		t.Fatalf("post-expiry mark = %+v, want acted with the snooze cleared", acted)
	}

	// An acted item cannot be snoozed — the snooze only defers actionable work.
	if _, err := b.engine.MarkSnoozed(b.repCtx, itemA.ID, until.Add(72*time.Hour), until.Add(2*time.Minute)); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("snooze on an acted item → %v, want ErrConflict", err)
	}

	// Both transitions are audited (write shape; brief rows are audit-only).
	if audits := briefItemAudits(t, owner, itemA.ID); audits != 2 {
		t.Fatalf("brief_item audit rows = %d, want 2 (snooze + post-expiry act)", audits)
	}
}

// briefItemAudits counts the update audit rows one brief item accrued —
// every snooze, mark, and re-surface must leave exactly one.
func briefItemAudits(t *testing.T, owner *pgx.Conn, itemID ids.UUID) int {
	t.Helper()
	var audits int
	if err := owner.QueryRow(context.Background(), `
		SELECT count(*) FROM audit_log WHERE entity_type = 'brief_item' AND entity_id = $1 AND action = 'update'`,
		itemID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	return audits
}

func TestBriefSnoozedItemHidesUntilExpiryThenResurfaces(t *testing.T) {
	b := setupBrief(t)
	owner := integration.OwnerConn(t)

	run, err := b.engine.SnapshotRun(b.repCtx, briefClock)
	if err != nil {
		t.Fatal(err)
	}
	itemA := run.Items[0]
	until := briefClock.Add(48 * time.Hour)
	if _, err := b.engine.MarkSnoozed(b.repCtx, itemA.ID, until, briefClock.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// While now < snoozed_until: the home read hides the item…
	during := briefClock.Add(24 * time.Hour)
	hidden, err := b.engine.LatestRun(b.repCtx, during)
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden.Items) != 1 || hidden.Items[0].DealID == itemA.DealID {
		t.Fatalf("mid-snooze read = %+v, want only the unsnoozed item", hidden.Items)
	}
	// …and a fresh run suppresses the deal without needing any activity
	// (unlike dismissed, snooze is a pure time window).
	ranked, err := b.engine.Rank(b.repCtx, during)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range ranked.Queue {
		if item.DealID == itemA.DealID {
			t.Fatal("a fresh run ranked a deal whose brief item is mid-snooze")
		}
	}

	// After snoozed_until passes, the home read re-surfaces the item as
	// actionable: state flips back to new on the read itself.
	after := until.Add(time.Hour)
	resurfaced, err := b.engine.LatestRun(b.repCtx, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(resurfaced.Items) != 2 {
		t.Fatalf("post-expiry read has %d items, want the snoozed one back (2)", len(resurfaced.Items))
	}
	back := resurfaced.Items[0]
	if back.DealID != itemA.DealID {
		t.Fatalf("post-expiry head = %s, want the re-surfaced deal %s", back.DealID, itemA.DealID)
	}
	if back.State != briefStateNew || back.StateAt != nil || back.SnoozedUntil != nil {
		t.Fatalf("re-surfaced item = %+v, want a clean actionable state", back)
	}

	// The flip persists (the next read at the same instant sees it too) and
	// is audited like every other state change on the item.
	var state string
	if err := owner.QueryRow(context.Background(),
		`SELECT state FROM brief_item WHERE id = $1`, itemA.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != briefStateNew {
		t.Fatalf("persisted state = %q, want new after the re-surface", state)
	}
	if audits := briefItemAudits(t, owner, itemA.ID); audits != 2 {
		t.Fatalf("brief_item audit rows = %d, want 2 (snooze + re-surface)", audits)
	}

	// A fresh run after expiry ranks the deal again — re-surfacing needs
	// no new activity, only time.
	reranked, err := b.engine.Rank(b.repCtx, after)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range reranked.Queue {
		found = found || item.DealID == itemA.DealID
	}
	if !found {
		t.Fatalf("post-expiry rank = %v misses the formerly snoozed deal", queueDeals(reranked.Queue))
	}
}
