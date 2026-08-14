// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Who a reply is addressed to, resolved from a real thread.
//
// The defect this is written against is the one an address resolver makes and
// a greeting resolver cannot: on an OUTBOUND message the `from` participant is
// US, so a rank that simply prefers `from` answers with our own address and
// mails the message straight back to the sender. The greeting beside it can
// afford that mistake — it produces a slightly wrong salutation. An address
// cannot.

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedReplyThread writes one activity with the participants a test names.
type replyParty struct {
	role    string
	address string
	person  *ids.UUID
	// ours marks a participant as one of this installation's own users, which
	// is what activity_participant.user_id records and what tells "the person
	// we are answering" apart from "the colleague who sent it".
	ours bool
}

func seedReplyThread(t *testing.T, e *Env, direction string, parties ...replyParty) ids.ActivityID {
	t.Helper()
	owner := OwnerConn(t)
	activity := SeedRow(t, owner, `
		INSERT INTO activity (id, workspace_id, kind, direction, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'email', '`+direction+`', 'Kickoff', now(), 'test', 'human:seed')`, e.WS)
	for _, p := range parties {
		var userID *ids.UUID
		if p.ours {
			id := e.Rep1
			userID = &id
		}
		e.WsExec(t, `
			INSERT INTO activity_participant (id, workspace_id, activity_id, role, person_id, user_id, address)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			ids.NewV7(), e.WS, activity, p.role, p.person, userID, p.address)
	}
	return ids.From[ids.ActivityKind](activity)
}

func TestAReplyIsAddressedToTheCounterpartyWhoWroteIn(t *testing.T) {
	e := Setup(t)
	anchor := seedReplyThread(t, e, "inbound",
		replyParty{role: "from", address: "anna@example.com"},
		replyParty{role: "to", address: "rep@ourcompany.test", ours: true},
	)

	got, err := e.Activities.ReplyAddressFor(e.Admin(), anchor)
	if err != nil {
		t.Fatalf("ReplyAddressFor → %v, want the counterparty's address", err)
	}
	if got != "anna@example.com" {
		t.Errorf("reply address = %q, want the sender we are answering", got)
	}
}

// The case a `from`-first rank gets wrong. On our own outbound message the
// sender is us, and the person to answer is the ADDRESSEE.
func TestAReplyToOurOwnOutboundGoesToTheAddresseeNotOurselves(t *testing.T) {
	e := Setup(t)
	anchor := seedReplyThread(t, e, "outbound",
		replyParty{role: "from", address: "rep@ourcompany.test", ours: true},
		replyParty{role: "to", address: "anna@example.com"},
	)

	got, err := e.Activities.ReplyAddressFor(e.Admin(), anchor)
	if err != nil {
		t.Fatalf("ReplyAddressFor → %v, want the addressee", err)
	}
	if got == "rep@ourcompany.test" {
		t.Fatal("the reply was addressed to our own sender — this message would have gone back to the person who wrote it")
	}
	if got != "anna@example.com" {
		t.Errorf("reply address = %q, want the counterparty %q", got, "anna@example.com")
	}
}

// A thread with nobody on it but us has no counterparty, and the honest answer
// is a refusal an operator can act on — not an empty string that reaches the
// send and arrives there as "no recipients", which reads as a different bug.
func TestAThreadWithNoCounterpartyRefusesRatherThanGuessing(t *testing.T) {
	e := Setup(t)
	anchor := seedReplyThread(t, e, "outbound",
		replyParty{role: "from", address: "rep@ourcompany.test", ours: true},
	)

	_, err := e.Activities.ReplyAddressFor(e.Admin(), anchor)
	var refusal *activities.NoReplyAddressError
	if !errors.As(err, &refusal) {
		t.Fatalf("ReplyAddressFor → %v, want NoReplyAddressError", err)
	}
	field, code, _ := refusal.FieldFault()
	if field != "to" || code != "no_reply_address" {
		t.Errorf("field fault = (%q, %q), want it to point at the addressee an operator must fix", field, code)
	}
}
