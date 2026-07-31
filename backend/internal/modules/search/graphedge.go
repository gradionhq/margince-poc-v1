// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// The user↔contact interaction edge (CG-DDL-1 / ADR-0078): which of our people
// interacts with which contact, how much, how recently.
//
// It is a PROJECTION. Every row is folded from activity_participant rows and
// holds no fact of its own, so it can be thrown away and rebuilt at any time —
// which is exactly the corruption remedy, and why it carries no audit or
// outbox row of its own.
//
// The maintenance rule that matters: RECOMPUTE, never increment. The event bus
// is at-least-once, so an increment would double-count on redelivery; and
// merge, archive and erasure all correct history BACKWARDS, which an increment
// cannot express at all. Recomputing a pair from the base tables is idempotent
// by construction — the same statement run five times leaves the same row —
// and that property is worth more than the writes it costs.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/relstrength"
)

// InteractionEdge is one colleague's relationship with one contact, as the
// projection stores it — counted facts and exact moments, no score. The score
// is computed at read by StrengthOf, because a stored decayed number is wrong
// the moment the clock moves.
type InteractionEdge struct {
	UserID   ids.UUID
	PersonID ids.UUID

	LastAt        time.Time
	LastInboundAt *time.Time
	LastOutbound  *time.Time

	Count90d    int
	InCount90d  int
	OutCount90d int
	CountTotal  int
}

// StrengthOf scores the edge at the given instant (PO-F-3b), through the same
// arithmetic the workspace-wide score uses.
func (e InteractionEdge) StrengthOf(now time.Time) relstrength.Score {
	last := e.LastAt
	return relstrength.Compute(relstrength.Inputs{
		LastInteraction: &last,
		Count90d:        e.Count90d,
		Inbound90d:      e.InCount90d,
		Outbound90d:     e.OutCount90d,
	}, now)
}

// interactionRoles are the participant roles that make an edge — every role
// there is, cc included (founder decision, 2026-07-31).
//
// The market convention is to exclude cc, on the argument that being copied is
// not a relationship. It was excluded here first and then reversed, and the
// reason is worth keeping: in the accounts this product is built for, the
// person who is always in copy on the thread is frequently the one who
// actually knows the customer — the account lead cc'd on their team's
// correspondence, the partner copied on every exchange. Dropping cc did not
// remove noise so much as remove exactly those people from the answer to "who
// here knows them".
//
// The score already handles the difference honestly without a role filter: a
// colleague who is only ever copied has one-directional traffic, so the
// reciprocity term floors them well below someone in a real exchange. They
// appear, ranked where they belong, instead of vanishing.
const interactionRoles = `('from','to','cc','attendee','organizer')`

// RecomputeEdgesForActivities re-folds every (user, person) pair reachable
// from the named activities, from the base tables.
//
// It is the ONE maintenance entry point. A consumer that learns an activity
// changed does not know which pairs that touched, so it hands over the
// activity and this resolves the pairs itself — including, on a relink, the
// pair the activity used to belong to, which is the pair the caller could not
// have named.
//
// Deleting is as important as writing. A pair whose last qualifying
// interaction was just archived must lose its row, not keep a stale one: an
// edge that outlives its evidence is a colleague being recommended for an
// introduction they can no longer make.
func RecomputeEdgesForActivities(ctx context.Context, tx pgx.Tx, activityIDs []ids.UUID) error {
	if len(activityIDs) == 0 {
		return nil
	}
	// The pairs the activities touch — BEFORE the recompute, so a pair whose
	// rows have all gone is still named and can be deleted below.
	pairs, err := affectedPairs(ctx, tx, activityIDs)
	if err != nil {
		return err
	}
	return recomputePairs(ctx, tx, pairs)
}

// pair is one (user, person) key.
type pair struct {
	user   ids.UUID
	person ids.UUID
}

