// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The seams behind the relationship-graph agent tools (ADR-0078).
//
// agents never reads a record table itself, so these adapters are where the
// tool surface meets the same row-scoped reads the HTTP surface uses. That is
// the point of the seam: one enforcement path, so a governed tool cannot see
// further than the person driving it.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/network"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/relstrength"
)

// whoKnowsLister answers "which colleagues know this contact" for the tool
// surface, through EdgesForPerson — which carries the person grant AND the row
// probe, so an unpromoted captured contact 404s here exactly as it does on the
// HTTP path rather than leaking through the agent.
func whoKnowsLister(pool *pgxpool.Pool) agents.WhoKnowsLister {
	return func(ctx context.Context, personID ids.UUID) ([]agents.KnownColleague, bool, error) {
		var out []agents.KnownColleague
		var truncated bool
		err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
			// Over-fetch, rank by warmth, THEN cap — the same three steps the
			// HTTP surface takes, for the same reason. EdgesForPerson orders
			// by last contact, so capping it directly would hand a model the
			// most RECENT colleagues under the label "who knows them best".
			// The HTTP path and this one must rank identically, or the answer
			// depends on who asked rather than on the relationships.
			edges, err := search.EdgesForPerson(ctx, tx, personID, agentWhoKnowsFetch)
			if err != nil {
				return err
			}
			now := clockNow()
			search.SortByStrength(edges, now)
			if len(edges) > agentWhoKnowsCap {
				// Said out loud, not just done. The two other bounded walks on
				// this surface report their cap, and a colleague list that
				// stopped silently is the one a model will call complete.
				truncated = true
				edges = edges[:agentWhoKnowsCap]
			}
			names, err := search.MemberNames(ctx, tx, edges)
			if err != nil {
				return err
			}
			for _, e := range edges {
				score := e.StrengthOf(now)
				colleague := agents.KnownColleague{
					UserID: e.UserID, DisplayName: names[e.UserID],
					StrengthBucket: score.Bucket, Interactions90d: e.Count90d,
				}
				if score.Bucket != relstrength.BucketNone {
					strength := score.Strength
					colleague.Strength = &strength
				}
				out = append(out, colleague)
			}
			return nil
		})
		return out, truncated, err
	}
}

// agentWhoKnowsCap bounds what the tool hands a model. The question is who to
// ask; a model given forty names will pick one at random and present it with
// the same confidence as the right one.
const agentWhoKnowsCap = 10

// agentWhoKnowsFetch is how many edges are read before ranking, matching the
// HTTP surface. Ranking a capped set would rank the wrong set.
const agentWhoKnowsFetch = 100

// coverageReader answers "how is this deal covered" for the tool surface.
func coverageReader(pool *pgxpool.Pool) agents.CoverageReader {
	return func(ctx context.Context, dealID ids.UUID) (agents.DealCoverageAnswer, error) {
		var out agents.DealCoverageAnswer
		err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
			// The deal gate before the payload: a coverage answer names the
			// deal's people, so a caller who cannot read the deal must not
			// learn who sits on it — through a tool any more than a URL.
			if err := requireVisibleDeal(ctx, tx, dealID); err != nil {
				return err
			}
			coverage, err := network.CoverageFor(ctx, tx, ids.From[ids.DealKind](dealID), clockNow())
			if err != nil {
				return err
			}
			// Names resolved here: a coverage answer whose colleagues are
			// bare ids leaves a model unable to say who to ask, which is the
			// only reason it asked.
			names, err := coverageUserNames(ctx, tx, coverage)
			if err != nil {
				return err
			}
			out = toAgentCoverage(coverage, names)
			return nil
		})
		return out, err
	}
}

func toAgentCoverage(c network.DealCoverage, names map[ids.UUID]string) agents.DealCoverageAnswer {
	// Both collections start EMPTY, never nil: a deal with no stakeholder seat
	// and nobody on our side is the answer that says the deal is uncovered, and
	// it is the answer a rep most needs. Marshalled from a nil slice it reaches a
	// model as `null`, which reads as "unknown" — so the tool would hedge about
	// coverage exactly where it has something definite to say. The same rule the
	// sibling reads uphold (whoKnowsTool, introPathTool, list_pipelines), applied
	// where the answer is BUILT rather than at the one tool that returns it.
	out := agents.DealCoverageAnswer{
		DealID:       c.DealID,
		Stakeholders: make([]agents.CoverageSeat, 0, len(c.Stakeholders)),
		OurSide:      make([]agents.KnownColleague, 0, len(c.OurSide)),
	}
	for _, s := range c.Stakeholders {
		out.Stakeholders = append(out.Stakeholders, agents.CoverageSeat{
			PersonID: s.PersonID, Role: s.Role, Engaged: s.Engaged,
		})
	}
	for _, e := range c.OurSide {
		colleague := agents.KnownColleague{
			UserID: e.UserID, DisplayName: names[e.UserID],
			StrengthBucket: e.Strength.Bucket, Interactions90d: e.Count90d,
		}
		if e.Strength.Bucket != relstrength.BucketNone {
			strength := e.Strength.Strength
			colleague.Strength = &strength
		}
		out.OurSide = append(out.OurSide, colleague)
	}
	out.Risks = toAgentRisks(c.Risks)
	return out
}

// toAgentRisks maps the findings onto the tool shape. Spelled once because two
// tools return risks — the coverage read and the at-risk sweep — and a second
// copy would be a second place for the day-count rule below to be wrong.
func toAgentRisks(risks []network.Risk) []agents.CoverageRisk {
	out := make([]agents.CoverageRisk, 0, len(risks))
	for _, r := range risks {
		risk := agents.CoverageRisk{
			Kind: r.Kind, Summary: r.Summary, PersonIDs: r.PersonIDs, UserIDs: r.UserIDs,
		}
		// Only going-cold carries a day count; a zero on the others would read
		// as "touched today", which is the opposite of what a departure finding
		// says about recency.
		if r.Kind == network.RiskGoingCold {
			days := r.DaysSinceTouch
			risk.DaysSinceTouch = &days
		}
		out = append(out, risk)
	}
	return out
}

// clockNow is the read instant for the decayed scores. A single call per
// answer, so every colleague in one payload is scored against the same moment
// — scoring each as it is read would let two edges in one list disagree about
// what "today" is.
func clockNow() time.Time { return time.Now().UTC() }

// requireVisibleDeal is the deal gate the tool path shares with the HTTP one.
func requireVisibleDeal(ctx context.Context, tx pgx.Tx, dealID ids.UUID) error {
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		return err
	}
	// Live probe, matching the HTTP path exactly: EnsureVisible skips its
	// existence check for an unbounded caller, so an unknown deal would answer
	// an empty coverage picture rather than a refusal — through the agent as
	// readily as through the URL.
	return auth.EnsureVisibleLive(ctx, tx, "deal", dealID)
}

// coverageUserNames resolves the display names for a coverage answer's
// colleagues.
func coverageUserNames(ctx context.Context, tx pgx.Tx, c network.DealCoverage) (map[ids.UUID]string, error) {
	edges := make([]search.InteractionEdge, 0, len(c.OurSide))
	for _, e := range c.OurSide {
		edges = append(edges, search.InteractionEdge{UserID: e.UserID})
	}
	return search.MemberNames(ctx, tx, edges)
}
