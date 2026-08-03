// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The identity-review half of D8 (design §7.3) over a real migrated
// Postgres: an exact-lane conflict names both persons and both lanes in ONE
// dedupe_candidate row, writes nothing onto the rival, and the same
// recurring conflict — every later message from that identity, until a
// human resolves it — never raises a second review.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// evidenceEntry mirrors the wire shape (crm.yaml DedupeCandidate.evidence)
// closely enough to assert on it without re-implementing the schema.
type evidenceEntry struct {
	Field      string  `json:"field"`
	LeftValue  *string `json:"left_value"`
	RightValue *string `json:"right_value"`
	Signal     string  `json:"signal"`
}

// seedConflictingPair binds a channel identity to A and a phone to B, so a
// candidate carrying both keys names two different existing people — the
// scenario design §7.3 illustrates: "the channel-identity lane resolves to
// Person A, the phone lane to Person B".
func (e *dedupeEnv) seedConflictingPair(ctx context.Context, t *testing.T, phone string, ci connector.ChannelIdentity) (personA, personB ids.PersonID) {
	t.Helper()
	// Deliberately UNALIKE names on the same mail domain. This suite is about a
	// conflict between two identity LANES — a phone naming one person, a channel
	// handle naming another — so the pair must not also collide on name
	// similarity, which would put a second row in the queue these tests count.
	personA = e.seedPerson(ctx, t, "Ada Lovelace", []string{"a@identityconflict.test"}, nil)
	personB = e.seedPerson(ctx, t, "Grace Hopper", []string{"b@identityconflict.test"}, []string{phone})
	e.bindIdentity(ctx, t, personA, ci)
	return personA, personB
}

// resolveConflict runs DedupePerson inside its own transaction and returns
// the reported LaneConflict, failing the test if none was reported — the
// same real-Postgres path Task 3's ladder tests already exercise.
func (e *dedupeEnv) resolveConflict(ctx context.Context, t *testing.T, candidate PersonCandidate, wantRoutedTo ids.PersonID) LaneConflict {
	t.Helper()
	r := e.dedupeInTx(ctx, t, candidate)
	if r.PersonID != wantRoutedTo {
		t.Fatalf("routed to %s, want %s — routing must stay deterministic while a conflict is open", r.PersonID, wantRoutedTo)
	}
	if r.Conflict == nil {
		t.Fatal("two exact lanes named different people; the conflict must be reported")
	}
	return *r.Conflict
}

func TestExactLaneConflictEnqueuesOneIdentityReviewNamingBothPersonsAndLanes(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "770401"}
	personA, personB := e.seedConflictingPair(ctx, t, "+4915100000099", ci)

	candidate := PersonCandidate{
		FullName:          "Grace Hopper",
		Phones:            []string{"+4915100000099"},
		ChannelIdentities: []connector.ChannelIdentity{ci},
	}
	conflict := e.resolveConflict(ctx, t, candidate, personA)

	recorded, err := e.store.EnqueueIdentityConflict(ctx, conflict, "telegram:bot1:chat1:1", "connector:telegram")
	if err != nil {
		t.Fatalf("EnqueueIdentityConflict: %v", err)
	}
	if !recorded {
		t.Fatal("the first conflict on this pair must record a new row")
	}

	rows := openCandidates(ctx, t, e, entityPerson)
	if len(rows) != 1 {
		t.Fatalf("open queue holds %d candidates, want exactly 1", len(rows))
	}
	row := rows[0]
	gotPair := map[ids.UUID]bool{row.LeftID: true, row.RightID: true}
	if !gotPair[personA.UUID] || !gotPair[personB.UUID] {
		t.Fatalf("candidate pair = {%s, %s}, want it to name {%s, %s}", row.LeftID, row.RightID, personA, personB)
	}
	if row.Confidence != identityConflictConfidence {
		t.Fatalf("confidence = %v, want the exact-conflict ceiling %v", row.Confidence, identityConflictConfidence)
	}

	var evidence []evidenceEntry
	if err := json.Unmarshal(row.Evidence, &evidence); err != nil {
		t.Fatalf("unmarshal evidence: %v", err)
	}
	foundLanes := false
	for _, ev := range evidence {
		if ev.Signal != evidenceSignalExactConflict {
			continue
		}
		if ev.LeftValue == nil || ev.RightValue == nil {
			t.Fatalf("exact-conflict evidence entry %+v names only one lane", ev)
		}
		lanes := map[string]bool{*ev.LeftValue: true, *ev.RightValue: true}
		if lanes[laneChannelIdentity] && lanes[lanePhone] {
			foundLanes = true
		}
	}
	if !foundLanes {
		t.Fatalf("evidence %+v does not name both conflicting lanes (%s, %s)", evidence, laneChannelIdentity, lanePhone)
	}
}