// affectedPairs resolves which edges the named activities can affect: every
// (user, person) combination appearing on their participant rows, plus every
// pair that currently has a row citing one of those activities.
func affectedPairs(ctx context.Context, tx pgx.Tx, activityIDs []ids.UUID) ([]pair, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT u.user_id, p.person_id
		  FROM activity_participant u
		  JOIN activity_participant p ON p.activity_id = u.activity_id
		 WHERE u.activity_id = ANY($1)
		   AND u.user_id IS NOT NULL
		   AND p.person_id IS NOT NULL`, activityIDs)
	if err != nil {
		return nil, fmt.Errorf("search: resolving the pairs an activity touches: %w", err)
	}
	defer rows.Close()
	var out []pair
	for rows.Next() {
		var pr pair
		if err := rows.Scan(&pr.user, &pr.person); err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

// RecomputeEdgesForPerson re-folds every edge touching one contact — the
// handler for a merge, an archive or a restore, where the person changed and
// no single activity did.
func RecomputeEdgesForPerson(ctx context.Context, tx pgx.Tx, personID ids.UUID) error {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT u.user_id
		  FROM activity_participant p
		  JOIN activity_participant u ON u.activity_id = p.activity_id
		 WHERE p.person_id = $1 AND u.user_id IS NOT NULL
		 UNION
		SELECT user_id FROM graph_interaction_edge WHERE person_id = $1`, personID)
	if err != nil {
		return fmt.Errorf("search: resolving the colleagues who know a contact: %w", err)
	}
	defer rows.Close()
	var pairs []pair
	for rows.Next() {
		var u ids.UUID
		if err := rows.Scan(&u); err != nil {
			return err
		}
		pairs = append(pairs, pair{user: u, person: personID})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return recomputePairs(ctx, tx, pairs)
}

// DropEdgesForPerson removes every edge to one contact outright — the erasure
// and merge-source handler. The projection must not be the one place a
// deleted person's correspondence pattern survives.
func DropEdgesForPerson(ctx context.Context, tx pgx.Tx, personID ids.UUID) error {
	_, err := tx.Exec(ctx, `DELETE FROM graph_interaction_edge WHERE person_id = $1`, personID)
	if err != nil {
		return fmt.Errorf("search: dropping a contact's interaction edges: %w", err)
	}
	return nil
}

