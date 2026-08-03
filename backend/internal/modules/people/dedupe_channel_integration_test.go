// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The three-lane exact ladder over a real migrated Postgres: a channel
// binding and an E.164 phone each resolve on their own key; the channel lane
// outranks email; a blocked identity still resolves (blocked_at is
// reachability, not identity); and when two lanes name different people the
// routing is deterministic, the rival is reported, and NOTHING is written
// onto the rival — preferring a lane routes, writing keys merges.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

const telegramProvider = "telegram"

// seedPerson creates a person carrying the given emails and phones — the
// incumbent each lane probes against.
func (e *dedupeEnv) seedPerson(ctx context.Context, t *testing.T, name string, emails, phones []string) ids.PersonID {
	t.Helper()
	in := CreatePersonInput{FullName: name, Source: "manual"}
	for i, addr := range emails {
		in.Emails = append(in.Emails, PersonEmailInput{Email: addr, EmailType: "work", IsPrimary: i == 0, Position: i + 1})
	}
	for i, phone := range phones {
		in.Phones = append(in.Phones, PersonPhoneInput{Phone: phone, PhoneType: "mobile", IsPrimary: i == 0, Position: i + 1})
	}
	person, err := e.store.CreatePerson(ctx, in)
	if err != nil {
		t.Fatalf("seed person %s: %v", name, err)
	}
	return ids.From[ids.PersonKind](ids.UUID(person.Id))
}

// bindIdentity binds one channel identity through the real write path, so the
// tests exercise the same insert-then-adopt the ingress will.
func (e *dedupeEnv) bindIdentity(ctx context.Context, t *testing.T, personID ids.PersonID, ci connector.ChannelIdentity) ids.PersonID {
	t.Helper()
	var bound ids.PersonID
	err := e.store.tx(ctx, func(tx pgx.Tx) (err error) {
		bound, err = ResolveOrCreateChannelIdentity(ctx, tx, personID, ci)
		return err
	})
	if err != nil {
		t.Fatalf("bind channel identity %s: %v", ci.ChannelUserID, err)
	}
	return bound
}

// blockIdentity is what my_chat_member does when the user blocks the bot: it
// sets reachability, and touches nothing about identity.
func (e *dedupeEnv) blockIdentity(ctx context.Context, t *testing.T, ci connector.ChannelIdentity) {
	t.Helper()
	err := e.store.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE person_channel_identity SET blocked_at = now()
			WHERE provider = $1 AND channel_user_id = $2 AND archived_at IS NULL`,
			ci.Provider, ci.ChannelUserID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			t.Errorf("blocking %s touched %d rows, want 1", ci.ChannelUserID, tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("block channel identity: %v", err)
	}
}

// assertNoChannelIdentityFor is the merge guard: a lane that lost the routing
// decision must not have gained the winner's key. If it ever does, two real
// humans have quietly become one record.
func assertNoChannelIdentityFor(ctx context.Context, t *testing.T, tx pgx.Tx, personID ids.PersonID) {
	t.Helper()
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM person_channel_identity WHERE person_id = $1`, personID).Scan(&n); err != nil {
		t.Fatalf("counting channel identities for %s: %v", personID, err)
	}
	if n != 0 {
		t.Fatalf("the rival %s carries %d channel identity rows; resolution must never write a key onto it", personID, n)
	}
}

func TestExactPersonByChannelIdentityMatchesOnProviderAndUserID(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	jane := e.seedPerson(ctx, t, "Jane Doe", []string{"jane@ci.test"}, nil)
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "770001", Username: "janedoe"}
	e.bindIdentity(ctx, t, jane, ci)

	// The binding is the key: neither the name nor an unknown address on the
	// candidate may change where an inbound message lands.
	r := e.dedupeInTx(ctx, t, PersonCandidate{
		FullName: "unrelated stranger", ChannelIdentities: []connector.ChannelIdentity{ci},
	})
	if r.Decision != DecisionExactCollision {
		t.Fatalf("decision = %s, want exact_collision", r.Decision)
	}
	if r.PersonID != jane {
		t.Fatalf("resolved %s, want the bound person %s", r.PersonID, jane)
	}
	if r.Conflict != nil {
		t.Fatalf("unexpected conflict %+v — only one lane carried a key", r.Conflict)
	}

	// A different channel user id on the same provider is a different human.
	other := e.dedupeInTx(ctx, t, PersonCandidate{
		FullName: "Jane Doe",
		ChannelIdentities: []connector.ChannelIdentity{
			{Provider: telegramProvider, ChannelUserID: "770002"},
		},
	})
	if other.PersonID == jane {
		t.Fatal("channel user 770002 resolved onto the person bound to 770001")
	}
}

