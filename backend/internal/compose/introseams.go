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
	"errors"
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
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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
	return func(ctx context.Context, orgID ids.UUID) ([]agents.IntroRoute, bool, error) {
		var out []agents.IntroRoute
		var truncated bool
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
			// The person grant, taken BEFORE the read rather than inferred from
			// whether it returned anything. Without it an account with no
			// visible contacts and an account this caller may not read people
			// at all answer identically — and the difference between those two
			// is itself a fact about the account.
			if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
				return err
			}
			contacts, err := accountContacts(ctx, tx, orgID)
			if err != nil {
				return err
			}
			// The fetch is bounded and the ranking happens after it, so an
			// account bigger than the bound contributes only its first slice by
			// id. The caller is told rather than left to assume the ranking saw
			// everything.
			contacts, truncated = trimToFetchBound(contacts)
			if len(contacts) == 0 {
				return nil
			}
			out, err = rankIntroRoutes(ctx, tx, contacts)
			return err
		})
		return out, truncated, err
	}
}

// rankIntroRoutes turns the account's contacts into warmest-first routes.
//
// Over-fetch, rank, THEN cap — the order every warmth-ranked read here takes,
// because the score is computed after the read and capping first would cut by
// last contact instead of by warmth.
func rankIntroRoutes(ctx context.Context, tx pgx.Tx, contacts []accountContact) ([]agents.IntroRoute, error) {
	people := make([]ids.UUID, 0, len(contacts))
	names := make(map[ids.UUID]string, len(contacts))
	for _, contact := range contacts {
		people = append(people, contact.id)
		names[contact.id] = contact.name
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
	// The colleague names, which are a different lookup from the CONTACT names
	// gathered above: one side of a route is a workspace member, the other is
	// the account's employee.
	members, err := search.MemberNames(ctx, tx, edges)
	if err != nil {
		return nil, err
	}
	out := make([]agents.IntroRoute, 0, len(edges))
	for _, e := range edges {
		score := e.StrengthOf(now)
		route := agents.IntroRoute{
			UserID: e.UserID, DisplayName: members[e.UserID],
			PersonID: e.PersonID, PersonName: names[e.PersonID],
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

// trimToFetchBound cuts one over-fetched row back to the bound and answers
// whether there was one.
//
// Reading ONE row past the bound is what makes the answer honest at the
// boundary: the bound itself is not evidence of anything, so an account holding
// exactly accountContactFetch contacts had none of them dropped — and telling a
// model that warmer routes exist outside a complete list is the same false
// claim as hiding a cap, in the other direction. The row dropped is the last by
// id, which is the one the ORDER BY put past the bound.
func trimToFetchBound(contacts []accountContact) ([]accountContact, bool) {
	if len(contacts) <= accountContactFetch {
		return contacts, false
	}
	return contacts[:accountContactFetch], true
}

// accountContact is one live employee of the account, in the id order the fetch
// bound is applied over.
type accountContact struct {
	id   ids.UUID
	name string
}

// accountContacts reads the account's live employees under the caller's person
// row scope, in id order, reading one row past the bound so the caller can tell
// a full account from a cut one.
func accountContacts(ctx context.Context, tx pgx.Tx, orgID ids.UUID) ([]accountContact, error) {
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
		   -- Still employed TODAY. A future end date is still employment: a
		   -- person leaving next month can still make an introduction this
		   -- week, and the departure rule in compose/network already treats
		   -- that row as live. Two spellings would let one surface call them
		   -- gone while the other calls them current.
		   AND r.archived_at IS NULL
		   AND (r.ended_at IS NULL OR r.ended_at > current_date)
		   AND (%s)
		 ORDER BY p.id LIMIT %d`, orgPos, visible, accountContactFetch+1), args...)
	if err != nil {
		return nil, fmt.Errorf("compose: reading an account's contacts for an intro route: %w", err)
	}
	defer rows.Close()
	out := make([]accountContact, 0, accountContactFetch+1)
	for rows.Next() {
		var contact accountContact
		if err := rows.Scan(&contact.id, &contact.name); err != nil {
			return nil, err
		}
		out = append(out, contact)
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
		openStatus := network.DealStatusOpen
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
// The visibility gate is re-taken HERE, not inherited from the ListDeals that
// produced the candidate. That list ran in an earlier transaction, and a grant
// revoked in between would otherwise let this sweep report a deal's name and
// freshly computed risks to somebody who had just lost the right to read it.
// CoverageFor does not gate on its own — its first read is an unrestricted deal
// row — so nothing else in this path would have caught it.
func dealAtRisk(ctx context.Context, tx pgx.Tx, d crmcontracts.Deal, now time.Time) (agents.AtRiskDeal, bool, error) {
	if err := requireVisibleDeal(ctx, tx, ids.UUID(d.Id)); err != nil {
		if errors.Is(err, apperrors.ErrNotFound) || errors.Is(err, apperrors.ErrPermissionDenied) {
			// The grant went away between the list and this read. Dropping the
			// deal is the right answer; failing the whole sweep would turn one
			// revoked grant into a broken tool.
			return agents.AtRiskDeal{}, false, nil
		}
		return agents.AtRiskDeal{}, false, err
	}
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