// recomputePairs re-folds the named pairs from the base tables in one
// statement, and deletes the ones that no longer qualify.
//
// Written as one INSERT … ON CONFLICT DO UPDATE plus one DELETE rather than a
// read-modify-write: the fold has to be atomic with respect to concurrent
// capture, and a Go-side loop would leave a window in which a pair is
// half-updated.
func recomputePairs(ctx context.Context, tx pgx.Tx, pairs []pair) error {
	if len(pairs) == 0 {
		return nil
	}
	users := make([]ids.UUID, 0, len(pairs))
	people := make([]ids.UUID, 0, len(pairs))
	for _, p := range pairs {
		users = append(users, p.user)
		people = append(people, p.person)
	}
	window := fmt.Sprintf("now() - interval '%d days'", relstrength.WindowDays)

	if _, err := tx.Exec(ctx, `
		WITH target AS (
		    SELECT DISTINCT user_id, person_id
		      FROM unnest($1::uuid[], $2::uuid[]) AS t(user_id, person_id)
		),
		folded AS (
		    SELECT t.user_id, t.person_id,
		           max(a.occurred_at) AS last_at,
		           max(a.occurred_at) FILTER (WHERE a.direction = 'inbound')  AS last_inbound_at,
		           max(a.occurred_at) FILTER (WHERE a.direction = 'outbound') AS last_outbound_at,
		           count(*) FILTER (WHERE a.occurred_at >= `+window+`) AS count_90d,
		           count(*) FILTER (WHERE a.occurred_at >= `+window+` AND a.direction = 'inbound')  AS in_90d,
		           count(*) FILTER (WHERE a.occurred_at >= `+window+` AND a.direction = 'outbound') AS out_90d,
		           count(*) AS count_total
		      FROM target t
		      JOIN activity_participant up
		        ON up.user_id = t.user_id AND up.role IN `+interactionRoles+`
		      JOIN activity_participant pp
		        ON pp.activity_id = up.activity_id
		       AND pp.person_id = t.person_id AND pp.role IN `+interactionRoles+`
		      JOIN activity a
		        ON a.id = up.activity_id AND a.archived_at IS NULL
		     GROUP BY t.user_id, t.person_id
		)
		INSERT INTO graph_interaction_edge AS e
		    (workspace_id, user_id, person_id, last_at, last_inbound_at, last_outbound_at,
		     count_90d, in_count_90d, out_count_90d, count_total, computed_at)
		SELECT NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		       f.user_id, f.person_id, f.last_at, f.last_inbound_at, f.last_outbound_at,
		       f.count_90d, f.in_90d, f.out_90d, f.count_total, now()
		  FROM folded f
		ON CONFLICT (workspace_id, user_id, person_id) DO UPDATE SET
		    last_at          = EXCLUDED.last_at,
		    last_inbound_at  = EXCLUDED.last_inbound_at,
		    last_outbound_at = EXCLUDED.last_outbound_at,
		    count_90d        = EXCLUDED.count_90d,
		    in_count_90d     = EXCLUDED.in_count_90d,
		    out_count_90d    = EXCLUDED.out_count_90d,
		    count_total      = EXCLUDED.count_total,
		    computed_at      = EXCLUDED.computed_at`,
		users, people); err != nil {
		return fmt.Errorf("search: recomputing interaction edges: %w", err)
	}

	// The pairs that no longer have a single qualifying interaction lose their
	// row. Without this an archived last message leaves an edge recommending a
	// colleague for an introduction the evidence no longer supports.
	if _, err := tx.Exec(ctx, `
		DELETE FROM graph_interaction_edge e
		 USING unnest($1::uuid[], $2::uuid[]) AS t(user_id, person_id)
		 WHERE e.user_id = t.user_id AND e.person_id = t.person_id
		   AND NOT EXISTS (
		       SELECT 1
		         FROM activity_participant up
		         JOIN activity_participant pp ON pp.activity_id = up.activity_id
		         JOIN activity a ON a.id = up.activity_id AND a.archived_at IS NULL
		        WHERE up.user_id = t.user_id AND up.role IN `+interactionRoles+`
		          AND pp.person_id = t.person_id AND pp.role IN `+interactionRoles+`)`,
		users, people); err != nil {
		return fmt.Errorf("search: pruning interaction edges that lost their evidence: %w", err)
	}
	return nil
}

// EdgesForPerson answers "who on our team knows this contact", warmest first.
//
// Two gates, and both are load-bearing. The caller must be able to read the
// PERSON — capture privacy means an unpromoted contact is nobody's business
// but their importer's, and an edge list would otherwise disclose both that
// the contact exists and who talks to them. And the colleagues named are
// filtered to live members, so a departed employee stops being recommended
// without the projection needing to be rewritten when they leave.
func EdgesForPerson(ctx context.Context, tx pgx.Tx, personID ids.UUID, limit int) ([]InteractionEdge, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return nil, err
	}
	if err := auth.EnsureVisible(ctx, tx, "person", personID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT e.user_id, e.person_id, e.last_at, e.last_inbound_at, e.last_outbound_at,
		       e.count_90d, e.in_count_90d, e.out_count_90d, e.count_total
		  FROM graph_interaction_edge e
		  JOIN app_user u ON u.id = e.user_id AND u.archived_at IS NULL
		 WHERE e.person_id = $1
		 ORDER BY e.last_at DESC, e.user_id
		 LIMIT $2`, personID, limit)
	if err != nil {
		return nil, fmt.Errorf("search: reading who knows a contact: %w", err)
	}
	defer rows.Close()
	return scanEdges(rows)
}

// EdgesForPeople answers the same question for a whole contact set in one
// pass — what a company page needs, where asking per contact would open one
// query per row and read a different instant for each.
//
// It does NOT probe each person's visibility: the caller assembled the set
// from its own row-scoped read, and re-probing here would be a second
// enforcement of the same rule that could drift from the first. Callers that
// take a person id from a request use EdgesForPerson.
func EdgesForPeople(ctx context.Context, tx pgx.Tx, people []ids.UUID) ([]InteractionEdge, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return nil, err
	}
	if len(people) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT e.user_id, e.person_id, e.last_at, e.last_inbound_at, e.last_outbound_at,
		       e.count_90d, e.in_count_90d, e.out_count_90d, e.count_total
		  FROM graph_interaction_edge e
		  JOIN app_user u ON u.id = e.user_id AND u.archived_at IS NULL
		 WHERE e.person_id = ANY($1)
		 ORDER BY e.person_id, e.last_at DESC, e.user_id`, people)
	if err != nil {
		return nil, fmt.Errorf("search: reading who knows a contact set: %w", err)
	}
	defer rows.Close()
	return scanEdges(rows)
}

