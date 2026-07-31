// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// StrengthForPeopleAsOf over a real Postgres: what §4 WOULD have said at a
// past instant. It is what "this relationship is going cold" compares against
// — today's score against the same contact's score a month ago — and it does
// it without a snapshot table.
//
// The two properties that make it usable, and the one that makes it honest:
//
//   - it excludes interactions that happened AFTER the instant asked about,
//     or every past score would equal today's and no decay would ever show;
//   - it still reads the window relative to that instant, so a contact who
//     was busy then scores as busy then;
//   - and it is a counterfactual over TODAY's corpus, not a recording. An
//     erased interaction is absent from the past answer too, which is the
//     behaviour we want: a score must not become a way to read deleted data.

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/relstrength"
)

// seedInteraction writes one qualifying activity linked to a person at a
// chosen moment.
func (e *dedupeEnv) seedInteraction(t *testing.T, personID ids.PersonID, at time.Time, direction string) {
	t.Helper()
	id := ids.New[ids.ActivityKind]()
	ctx := e.as()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity (id, workspace_id, kind, subject, direction, occurred_at,
			                      source_system, source_id, source, captured_by)
			VALUES ($1, $2, 'email', 'Hallo', $3, $4, 'gmail', $5, 'gmail:seed', 'connector:gmail')`,
			id, e.ws, direction, at, id.String()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
			VALUES ($1, $2, 'person', $3)`, e.ws, id, personID)
		return err
	}); err != nil {
		t.Fatalf("seeding an interaction: %v", err)
	}
}

// seedContact writes one workspace-visible person.
func (e *dedupeEnv) seedContact(t *testing.T, name string) ids.PersonID {
	t.Helper()
	id := ids.New[ids.PersonKind]()
	ctx := e.as()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by, visibility)
			VALUES ($1, $2, $3, $4, 'manual', 'human:test', 'workspace')`,
			id, e.ws, name, e.rep)
		return err
	}); err != nil {
		t.Fatalf("seeding contact %s: %v", name, err)
	}
	return id
}

// strengthAt reads one person's score at an instant, through whichever entry
// point the caller names.
func (e *dedupeEnv) strengthAt(t *testing.T, personID ids.PersonID, at time.Time, asOf bool) relstrengthResult {
	t.Helper()
	var out relstrengthResult
	ctx := e.as()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		var got []ContactStrength
		var err error
		if asOf {
			got, err = StrengthForPeopleAsOf(ctx, tx, []ids.PersonID{personID}, at)
		} else {
			got, err = StrengthForPeople(ctx, tx, []ids.PersonID{personID}, at)
		}
		if err != nil {
			return err
		}
		if len(got) != 1 {
			t.Fatalf("scored %d contacts, want 1", len(got))
		}
		out = relstrengthResult{
			strength: got[0].Strength.Strength,
			bucket:   got[0].Strength.Bucket,
			count90d: got[0].Strength.InteractionCount90d,
		}
		return nil
	}); err != nil {
		t.Fatalf("reading strength: %v", err)
	}
	return out
}

type relstrengthResult struct {
	strength int
	bucket   string
	count90d int
}

func TestStrengthAsOfExcludesWhatHadNotHappenedYet(t *testing.T) {
	e := setupDedupe(t)
	now := time.Now().UTC()
	contact := e.seedContact(t, "Cold Contact")

	// A burst of two-way traffic ending 40 days ago, then silence.
	for i := 0; i < 12; i++ {
		direction := "inbound"
		if i%2 == 0 {
			direction = "outbound"
		}
		e.seedInteraction(t, contact, now.AddDate(0, 0, -40-i), direction)
	}

	// Then, as of 35 days ago, the relationship was live.
	then := e.strengthAt(t, contact, now.AddDate(0, 0, -35), true)
	if then.count90d != 12 {
		t.Errorf("as-of 35 days ago counted %d interactions in window, want 12", then.count90d)
	}
	if then.bucket == relstrength.BucketNone || then.strength == 0 {
		t.Fatalf("as-of 35 days ago scored %d (%s); the relationship was active then", then.strength, then.bucket)
	}

	// Today it has decayed. If the as-of read did not exclude later
	// interactions this comparison would be meaningless — which is the whole
	// point of the going-cold detector that consumes it.
	today := e.strengthAt(t, contact, now, false)
	if today.strength >= then.strength {
		t.Errorf("today's score %d is not below the %d it was 35 days ago; the relationship has gone quiet and the comparison cannot see it",
			today.strength, then.strength)
	}
}

func TestStrengthAsOfIgnoresInteractionsAfterTheInstantAsked(t *testing.T) {
	e := setupDedupe(t)
	now := time.Now().UTC()
	contact := e.seedContact(t, "Recent Contact")

	// Everything happened in the last week.
	for i := 0; i < 10; i++ {
		e.seedInteraction(t, contact, now.AddDate(0, 0, -i), "inbound")
	}

	// Asked about a month ago, the answer must be "we had never spoken" —
	// not today's score with an older label on it.
	before := e.strengthAt(t, contact, now.AddDate(0, 0, -30), true)
	if before.bucket != relstrength.BucketNone {
		t.Errorf("as-of a month before first contact = %d (%s), want the %q bucket — those interactions had not happened yet",
			before.strength, before.bucket, relstrength.BucketNone)
	}
	if before.count90d != 0 {
		t.Errorf("as-of a month before first contact counted %d interactions, want 0", before.count90d)
	}
}

func TestTheLiveScoreStillCountsAnInteractionTimestampedAhead(t *testing.T) {
	e := setupDedupe(t)
	now := time.Now().UTC()
	contact := e.seedContact(t, "Skewed Contact")

	// Clock skew between the app and the database is ordinary, and the live
	// score has always counted such a row. Bounding the live read at `now`
	// would silently drop it — which is why the upper bound is optional
	// rather than defaulted to the read instant.
	e.seedInteraction(t, contact, now.Add(2*time.Minute), "inbound")
	e.seedInteraction(t, contact, now.AddDate(0, 0, -1), "outbound")

	live := e.strengthAt(t, contact, now, false)
	if live.count90d != 2 {
		t.Errorf("the live score counted %d interactions, want 2 — an interaction timestamped slightly ahead is still an interaction", live.count90d)
	}

	// The as-of read is the one that excludes it, on purpose.
	asOf := e.strengthAt(t, contact, now, true)
	if asOf.count90d != 1 {
		t.Errorf("the as-of read counted %d, want 1 — it must exclude what had not happened at the instant asked", asOf.count90d)
	}
}
