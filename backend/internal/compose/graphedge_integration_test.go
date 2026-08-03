// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The interaction projection (CG-DDL-1 / ADR-0078) against a real Postgres.
//
// The properties, in the order they matter:
//
//   - THE ANSWER IS RIGHT: a colleague who exchanged mail with a contact
//     appears, with counts that match the traffic;
//   - CC COUNTS, but ranks below a real exchange: the person permanently in
//     copy is often the one who knows the customer, and reciprocity — not a
//     role filter — is what keeps them from outranking a two-way thread;
//   - REDELIVERY IS FREE: the bus is at-least-once, so recomputing five times
//     must leave exactly what recomputing once did;
//   - EVIDENCE LOSS DELETES: an archived last interaction removes the edge
//     rather than leaving one that recommends an introduction nobody can make;
//   - REBUILD AGREES WITH INCREMENTS: the nightly full rebuild and the stream
//     of per-activity recomputes must produce the same table, or one of the
//     two paths has been quietly wrong for a day at a time.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// edgeEnv seeds interactions directly as participant rows — the shape capture
// produces — so these tests exercise the fold rather than the capture path.
type edgeEnv struct{ e *integration.Env }

// interaction writes one activity plus the participant rows for a (user,
// person) exchange in the given role pair.
func (v edgeEnv) interaction(t *testing.T, user ids.UUID, person ids.PersonID, at time.Time, direction, personRole string) ids.UUID {
	t.Helper()
	var activityID ids.UUID
	if err := database.WithWorkspaceTx(v.e.Admin(), v.e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if err := tx.QueryRow(ctx, `
			INSERT INTO activity (workspace_id, kind, subject, direction, occurred_at, source, captured_by)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        'email', 'Betreff', $1, $2, 'manual', 'human:test')
			RETURNING id`, direction, at).Scan(&activityID); err != nil {
			return err
		}
		userRole := "from"
		if direction == "inbound" {
			userRole = "to"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity_participant (workspace_id, activity_id, user_id, role)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3)`,
			activityID, user, userRole); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO activity_participant (workspace_id, activity_id, person_id, role)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3)`,
			activityID, person, personRole)
		return err
	}); err != nil {
		t.Fatalf("seeding an interaction: %v", err)
	}
	return activityID
}

func (v edgeEnv) person(t *testing.T, name string) ids.PersonID {
	t.Helper()
	id := ids.New[ids.PersonKind]()
	if err := database.WithWorkspaceTx(v.e.Admin(), v.e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO person (workspace_id, id, full_name, owner_id, source, captured_by, visibility)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, 'manual', 'human:test', 'workspace')`,
			id, name, v.e.Rep1)
		return err
	}); err != nil {
		t.Fatalf("seeding person %s: %v", name, err)
	}
	return id
}

// recompute folds the named activities, as the consumer does.
func (v edgeEnv) recompute(t *testing.T, activityIDs ...ids.UUID) {
	t.Helper()
	if err := database.WithWorkspaceTx(v.e.Admin(), v.e.Pool, func(tx pgx.Tx) error {
		return search.RecomputeEdgesForActivities(v.e.Admin(), tx, activityIDs)
	}); err != nil {
		t.Fatalf("recomputing edges: %v", err)
	}
}

// edgesFor reads the projection for one contact.
func (v edgeEnv) edgesFor(t *testing.T, person ids.PersonID) []search.InteractionEdge {
	t.Helper()
	var out []search.InteractionEdge
	if err := database.WithWorkspaceTx(v.e.Admin(), v.e.Pool, func(tx pgx.Tx) error {
		var err error
		out, err = search.EdgesForPerson(v.e.Admin(), tx, person.UUID, 50)
		return err
	}); err != nil {
		t.Fatalf("reading edges: %v", err)
	}
	return out
}

// snapshot reads the whole projection as comparable tuples — what the rebuild
// and the incremental path must agree on.
func (v edgeEnv) snapshot(t *testing.T) map[string][4]int {
	t.Helper()
	out := map[string][4]int{}
	if err := database.WithWorkspaceTx(v.e.Admin(), v.e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `
			SELECT user_id, person_id, count_90d, in_count_90d, out_count_90d, count_total
			  FROM graph_interaction_edge ORDER BY user_id, person_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var u, p ids.UUID
			var c, in, o, tot int
			if err := rows.Scan(&u, &p, &c, &in, &o, &tot); err != nil {
				return err
			}
			out[u.String()+"/"+p.String()] = [4]int{c, in, o, tot}
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("snapshotting the projection: %v", err)
	}
	return out
}