func scanEdges(rows pgx.Rows) ([]InteractionEdge, error) {
	var out []InteractionEdge
	for rows.Next() {
		var e InteractionEdge
		if err := rows.Scan(&e.UserID, &e.PersonID, &e.LastAt, &e.LastInboundAt, &e.LastOutbound,
			&e.Count90d, &e.InCount90d, &e.OutCount90d, &e.CountTotal); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// RebuildEdges re-folds the WHOLE projection for the bound workspace from the
// base tables, and drops anything the base tables no longer support.
//
// It is the corruption remedy, and it is why the projection can afford to
// carry no audit trail: whatever state the incremental path leaves the table
// in, this restores the only correct one. The nightly reconcile runs it for a
// second reason too — the 90-day window counts go stale purely by the passage
// of time, with no event to trigger a recompute, so something has to re-true
// them on a clock.
//
// It is also the determinism fixture: a rebuild and a stream of incremental
// recomputes over the same history must agree, and a test that says so is
// what keeps the two paths from drifting apart.
func RebuildEdges(ctx context.Context, tx pgx.Tx) error {
	window := fmt.Sprintf("now() - interval '%d days'", relstrength.WindowDays)
	// Replace wholesale rather than diff: the table is derived, the workspace
	// is RLS-bound, and a diff would be a second implementation of the fold
	// with its own way of being wrong.
	if _, err := tx.Exec(ctx, `DELETE FROM graph_interaction_edge`); err != nil {
		return fmt.Errorf("search: clearing the interaction projection for rebuild: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO graph_interaction_edge
		    (workspace_id, user_id, person_id, last_at, last_inbound_at, last_outbound_at,
		     count_90d, in_count_90d, out_count_90d, count_total, computed_at)
		SELECT NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		       up.user_id, pp.person_id,
		       max(a.occurred_at),
		       max(a.occurred_at) FILTER (WHERE a.direction = 'inbound'),
		       max(a.occurred_at) FILTER (WHERE a.direction = 'outbound'),
		       count(*) FILTER (WHERE a.occurred_at >= `+window+`),
		       count(*) FILTER (WHERE a.occurred_at >= `+window+` AND a.direction = 'inbound'),
		       count(*) FILTER (WHERE a.occurred_at >= `+window+` AND a.direction = 'outbound'),
		       count(*),
		       now()
		  FROM activity_participant up
		  JOIN activity_participant pp ON pp.activity_id = up.activity_id
		  JOIN activity a ON a.id = up.activity_id AND a.archived_at IS NULL
		 WHERE up.user_id IS NOT NULL AND up.role IN `+interactionRoles+`
		   AND pp.person_id IS NOT NULL AND pp.role IN `+interactionRoles+`
		 GROUP BY up.user_id, pp.person_id`); err != nil {
		return fmt.Errorf("search: rebuilding the interaction projection: %w", err)
	}
	return nil
}
