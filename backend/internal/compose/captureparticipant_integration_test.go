// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Capture records WHO was in the interaction (ACT-DDL-3 / ADR-0078).
//
// This is the fact the whole "who on our team knows this contact" feature was
// missing. The derivation that existed before matched
// `captured_by = 'human:<uuid>'` on the activity row, and connector-captured
// mail is stamped `connector:gmail` — so the overwhelming majority of the
// timeline produced no our-side edge at all. The mailbox owner was known at
// ingest and simply never written down.
//
// What these tests hold to the database:
//
//   - a captured message names the mailbox owner as a participant, with the
//     role its direction implies (our user sends on outbound, receives on
//     inbound), because a one-way blast and a real exchange must be
//     distinguishable later;
//   - the counterparty is recorded even before any person exists for them;
//   - replay writes nothing new — capture's sync loop is at-least-once, and a
//     participant set that grows on every poll is worse than none.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// mailboxOwnerCtx binds a connector principal carrying the granting human, the
// way capture.Registry builds it — UserID is the mailbox owner, and it is the
// only place that fact exists.
func mailboxOwnerCtx(e *integration.Env, owner ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalConnector, ID: "connector:gmail",
		UserID: owner, OnBehalfOf: owner,
		Permissions: principal.Permissions{
			Objects: map[string]principal.ObjectGrant{
				"activity":     {Create: true, Read: true},
				"person":       {Create: true, Read: true, Update: true},
				"organization": {Create: true, Read: true, Update: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
}

// participantRow is one row of the recorded interaction, flattened for
// assertion: which arm named the party, and in what role.
type participantRow struct {
	role    string
	user    *ids.UUID
	person  *ids.UUID
	address string
}

func readParticipants(t *testing.T, e *integration.Env, activityID ids.UUID) []participantRow {
	t.Helper()
	var out []participantRow
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `
			SELECT role, user_id, person_id, coalesce(address, '')
			  FROM activity_participant WHERE activity_id = $1
			 ORDER BY role, coalesce(address, '')`, activityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p participantRow
			if err := rows.Scan(&p.role, &p.user, &p.person, &p.address); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("reading participants of %s: %v", activityID, err)
	}
	return out
}

// captureMail lands one mail through the real sink under the mailbox owner's
// connector principal.
func captureMail(t *testing.T, e *integration.Env, owner ids.UUID, sourceID, direction, counterparty string) ids.UUID {
	t.Helper()
	sink := capture.NewSink(e.Pool)
	ref, err := sink.Upsert(mailboxOwnerCtx(e, owner), connector.NormalizedRecord{
		EntityType:   "activity",
		NaturalKey:   connector.NaturalKey{SourceSystem: "gmail", SourceID: sourceID},
		Counterparty: connector.Counterparty{Email: counterparty, DisplayName: "Pat Counterparty"},
		Fields: capture.ActivityFields{
			Kind: "email", Subject: "Angebot", Body: "Anbei.", Direction: direction,
		},
		Source: "gmail:" + sourceID, CapturedBy: "connector:gmail",
	})
	if err != nil {
		t.Fatalf("capturing %s: %v", sourceID, err)
	}
	return ref.ID
}

func TestCapturedMailRecordsTheMailboxOwnerAsAParticipant(t *testing.T) {
	e := integration.Setup(t)
	owner := e.Rep1

	// Outbound: our user is the sender.
	sent := captureMail(t, e, owner, "p-out-1", connector.DirectionOutbound, "pat@counterparty.test")
	out := readParticipants(t, e, sent)
	if len(out) != 2 {
		t.Fatalf("an outbound mail recorded %d participants, want 2 (the mailbox owner and the counterparty): %+v", len(out), out)
	}
	assertParticipant(t, out, "from", &owner, "")
	assertParticipant(t, out, "to", nil, "pat@counterparty.test")

	// Inbound: the SAME two parties, roles swapped. Without this the edge
	// derivation cannot tell a real two-way exchange from a hundred
	// unanswered sends, which is the quality bar the whole feature rests on.
	got := captureMail(t, e, owner, "p-in-1", connector.DirectionInbound, "pat@counterparty.test")
	in := readParticipants(t, e, got)
	if len(in) != 2 {
		t.Fatalf("an inbound mail recorded %d participants, want 2: %+v", len(in), in)
	}
	assertParticipant(t, in, "to", &owner, "")
	assertParticipant(t, in, "from", nil, "pat@counterparty.test")
}

func TestTheCounterpartyIsRecordedBeforeAnyPersonExists(t *testing.T) {
	e := integration.Setup(t)
	// The address arm is not a fallback, it is the honest answer: capture
	// decides whether to create a person AFTER this transaction commits, and
	// for a suppressed or deferred sender it never does. Dropping the party
	// until a record exists would lose the fact that they were in the
	// conversation at all.
	id := captureMail(t, e, e.Rep1, "p-ghost-1", connector.DirectionInbound, "nobody@stranger.test")
	for _, p := range readParticipants(t, e, id) {
		if p.address == "nobody@stranger.test" {
			if p.person != nil {
				t.Errorf("the counterparty already carries a person id; capture has not created one yet")
			}
			return
		}
	}
	t.Error("the counterparty was not recorded at all — an unresolved party must still be a participant")
}

func TestReplayingACapturedMailAddsNoParticipants(t *testing.T) {
	e := integration.Setup(t)
	owner := e.Rep1

	first := captureMail(t, e, owner, "p-replay-1", connector.DirectionOutbound, "pat@counterparty.test")
	before := readParticipants(t, e, first)

	// The same message again, exactly as the at-least-once sync loop delivers
	// it. A participant set that grows on every poll would inflate every
	// interaction count derived from it.
	second := captureMail(t, e, owner, "p-replay-1", connector.DirectionOutbound, "pat@counterparty.test")
	if second != first {
		t.Fatalf("replay landed a second activity (%s, then %s); the natural key is not holding", first, second)
	}
	after := readParticipants(t, e, first)
	if len(after) != len(before) {
		t.Errorf("replay grew the participant set from %d to %d rows: %+v", len(before), len(after), after)
	}
}

// assertParticipant finds the row for one role and checks which arm names it.
func assertParticipant(t *testing.T, rows []participantRow, role string, user *ids.UUID, address string) {
	t.Helper()
	for _, p := range rows {
		if p.role != role {
			continue
		}
		switch {
		case user != nil:
			if p.user == nil || *p.user != *user {
				t.Errorf("the %q participant is %v, want the mailbox owner %s", role, p.user, *user)
			}
		case address != "":
			if p.address != address {
				t.Errorf("the %q participant's address is %q, want %q", role, p.address, address)
			}
			if p.user != nil {
				t.Errorf("the %q participant carries a user id; the counterparty is not one of ours", role)
			}
		}
		return
	}
	t.Errorf("no participant in role %q: %+v", role, rows)
}

// The two deletes the FK actions exist to survive. Both used to fail outright:
// SET NULL on a user-only participant row leaves it naming nobody, which the
// identity CHECK refuses, and clearing a ghost's matched_person_id leaves
// match_status = 'confirmed', which the shape CHECK refuses. Neither failure was
// visible from the graph tests, because nothing in them ever deleted anybody.
func TestDeletingAPersonOrAUserIsNotBlockedByGraphRows(t *testing.T) {
	e := integration.Setup(t)
	ctx := context.Background()
	ws := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`

	var contact, activityID ids.UUID
	var doomed ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO person (workspace_id, full_name, owner_id, source, captured_by, visibility)
			VALUES (`+ws+`, 'Departing Contact', $1, 'manual', 'human:test', 'workspace')
			RETURNING id`, e.Rep1).Scan(&contact); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO app_user (workspace_id, email, display_name)
			VALUES (`+ws+`, 'doomed@example.test', 'Doomed Colleague')
			RETURNING id`).Scan(&doomed); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO activity (workspace_id, kind, subject, direction, occurred_at, source, captured_by)
			VALUES (`+ws+`, 'email', 'Letzte Mail', 'outbound', now(), 'manual', 'human:test')
			RETURNING id`).Scan(&activityID); err != nil {
			return err
		}
		// A user-ONLY participant row: no person arm, no address arm.
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity_participant (workspace_id, activity_id, user_id, role)
			VALUES (`+ws+`, $1, $2, 'from')`, activityID, doomed); err != nil {
			return err
		}
		// A CONFIRMED ghost pointing at the contact.
		_, err := tx.Exec(ctx, `
			INSERT INTO linkedin_connection
			  (workspace_id, owner_user_id, full_name, normalized_name, company_name,
			   normalized_company, matched_person_id, match_status, source)
			VALUES (`+ws+`, $1, 'Departing Contact', 'departing contact', 'Acme',
			        'acme', $2, 'confirmed', 'csv_export')`, e.Rep1, contact)
		return err
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM app_user WHERE id = $1`, doomed)
		return err
	}); err != nil {
		t.Errorf("deleting a colleague who was in one conversation failed: %v\n"+
			"an administrator removing an account cannot be blocked by a row "+
			"that only records who was on a thread", err)
	}

	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM person WHERE id = $1`, contact)
		return err
	}); err != nil {
		t.Errorf("deleting a person with a confirmed LinkedIn match failed: %v\n"+
			"this is the Art. 17 path — an erasure request that cannot complete "+
			"is a compliance failure, not an inconvenience", err)
	}

	var ghosts int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM linkedin_connection WHERE matched_person_id = $1`,
			contact).Scan(&ghosts)
	}); err != nil {
		t.Fatalf("counting ghosts: %v", err)
	}
	if ghosts != 0 {
		t.Errorf("the erased person still has %d LinkedIn ghost(s) pointing at them", ghosts)
	}
}