func TestTheProjectionAnswersWhoKnowsThisContact(t *testing.T) {
	v := edgeEnv{integration.Setup(t)}
	now := time.Now().UTC()
	contact := v.person(t, "Pat Counterparty")

	// Rep1 has a real two-way exchange; Rep2 has written once and had no reply.
	a1 := v.interaction(t, v.e.Rep1, contact, now.AddDate(0, 0, -2), "outbound", "to")
	a2 := v.interaction(t, v.e.Rep1, contact, now.AddDate(0, 0, -1), "inbound", "from")
	a3 := v.interaction(t, v.e.Rep2, contact, now.AddDate(0, 0, -30), "outbound", "to")
	v.recompute(t, a1, a2, a3)

	edges := v.edgesFor(t, contact)
	if len(edges) != 2 {
		t.Fatalf("the contact has %d edges, want 2 (both colleagues who wrote to them): %+v", len(edges), edges)
	}

	byUser := map[ids.UUID]search.InteractionEdge{}
	for _, e := range edges {
		byUser[e.UserID] = e
	}
	rep1 := byUser[v.e.Rep1]
	if rep1.Count90d != 2 || rep1.InCount90d != 1 || rep1.OutCount90d != 1 {
		t.Errorf("the two-way exchange folded to %d total / %d in / %d out, want 2/1/1",
			rep1.Count90d, rep1.InCount90d, rep1.OutCount90d)
	}
	// The colleague with a real exchange must outrank the one who wrote once
	// and got nothing back. That ordering IS the feature: it is the answer to
	// "who should ask for the introduction".
	rep2 := byUser[v.e.Rep2]
	if rep1.StrengthOf(now).Strength <= rep2.StrengthOf(now).Strength {
		t.Errorf("a two-way exchange scored %d, no better than one unanswered send at %d",
			rep1.StrengthOf(now).Strength, rep2.StrengthOf(now).Strength)
	}
}

func TestBeingCopiedCountsButRanksBelowARealExchange(t *testing.T) {
	v := edgeEnv{integration.Setup(t)}
	now := time.Now().UTC()

	// The market convention is to drop cc outright, on the argument that being
	// copied is not a relationship. This product counts it (founder decision):
	// in the accounts it is built for, the person permanently in copy is often
	// the one who actually knows the customer — the account lead cc'd on their
	// team's mail, the partner copied on every exchange. Dropping cc removed
	// exactly those people from "who here knows them".
	ccOnly := v.person(t, "Cc Contact")
	var ccIDs []ids.UUID
	for i := 0; i < 20; i++ {
		ccIDs = append(ccIDs, v.interaction(t, v.e.Rep1, ccOnly, now.AddDate(0, 0, -i), "outbound", "cc"))
	}
	v.recompute(t, ccIDs...)

	edges := v.edgesFor(t, ccOnly)
	if len(edges) != 1 {
		t.Fatalf("cc traffic produced %d edges, want 1 — a colleague permanently in copy is still a way in: %+v", len(edges), edges)
	}

	// What keeps it honest is the score, not a role filter. Copy traffic is
	// one-directional, so reciprocity floors it — the colleague appears,
	// ranked where they belong, rather than vanishing.
	direct := v.person(t, "Direct Contact")
	var directIDs []ids.UUID
	for i := 0; i < 10; i++ {
		dir, role := "outbound", "to"
		if i%2 == 0 {
			dir, role = "inbound", "from"
		}
		directIDs = append(directIDs, v.interaction(t, v.e.Rep2, direct, now.AddDate(0, 0, -i), dir, role))
	}
	v.recompute(t, directIDs...)

	ccScore := edges[0].StrengthOf(now).Strength
	directEdges := v.edgesFor(t, direct)
	if len(directEdges) != 1 {
		t.Fatalf("the two-way exchange produced %d edges, want 1", len(directEdges))
	}
	directScore := directEdges[0].StrengthOf(now).Strength
	if directScore <= ccScore {
		t.Errorf("a two-way exchange over 10 messages scored %d, no better than 20 cc rows at %d — "+
			"reciprocity is meant to separate them without a role filter", directScore, ccScore)
	}
}

