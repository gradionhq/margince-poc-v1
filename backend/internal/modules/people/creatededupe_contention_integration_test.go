// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// What a manual create does when another create of the SAME number is
// already in flight. The sibling file holds the single-writer policy per
// lane; the ordering is what this one is about, and it may not depend on
// scheduling: the first create is frozen by hand exactly where the defect
// lives — past its probe and its person_phone insert, before its commit —
// and the second is released only once a backend is provably waiting on it.
// No sleep, no clock, no guess.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// beginHeldPhoneCreate opens a transaction that has done everything a manual
// create on this number does up to its commit — it holds the phone lane's
// write identity and its person_phone row is written but visible to nobody
// else — and hands it back still open, so the test owns the moment a second
// create can see it. Its rollback is registered here rather than left to the
// caller: a transaction still open on a failure path holds the lane's lock
// and a pooled connection with it, and the run that meant to fail loudly
// would hang instead.
func (e *dedupeEnv) beginHeldPhoneCreate(ctx context.Context, t *testing.T, name, phone string) (pgx.Tx, ids.PersonID, int) {
	t.Helper()
	tx, err := e.store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("opening the in-flight create's transaction: %v", err)
	}
	t.Cleanup(func() {
		err := tx.Rollback(context.Background())
		if err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("releasing the in-flight create's transaction: %v", err)
		}
	})
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, e.ws.String()); err != nil {
		t.Fatalf("binding the workspace GUC: %v", err)
	}
	var pid int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatalf("reading the in-flight create's backend pid: %v", err)
	}
	// Through the create path's own key derivation, so a change to it can
	// never leave this transaction holding a key no create waits on.
	keys, err := phoneLaneKeys([]PersonPhoneInput{{Phone: phone}})
	if err != nil {
		t.Fatalf("deriving the phone lane key for %s: %v", phone, err)
	}
	if err := lockPhoneLane(ctx, tx, keys); err != nil {
		t.Fatalf("taking the phone lane's write identity: %v", err)
	}

	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		t.Fatalf("resolving captured_by: %v", err)
	}
	id := ids.New[ids.PersonKind]()
	if _, err := tx.Exec(ctx,
		`INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, $3, 'manual', $4)`,
		id, e.ws, name, by); err != nil {
		t.Fatalf("inserting the in-flight person: %v", err)
	}
	if err := insertPersonPhones(ctx, tx, workspaceID(ctx), id, "manual", by,
		[]PersonPhoneInput{{Phone: phone, PhoneType: "mobile", IsPrimary: true, Position: 1}}); err != nil {
		t.Fatalf("inserting the in-flight person's number: %v", err)
	}
	return tx, id, pid
}

// racingCreate is one background create's whole outcome, so the test can
// judge it after it has released the transaction that was blocking it.
type racingCreate struct {
	person crmcontracts.Person
	err    error
}

// createInBackground runs one manual create on its own connection — the
// second request, arriving while the first is still uncommitted. The channel
// is buffered so the goroutine can always finish and hand its result back,
// even on a path where the test fails before reading it.
func (e *dedupeEnv) createInBackground(ctx context.Context, in CreatePersonInput) <-chan racingCreate {
	done := make(chan racingCreate, 1)
	go func() {
		person, err := e.store.CreatePerson(ctx, in)
		done <- racingCreate{person: person, err: err}
	}()
	return done
}

// awaitRacer returns once the background create is provably waiting on a lock
// the given pid holds, or once it has finished without ever waiting. Busy-read
// of pg_stat_activity: no clock and no sleep, and the pid makes it exact —
// pg_stat_activity is cluster-wide and the parallel lane runs a dozen packages
// against one server.
//
// The sibling probe in ensurechannel_contention_integration_test.go fails the
// run when nothing ever waited. Here that outcome is the defect itself — a
// create that read straight through an in-flight one — and it has to be judged
// on the trail it left rather than aborted at the probe, so it comes back as a
// finished result instead.
func awaitRacer(t *testing.T, probe pgx.Tx, pid int, done <-chan racingCreate) (racingCreate, bool) {
	t.Helper()
	// Generous enough that a loaded machine cannot trip it, small enough that a
	// genuine miss reports in seconds rather than minutes.
	const maxProbes = 20_000
	for i := 0; i < maxProbes; i++ {
		var blocked bool
		if err := probe.QueryRow(context.Background(), `
			SELECT EXISTS (
			  SELECT 1 FROM pg_stat_activity a
			   WHERE a.datname = current_database() AND $1 = ANY (pg_blocking_pids(a.pid)))`,
			pid).Scan(&blocked); err != nil {
			t.Fatalf("probing for a waiting backend: %v", err)
		}
		if blocked {
			return racingCreate{}, false
		}
		select {
		case res := <-done:
			return res, true
		default:
		}
	}
	t.Fatalf("within %d probes the second create neither waited on the held number nor returned", maxProbes)
	return racingCreate{}, false
}

// Two people can legitimately share a switchboard, so person_phone has no
// cross-person unique index and nothing structural stops two creates carrying
// the same number from both landing. What must not happen is that they land
// SILENTLY: the exact phone lane routes past the fuzzy tier, so if the second
// create's probe runs while the first is still uncommitted, both read an empty
// lane, both fall to no-match, and the pair reaches nobody — no queue row, and
// no later sweep that would find it.
func TestASecondCreateSharingAPhoneStillRecordsTheReviewPair(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	const switchboard = "+4930900000501"

	first, firstID, pid := e.beginHeldPhoneCreate(ctx, t, "Ada Vorreiter", switchboard)
	second := e.createInBackground(ctx, CreatePersonInput{
		FullName: "Bruno Kellerwald", Source: "manual",
		Emails: []PersonEmailInput{{Email: "bruno@kellerwald.test", EmailType: "work", IsPrimary: true}},
		Phones: []PersonPhoneInput{{Phone: switchboard, PhoneType: "work", IsPrimary: true}},
	})
	res, finished := awaitRacer(t, first, pid, second)
	if err := first.Commit(ctx); err != nil {
		t.Fatalf("committing the first create: %v", err)
	}
	if !finished {
		res = <-second
	}
	if res.err != nil {
		t.Fatalf("a shared number must not refuse the second create: %v", res.err)
	}
	secondID := ids.From[ids.PersonKind](ids.UUID(res.person.Id))

	// Both records really are live on the number: without that this test would
	// pass on an outcome where one create simply never landed.
	if n := e.countInWorkspace(ctx, t, `
		SELECT count(*) FROM person_phone ph
		  JOIN person p ON p.id = ph.person_id
		 WHERE ph.phone = $1 AND ph.archived_at IS NULL AND p.archived_at IS NULL`,
		switchboard); n != 2 {
		t.Fatalf("%d live people hold %s, want 2 — both creates must have landed for this ordering to mean anything", n, switchboard)
	}

	rows := openCandidates(ctx, t, e, entityPerson)
	if len(rows) != 1 {
		t.Fatalf("the review queue holds %d candidates, want exactly 1 — two live people share %s and no human will ever be asked about it",
			len(rows), switchboard)
	}
	pair := map[ids.UUID]bool{rows[0].LeftID: true, rows[0].RightID: true}
	if !pair[firstID.UUID] || !pair[secondID.UUID] {
		t.Fatalf("candidate pair = {%s, %s}, want it to name both creates %s and %s",
			rows[0].LeftID, rows[0].RightID, firstID, secondID)
	}
	if rows[0].Confidence != identityConflictConfidence {
		t.Fatalf("confidence = %v, want the exact-key ceiling %v", rows[0].Confidence, identityConflictConfidence)
	}
}
