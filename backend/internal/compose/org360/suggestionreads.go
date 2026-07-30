// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// What the suggestion rules read, and why they do not read the nested
// sections above them.
//
// The 360's collections are TRUNCATED SUMMARIES capped at sectionLimit — the
// card shows the newest 25 and links to the dedicated endpoint for the rest.
// A rule derived from that page answers a different question than the one it
// claims to: an account whose newest 25 timeline entries are all notes would
// miss an overdue unanswered email underneath them, and its 26th open deal
// would never be reported as stalled. A rep would read that as "nothing to
// chase here", which is the one thing the surface must never say wrongly.
//
// So each rule reads exactly what it needs, under the SAME row-scope
// predicates the sections use — the caller cannot see more this way, only
// further back.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// suggestionScanCap bounds the open-deal scan. An account with more open deals
// than this has a pipeline problem no card can advise on, and the deals beyond
// it are counted into the dropped total rather than dropped in silence.
const suggestionScanCap = 200

// lastMessage is the newest two-way message on an account, as the no-reply
// rule needs it: who spoke, when, and which activity to cite.
type lastMessage struct {
	ID        ids.UUID
	Direction string
	At        time.Time
}

// newestMessage reads the newest two-way exchange linked to the account, or
// reports that there is none.
//
// The kind set is every channel an answer can ARRIVE on, which is wider than
// the set we send on. A returned call or a meeting answers an email as
// completely as a reply does, so leaving them out would tell a rep to chase
// someone they spoke to yesterday. A note or a task is excluded for the
// opposite reason: it is something we wrote to ourselves, nobody owes a reply
// to it, and letting one count would silence the rule every time a rep left
// themselves a reminder.
func newestMessage(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID,
) (lastMessage, bool, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	activityScope, err := auth.ActivityScopeClause(ctx, "a", arg)
	if err != nil {
		return lastMessage{}, false, err
	}
	if activityScope == "" {
		activityScope = scopeAll
	}
	var found lastMessage
	var direction *string
	err = tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT a.id, a.direction, a.occurred_at
		FROM activity a
		WHERE a.kind IN ('email','whatsapp','telegram','call','meeting') AND a.archived_at IS NULL AND %[1]s
		  AND EXISTS (
		    SELECT 1 FROM activity_link l
		    LEFT JOIN deal d ON d.id = l.deal_id
		    LEFT JOIN relationship r ON r.person_id = l.person_id AND r.kind = 'employment'
		      AND r.ended_at IS NULL AND r.archived_at IS NULL
		    WHERE l.activity_id = a.id
		      AND (l.organization_id = $%[2]d OR d.organization_id = $%[3]d OR r.organization_id = $%[4]d))
		ORDER BY a.occurred_at DESC, a.id DESC
		LIMIT 1`, activityScope, orgPos, orgPos, orgPos), args...).
		Scan(&found.ID, &direction, &found.At)
	if errors.Is(err, pgx.ErrNoRows) {
		return lastMessage{}, false, nil
	}
	if err != nil {
		return lastMessage{}, false, fmt.Errorf("read the account's newest message: %w", err)
	}
	if direction != nil {
		found.Direction = *direction
	}
	return found, true, nil
}

// openDeal is one open deal as the suggestion rules read it.
type openDeal struct {
	ID      ids.UUID
	Name    string
	Stalled bool
}

// visibleOpenDeals reads every open deal on the account the caller may see,
// longest-idle first — so a capped scan keeps the deals most worth chasing
// rather than the most recently created ones the deals card already shows.
//
// The second return is how many rows past the scan cap exist; the caller
// folds it into the dropped total.
func visibleOpenDeals(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time,
) ([]openDeal, int, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	dealScope, err := scopeClause(ctx, "deal", "d", arg)
	if err != nil {
		return nil, 0, err
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT d.id, d.name, d.status, d.created_at, d.last_activity_at, d.wait_until
		FROM deal d
		WHERE d.organization_id = $%d AND d.status = 'open' AND d.archived_at IS NULL AND (%s)
		ORDER BY coalesce(d.last_activity_at, d.created_at), d.id
		LIMIT %d`, orgPos, dealScope, suggestionScanCap+1), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("read the account's open deals: %w", err)
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (openDeal, error) {
		var d openDeal
		var status string
		var createdAt time.Time
		var lastActivityAt, waitUntil *time.Time
		if err := row.Scan(&d.ID, &d.Name, &status, &createdAt, &lastActivityAt, &waitUntil); err != nil {
			return d, err
		}
		d.Stalled = deals.IsStalled(status, createdAt, lastActivityAt, waitUntil, now)
		return d, nil
	})
	if err != nil {
		return nil, 0, err
	}
	if len(found) > suggestionScanCap {
		return found[:suggestionScanCap], len(found) - suggestionScanCap, nil
	}
	return found, 0, nil
}

// openTasks reports whether the next-steps section reached this caller, and
// whether it carried anything.
//
// This one rule CAN read the section page: truncation only hides rows past the
// first 25, so an empty page is a real zero and any non-empty page answers
// "something is scheduled". The rules above cannot, because they need the row
// the cap hid rather than a count of the rows it kept.
func openTasks(view *crmcontracts.Organization360) (present, scheduled bool) {
	if view.NextSteps == nil {
		return false, false
	}
	return true, len(view.NextSteps.Data) > 0
}
