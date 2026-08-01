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

// EngagedStakeholders lists the deal's live stakeholders who have had a
// two-way exchange inside the window, in deterministic id order.
//
// It takes the caller's transaction rather than opening its own: the callers
// are assembling a wider picture (a coverage payload, a risk scan) and must
// read one instant, not one per question.
func EngagedStakeholders(ctx context.Context, tx pgx.Tx, dealID ids.DealID, now time.Time) ([]ids.UUID, error) {
	windowStart := now.AddDate(0, 0, -healthEngagementWindowDays)
	return collectIDs(tx.Query(ctx, `
		SELECT DISTINCT r.person_id FROM relationship r
		WHERE r.kind = 'deal_stakeholder' AND r.deal_id = $1 AND r.archived_at IS NULL
		  AND EXISTS (
			SELECT 1 FROM activity a
			JOIN activity_link l ON l.activity_id = a.id AND l.person_id = r.person_id
			WHERE a.kind IN `+healthActivityKinds+` AND a.archived_at IS NULL
			  AND a.occurred_at >= $2 AND a.direction = 'inbound')
		  AND EXISTS (
			SELECT 1 FROM activity a
			JOIN activity_link l ON l.activity_id = a.id AND l.person_id = r.person_id
			WHERE a.kind IN `+healthActivityKinds+` AND a.archived_at IS NULL
			  AND a.occurred_at >= $2 AND a.direction = 'outbound')
		ORDER BY r.person_id`, dealID, windowStart))
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
	scope, err := auth.ScopeClauseFor(ctx, "person", "p", arg)
	if err != nil {
		return nil, err
	}
	visible := "true"
	if scope != "" {
		visible = scope
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT r.person_id, coalesce(r.role, '')
		  FROM relationship r
		  JOIN person p ON p.id = r.person_id AND p.archived_at IS NULL
		 WHERE r.kind = 'deal_stakeholder' AND r.deal_id = $%d AND r.archived_at IS NULL
		   AND (%s)
		 ORDER BY r.person_id`, dealPos, visible), args...)
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
