// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The one read the automation module's clock scan needs (Task 14a,
// automation/seams.go's ActivityScan): which linked entities have gone
// quiet. Sourced from this module's OWN tables (activity + activity_link)
// rather than a sibling's denormalized last_activity_at column
// (deal.last_activity_at, activity.go's insertActivityLinks) — a module
// reaches records only through seams (ADR-0054 §9), and this file is the
// seam's implementation, adapted onto automation.ActivityScan in
// compose/timescan.go.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// LastTouchCandidate is one linked entity whose most recent activity
// (across every kind and every link) landed before the caller's cutoff.
type LastTouchCandidate struct {
	EntityType string
	EntityID   ids.UUID
	LastTouch  time.Time
}

// LastTouchBefore returns, for every entity linked through activity_link,
// its most recent activity.occurred_at whenever that maximum is before
// cutoff, oldest-touch first, capped at limit — the read
// automation.TimeScanner's no_activity_for_n_days clock candidates are
// built from.
//
// Honest limitation: an entity that has never had ANY linked activity
// carries no activity_link row at all, so it never appears here — a
// created-but-never-touched record is not (yet) surfaced as "stale" by
// this read. Widening that is a real change to what "no activity" means
// for this trigger, not a bug in this query.
func (s *Store) LastTouchBefore(ctx context.Context, cutoff time.Time, limit int) ([]LastTouchCandidate, error) {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return nil, err
	}
	if limit < 1 {
		limit = 1
	}
	var out []LastTouchCandidate
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT al.entity_type,
			       coalesce(al.person_id, al.organization_id, al.deal_id, al.lead_id) AS entity_id,
			       max(a.occurred_at) AS last_touch
			FROM activity_link al
			JOIN activity a ON a.id = al.activity_id
			WHERE a.archived_at IS NULL
			GROUP BY al.entity_type, coalesce(al.person_id, al.organization_id, al.deal_id, al.lead_id)
			HAVING max(a.occurred_at) < $1
			ORDER BY max(a.occurred_at), entity_id
			LIMIT $2`, cutoff, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c LastTouchCandidate
			if err := rows.Scan(&c.EntityType, &c.EntityID, &c.LastTouch); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}
