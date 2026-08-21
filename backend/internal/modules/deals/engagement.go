// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The ONE definition of "an engaged stakeholder on this deal".
//
// It was already spelled inside the deal-health engine, where the composite
// reads it. The coverage and risk surfaces (ADR-0078) need the same answer,
// and a second spelling would let two screens disagree about whether a deal is
// single-threaded — which is precisely the flag reporting.md requires to
// reconcile across every surface that shows it (REPORT-PARAM-1).
//
// Engaged means a REAL two-way exchange in the window: both an inbound and an
// outbound qualifying interaction. A one-way broadcast target is not engaged,
// however many messages we sent them, and a deal threaded only through people
// who never replied is exactly the deal this flag exists to catch.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// EngagementWindowDays is the window the engagement test looks back over.
const EngagementWindowDays = healthEngagementWindowDays

// predicateAlways is a WHERE fragment that admits every row.
//
// It is what an absent clause becomes on the way into a statement: auth answers
// "" for a caller bounded by nothing and a nil filter has nothing to say, and an
// empty string interpolated into a WHERE is a syntax error rather than "no
// restriction". Named because three statements in this package make that
// substitution and the SQL they build must agree about it.
const predicateAlways = "true"

// edgeBound resolves the edge's read admission and returns the clause bounding
// WHICH edges, admitting every edge for a caller bounded by nothing.
//
// A function rather than two copies of an `if clause == ""`: both reads in this
// file take the same admission, so they take it through the same three lines.
func edgeBound(ctx context.Context, alias string, arg func(any) int) (string, error) {
	clause, err := auth.EdgeReadScope(ctx, alias, arg)
	if err != nil {
		return "", err
	}
	if clause == "" {
		return predicateAlways, nil
	}
	return clause, nil
}

// EngagedStakeholders lists the deal's live stakeholders who have had a
// two-way exchange inside the window, in deterministic id order.
//
// It takes the caller's transaction rather than opening its own: the callers
// are assembling a wider picture (a coverage payload, a risk scan) and must
// read one instant, not one per question.
//
// A seat is an EDGE, so this carries the edge's own admission: knowing a deal
// does not license learning who sits on it, and the endpoint grants do not
// cover the pair. The gate resolves before the statement — a caller refused it
// gets apperrors.ErrPermissionDenied and no rows are ever read, which is what
// lets CoverageFor name the omission instead of reporting an uncovered deal.
func EngagedStakeholders(ctx context.Context, tx pgx.Tx, dealID ids.DealID, now time.Time) ([]ids.UUID, error) {
	windowStart := now.AddDate(0, 0, -healthEngagementWindowDays)
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	dealPos := arg(dealID)
	windowPos := arg(windowStart)
	bound, err := edgeBound(ctx, "r", arg)
	if err != nil {
		return nil, err
	}
	// The kind list is an ARGUMENT to Sprintf, not concatenated into its format
	// string. Concatenated, a `%` ever appearing in a kind would be read as a
	// verb and corrupt the statement at runtime with nothing to catch it.
	return collectIDs(tx.Query(ctx, fmt.Sprintf(`
		SELECT DISTINCT r.person_id FROM relationship r
		WHERE r.kind = 'deal_stakeholder' AND r.deal_id = $%[1]d AND r.archived_at IS NULL
		  AND (%[3]s)
		  AND EXISTS (
			SELECT 1 FROM activity a
			JOIN activity_link l ON l.activity_id = a.id AND l.person_id = r.person_id
			WHERE a.kind IN %[4]s AND a.archived_at IS NULL
			  AND a.occurred_at >= $%[2]d AND a.direction = 'inbound')
		  AND EXISTS (
			SELECT 1 FROM activity a
			JOIN activity_link l ON l.activity_id = a.id AND l.person_id = r.person_id
			WHERE a.kind IN %[4]s AND a.archived_at IS NULL
			  AND a.occurred_at >= $%[2]d AND a.direction = 'outbound')
		ORDER BY r.person_id`, dealPos, windowPos, bound, healthActivityKinds), args...))
}

// DealStakeholder is one seat on a deal: who, in what role, and whether they
// are engaged.
type DealStakeholder struct {
	PersonID ids.UUID
	Role     string
	Engaged  bool
}

// Stakeholders lists every live seat on the deal with its role, marking the
// engaged ones — the shape a coverage view needs, where the unengaged seats
// are the finding rather than noise to filter out.
func Stakeholders(ctx context.Context, tx pgx.Tx, dealID ids.DealID, now time.Time) ([]DealStakeholder, error) {
	engaged, err := EngagedStakeholders(ctx, tx, dealID, now)
	if err != nil {
		return nil, err
	}
	isEngaged := make(map[ids.UUID]bool, len(engaged))
	for _, id := range engaged {
		isEngaged[id] = true
	}
	// The person row scope, not just the deal's. Being able to read a deal
	// does not license learning WHO is on it: a stakeholder can be an
	// owner-private captured contact, and a coverage payload that listed them
	// would disclose through a side door exactly what the person read closes.
	// Seats the caller cannot see are absent, and the caller cannot tell an
	// invisible seat from an empty one — which is the same answer every other
	// row-scoped list gives.
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	dealPos := arg(dealID)
	// The edge's own admission FIRST, and the person row scope after it. Both,
	// because they answer different questions: the edge grant asks whether this
	// caller may read seats at all, the person scope asks which of them. A
	// caller holding the deal and person grants and neither of these is served a
	// pair that neither grant covers.
	bound, err := edgeBound(ctx, "r", arg)
	if err != nil {
		return nil, err
	}
	scope, err := auth.ScopeClauseFor(ctx, "person", "p", arg)
	if err != nil {
		return nil, err
	}
	visible := predicateAlways
	if scope != "" {
		visible = scope
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT r.person_id, coalesce(r.role, '')
		  FROM relationship r
		  JOIN person p ON p.id = r.person_id AND p.archived_at IS NULL
		 WHERE r.kind = 'deal_stakeholder' AND r.deal_id = $%d AND r.archived_at IS NULL
		   AND (%s)
		   AND (%s)
		 ORDER BY r.person_id`, dealPos, bound, visible), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DealStakeholder
	for rows.Next() {
		var s DealStakeholder
		if err := rows.Scan(&s.PersonID, &s.Role); err != nil {
			return nil, err
		}
		s.Engaged = isEngaged[s.PersonID]
		out = append(out, s)
	}
	return out, rows.Err()
}