func TestExactPersonByPhoneMatchesOnE164(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	sam := e.seedPerson(ctx, t, "Sam Reed", []string{"sam@ph.test"}, []string{"+4915100000001"})

	// The candidate arrives in a different notation of the same number; the
	// lane compares the E.164 form person_phone actually stores.
	r := e.dedupeInTx(ctx, t, PersonCandidate{
		FullName: "S. Reed", Phones: []string{"0049 151 000 000 01"},
	})
	if r.Decision != DecisionExactCollision {
		t.Fatalf("decision = %s, want exact_collision", r.Decision)
	}
	if r.PersonID != sam {
		t.Fatalf("resolved %s, want %s", r.PersonID, sam)
	}

	// A number that cannot be normalized is no key: it is dropped, and the
	// ladder falls through instead of matching something by accident.
	unusable := e.dedupeInTx(ctx, t, PersonCandidate{
		FullName: "Nobody Here", Phones: []string{"151-000-000-01"},
	})
	if unusable.Decision != DecisionNoMatch {
		t.Fatalf("decision = %s (person %s), want no_match for an un-normalizable number",
			unusable.Decision, unusable.PersonID)
	}
}

func TestLadderPrefersChannelIdentityOverEmail(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	bound := e.seedPerson(ctx, t, "Bound Person", []string{"bound@ladder.test"}, nil)
	byEmail := e.seedPerson(ctx, t, "Email Person", []string{"shared@ladder.test"}, nil)
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "770101"}
	e.bindIdentity(ctx, t, bound, ci)

	r := e.dedupeInTx(ctx, t, PersonCandidate{
		FullName:          "Bound Person",
		Emails:            []string{"shared@ladder.test"},
		ChannelIdentities: []connector.ChannelIdentity{ci},
	})
	if r.PersonID != bound {
		t.Fatalf("resolved %s, want the channel-bound person %s — an established binding outranks a shared address", r.PersonID, bound)
	}
	if r.Conflict == nil {
		t.Fatal("two lanes named different people and no conflict was reported")
	}
	if r.Conflict.RoutedLane != laneChannelIdentity || r.Conflict.RivalLane != laneEmail {
		t.Fatalf("conflict lanes = %s over %s, want %s over %s",
			r.Conflict.RoutedLane, r.Conflict.RivalLane, laneChannelIdentity, laneEmail)
	}
	if r.Conflict.Rival != byEmail {
		t.Fatalf("rival = %s, want the email lane's person %s", r.Conflict.Rival, byEmail)
	}
}

func TestBlockedIdentityStillResolves(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	returning := e.seedPerson(ctx, t, "Returning Customer", []string{"back@blocked.test"}, nil)
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "770201"}
	e.bindIdentity(ctx, t, returning, ci)
	e.blockIdentity(ctx, t, ci)

	// blocked_at is reachability, not identity. A lane that honoured it would
	// miss here and — with no email and no phone on the candidate — fork this
	// human into a second person the moment they unblock and write again.
	r := e.dedupeInTx(ctx, t, PersonCandidate{
		ChannelIdentities: []connector.ChannelIdentity{ci},
	})
	if r.Decision != DecisionExactCollision {
		t.Fatalf("decision = %s, want exact_collision — blocking must not unbind an identity", r.Decision)
	}
	if r.PersonID != returning {
		t.Fatalf("resolved %s, want %s", r.PersonID, returning)
	}
}

func TestLaneConflictRoutesDeterministicallyAndReportsRival(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	personA := e.seedPerson(ctx, t, "Person A", []string{"a@conflict.test"}, nil)
	personB := e.seedPerson(ctx, t, "Person B", []string{"b@conflict.test"}, []string{"+4915100000042"})
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "770301"}
	e.bindIdentity(ctx, t, personA, ci)

	candidate := PersonCandidate{
		FullName:          "Person B",
		Phones:            []string{"+4915100000042"},
		ChannelIdentities: []connector.ChannelIdentity{ci},
	}
	var r PersonResolution
	err := e.store.tx(ctx, func(tx pgx.Tx) (err error) {
		if r, err = DedupePerson(ctx, tx, candidate); err != nil {
			return err
		}
		// Asserted inside the resolver's own transaction: if resolution wrote
		// anything onto the rival, it is visible here and nowhere else.
		assertNoChannelIdentityFor(ctx, t, tx, personB)
		return nil
	})
	if err != nil {
		t.Fatalf("DedupePerson: %v", err)
	}
	if r.PersonID != personA {
		t.Fatalf("routed to %s, want %s — routing must be deterministic, never deferred", r.PersonID, personA)
	}
	if r.Conflict == nil {
		t.Fatal("the identity lane and the phone lane named different people; the conflict must be reported")
	}
	if r.Conflict.RoutedTo != personA || r.Conflict.Rival != personB {
		t.Fatalf("conflict = routed %s / rival %s, want routed %s / rival %s",
			r.Conflict.RoutedTo, r.Conflict.Rival, personA, personB)
	}
	if r.Conflict.RoutedLane != laneChannelIdentity || r.Conflict.RivalLane != lanePhone {
		t.Fatalf("conflict lanes = %s over %s, want %s over %s",
			r.Conflict.RoutedLane, r.Conflict.RivalLane, laneChannelIdentity, lanePhone)
	}
}
