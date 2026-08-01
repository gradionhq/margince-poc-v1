// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Recovering participants for activities captured before ACT-DDL-3 existed
// (ADR-0078). Without this pass the who-knows-whom surface reads empty on a
// workspace with years of history, which looks exactly like a broken feature.
//
// The behaviours that matter, in the order they matter:
//
//   - a human-logged activity attributes to the human it names (exact);
//   - a connector-captured activity attributes to the mailbox owner, which is
//     the case the whole feature exists for and the one the old captured_by
//     string-match could never reach;
//   - with TWO connections for one provider it attributes to NEITHER — a
//     wrong edge tells someone to ask a colleague who has never met the
//     contact, which is worse than an absent one;
//   - the pass is idempotent and terminates: re-running writes nothing, and
//     it never re-selects an activity it cannot attribute.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedLegacyActivity writes an activity the way the timeline held them before
// participants existed: no participant rows, only a captured_by string.
func seedLegacyActivity(t *testing.T, e *integration.Env, sourceID, capturedBy, direction, counterparty string) ids.UUID {
	t.Helper()
	var id ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			INSERT INTO activity (workspace_id, kind, subject, direction, occurred_at,
			                      source_system, source_id, source, captured_by, counterparty_email)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        'email', 'Alt', $1, now(), 'gmail', $2, $3, $4, $5)
			RETURNING id`,
			direction, sourceID, "gmail:"+sourceID, capturedBy, counterparty).Scan(&id)
	}); err != nil {
		t.Fatalf("seeding legacy activity %s: %v", sourceID, err)
	}
	return id
}

// seedConnection binds one user to gmail, as capture_connection does. The
// provider is fixed because the ambiguity these tests probe is two users on
// ONE provider, not one user on two.
func seedConnection(t *testing.T, e *integration.Env, user ids.UUID) {
	t.Helper()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO capture_connection (workspace_id, user_id, provider, status, credential_ref)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, 'gmail', 'connected', 'vault:test')
			ON CONFLICT DO NOTHING`, user)
		return err
	}); err != nil {
		t.Fatalf("seeding a gmail connection for %s: %v", user, err)
	}
}

// participantUsers returns the user ids recorded as participants of one
// activity — the answer the whole backfill exists to produce.
func participantUsers(t *testing.T, e *integration.Env, activityID ids.UUID) []ids.UUID {
	t.Helper()
	var out []ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(),
			`SELECT user_id FROM activity_participant
			  WHERE activity_id = $1 AND user_id IS NOT NULL ORDER BY user_id`, activityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var u ids.UUID
			if err := rows.Scan(&u); err != nil {
				return err
			}
			out = append(out, u)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("reading participants of %s: %v", activityID, err)
	}
	return out
}

// runBackfill drains the pass the way the job does — until it reports no more
// work — and fails if it does not terminate, which is the failure a predicate
// that selects unattributable rows would produce.
func runBackfill(t *testing.T, e *integration.Env) int {
	t.Helper()
	store := activities.NewStore(e.Pool)
	total := 0
	for pass := 0; ; pass++ {
		if pass > 20 {
			t.Fatal("the participant backfill never reported itself done — it is re-selecting activities it cannot attribute")
		}
		n, err := store.BackfillParticipantsBatch(e.Admin(), 100)
		if err != nil {
			t.Fatalf("backfill pass %d: %v", pass, err)
		}
		if n == 0 {
			return total
		}
		total += n
	}
}

func TestTheBackfillAttributesConnectorMailToItsMailboxOwner(t *testing.T) {
	e := integration.Setup(t)
	seedConnection(t, e, e.Rep1)

	// The case the old derivation could never reach: captured_by names the
	// connector, not a human, so a string-match on 'human:%' found nothing.
	captured := seedLegacyActivity(t, e, "bf-conn-1", "connector:gmail", "outbound", "pat@counterparty.test")
	// And one a human logged, which states its user outright.
	logged := seedLegacyActivity(t, e, "bf-human-1", "human:"+e.Rep1.String(), "outbound", "pat@counterparty.test")

	runBackfill(t, e)

	if got := participantUsers(t, e, captured); len(got) != 1 || got[0] != e.Rep1 {
		t.Errorf("connector-captured mail attributed to %v, want the single mailbox owner %s", got, e.Rep1)
	}
	if got := participantUsers(t, e, logged); len(got) != 1 || got[0] != e.Rep1 {
		t.Errorf("human-logged activity attributed to %v, want %s", got, e.Rep1)
	}
}

