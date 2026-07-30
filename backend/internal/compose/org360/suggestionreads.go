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
// further back, and nothing here reads an assembled section at all.
//
// Every figure they state covers the whole visible set, and comes from ONE read
// of it. A count bounded by its own fetch is one a rep cannot tell from a real
// one, and two reads of the same pipeline can disagree — the 360's as_of
// promises one instant, so the rules take one snapshot.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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
//
// Reachability is the any-link walk — a direct link, a deal of this account, or
// an employment relationship — the same one nextStepsSection uses. It is wider
// than the timeline SECTION's direct-link match, so the cited message can be one
// the rendered timeline does not list. Every candidate still passes the activity
// row scope, so the reader can open it; they may have to open it from the
// citation rather than find it on the page.
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
	// IdleSince is the instant the stall is measured from — the deal's last
	// activity, or its creation if it has none.
	IdleSince time.Time
}

// episode identifies the STALL, not the deal: the deal and the instant it went
// idle, and nothing else.
//
// It must MOVE when the deal is worked and stalls again, or one dismissal silences
// that deal for good. It must move only FORWARD, or a shape the rep already
// dismissed can recur and that old dismissal comes back to life — silencing advice
// they may have been shown again in between. last_activity_at satisfies both:
// activities.LogActivity advances it with greatest() and nothing lowers it.
//
// Two mutable facts the stall rule also reads are deliberately left out, because
// each of them can return to an earlier value and would break the second property:
//
//   - wait_until. While a deferral runs the deal is not stalled at all, so no
//     advice is due for a dismissal to affect; when it ends the deal is in exactly
//     the state the rep declined, with nothing worked in between.
//   - status and archived_at. A deal that is closed and reopened, or archived and
//     restored, leaves the candidate set and comes back — and comes back to the
//     same episode. So a dismissal survives that round trip.
//
// The rule that leaves is one sentence, and it is the one a rep means: "not now"
// silences this deal until it is next worked. TestADismissalSurvivesADealRoundTrip
// holds the round-trip half of it, so the behaviour is chosen rather than noticed.
func (d stalledDeal) episode() string {
	return d.ID.String() + "@" + d.IdleSince.UTC().Format(time.RFC3339Nano)
}

// pipeline is the account's open pipeline as the deal-shaped rules read it.
//
// Every field is derived from ONE read of the whole visible open set, so the
// count, the digest and the stalled list cannot disagree with each other. Two
// statements would take two Read Committed snapshots, and a deal closing between
// them would leave the card reporting a pipeline that never existed at any
// instant — the 360's as_of promises the opposite.
type pipeline struct {
	// OpenCount is how many open deals the caller can see.
	OpenCount int
	// OpenDigest identifies WHICH ones, so a dismissal keyed on it re-arms the
	// moment the set changes — including a change to a deal no card listed.
	OpenDigest string
	// Stalled is every stalled one, longest idle first. The display cap is
	// applied by the rule that lists them, AFTER dismissals are filtered out, so
	// dismissing one suggestion reveals the next rather than shrinking the card.
	Stalled []stalledDeal
}

