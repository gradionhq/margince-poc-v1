// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package notes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

const archivedActivityID = "22222222-2222-4222-8222-222222222222"

// archivedDelivery is the bus delivery this unit listens for.
func archivedDelivery() extension.Delivery {
	return extension.Delivery{
		EventID:    "33333333-3333-4333-8333-333333333333",
		Type:       "activity.archived",
		OccurredAt: time.Now().UTC(),
		Entity:     extension.EntityRef{Type: "activity", ID: archivedActivityID},
	}
}

// TestWithdrawFilingClearsTheFilingAndRecordsWhy: the note kept the id of an
// activity that is no longer on any timeline, and nothing in the core knows
// this unit's column exists — so the claim only becomes false-and-corrected if
// the unit hears the archive itself.
func TestWithdrawFilingClearsTheFilingAndRecordsWhy(t *testing.T) {
	rt := newRuntime()
	rt.tx.rows = [][]any{
		filedNoteRow(removedNoteID, kindNote, "filed once", callerUserID, false, "", time.Now().UTC()),
	}
	if err := withdrawFiling(context.Background(), rt, archivedDelivery()); err != nil {
		t.Fatal(err)
	}

	sql := rt.tx.only(t)
	for _, want := range []string{"UPDATE", "filed_activity_id = NULL", "WHERE filed_activity_id = $1::uuid", "RETURNING"} {
		if !strings.Contains(sql, want) {
			t.Errorf("the withdrawal statement misses %q:\n%s", want, sql)
		}
	}
	if got := rt.tx.args[0][0]; got != archivedActivityID {
		t.Errorf("the withdrawal matched on %v, want the archived activity %s", got, archivedActivityID)
	}

	if len(rt.tx.audited) != 1 {
		t.Fatalf("the withdrawal recorded %d ledger rows, want 1", len(rt.tx.audited))
	}
	change := rt.tx.audited[0]
	if change.Action != extension.AuditUpdate || change.Entity != noteEntity || change.ID != removedNoteID {
		t.Errorf("recorded %s on %s/%s, want an update of %s/%s",
			change.Action, change.Entity, change.ID, noteEntity, removedNoteID)
	}
	// The images are the two states of the one column that moved: the before
	// carries the filing that was there, the after carries none.
	if !strings.Contains(string(change.Before), archivedActivityID) {
		t.Errorf("the before image does not carry the filing that was withdrawn: %s", change.Before)
	}
	if strings.Contains(string(change.After), archivedActivityID) {
		t.Errorf("the after image still carries the withdrawn filing: %s", change.After)
	}
	// Why it changed is evidence, not a field: the note's own columns never
	// recorded the archive.
	for _, want := range []string{"activity.archived", archivedActivityID} {
		if !strings.Contains(string(change.Detail), want) {
			t.Errorf("the evidence does not say %q: %s", want, change.Detail)
		}
	}
	if len(rt.tx.published) != 1 || rt.tx.published[0].Verb != eventFilingWithdrawn {
		t.Errorf("published %v, want one %s", rt.tx.published, eventFilingWithdrawn)
	}
}

// TestWithdrawFilingIsSafeToRunTwice: the bus is at-least-once, so the SAME
// delivery arrives twice and the second one must leave nothing behind. A ledger
// row for a write that changed nothing is a history of an event that did not
// happen.
//
// It runs the handler twice against a fake whose table remembers the first run,
// rather than asserting the second run's shape from a fixture set up to look
// like one: what makes the redelivery a no-op is that the first run cleared the
// column its UPDATE matches on, and a test that pre-cleared it would prove
// only that this handler does nothing when handed nothing.
func TestWithdrawFilingIsSafeToRunTwice(t *testing.T) {
	rt := newRuntime()
	// The UPDATE matches on the filing, so the row comes back once and never
	// again — which is exactly what the database does after the first commit.
	rt.tx.rows = [][]any{
		filedNoteRow(removedNoteID, kindNote, "filed once", callerUserID, false, "", time.Now().UTC()),
	}

	first := archivedDelivery()
	if err := withdrawFiling(context.Background(), rt, first); err != nil {
		t.Fatal(err)
	}
	if len(rt.tx.audited) != 1 || len(rt.tx.published) != 1 {
		t.Fatalf("the first delivery recorded %d changes and %d events, want 1 and 1",
			len(rt.tx.audited), len(rt.tx.published))
	}

	if err := withdrawFiling(context.Background(), rt, first); err != nil {
		t.Fatal(err)
	}
	if len(rt.tx.audited) != 1 || len(rt.tx.published) != 1 {
		t.Errorf("the redelivery recorded %d changes and %d events in total, want the first run's 1 and 1",
			len(rt.tx.audited), len(rt.tx.published))
	}
	if len(rt.tx.statements) != 2 {
		t.Errorf("the handler issued %d statements over two deliveries, want one each", len(rt.tx.statements))
	}
}

