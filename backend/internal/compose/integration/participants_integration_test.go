// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Who else was in the conversation (ACT-DDL-3 / ADR-0078), against a real
// database.
//
// The load-bearing claim is the one about TRUST. A recipient list on an
// inbound message is written by whoever sent it — nothing authenticates it,
// and DKIM does not cover a Cc line the sender chose. Binding a colleague's
// user_id from one would let an outsider manufacture an interaction edge, and
// the local graph would then name that colleague as the warmest route to the
// sender's own contact.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// seedInteraction records one captured email to hang participants off.
func seedInteraction(t *testing.T, e *Env) ids.ActivityID {
	t.Helper()
	id := SeedRow(t, OwnerConn(t), `INSERT INTO activity
		(id, workspace_id, kind, subject, occurred_at, direction, source, captured_by)
		VALUES ($1, $2, 'email', 'Q3 terms', '2026-08-01T09:00:00Z', 'inbound', 'gmail', 'connector:gmail')`, e.WS)
	return ids.From[ids.ActivityKind](id)
}

// stampParties runs the real capture write inside a workspace transaction.
func stampParties(t *testing.T, e *Env, activity ids.ActivityID, trusted bool, parties ...connector.MessageParticipant) {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return capture.StampFurtherParticipants(ctx, tx, activity, "email", trusted, parties)
	}); err != nil {
		t.Fatalf("StampFurtherParticipants: %v", err)
	}
}

// participantRow reads back one stamped party.
func participantRow(t *testing.T, activity ids.ActivityID, address string) (userID, personID *ids.UUID, found bool) {
	t.Helper()
	err := OwnerConn(t).QueryRow(context.Background(), `
		SELECT user_id, person_id FROM activity_participant
		 WHERE activity_id = $1 AND address = $2`, activity, address).Scan(&userID, &personID)
	if err == pgx.ErrNoRows {
		return nil, nil, false
	}
	if err != nil {
		t.Fatalf("reading the participant row: %v", err)
	}
	return userID, personID, true
}

// The forgery gate. An outsider mails a synced mailbox with a colleague on the
// Cc line; the address is kept as a party to the conversation, but it must not
// become that colleague's user_id, because the interaction graph is keyed on
// exactly that.
func TestAnUntrustedHeaderNeverBindsAColleaguesUserID(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	activity := seedInteraction(t, e)

	var colleagueEmail string
	if err := owner.QueryRow(context.Background(),
		`SELECT email FROM app_user WHERE id = $1`, e.Rep2).Scan(&colleagueEmail); err != nil {
		t.Fatalf("reading the colleague's address: %v", err)
	}

	stampParties(t, e, activity, false,
		connector.MessageParticipant{Email: colleagueEmail, Role: connector.ParticipantRoleCC})

	userID, _, found := participantRow(t, activity, colleagueEmail)
	if !found {
		t.Fatal("the copied address was dropped entirely; an unresolved party is still a fact about the conversation")
	}
	if userID != nil {
		t.Error("a forgeable Cc line bound a colleague's user_id — the graph would then name them as a warm route")
	}
}

// The same header on a message the provider attests OUR owner sent is ours to
// trust: the Cc line is what our user typed.
func TestAnAttestedHeaderBindsTheColleague(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	activity := seedInteraction(t, e)

	var colleagueEmail string
	if err := owner.QueryRow(context.Background(),
		`SELECT email FROM app_user WHERE id = $1`, e.Rep2).Scan(&colleagueEmail); err != nil {
		t.Fatalf("reading the colleague's address: %v", err)
	}

	stampParties(t, e, activity, true,
		connector.MessageParticipant{Email: colleagueEmail, Role: connector.ParticipantRoleCC})

	userID, _, found := participantRow(t, activity, colleagueEmail)
	if !found {
		t.Fatal("the copied colleague was not recorded at all")
	}
	if userID == nil || *userID != e.Rep2 {
		t.Error("our own sent message's recipient list did not bind the colleague it named")
	}
}

// A known contact resolves on either arm: their identity comes from
// person_email, not from who wrote the header.
func TestAKnownContactResolvesFromAnUntrustedHeaderToo(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	activity := seedInteraction(t, e)
	person := e.SeedPerson(t, "Sam Second", &e.Rep1)
	SeedRow(t, owner, `INSERT INTO person_email (id, workspace_id, person_id, email, source, captured_by)
		VALUES ($1, $2, '`+person.String()+`', 'sam@target.example', 'manual', 'human:x')`, e.WS)

	stampParties(t, e, activity, false,
		connector.MessageParticipant{Email: "sam@target.example", Role: connector.ParticipantRoleCC})

	_, personID, found := participantRow(t, activity, "sam@target.example")
	if !found {
		t.Fatal("the copied contact was not recorded")
	}
	if personID == nil || *personID != person {
		t.Error("a known contact did not resolve; who they are is a fact about them, not about the header")
	}
}

// Capture's sync loop is at-least-once, so a replayed message must add
// nothing. The uniqueness index is the correctness layer, not the dedupe.
func TestStampingTheSamePartiesTwiceAddsNothing(t *testing.T) {
	e := Setup(t)
	activity := seedInteraction(t, e)
	parties := []connector.MessageParticipant{
		{Email: "sam@target.example", Role: connector.ParticipantRoleCC},
		{Email: "kim@target.example", Role: connector.ParticipantRoleTo},
	}
	stampParties(t, e, activity, false, parties...)
	stampParties(t, e, activity, false, parties...)

	var rows int
	if err := OwnerConn(t).QueryRow(context.Background(),
		`SELECT count(*) FROM activity_participant WHERE activity_id = $1`, activity).Scan(&rows); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if rows != len(parties) {
		t.Errorf("a replayed capture left %d participant rows, want %d", rows, len(parties))
	}
}

// A kind that is not an interaction writes no participants however many the
// header names — a note and a task are not conversations, and the edge fold
// must not see them.
func TestANonInteractionKindStampsNoParties(t *testing.T) {
	e := Setup(t)
	note := ids.From[ids.ActivityKind](SeedRow(t, OwnerConn(t), `INSERT INTO activity
		(id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'note', 'thinking', '2026-08-01T09:00:00Z', 'manual', 'human:x')`, e.WS))

	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return capture.StampFurtherParticipants(ctx, tx, note, "note", true,
			[]connector.MessageParticipant{{Email: "sam@target.example", Role: connector.ParticipantRoleCC}})
	}); err != nil {
		t.Fatalf("StampFurtherParticipants: %v", err)
	}

	var rows int
	if err := OwnerConn(t).QueryRow(context.Background(),
		`SELECT count(*) FROM activity_participant WHERE activity_id = $1`, note).Scan(&rows); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if rows != 0 {
		t.Errorf("a note stamped %d participants; it is a record of thinking, not a conversation", rows)
	}
}
