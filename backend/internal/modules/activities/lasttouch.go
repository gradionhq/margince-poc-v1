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
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
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

// LastTouchCandidate is one REMINDER-ELIGIBLE linked entity whose most
// recent GENUINE engagement (across every kind and every link) landed
// before the caller's cutoff. "Genuine" excludes the automation engine's
// own output (automationSource); "eligible" is the live-work test each
// entity type gets in the query — see LastTouchBefore.
type LastTouchCandidate struct {
	EntityType string
	EntityID   ids.UUID
	LastTouch  time.Time
}

// LastTouchBefore returns the entities that are BOTH quiet and worth
// reminding about: linked through activity_link, most recent
// GENUINE-engagement activity.occurred_at before cutoff, and carrying
// live work as of that same cutoff — oldest-touch first, capped at limit.
// It is the read automation.TimeScanner's no_activity_for_n_days clock
// candidates are built from.
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
// Eligibility is decided HERE, in the SQL, not in the automation
// handler's Match: one scan pass draws at most clockScanBatchLimit (200)
// candidates per instance (automation/timescan.go), so on a large
// workspace a quiet record nobody is working would occupy that batch on
// every tick forever and starve the records that do deserve a reminder.
// A post-filter cannot fix that; only excluding them from the draw can.
//
// What counts as live work, per type:
//
//   - deal — the deal itself is open and unarchived.
//   - organization — it has at least one open, unarchived deal. The
//     account is what the rep works, so the reminder belongs on the
//     account, once.
//   - person — they hold a live deal_stakeholder seat on an open deal.
//     Deliberately NOT "their employer has an open deal": that would mint
//     one reminder per employee of every busy account, each one a
//     duplicate of the single organization reminder that account already
//     earns.
//   - lead — still in the working part of its lifecycle ('new' or
//     'working'); a promoted or disqualified lead is finished business.
//
// Every other entity type activity_link can carry (project today) is
// outside this trigger's vocabulary and never becomes a candidate.
//
// The cutoff does double duty: the entity row's own created_at must also
// precede it, so a record created yesterday cannot be "stale" merely
// because activities backfilled onto it are older than N days. One
// cutoff, one meaning — the coarse scan and the handler's precise Match
// (automation/handlers_clock.go) cannot drift onto two thresholds.
func (s *Store) LastTouchBefore(ctx context.Context, cutoff time.Time, limit int) ([]LastTouchCandidate, error) {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return nil, err
	}
	if limit < 1 {
		limit = 1
	}
	var out []LastTouchCandidate
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// The entity_type literals come from the module's own link
		// vocabulary (linktarget.go), the same source the coalesce
		// expression is built from, so a renamed record type cannot leave a
		// stale string behind in this query.
		rows, err := tx.Query(ctx, storekit.SQLf(`
			WITH quiet AS (
				SELECT al.entity_type AS entity_type,
				       %[1]s AS entity_id,
				       max(a.occurred_at) AS last_touch
				FROM activity_link al
				JOIN activity a ON a.id = al.activity_id
				WHERE a.archived_at IS NULL
				  AND a.source <> $1
				GROUP BY al.entity_type, %[1]s
				HAVING max(a.occurred_at) < $2
			)
			SELECT q.entity_type, q.entity_id, q.last_touch
			FROM quiet q
			WHERE (q.entity_type = '%[2]s' AND EXISTS (
			         SELECT 1 FROM deal d
			         WHERE d.id = q.entity_id
			           AND d.status = 'open' AND d.archived_at IS NULL
			           AND d.created_at < $2))
			   OR (q.entity_type = '%[3]s' AND EXISTS (
			         SELECT 1 FROM organization o
			         JOIN deal d ON d.organization_id = o.id
			                    AND d.status = 'open' AND d.archived_at IS NULL
			         WHERE o.id = q.entity_id
			           AND o.archived_at IS NULL
			           AND o.created_at < $2))
			   OR (q.entity_type = '%[4]s' AND EXISTS (
			         SELECT 1 FROM person p
			         JOIN relationship r ON r.person_id = p.id
			                    AND r.kind = 'deal_stakeholder'
			                    AND r.ended_at IS NULL AND r.archived_at IS NULL
			         JOIN deal d ON d.id = r.deal_id
			                    AND d.status = 'open' AND d.archived_at IS NULL
			         WHERE p.id = q.entity_id
			           AND p.archived_at IS NULL
			           AND p.created_at < $2))
			   OR (q.entity_type = '%[5]s' AND EXISTS (
			         SELECT 1 FROM lead l
			         WHERE l.id = q.entity_id
			           AND l.status IN ('new','working') AND l.archived_at IS NULL
			           AND l.created_at < $2))
			ORDER BY q.last_touch, q.entity_id
			LIMIT $3`,
			linkIDCoalesceQualified("al"),
			datasource.RecordDeal, datasource.RecordOrganization,
			datasource.RecordPerson, datasource.RecordLead),
			automationSource, cutoff, limit)
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
