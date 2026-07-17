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

// automationSource is the activity.source value the automation engine
// stamps on every activity it creates (its create_task / create_record
// output, automation/engine.go's systemSource). LastTouchBefore
// excludes these so the engine's OWN reminder task cannot count as a
// "touch" that resets the very clock it fires off. A module never imports
// a sibling (ADR-0054 §9), so this is the value-level shadow of that
// constant, not a shared symbol — kept in lockstep by the integration
// proof (compose/integration), which fires a real reminder and asserts
// the anchor does not move.
const automationSource = "system"

// LastTouchCandidate is one linked entity whose most recent GENUINE
// engagement (across every kind and every link) landed before the
// caller's cutoff. "Genuine" excludes the automation engine's own output
// (automationSource) — see LastTouchBefore.
type LastTouchCandidate struct {
	EntityType string
	EntityID   ids.UUID
	LastTouch  time.Time
}

// LastTouchBefore returns, for every entity linked through activity_link,
// its most recent GENUINE-engagement activity.occurred_at whenever that
// maximum is before cutoff, oldest-touch first, capped at limit — the read
// automation.TimeScanner's no_activity_for_n_days clock candidates are
// built from.
//
// "No activity for N days" means the rep has not ENGAGED the record — a
// human touch (call, email, meeting, note), an inbound reply, or a
// captured mail (Gmail/IMAP, source "gmail:…"/"imap:…") all count. The
// automation engine's OWN writes (source = automationSource) do NOT: a
// reminder task the engine created must not look like engagement, or the
// firing would reset its own anchor to ~now, age out of the candidate
// set, then re-surface with the task's timestamp as a fresh anchor and
// nag every N days forever. Excluding automationSource keeps the anchor
// pinned to the last real touch, so no_activity_reminder fires ONCE per
// quiet spell (recurring "check in every N days regardless" is
// check_in_cadence's job, not this trigger's).
//
// Honest limitation: an entity whose links are all archived or all
// automation-sourced contributes no genuine touch, so it never appears
// here — a never-genuinely-touched record is not (yet) surfaced as
// "stale" by this read. Widening that is a real change to what "no
// activity" means for this trigger, not a bug in this query.
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
			  AND a.source <> $1
			GROUP BY al.entity_type, coalesce(al.person_id, al.organization_id, al.deal_id, al.lead_id)
			HAVING max(a.occurred_at) < $2
			ORDER BY max(a.occurred_at), entity_id
			LIMIT $3`, automationSource, cutoff, limit)
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