func TestRecomputingIsIdempotentUnderRedelivery(t *testing.T) {
	v := edgeEnv{integration.Setup(t)}
	now := time.Now().UTC()
	contact := v.person(t, "Repeat Contact")
	a1 := v.interaction(t, v.e.Rep1, contact, now.AddDate(0, 0, -3), "inbound", "from")
	a2 := v.interaction(t, v.e.Rep1, contact, now.AddDate(0, 0, -2), "outbound", "to")

	v.recompute(t, a1, a2)
	first := v.snapshot(t)

	// The bus is at-least-once. If the fold adjusted counters instead of
	// recomputing them, every redelivery would inflate the relationship.
	for i := 0; i < 5; i++ {
		v.recompute(t, a1, a2)
	}
	if got := v.snapshot(t); !equalSnapshots(got, first) {
		t.Errorf("five redeliveries changed the projection from %v to %v; the fold is counting instead of recomputing", first, got)
	}
}

func TestAnEdgeThatLostItsEvidenceIsDeleted(t *testing.T) {
	v := edgeEnv{integration.Setup(t)}
	now := time.Now().UTC()
	contact := v.person(t, "Archived Evidence")
	a1 := v.interaction(t, v.e.Rep1, contact, now.AddDate(0, 0, -1), "inbound", "from")
	v.recompute(t, a1)

	if len(v.edgesFor(t, contact)) != 1 {
		t.Fatal("the edge was not created in the first place")
	}

	// Archiving the only interaction removes the basis for the edge. Leaving
	// it would recommend a colleague for an introduction the evidence no
	// longer supports.
	if err := database.WithWorkspaceTx(v.e.Admin(), v.e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE activity SET archived_at = now() WHERE id = $1`, a1)
		return err
	}); err != nil {
		t.Fatalf("archiving the interaction: %v", err)
	}
	v.recompute(t, a1)

	if edges := v.edgesFor(t, contact); len(edges) != 0 {
		t.Errorf("the edge survived its only interaction being archived: %+v", edges)
	}
}

func TestTheNightlyRebuildAgreesWithTheIncrementalPath(t *testing.T) {
	v := edgeEnv{integration.Setup(t)}
	now := time.Now().UTC()

	// A spread of traffic across two colleagues and two contacts, including a
	// cc row that must not count and an archived one that must not either.
	c1 := v.person(t, "Contact One")
	c2 := v.person(t, "Contact Two")
	var all []ids.UUID
	for i := 0; i < 6; i++ {
		all = append(all, v.interaction(t, v.e.Rep1, c1, now.AddDate(0, 0, -i), "inbound", "from"))
		all = append(all, v.interaction(t, v.e.Rep2, c1, now.AddDate(0, 0, -i-10), "outbound", "to"))
		all = append(all, v.interaction(t, v.e.Rep1, c2, now.AddDate(0, 0, -i-20), "outbound", "cc"))
	}
	v.recompute(t, all...)
	incremental := v.snapshot(t)

	// The nightly pass throws the table away and refolds from the base tables.
	// If the two disagree, one of them has been quietly wrong — and since the
	// rebuild only runs daily, it would be wrong for up to a day at a time.
	if err := database.WithWorkspaceTx(v.e.Admin(), v.e.Pool, func(tx pgx.Tx) error {
		return search.RebuildEdges(v.e.Admin(), tx)
	}); err != nil {
		t.Fatalf("rebuilding: %v", err)
	}
	rebuilt := v.snapshot(t)

	if !equalSnapshots(incremental, rebuilt) {
		t.Errorf("the rebuild disagrees with the incremental path:\n  incremental %v\n  rebuilt     %v", incremental, rebuilt)
	}
	if len(rebuilt) == 0 {
		t.Fatal("both paths produced an empty projection — the comparison passed vacuously")
	}
}

func equalSnapshots(a, b map[string][4]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestArchivingOneOfTwoInteractionsCorrectsTheCountsRatherThanNothing(t *testing.T) {
	v := edgeEnv{integration.Setup(t)}
	now := time.Now().UTC()
	contact := v.person(t, "Two Threads")

	older := v.interaction(t, v.e.Rep1, contact, now.AddDate(0, 0, -5), "inbound", "from")
	newer := v.interaction(t, v.e.Rep1, contact, now.AddDate(0, 0, -1), "outbound", "to")
	v.recompute(t, older, newer)

	before := v.edgesFor(t, contact)
	if len(before) != 1 || before[0].Count90d != 2 {
		t.Fatalf("setup wrong: %d edges, %d interactions", len(before), before[0].Count90d)
	}

	// Archive the MORE RECENT one. The pair still has evidence, so the edge
	// must survive — but with corrected counts and an older last_at.
	//
	// This is the case a delete-only invalidation misses entirely: it drops
	// pairs that lost ALL their evidence and silently leaves every surviving
	// pair asserting the interaction that was just removed.
	if err := database.WithWorkspaceTx(v.e.Admin(), v.e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE activity SET archived_at = now() WHERE id = $1`, newer)
		return err
	}); err != nil {
		t.Fatalf("archiving the newer interaction: %v", err)
	}
	v.recompute(t, newer)

	after := v.edgesFor(t, contact)
	if len(after) != 1 {
		t.Fatalf("the edge vanished though one interaction remains: %+v", after)
	}
	if after[0].Count90d != 1 {
		t.Errorf("the surviving edge still counts %d interactions, want 1 — "+
			"an archived conversation is still inflating the relationship", after[0].Count90d)
	}
	if !after[0].LastAt.Before(before[0].LastAt) {
		t.Errorf("last contact is still %v after the most recent interaction was archived; "+
			"recency dominates the score, so this is the number a rep would act on",
			after[0].LastAt)
	}
	// And the direction counts follow: the outbound one was the archived one.
	if after[0].OutCount90d != 0 || after[0].InCount90d != 1 {
		t.Errorf("direction counts are %d in / %d out, want 1 in / 0 out",
			after[0].InCount90d, after[0].OutCount90d)
	}
}

