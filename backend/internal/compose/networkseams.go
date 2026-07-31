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
	return func(ctx context.Context, personID ids.UUID) ([]agents.KnownColleague, error) {
		var out []agents.KnownColleague
		err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
			edges, err := search.EdgesForPerson(ctx, tx, personID, agentWhoKnowsCap)
			if err != nil {
				return err
			}
			names, err := networkUserNames(ctx, tx, edges)
			if err != nil {
				return err
			}
			now := clockNow()
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
		return out, err
	}
}

// agentWhoKnowsCap bounds what the tool hands a model. The question is who to
// ask; a model given forty names will pick one at random and present it with
// the same confidence as the right one.
const agentWhoKnowsCap = 10

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
			out = toAgentCoverage(coverage)
			return nil
		})
		return out, err
	}
}

func toAgentCoverage(c network.DealCoverage) agents.DealCoverageAnswer {
	out := agents.DealCoverageAnswer{DealID: c.DealID}
	for _, s := range c.Stakeholders {
		out.Stakeholders = append(out.Stakeholders, agents.CoverageSeat{
			PersonID: s.PersonID, Role: s.Role, Engaged: s.Engaged,
		})
	}
	for _, e := range c.OurSide {
		colleague := agents.KnownColleague{
			UserID: e.UserID, StrengthBucket: e.Strength.Bucket, Interactions90d: e.Count90d,
		}
		if e.Strength.Bucket != relstrength.BucketNone {
			strength := e.Strength.Strength
			colleague.Strength = &strength
		}
		out.OurSide = append(out.OurSide, colleague)
	}
	for _, r := range c.Risks {
		out.Risks = append(out.Risks, agents.CoverageRisk{
			Kind: r.Kind, Summary: r.Summary, PersonIDs: r.PersonIDs, UserIDs: r.UserIDs,
		})
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
	return auth.EnsureVisible(ctx, tx, "deal", dealID)
}

// networkUserNames resolves colleague names for one answer. The roster is
// readable by any authenticated member, so naming a colleague on a record the
// caller can already open discloses nothing new.
func networkUserNames(ctx context.Context, tx pgx.Tx, edges []search.InteractionEdge) (map[ids.UUID]string, error) {
	out := map[ids.UUID]string{}
	if len(edges) == 0 {
		return out, nil
	}
	users := make([]ids.UUID, 0, len(edges))
	for _, e := range edges {
		users = append(users, e.UserID)
	}
	rows, err := tx.Query(ctx, `SELECT id, display_name FROM app_user WHERE id = ANY($1)`, users)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id ids.UUID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}
