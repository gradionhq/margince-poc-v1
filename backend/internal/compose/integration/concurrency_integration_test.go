// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The merge/promote concurrency guarantees, against the real migrated
// Postgres: the pair lock keeps a merge target live to commit, and the
// lead row lock makes promotion once-only — a lost race answers a typed
// conflict instead of minting a duplicate person or phantom events.
// Races are exercised by genuinely concurrent store calls (goroutines
// against the same rows); the assertions are on the invariants that
// must hold regardless of which side wins.

import (
	"errors"
	"sync"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestConcurrentPromotesMintExactlyOnePerson(t *testing.T) {
	e := Setup(t)
	leadID := seedLead(t, e, "Racy Prospect", "racy@prospect.test", nil)

	const racers = 4
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = e.People.PromoteLead(e.Admin(), leadID,
				people.PromoteLeadInput{Trigger: "human_qualify"})
		}()
	}
	wg.Wait()

	wins, conflicts := 0, 0
	for _, err := range errs {
		var already *people.AlreadyPromotedError
		switch {
		case err == nil:
			wins++
		case errors.As(err, &already), errors.Is(err, apperrors.ErrConflict):
			conflicts++
		default:
			t.Errorf("loser answered %v, want AlreadyPromoted/conflict", err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d promotes won, want exactly 1", wins)
	}
	if conflicts != racers-1 {
		t.Errorf("%d losers answered a conflict, want %d", conflicts, racers-1)
	}

	// One lead, one person — the duplicate-mint bug this guards against.
	if n := e.WsCount(t, `SELECT count(*) FROM person WHERE converted_from_lead_id = $1`, leadID); n != 1 {
		t.Errorf("%d persons carry the lead's origin pointer, want exactly 1", n)
	}
	// And exactly one lead.promoted event: the losing transaction must
	// not have committed its phantom events.
	if n := e.WsCount(t, `SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'lead.promoted' AND envelope->'entity'->>'id' = $1::text`, leadID); n != 1 {
		t.Errorf("%d lead.promoted events staged, want exactly 1", n)
	}
}

func TestConcurrentMergesNeverStrandChildrenOnADeadRecord(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()

	mkPerson := func(name, email string) ids.PersonID {
		t.Helper()
		p, err := e.People.CreatePerson(admin, people.CreatePersonInput{
			FullName: name, Source: "manual",
			Emails: []people.PersonEmailInput{{Email: email, EmailType: "work", IsPrimary: true}},
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return personIDOf(ids.UUID(p.Id))
	}
	a := mkPerson("Person A", "a@merge-race.test")
	b := mkPerson("Person B", "b@merge-race.test")
	c := mkPerson("Person C", "c@merge-race.test")

	// merge(A→B) races merge(C→A): without the pair lock the second
	// merge can relink C's children onto an A that the first archives
	// mid-flight. Whichever order the locks serialize them into, no
	// live child may end on an archived person and every redirect must
	// land on a live row in one hop.
	var wg sync.WaitGroup
	var errAB, errCA error
	wg.Add(2)
	go func() { defer wg.Done(); _, errAB = e.People.MergePerson(e.Admin(), a, b) }()
	go func() { defer wg.Done(); _, errCA = e.People.MergePerson(e.Admin(), c, a) }()
	wg.Wait()

	// merge(A→B) always has a live source or answers the merged
	// conflict; merge(C→A) may lose to A's archival (a dead target is a
	// refusal, never a partial write).
	for name, err := range map[string]error{"merge A->B": errAB, "merge C->A": errCA} {
		var already *people.AlreadyMergedError
		var deadTarget *people.MergedTargetError
		if err != nil && !errors.As(err, &already) && !errors.As(err, &deadTarget) &&
			!errors.Is(err, apperrors.ErrConflict) && !errors.Is(err, apperrors.ErrNotFound) {
			t.Errorf("%s: unexpected error class %v", name, err)
		}
	}

	if n := e.WsCount(t, `
		SELECT count(*) FROM person_email pe
		JOIN person p ON p.id = pe.person_id
		WHERE pe.archived_at IS NULL AND p.archived_at IS NOT NULL`); n != 0 {
		t.Errorf("%d live emails point at archived persons — a merge stranded its relinked children", n)
	}
	if n := e.WsCount(t, `
		SELECT count(*) FROM person dead
		JOIN person hop ON hop.id = dead.merged_into_id
		WHERE hop.archived_at IS NOT NULL`); n != 0 {
		t.Errorf("%d merge redirects point at archived rows — the chain must stay one live hop", n)
	}
}

func TestMergeWithdrawalCarriesAConsentProofEvent(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()

	src, err := e.People.CreatePerson(admin, people.CreatePersonInput{FullName: "Withdrawn Src", Source: "manual"})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	tgt, err := e.People.CreatePerson(admin, people.CreatePersonInput{FullName: "Granted Tgt", Source: "manual"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	srcID, tgtID := personIDOf(ids.UUID(src.Id)), personIDOf(ids.UUID(tgt.Id))

	purpose := ids.NewV7()
	e.WsExec(t, `INSERT INTO consent_purpose (id, workspace_id, key, label) VALUES ($1, $2, 'marketing_email', 'Marketing')`, purpose, e.WS)
	e.WsExec(t, `INSERT INTO person_consent (id, workspace_id, person_id, purpose_id, state) VALUES ($1, $2, $3, $4, 'withdrawn')`, ids.NewV7(), e.WS, srcID, purpose)
	e.WsExec(t, `INSERT INTO person_consent (id, workspace_id, person_id, purpose_id, state) VALUES ($1, $2, $3, $4, 'granted')`, ids.NewV7(), e.WS, tgtID, purpose)

	if _, err := e.People.MergePerson(admin, srcID, tgtID); err != nil {
		t.Fatalf("merge: %v", err)
	}

	// The survivor is withdrawn (A's withdrawal wins) AND the state
	// change is proven: a paired consent_event names the transition.
	if n := e.WsCount(t, `SELECT count(*) FROM person_consent WHERE person_id = $1 AND purpose_id = $2 AND state = 'withdrawn'`, tgtID, purpose); n != 1 {
		t.Fatalf("survivor withdrawn-consent rows = %d, want exactly 1", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM consent_event WHERE person_id = $1 AND purpose_id = $2 AND new_state = 'withdrawn' AND source = 'merge'`, tgtID, purpose); n != 1 {
		t.Errorf("%d merge-sourced withdrawal proof events on the survivor, want exactly 1", n)
	}
}