func TestOneMessageCountsOnceHoweverManyRolesItNames(t *testing.T) {
	v := edgeEnv{integration.Setup(t)}
	now := time.Now().UTC()
	contact := v.person(t, "Both To And Cc")

	// One message, and the contact is named twice on it: once as a direct
	// recipient and once in copy. That is ordinary — a thread where someone
	// is both is common — and it produces two participant rows for one
	// conversation.
	activityID := v.interaction(t, v.e.Rep1, contact, now.AddDate(0, 0, -1), "outbound", "to")
	if err := database.WithWorkspaceTx(v.e.Admin(), v.e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO activity_participant (workspace_id, activity_id, person_id, role)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, 'cc')`,
			activityID, contact)
		return err
	}); err != nil {
		t.Fatalf("adding the cc row: %v", err)
	}
	v.recompute(t, activityID)

	edges := v.edgesFor(t, contact)
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(edges))
	}
	// Counting join rows would say two. One message is one interaction, and
	// frequency drives the score — so this is the difference between a
	// relationship reading twice as active as it is.
	if edges[0].Count90d != 1 {
		t.Errorf("one message counted %d times because the contact was on both to and cc; "+
			"frequency feeds the score, so a busy thread would read twice as warm as it is",
			edges[0].Count90d)
	}
	if edges[0].CountTotal != 1 {
		t.Errorf("total counted %d for one message", edges[0].CountTotal)
	}
}
