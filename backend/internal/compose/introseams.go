// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The seams behind intro_path_to and at_risk_relationships (ADR-0078).
//
// Both answer a question that spans modules — a route in crosses employment,
// the interaction projection and the roster; a risk sweep crosses deals and
// coverage — so both are composed here, and neither module learns about the
// other.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/network"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/relstrength"
)

// introRouteCap bounds what the tool hands a model. A rep asks this to get one
// name; a list of forty routes is a list they have to re-rank themselves, and a
// model given one picks arbitrarily from the top.
const introRouteCap = 5

// accountContactFetch bounds the contacts read before the routes are ranked.
// Generous relative to the cap for the reason every warmth-ranked read in this
// codebase over-fetches: the score is computed after the read, so a tight fetch
// would cut by employment id and evict the warmest contact at the account.
const accountContactFetch = 200

// introPathLister answers "who here can get me into this account".
//
// The two-hop join ADR-0021 pins, and it is fixed by construction rather than
// by a depth parameter: colleague → contact (the interaction projection) →
// account (employment). There is no third hop and no recursion, so the cost is
// the account's contact count and nothing about the shape of the graph beyond
// it.
func introPathLister(pool *pgxpool.Pool) agents.IntroPathLister {
	return func(ctx context.Context, orgID ids.UUID) ([]agents.IntroRoute, error) {
		var out []agents.IntroRoute
		err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
			// The account gate first. A route names the account's people, so a
			// caller who cannot read the account must not learn who works
			// there — through a tool any more than through a URL.
			if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
				return err
			}
			// Live probe, matching every other single-record read: EnsureVisible
			// skips its existence check for an unbounded caller, so an unknown
			// id would answer "no routes" instead of a refusal, and "no routes"
			// is a believable answer that hides a 404.
			if err := auth.EnsureVisibleLive(ctx, tx, "organization", orgID); err != nil {
				return err
			}
			contacts, err := accountContacts(ctx, tx, orgID)
			if err != nil {
				return err
			}
			if len(contacts) == 0 {
				return nil
			}
			out, err = rankIntroRoutes(ctx, tx, contacts)
			return err
		})
		return out, err
	}
}

// rankIntroRoutes turns the account's contacts into warmest-first routes.
//
// Over-fetch, rank, THEN cap — the order every warmth-ranked read here takes,
// because the score is computed after the read and capping first would cut by
// last contact instead of by warmth.
func rankIntroRoutes(ctx context.Context, tx pgx.Tx, contacts map[ids.UUID]string) ([]agents.IntroRoute, error) {
	people := make([]ids.UUID, 0, len(contacts))
	for id := range contacts {
		people = append(people, id)
	}
	// EdgesForPeople takes the person grant; the contact set it is given
	// already passed the person row scope in accountContacts, so an unpromoted
	// captured contact never becomes a route.
	edges, err := search.EdgesForPeople(ctx, tx, people)
	if err != nil {
		return nil, err
	}
	now := clockNow()
	search.SortByStrength(edges, now)
	if len(edges) > introRouteCap {
		edges = edges[:introRouteCap]
	}
	names, err := networkUserNames(ctx, tx, edges)
	if err != nil {
		return nil, err
	}
	out := make([]agents.IntroRoute, 0, len(edges))
	for _, e := range edges {
		score := e.StrengthOf(now)
		route := agents.IntroRoute{
			UserID: e.UserID, DisplayName: names[e.UserID],
			PersonID: e.PersonID, PersonName: contacts[e.PersonID],
			StrengthBucket: score.Bucket, Interactions90d: e.Count90d,
		}
		// A `none` band carries NO number: never spoken and spoken-then-cold
		// are different facts, and a zero renders them identically.
		if score.Bucket != relstrength.BucketNone {
			strength := score.Strength
			route.Strength = &strength
		}
		out = append(out, route)
	}
	return out, nil
}

// accountContacts reads the account's live employees under the caller's person
// row scope, keyed by id so a route can be named without a second read.
func accountContacts(ctx context.Context, tx pgx.Tx, orgID ids.UUID) (map[ids.UUID]string, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	scope, err := auth.ScopeClauseFor(ctx, "person", "p", arg)
	if err != nil {
		return nil, err
	}
	visible := "true"
	if scope != "" {
		visible = scope
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT DISTINCT p.id, p.full_name
		  FROM relationship r
		  JOIN person p ON p.id = r.person_id AND p.archived_at IS NULL
		 WHERE r.kind = 'employment' AND r.organization_id = $%d
		   AND r.archived_at IS NULL AND r.ended_at IS NULL
		   AND (%s)
		 ORDER BY p.id LIMIT %d`, orgPos, visible, accountContactFetch), args...)
	if err != nil {
		return nil, fmt.Errorf("compose: reading an account's contacts for an intro route: %w", err)
	}
	defer rows.Close()
	out := map[ids.UUID]string{}
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

// atRiskScanLimit bounds how many open deals one sweep assesses. Coverage is
// several reads per deal, so this is a real cost and not a display cap — which
// is exactly why the answer reports the number scanned and whether it was cut
// short, rather than presenting a partial sweep as a clean pipeline.
const atRiskScanLimit = 25

// atRiskLister sweeps the caller's open deals for coverage findings.
//
// The candidate set comes from the deals module's own row-scoped list, so the
// sweep sees precisely the book the caller sees. Deals are assessed in the
// order that list returns them, and the sweep stops at the cap rather than
// sampling: a deterministic prefix can be explained to a rep, a sample cannot.
func atRiskLister(pool *pgxpool.Pool) agents.AtRiskLister {
	store := deals.NewStore(pool)
	return func(ctx context.Context) (agents.AtRiskReport, error) {
		var out agents.AtRiskReport
		openStatus := "open"
		// One over the cap, so "there was more" is observed rather than
		// inferred from a full page — a page that happens to be exactly full is
		// not evidence of a remainder.
		limit := atRiskScanLimit + 1
		open, _, err := store.ListDeals(ctx, deals.ListDealsInput{Status: &openStatus, Limit: &limit})
		if err != nil {
			return out, err
		}
		if len(open) > atRiskScanLimit {
			out.Truncated = true
			open = open[:atRiskScanLimit]
		}
		out.DealsScanned = len(open)
		err = database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
			now := clockNow()
			for _, d := range open {
				at, flagged, err := dealAtRisk(ctx, tx, d, now)
				if err != nil {
					return err
				}
				if flagged {
					out.Deals = append(out.Deals, at)
				}
			}
			return nil
		})
		return out, err
	}
}

// dealAtRisk assesses one deal. The bool says whether anything is wrong with
// it — a healthy deal is not an error and not a zero value the caller has to
// recognise.
//
// Every deal in the sweep came from the caller's own row-scoped list, so
// CoverageFor's own gates re-confirm rather than establish visibility.
func dealAtRisk(ctx context.Context, tx pgx.Tx, d crmcontracts.Deal, now time.Time) (agents.AtRiskDeal, bool, error) {
	coverage, err := network.CoverageFor(ctx, tx, ids.From[ids.DealKind](ids.UUID(d.Id)), now)
	if err != nil {
		return agents.AtRiskDeal{}, false, err
	}
	if len(coverage.Risks) == 0 {
		return agents.AtRiskDeal{}, false, nil
	}
	return agents.AtRiskDeal{
		DealID: ids.UUID(d.Id), Name: d.Name, Risks: toAgentRisks(coverage.Risks),
	}, true, nil
}