// openPipeline reads every open deal on the account the caller may see, in one
// statement, and folds the figures the rules need out of it.
//
// It is deliberately unbounded. A bound would put the card's own read inside
// every number it reports: a count capped at the fetch is one a rep cannot tell
// from a real one, a digest over a fetched page leaves a dismissal in force when
// a deal outside it changes, and a stalled list cut before dismissals are
// applied shrinks by one each time the rep judges a row. The rows are three
// small columns of one account's open pipeline, reached through the
// organization_id index.
//
// The stall flag is folded with deals.IsStalled — the same call that stamps the
// wire flag — rather than filtered in SQL. The deals module's SQL spelling of the
// rule is unexported, and it evaluates against the database's now(); this read
// pins its own instant, so a clause on the database clock would put a suggestion
// on a different moment than the as_of it is reported under.
func openPipeline(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time,
) (pipeline, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	dealScope, err := scopeClause(ctx, "deal", "d", arg)
	if err != nil {
		return pipeline{}, err
	}
	// Longest idle first, which is the order the stalled rows are offered in, and
	// by id so that order is deterministic between two deals idle since the same
	// instant.
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT d.id, d.name, d.status, d.created_at, d.last_activity_at, d.wait_until
		%s
		ORDER BY coalesce(d.last_activity_at, d.created_at), d.id`,
		"FROM deal d\n\t\t"+openDealsWhere(orgPos, dealScope)), args...)
	if err != nil {
		return pipeline{}, fmt.Errorf("read the account's open pipeline: %w", err)
	}
	type openRow struct {
		id        ids.UUID
		name      string
		stalled   bool
		idleSince time.Time
	}
	open, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (openRow, error) {
		var r openRow
		var status string
		var createdAt time.Time
		var lastActivityAt, waitUntil *time.Time
		if err := row.Scan(&r.id, &r.name, &status, &createdAt, &lastActivityAt, &waitUntil); err != nil {
			return r, err
		}
		r.stalled = deals.IsStalled(status, createdAt, lastActivityAt, waitUntil, now)
		// The same base IsStalled measures from, so the fingerprint moves exactly
		// when the stall the rep judged is replaced by a new one.
		r.idleSince = createdAt
		if lastActivityAt != nil {
			r.idleSince = *lastActivityAt
		}
		return r, nil
	})
	if err != nil {
		return pipeline{}, err
	}

	out := pipeline{OpenCount: len(open), Stalled: make([]stalledDeal, 0, len(open))}
	sorted := make([]string, 0, len(open))
	for _, deal := range open {
		sorted = append(sorted, deal.id.String())
		if deal.stalled {
			out.Stalled = append(out.Stalled, stalledDeal{
				ID: deal.id, Name: deal.name, IdleSince: deal.idleSince,
			})
		}
	}
	// Sorted by id rather than by the read's order, so the digest depends on
	// WHICH deals are open and on nothing else — a deal whose last activity moves
	// must not read as a changed pipeline.
	slices.Sort(sorted)
	out.OpenDigest = strings.Join(sorted, ",")
	return out, nil
}

// hasOpenTask answers whether anything at all is scheduled on the account.
//
// The rule needs "is there one?", so that is what is asked. Reading the
// next-steps page instead would answer the same question correctly today —
// truncation only hides rows past the first 25 — while coupling the rules to a
// section, which is the coupling the whole file exists to remove.
//
// Reachability is the any-link walk nextStepsSection uses: a task reaches the
// account through its own link, its deal, or the contact it is about.
func hasOpenTask(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID,
) (bool, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	activityScope, err := auth.ActivityScopeClause(ctx, "a", arg)
	if err != nil {
		return false, err
	}
	if activityScope == "" {
		activityScope = scopeAll
	}
	var scheduled bool
	err = tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT EXISTS (
		  SELECT 1 FROM activity a
		  WHERE a.kind = 'task' AND NOT a.is_done AND a.archived_at IS NULL AND %[1]s
		    AND EXISTS (
		      SELECT 1 FROM activity_link l
		      LEFT JOIN deal d ON d.id = l.deal_id
		      LEFT JOIN relationship r ON r.person_id = l.person_id AND r.kind = 'employment'
		        AND r.ended_at IS NULL AND r.archived_at IS NULL
		      WHERE l.activity_id = a.id
		        AND (l.organization_id = $%[2]d OR d.organization_id = $%[3]d OR r.organization_id = $%[4]d)))`,
		activityScope, orgPos, orgPos, orgPos), args...).Scan(&scheduled)
	if err != nil {
		return false, fmt.Errorf("read whether anything is scheduled on the account: %w", err)
	}
	return scheduled, nil
}

// suggestionInputs is everything the rules read, gathered once.
//
// Both callers build from this same struct — the composite read that serves the
// card, and the dismissal that has to recognize what the card served. Two
// gatherers would let the two disagree about what a suggestion IS, and then a
// dismissal would silently match nothing.
type suggestionInputs struct {
	// timeline and pipeline are the two grants that decide which rules run at
	// all. They are the object grants, which is exactly what makes a 360 section
	// absent — row scope narrows a section, it does not withhold it.
	timeline bool
	pipeline bool

	newest    lastMessage
	hasNewest bool
	open      pipeline
	scheduled bool
}

// advisable reports whether this caller can be advised at all. Neither input
// means nothing to derive advice from, so the section is omitted and named
// rather than answering empty.
func (in suggestionInputs) advisable() bool { return in.timeline || in.pipeline }

// gatherSuggestionInputs reads what the rules need, skipping whatever this
// caller has no grant for.
func gatherSuggestionInputs(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time,
) (suggestionInputs, error) {
	in := suggestionInputs{
		timeline: auth.Require(ctx, "activity", principal.ActionRead) == nil,
		pipeline: auth.Require(ctx, "deal", principal.ActionRead) == nil,
	}
	if in.timeline {
		newest, found, err := newestMessage(ctx, tx, orgID)
		if err != nil {
			return suggestionInputs{}, err
		}
		in.newest, in.hasNewest = newest, found
		scheduled, err := hasOpenTask(ctx, tx, orgID)
		if err != nil {
			return suggestionInputs{}, err
		}
		in.scheduled = scheduled
	}
	if in.pipeline {
		open, err := openPipeline(ctx, tx, orgID, now)
		if err != nil {
			return suggestionInputs{}, err
		}
		in.open = open
	}
	return in, nil
}
