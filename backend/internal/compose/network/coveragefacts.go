// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package network

// The gather half of the coverage read: the facts the risk rules decide on.
//
// Split from the fold so the thresholds can be tested against hand-built
// numbers with no database, and so every fact the rules see is read at ONE
// instant inside ONE transaction — a deal whose status came from a later
// snapshot than its last touch could report a won deal as going cold.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// dealStatusOpen is the status the pipeline rules apply to. Named rather than
// inline because two of them ask for it and a literal in both places is a
// second definition waiting to drift.
const dealStatusOpen = "open"

// dealFacts is the deal's own row, as the risk rules need it.
type dealFacts struct {
	status         string
	organizationID ids.UUID
	lastTouchAt    time.Time
}

// readDealFacts loads the deal row the rules decide on.
//
// The last touch falls back to the deal's creation, which is the same
// coalesce every stalled-deal surface in this codebase takes: a deal nothing
// has ever touched has been silent since the day somebody wrote it down, and
// treating that as "no data" would hide the oldest untouched deals — exactly
// the ones the rule is for.
func readDealFacts(ctx context.Context, tx pgx.Tx, dealID ids.DealID) (dealFacts, error) {
	var out dealFacts
	var org *ids.UUID
	err := tx.QueryRow(ctx, `
		SELECT status, organization_id, coalesce(last_activity_at, created_at)
		  FROM deal WHERE id = $1`, dealID).Scan(&out.status, &org, &out.lastTouchAt)
	if err != nil {
		return out, fmt.Errorf("network: reading the deal a coverage view describes: %w", err)
	}
	if org != nil {
		out.organizationID = *org
	}
	return out, nil
}

// readDeparted answers which of the deal's stakeholders have LEFT the account.
//
// The test demands evidence of a departure, not merely absence of an
// employment. Most stakeholders have no employment row on file at all, and
// treating "we never recorded where they work" as "they left" would put a
// departure flag on nearly every deal in a young workspace — a warning that is
// always on is a warning nobody reads.
//
// So a person qualifies only when BOTH halves hold: an employment at this
// account that has ended or been archived, and no live employment there now. A
// contract renewed after a gap, or a role change recorded as end-then-start,
// leaves a live row and correctly raises nothing.
//
// No visibility probe: the caller passes the stakeholder ids it already read
// under its own person row scope, so a seat this caller cannot see never
// reaches here. Re-probing would be a second enforcement of the same rule with
// its own way of being wrong.
func readDeparted(ctx context.Context, tx pgx.Tx, orgID ids.UUID, people []ids.UUID, now time.Time) ([]ids.UUID, error) {
	if orgID == ids.Nil || len(people) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT r.person_id
		  FROM relationship r
		 WHERE r.kind = 'employment'
		   AND r.organization_id = $1
		   AND r.person_id = ANY($2)
		   AND (r.archived_at IS NOT NULL OR (r.ended_at IS NOT NULL AND r.ended_at <= $3::date))
		   AND NOT EXISTS (
		       SELECT 1 FROM relationship live
		        WHERE live.kind = 'employment'
		          AND live.organization_id = r.organization_id
		          AND live.person_id = r.person_id
		          AND live.archived_at IS NULL
		          AND (live.ended_at IS NULL OR live.ended_at > $3::date))
		 ORDER BY r.person_id`, orgID, people, now)
	if err != nil {
		return nil, fmt.Errorf("network: reading which stakeholders have left the account: %w", err)
	}
	defer rows.Close()
	var out []ids.UUID
	for rows.Next() {
		var id ids.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
