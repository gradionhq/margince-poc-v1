// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package meetingbrief

// "What changed since we last spoke" — the section a rep actually opens a
// brief for. "We last spoke" is the READER's last interaction with the
// people in this room: the newest past activity, linked to any of them,
// that the reader took part in. Two reps honestly get two baselines on one
// deal, which is correct — it is what "last spoke" means to the person
// reading. With no such interaction the section says FIRST CONTACT rather
// than "nothing changed", which would be a false claim.
//
// What counts as changed after the baseline comes from the input the brief
// already holds — claims made, conversations captured — so the section adds
// one read (the baseline) and no new authority.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// readLastSpoke returns when the reader last dealt with anyone in the room
// before this meeting, and whether they ever have. Any captured activity the
// reader took part in counts, whichever way it went: an outbound-only gap
// does not reset the baseline.
func (s *Service) readLastSpoke(ctx context.Context, tx pgx.Tx, room meeting, project *ids.ProjectID, now time.Time) (time.Time, bool, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID == (ids.UUID{}) || len(room.Room) == 0 {
		// An agent on a passport reads as the human behind it; a principal with
		// no user has never spoken to anyone.
		return time.Time{}, false, nil
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	mePos := arg(actor.UserID)
	roomPos := arg(room.ID)
	// Before the meeting AND before now: a brief read for a meeting already
	// held must not take a later conversation as "before we spoke".
	ceiling := now
	if room.StartsAt.Before(ceiling) {
		ceiling = room.StartsAt
	}
	ceilingPos := arg(ceiling)
	peoplePos := arg(room.Room)
	scope, err := auth.ActivityDiscoverClause(ctx, "a", arg)
	if err != nil {
		return time.Time{}, false, err
	}
	if scope == "" {
		scope = scopeAll
	}
	// The resolved project — the meeting's own filing, or the one requested
	// for an unattributed meeting — narrows the baseline like every other read
	// the brief makes: contact on another engagement is not "we last spoke".
	within := scopeAll
	if project != nil {
		within = fmt.Sprintf(`(EXISTS (
			    SELECT 1 FROM activity_link pl
			    WHERE pl.activity_id = a.id AND pl.project_id = $%d)
			  OR NOT EXISTS (
			    SELECT 1 FROM activity_link pf
			    WHERE pf.activity_id = a.id AND pf.project_id IS NOT NULL))`,
			arg(*project))
	}
	var last *time.Time
	err = tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT max(a.occurred_at)
		  FROM activity a
		  JOIN activity_participant ap ON ap.activity_id = a.id AND ap.user_id = $%d
		 WHERE a.id <> $%d AND a.occurred_at < $%d AND a.archived_at IS NULL
		   AND EXISTS (SELECT 1 FROM activity_link l
		                WHERE l.activity_id = a.id AND l.entity_type = 'person' AND l.person_id = ANY($%d))
		   AND %s AND %s`, mePos, roomPos, ceilingPos, peoplePos, scope, within), args...).Scan(&last)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, fmt.Errorf("read when the reader last spoke to the room: %w", err)
	}
	if last == nil {
		return time.Time{}, false, nil
	}
	return *last, true, nil
}

// whatChangedSection lists what happened after the baseline, sharpest first
// from the ranked set it takes from, and the conversations captured since.
// The first line names the baseline, so the reader knows what "since" means.
func whatChangedSection(in Input, ranked *rankedClaims) []Sentence {
	if in.LastSpokeAt == nil {
		return []Sentence{{
			Text:     "First contact: you have not dealt with anyone in this room before.",
			Nature:   natureAssessment,
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: in.ActivityID}},
		}}
	}
	since := *in.LastSpokeAt
	out := []Sentence{{
		Text:     fmt.Sprintf("You last dealt with this room %s.", daysAgoPhrase(in.Now, since)),
		Evidence: []Evidence{{EntityType: citeActivity, EntityID: in.ActivityID}},
	}}
	// After the baseline and not after now: a claim on a future-dated row
	// has not happened yet.
	after := func(c ClaimIn) bool {
		return c.OccurredAt != nil && c.OccurredAt.After(since) && !c.OccurredAt.After(in.Now)
	}
	for _, claim := range ranked.takeAll(after, whatChangedCap) {
		out = append(out, Sentence{
			Text:     changedClaimLine(claim),
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: claim.SourceID}},
		})
	}
	conversations := 0
	var newest *ActIn
	for i := range in.Recent {
		if in.Recent[i].At.After(since) && !in.Recent[i].At.After(in.Now) {
			conversations++
			if newest == nil {
				newest = &in.Recent[i]
			}
		}
	}
	if newest != nil {
		out = append(out, Sentence{
			Text:     fmt.Sprintf("%s since then, the latest %q.", pluralOf(conversations, "conversation"), subjectOrKind(*newest)),
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: newest.ID}},
		})
	}
	if len(out) == 1 {
		out[0].Text += " Nothing captured has changed since."
	}
	return out
}

// whatChangedCap keeps the section to what a reader takes in before a room.
const whatChangedCap = 5

func changedClaimLine(claim ClaimIn) string {
	switch claim.Kind {
	case kindCommitmentOurs:
		return fmt.Sprintf("Since then we promised %s: %s", claim.PersonName, claim.Body)
	case kindCommitmentTheirs:
		return fmt.Sprintf("Since then %s promised: %s", claim.PersonName, claim.Body)
	case kindObjection:
		return fmt.Sprintf("Since then %s objected: %s", claim.PersonName, claim.Body)
	case kindDecision:
		return fmt.Sprintf("Since then it was agreed with %s: %s", claim.PersonName, claim.Body)
	case kindOpenQuestion:
		return fmt.Sprintf("Since then %s asked: %s", claim.PersonName, claim.Body)
	default:
		return fmt.Sprintf("Since then %s said: %s", claim.PersonName, claim.Body)
	}
}

func daysAgoPhrase(now, at time.Time) string {
	days := int(now.Sub(at).Hours() / 24)
	switch {
	case days <= 0:
		return "earlier today"
	case days == 1:
		return "yesterday"
	default:
		return fmt.Sprintf("%d days ago", days)
	}
}

func pluralOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func subjectOrKind(a ActIn) string {
	if a.Subject != "" {
		return a.Subject
	}
	return a.Kind
}