// TestWithdrawFilingClearsEveryNoteFiledToTheActivity: nothing in the schema
// stops two notes naming one activity, and a handler that stopped at the first
// would leave the rest claiming a filing — the state it exists to remove.
func TestWithdrawFilingClearsEveryNoteFiledToTheActivity(t *testing.T) {
	const secondID = "44444444-4444-4444-8444-444444444444"
	rt := newRuntime()
	now := time.Now().UTC()
	rt.tx.rows = [][]any{
		filedNoteRow(removedNoteID, kindNote, "first", callerUserID, false, "", now),
		filedNoteRow(secondID, kindNote, "second", callerUserID, false, "", now),
	}
	if err := withdrawFiling(context.Background(), rt, archivedDelivery()); err != nil {
		t.Fatal(err)
	}
	if len(rt.tx.audited) != 2 || len(rt.tx.published) != 2 {
		t.Fatalf("recorded %d changes and %d events for two withdrawn notes, want 2 and 2",
			len(rt.tx.audited), len(rt.tx.published))
	}
	if rt.tx.audited[0].ID == rt.tx.audited[1].ID {
		t.Errorf("both ledger rows name the same note %s", rt.tx.audited[0].ID)
	}
}

// TestWithdrawFilingIgnoresADeliveryItCannotActOn: an event whose subject is
// not an activity, or whose id is not a UUID, is ACKED without touching the
// database. Failing it would re-deliver it forever — the reclaim pass hands
// back an entry nothing can ever make succeed — where an ack says the honest
// thing, that this handler has nothing to do with it.
func TestWithdrawFilingIgnoresADeliveryItCannotActOn(t *testing.T) {
	for name, d := range map[string]extension.Delivery{
		"a subject that is not an activity": {
			Type: "activity.archived", Entity: extension.EntityRef{Type: "person", ID: archivedActivityID},
		},
		"no subject at all": {Type: "activity.archived"},
		"an id that is not a UUID": {
			Type: "activity.archived", Entity: extension.EntityRef{Type: "activity", ID: "activity-7"},
		},
	} {
		rt := newRuntime()
		if err := withdrawFiling(context.Background(), rt, d); err != nil {
			t.Errorf("%s: err = %v, want it acked", name, err)
		}
		if len(rt.tx.statements) != 0 {
			t.Errorf("%s: it still reached the database: %v", name, rt.tx.statements)
		}
	}
}

// TestWithdrawFilingPropagatesAFailedWrite: an error leaves the entry pending
// and re-delivers it, which is the right outcome for a database that was
// briefly unavailable — and the wrong one to swallow, because the note would
// keep a filing nobody would ever correct.
func TestWithdrawFilingPropagatesAFailedWrite(t *testing.T) {
	rt := newRuntime()
	rt.tx.err = errors.New("deadlock detected")
	if err := withdrawFiling(context.Background(), rt, archivedDelivery()); err == nil {
		t.Fatal("a failed withdrawal was acked")
	}

	// The same holds for the ledger half: a cleared filing whose history was
	// not written must roll the whole transaction back.
	ledgerFailed := newRuntime()
	ledgerFailed.tx.rows = [][]any{
		filedNoteRow(removedNoteID, kindNote, "filed once", callerUserID, false, "", time.Now().UTC()),
	}
	ledgerFailed.tx.recordErr = errors.New("the ledger row could not be written")
	if err := withdrawFiling(context.Background(), ledgerFailed, archivedDelivery()); err == nil {
		t.Fatal("a withdrawal whose ledger row failed was acked")
	}
}
