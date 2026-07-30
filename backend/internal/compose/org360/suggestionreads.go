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
//
// Every count here is an aggregate over the whole visible set, never over a
// fetched page. A count taken from transferred rows is bounded by its own read,
// and a rep cannot tell a capped figure from a real one.

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

// lastMessage is the newest two-way exchange on an account, as the no-reply
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

// stalledDeal is one stalled open deal as the suggestion rules cite it.
type stalledDeal struct {
	ID   ids.UUID
	Name string
}

// pipeline is the account's open pipeline as the deal-shaped rules read it.
//
// The counts and the digest cover EVERY open deal the caller may see; Stalled
// carries only the rows the card can show. That split is the point: the
// no-next-step reason states how many deals are open, and its fingerprint has
// to change when any of them does — including one no card listed.
type pipeline struct {
	// OpenCount is how many open deals the caller can see, exactly.
	OpenCount int
	// OpenDigest identifies WHICH ones, so a dismissal keyed on it re-arms the
	// moment the set changes rather than only when the listed part does.
	OpenDigest string
	// StalledCount is how many of them are stalled, exactly.
	StalledCount int
	// Stalled is the longest-idle few, for the rows the card offers.
	Stalled []stalledDeal
}

// openPipeline reads the account's open pipeline: the exact figures from an
// aggregate, and the handful of stalled deals the card lists.
//
// Two statements rather than one, because they answer two different shapes —
// one row of totals, and a bounded list. Both run in the caller's own
// transaction over the same predicate, so they cannot disagree about what is
// visible or about what counts as stalled.
func openPipeline(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time, listLimit int,
) (pipeline, error) {
	var out pipeline
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	nowPos := arg(now)
	dealScope, err := scopeClause(ctx, "deal", "d", arg)
	if err != nil {
		return pipeline{}, err
	}
	// The stall predicate is the deals module's own, at THIS read's injected
	// instant: the 360 pins its clock, and a clause on the database's now()
	// would count against a different moment than the wire flag was stamped at.
	// The bind is cast explicitly: an untyped parameter next to an interval
	// leaves Postgres unable to resolve the subtraction at all.
	stalled := deals.StalledClause("d", fmt.Sprintf("$%d::timestamptz", nowPos))
	openRows := fmt.Sprintf(
		`FROM deal d
		 WHERE d.organization_id = $%d AND d.status = 'open' AND d.archived_at IS NULL AND (%s)`,
		orgPos, dealScope)

	err = tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT count(*),
		       coalesce(md5(string_agg(d.id::text, ',' ORDER BY d.id)), ''),
		       count(*) FILTER (WHERE %s)
		%s`, stalled, openRows), args...).
		Scan(&out.OpenCount, &out.OpenDigest, &out.StalledCount)
	if err != nil {
		return pipeline{}, fmt.Errorf("read the account's open pipeline: %w", err)
	}
	if out.StalledCount == 0 {
		return out, nil
	}
	// Longest idle first, so a bounded list keeps the deals most worth chasing
	// rather than whichever the deals card happens to show at the top.
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT d.id, d.name
		%s AND (%s)
		ORDER BY coalesce(d.last_activity_at, d.created_at), d.id
		LIMIT %d`, openRows, stalled, listLimit), args...)
	if err != nil {
		return pipeline{}, fmt.Errorf("read the account's stalled deals: %w", err)
	}
	out.Stalled, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (stalledDeal, error) {
		var d stalledDeal
		err := row.Scan(&d.ID, &d.Name)
		return d, err
	})
	if err != nil {
		return pipeline{}, err
	}
	return out, nil
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
