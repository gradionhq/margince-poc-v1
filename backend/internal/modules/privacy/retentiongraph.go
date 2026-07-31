// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// Keeping the relationship graph honest when retention removes its evidence
// (ADR-0078).
//
// The retention sweep is the one deletion path that emits `retention.applied`
// rather than the entity's own verb, so the cg:graph-edge consumer never hears
// about it. Without this the projection keeps asserting conversations the
// timeline no longer shows until the nightly rebuild — a day of a derived
// number outliving the data it was derived from.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// invalidateGraphEdgesFor re-folds the interaction edges an activity
// contributed to, after retention has archived or emptied it.
//
// The retention sweep emits `retention.applied`, not `activity.archived`, so
// the cg:graph-edge consumer never hears about it — an interaction removed by
// retention would keep contributing exact recency and counts until the nightly
// rebuild. That is a day of a projection asserting a conversation the timeline
// no longer shows, and this closes it in the same transaction that caused it.
//
// It recomputes rather than deletes: the pair may still have other qualifying
// interactions, and only the fold knows.
func invalidateGraphEdgesFor(ctx context.Context, tx pgx.Tx, activityID ids.UUID) error {
	if _, err := tx.Exec(ctx, `
		WITH pair AS (
		    SELECT DISTINCT u.user_id, p.person_id
		      FROM activity_participant u
		      JOIN activity_participant p ON p.activity_id = u.activity_id
		     WHERE u.activity_id = $1
		       AND u.user_id IS NOT NULL AND p.person_id IS NOT NULL
		)
		DELETE FROM graph_interaction_edge e
		 USING pair
		 WHERE e.user_id = pair.user_id AND e.person_id = pair.person_id
		   AND NOT EXISTS (
		       SELECT 1
		         FROM activity_participant up
		         JOIN activity_participant pp ON pp.activity_id = up.activity_id
		         JOIN activity a ON a.id = up.activity_id AND a.archived_at IS NULL
		        WHERE up.user_id = pair.user_id AND pp.person_id = pair.person_id)`,
		activityID); err != nil {
		return fmt.Errorf("privacy: invalidating interaction edges after retention: %w", err)
	}
	return nil
}