// A relink corrects ONE association, and must leave every other participant
// alone. Inferring the displaced person from "is a participant but no longer
// linked" was wrong twice over: a participant can name somebody who was never
// linked at all — capture stamps a counterparty whether or not a link exists —
// and that row would then be rewritten to name a contact who was never in the
// conversation.
func TestRelinkRepointsOnlyThePersonItDisplaced(t *testing.T) {
	e := integration.Setup(t)
	ctx := context.Background()
	ws := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`

	linked := e.SeedPerson(t, "Linked Contact", &e.Rep1)
	unlinked := e.SeedPerson(t, "Never Linked", &e.Rep1)
	corrected := e.SeedPerson(t, "The Real Contact", &e.Rep1)

	var activityID ids.ActivityID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO activity (workspace_id, kind, subject, direction, occurred_at, source, captured_by)
			VALUES (`+ws+`, 'email', 'Verwechslung', 'inbound', now(), 'gmail:m-9', 'connector:gmail')
			RETURNING id`).Scan(&activityID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
			VALUES (`+ws+`, $1, 'person', $2)`, activityID, linked); err != nil {
			return err
		}
		// Two participants: one matching the link, one that never had a link.
		_, err := tx.Exec(ctx, `
			INSERT INTO activity_participant (workspace_id, activity_id, person_id, role)
			VALUES (`+ws+`, $1, $2, 'from'), (`+ws+`, $1, $3, 'cc')`, activityID, linked, unlinked)
		return err
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	if _, err := e.Activities.RelinkActivity(e.Admin(), activityID, activities.RelinkActivityInput{
		EntityType: "person", EntityID: corrected, ReplaceExistingOfType: true,
	}); err != nil {
		t.Fatalf("relinking: %v", err)
	}

	got := map[ids.UUID]string{}
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT person_id, role FROM activity_participant WHERE activity_id = $1`, activityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pid ids.UUID
			var role string
			if err := rows.Scan(&pid, &role); err != nil {
				return err
			}
			got[pid] = role
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("reading participants back: %v", err)
	}

	if _, ok := got[corrected]; !ok {
		t.Error("the relink did not repoint the participant it displaced, so the " +
			"participants and the links now tell different stories about the same mail")
	}
	if _, ok := got[unlinked]; !ok {
		t.Error("the relink rewrote a participant it never displaced: that row named " +
			"somebody who was never linked, and now names a contact who was never " +
			"in the conversation")
	}
	if _, ok := got[linked]; ok {
		t.Error("the displaced contact is still a participant")
	}
}