func TestTheBackfillRefusesToGuessBetweenTwoMailboxes(t *testing.T) {
	e := integration.Setup(t)
	// Two people have connected the same provider. An activity captured as
	// 'connector:gmail' could be either mailbox and the row carries no
	// evidence that separates them.
	seedConnection(t, e, e.Rep1)
	seedConnection(t, e, e.Rep2)

	ambiguous := seedLegacyActivity(t, e, "bf-ambig-1", "connector:gmail", "inbound", "pat@counterparty.test")
	// A human-logged row in the same workspace still attributes: the
	// ambiguity is class 2's, and it must not suppress class 1.
	stated := seedLegacyActivity(t, e, "bf-stated-1", "human:"+e.Rep2.String(), "inbound", "pat@counterparty.test")

	runBackfill(t, e)

	if got := participantUsers(t, e, ambiguous); len(got) != 0 {
		t.Errorf("an activity that could belong to either mailbox was attributed to %v; a wrong edge sends someone to a colleague who has never met the contact", got)
	}
	if got := participantUsers(t, e, stated); len(got) != 1 || got[0] != e.Rep2 {
		t.Errorf("the human-logged activity attributed to %v, want %s — class 2's ambiguity must not suppress class 1", got, e.Rep2)
	}
}

func TestTheBackfillIsIdempotentAndTerminates(t *testing.T) {
	e := integration.Setup(t)
	seedConnection(t, e, e.Rep1)
	seedLegacyActivity(t, e, "bf-idem-1", "connector:gmail", "outbound", "pat@counterparty.test")
	// An activity nothing can attribute: no connection matches its provider.
	// It must be skipped forever rather than re-selected, or the drain loop
	// above would never end.
	orphan := seedLegacyActivity(t, e, "bf-orphan-1", "connector:outlook", "inbound", "x@stranger.test")

	first := runBackfill(t, e)
	if first == 0 {
		t.Fatal("the backfill wrote nothing on a workspace that has attributable history")
	}
	// A second drain must be a no-op: the pass carries no cursor, so this is
	// what makes it safe to re-run after a crash mid-batch.
	if second := runBackfill(t, e); second != 0 {
		t.Errorf("re-running the backfill wrote %d more rows; the pass is not idempotent", second)
	}
	if got := participantUsers(t, e, orphan); len(got) != 0 {
		t.Errorf("an unattributable activity was attributed to %v", got)
	}
}

func TestTheBackfillDoesNotInventParticipantsForNotesAndTasks(t *testing.T) {
	e := integration.Setup(t)
	seedConnection(t, e, e.Rep1)

	// A note and a task are not interactions: assigning work is intent, and
	// writing something down is not reaching out. Live stamping already
	// refuses them, and the backfill has to agree — otherwise a workspace's
	// HISTORY and its NEW mail disagree about what counts as contact, and the
	// same relationship scores differently depending on when it happened.
	note := seedLegacyActivityOfKind(t, e, "bf-note-1", "human:"+e.Rep1.String(), "note")
	task := seedLegacyActivityOfKind(t, e, "bf-task-1", "human:"+e.Rep1.String(), "task")
	mail := seedLegacyActivityOfKind(t, e, "bf-mail-1", "human:"+e.Rep1.String(), "email")

	runBackfill(t, e)

	for _, c := range []struct {
		id   ids.UUID
		kind string
		want int
	}{{note, "note", 0}, {task, "task", 0}, {mail, "email", 2}} {
		var got int
		if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(),
				`SELECT count(*) FROM activity_participant WHERE activity_id = $1`, c.id).Scan(&got)
		}); err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("a %s produced %d participant rows, want %d", c.kind, got, c.want)
		}
	}
}

// seedLegacyActivityOfKind is seedLegacyActivity with the kind chosen.
func seedLegacyActivityOfKind(t *testing.T, e *integration.Env, sourceID, capturedBy, kind string) ids.UUID {
	t.Helper()
	var id ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			INSERT INTO activity (workspace_id, kind, subject, direction, occurred_at,
			                      source_system, source_id, source, captured_by, counterparty_email)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, 'Alt', 'outbound', now(), 'gmail', $2, $3, $4, 'pat@counterparty.test')
			RETURNING id`, kind, sourceID, "gmail:"+sourceID, capturedBy).Scan(&id)
	}); err != nil {
		t.Fatalf("seeding a %s: %v", kind, err)
	}
	return id
}