func TestExactLaneConflictEnqueueWritesNothingOntoTheRival(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "770402"}
	personA, personB := e.seedConflictingPair(ctx, t, "+4915100000098", ci)

	candidate := PersonCandidate{
		FullName:          "Grace Hopper",
		Phones:            []string{"+4915100000098"},
		ChannelIdentities: []connector.ChannelIdentity{ci},
	}
	conflict := e.resolveConflict(ctx, t, candidate, personA)

	if _, err := e.store.EnqueueIdentityConflict(ctx, conflict, "telegram:bot1:chat2:1", "connector:telegram"); err != nil {
		t.Fatalf("EnqueueIdentityConflict: %v", err)
	}

	// The rival must carry no new satellite key — enqueuing the review is a
	// report, not a merge, exactly like routing itself (Task 3's own guard).
	err := e.store.tx(ctx, func(tx pgx.Tx) error {
		assertNoChannelIdentityFor(ctx, t, tx, personB)
		return nil
	})
	if err != nil {
		t.Fatalf("checking the rival: %v", err)
	}
}

func TestRepeatedConflictingMessageDoesNotEnqueueASecondReview(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "770403"}
	personA, _ := e.seedConflictingPair(ctx, t, "+4915100000097", ci)

	candidate := PersonCandidate{
		FullName:          "Grace Hopper",
		Phones:            []string{"+4915100000097"},
		ChannelIdentities: []connector.ChannelIdentity{ci},
	}

	// The first message raises the review.
	first := e.resolveConflict(ctx, t, candidate, personA)
	recorded, err := e.store.EnqueueIdentityConflict(ctx, first, "telegram:bot1:chat3:1", "connector:telegram")
	if err != nil {
		t.Fatalf("EnqueueIdentityConflict (first message): %v", err)
	}
	if !recorded {
		t.Fatal("the first message on a new conflicting pair must record a row")
	}

	// A second message from the SAME conflicting identity re-detects the
	// identical disagreement (routing is still deterministic and open) —
	// this must not raise a second review.
	second := e.resolveConflict(ctx, t, candidate, personA)
	recorded, err = e.store.EnqueueIdentityConflict(ctx, second, "telegram:bot1:chat3:2", "connector:telegram")
	if err != nil {
		t.Fatalf("EnqueueIdentityConflict (second message): %v", err)
	}
	if recorded {
		t.Fatal("a repeated conflict on the same pair must not enqueue a second review")
	}

	rows := openCandidates(ctx, t, e, entityPerson)
	if len(rows) != 1 {
		t.Fatalf("open queue holds %d candidates after two conflicting messages, want exactly 1", len(rows))
	}

	// A human's not_a_duplicate verdict must suppress the pair forever —
	// design §7.3's own warning — so a third message must still not re-raise it.
	if _, err := e.store.DisposeDedupeCandidate(ctx, rows[0].ID, "not_a_duplicate", nil); err != nil {
		t.Fatalf("DisposeDedupeCandidate: %v", err)
	}
	third := e.resolveConflict(ctx, t, candidate, personA)
	recorded, err = e.store.EnqueueIdentityConflict(ctx, third, "telegram:bot1:chat3:3", "connector:telegram")
	if err != nil {
		t.Fatalf("EnqueueIdentityConflict (third message, after not_a_duplicate): %v", err)
	}
	if recorded {
		t.Fatal("a pair already resolved not_a_duplicate must never be re-raised")
	}
	if rows := openCandidates(ctx, t, e, entityPerson); len(rows) != 0 {
		t.Fatalf("open queue holds %d candidates after the not_a_duplicate verdict, want 0", len(rows))
	}

	// Routing itself is unaffected by any of this — it is a pure read over
	// the satellite tables, never the review queue (design §7.3's split).
	final := e.dedupeInTx(ctx, t, candidate)
	if final.PersonID != personA {
		t.Fatalf("routed to %s after disposition, want %s — routing must stay deterministic regardless of the review's state", final.PersonID, personA)
	}
}
