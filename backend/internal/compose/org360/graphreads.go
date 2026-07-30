// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The connections graph's reads: one query per group, each behind its own
// grant and each pruned to that object's row scope. Nothing here decides what
// the card shows — that is graphplace.go — so a scope predicate added to a
// query reaches the picture without anyone remembering to update it.

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// readEmployment reads the account's current employees, pruned to the
// caller's person row scope. A person with more than one live employment row
// at the same account appears once — the lowest relationship id wins, so two
// reads of the same account report the same ROLE for them. (The title comes
// off the person row, so the dedupe cannot change it.)
//
// The read is bounded by graphScanCap, and the headcount rides the SAME
// statement as the rows. Two statements would each take their own Read
// Committed snapshot, so a concurrent hire between them could make
// dropped_count NEGATIVE — a response violating the contract's own
// `minimum: 0`. The contact ORDER is the §4 score, resolved after this read,
// so the bound cannot be a top-N LIMIT; graphScanCap says what that costs on
// an account past it.
func (g *graphAssembly) readEmployment() error {
	if err := auth.Require(g.ctx, "person", principal.ActionRead); err != nil {
		return err
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(g.orgID)
	personScope, err := scopeClause(g.ctx, "person", "p", arg)
	if err != nil {
		return err
	}
	rows, err := g.tx.Query(g.ctx, fmt.Sprintf(`
		WITH employed AS (
			SELECT p.id, p.full_name, p.title, r.role,
			       row_number() OVER (PARTITION BY p.id ORDER BY r.id) AS edge_rank
			FROM relationship r
			JOIN person p ON p.id = r.person_id AND p.archived_at IS NULL
			WHERE r.kind = 'employment' AND r.organization_id = $%d
			  AND r.ended_at IS NULL AND r.archived_at IS NULL AND (%s)
		)
		SELECT id, full_name, title, role, count(*) OVER () AS headcount
		FROM employed WHERE edge_rank = 1
		ORDER BY id
		LIMIT %d`, orgPos, personScope, graphScanCap), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var edge graphPersonEdge
		// Every row carries the same headcount; they agree because they come
		// from one statement.
		if err := rows.Scan(&edge.personID, &edge.fullName, &edge.title, &edge.role,
			&g.employeeTotal); err != nil {
			return err
		}
		g.employees = append(g.employees, edge)
	}
	return rows.Err()
}

// readOpenDeals reads the account's open deals and the stakeholder seats on
// them. It builds its query around openDealsWhere, the ONE spelling of "an
// open deal of this account that this caller may list" — so this card can
// never draw a deal the deals section would refuse to show.
//
// The seats are read behind their OWN person gate, inside readSeats: an edge
// names two records, and a caller who may not read people may not learn who
// sits on a deal either. A missing person grant leaves the seats out and the
// deals in, which is why that refusal is swallowed here rather than failing
// the deals group with it.
func (g *graphAssembly) readOpenDeals() error {
	if err := auth.Require(g.ctx, "deal", principal.ActionRead); err != nil {
		return err
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(g.orgID)
	dealScope, err := scopeClause(g.ctx, "deal", "d", arg)
	if err != nil {
		return err
	}
	// The order is SQL's, so LIMIT gives the true top N — this bound costs no
	// accuracy, unlike the contact scan above. The total rides the same
	// statement, for the same reason readEmployment's headcount does: two
	// snapshots could disagree and make dropped_count negative.
	rows, err := g.tx.Query(g.ctx, fmt.Sprintf(`
		SELECT d.id, d.name, s.name, d.amount_minor, count(*) OVER () AS open_total
		FROM deal d
		LEFT JOIN stage s ON s.id = d.stage_id AND s.workspace_id = d.workspace_id
		%s
		ORDER BY d.amount_minor DESC NULLS LAST, d.id
		LIMIT %d`, openDealsWhere(orgPos, dealScope), graphDealCap), args...)
	if err != nil {
		return err
	}
	g.openDeals, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (graphDeal, error) {
		var deal graphDeal
		err := row.Scan(&deal.dealID, &deal.name, &deal.stageName, &deal.amountMinor,
			&g.openDealTotal)
		return deal, err
	})
	if err != nil {
		return err
	}
	if err := g.readSeats(g.selectedDealIDs()); errors.Is(err, apperrors.ErrPermissionDenied) {
		// No person grant. The contacts group names that refusal, and the deals
		// still belong on the card — so this one is absorbed here rather than
		// failing the deals group along with it.
		return nil
	} else if err != nil {
		return err
	}
	return nil
}

// selectedDealIDs is the deal ids the graph will actually draw. Seats are
// read for those alone: a seat on a deal the cap dropped would be an edge
// with no node at its far end.
func (g *graphAssembly) selectedDealIDs() []ids.UUID {
	kept := g.keptDeals()
	out := make([]ids.UUID, len(kept))
	for i, deal := range kept {
		out[i] = deal.dealID
	}
	return out
}

// readSeats reads the stakeholder seats on the given deals. It carries its own
// PERSON gate rather than trusting the order the groups happen to run in: a
// seat names a person, so this is a person read, and a reordered group list
// must not be able to turn it into an ungated one.
func (g *graphAssembly) readSeats(dealIDs []ids.UUID) error {
	if err := auth.Require(g.ctx, "person", principal.ActionRead); err != nil {
		return err
	}
	if len(dealIDs) == 0 {
		return nil
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	dealsPos := arg(dealIDs)
	personScope, err := scopeClause(g.ctx, "person", "p", arg)
	if err != nil {
		return err
	}
	rows, err := g.tx.Query(g.ctx, fmt.Sprintf(`
		SELECT r.deal_id, p.id, p.full_name, p.title, r.role
		FROM relationship r
		JOIN person p ON p.id = r.person_id AND p.archived_at IS NULL
		WHERE r.kind = 'deal_stakeholder' AND r.deal_id = ANY($%d)
		  AND r.ended_at IS NULL AND r.archived_at IS NULL AND (%s)
		ORDER BY r.deal_id, p.id, r.id`, dealsPos, personScope), args...)
	if err != nil {
		return err
	}
	g.seats, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (graphSeat, error) {
		var seat graphSeat
		err := row.Scan(&seat.dealID, &seat.person.personID, &seat.person.fullName,
			&seat.person.title, &seat.role)
		return seat, err
	})
	return err
}

// readRouteIn reads the warm-intro path: the contact an active signal routes
// through, ranked by the warm room's own ranking so this card can never name a
// different person than the intro-path endpoint does.
//
// It asks for both of its own objects: the group exists only while there is a
// live signal to route, and the thing it places is a person.
func (g *graphAssembly) readRouteIn() error {
	if err := auth.Require(g.ctx, "signal", principal.ActionRead); err != nil {
		return err
	}
	if err := auth.Require(g.ctx, "person", principal.ActionRead); err != nil {
		return err
	}
	signalID, active, err := g.activeSignal()
	if err != nil {
		return err
	}
	if !active {
		// No open resolved signal on this account: there is no active path to
		// propose, which is not the same as a group the caller may not read.
		return nil
	}
	g.signalID = &signalID
	g.routeIn, err = signals.RouteInEdges(g.ctx, g.tx, g.orgID)
	return err
}

// activeSignal is the account's most recent open, resolved signal within the
// caller's signal row scope. The bool says whether there is one — an account
// with no active signal is an ordinary answer, not a zero id the caller has to
// recognize. Most recent by detection, id descending as the deterministic
// tie-break.
//
// "Belongs to this account" is the signals module's own predicate, not a local
// copy: a deal-subject signal belongs to its deal even when the resolver
// attributed it here, and a signal created directly about the organization has
// no resolved_org_id at all. A second spelling would let this card cite a
// signal the account's signal list refuses to show, and miss one it does.
func (g *graphAssembly) activeSignal() (ids.UUID, bool, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(g.orgID)
	signalScope, err := auth.SignalScopeClause(g.ctx, "s", arg)
	if err != nil {
		return ids.UUID{}, false, err
	}
	if signalScope == "" {
		signalScope = scopeAll
	}
	var id ids.UUID
	err = g.tx.QueryRow(g.ctx, fmt.Sprintf(`
		SELECT s.id FROM signal s
		WHERE %s AND s.status = 'open'
		  AND s.resolution_state = 'resolved' AND s.archived_at IS NULL AND (%s)
		ORDER BY s.detected_at DESC, s.id DESC
		LIMIT 1`, signals.OfOrganizationWhere(orgPos), signalScope), args...).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.UUID{}, false, nil
	}
	if err != nil {
		return ids.UUID{}, false, err
	}
	return id, true, nil
}

// scorePeople resolves §4 for every person the graph touches in ONE batch:
// the employees, the stakeholders, and the warm room's route-in candidates.
// One pass means one instant for every score, and it means the route-in
// ranking sees the same candidates the warm room ranks over.
//
// A person whose strength the caller's row scope did not resolve is simply
// absent from the map: their node carries no strength rather than a zero,
// and the route-in ranking drops them the way the warm room does.
func (g *graphAssembly) scorePeople() error {
	seen := map[ids.PersonID]bool{}
	var wanted []ids.PersonID
	add := func(personID ids.PersonID) {
		if seen[personID] {
			return
		}
		seen[personID] = true
		wanted = append(wanted, personID)
	}
	for _, edge := range g.employees {
		add(edge.personID)
	}
	for _, seat := range g.seats {
		add(seat.person.personID)
	}
	for _, candidate := range g.routeIn {
		add(candidate.PersonID)
	}
	if len(wanted) == 0 {
		// Either no group produced a person, or the caller may not read people
		// and every person read already refused. Both mean there is nothing to
		// score — and returning early keeps the people store's own grant check
		// from failing a whole graph that has already named contacts as
		// withheld.
		return nil
	}
	scored, err := people.StrengthForPeople(g.ctx, g.tx, wanted, g.now)
	if err != nil {
		return err
	}
	for _, contact := range scored {
		g.strengths[contact.PersonID] = contact.Strength
	}
	return nil
}

// readRelatedOrganizations reads the three hierarchy/partner arms as one
// ordered set. Each arm renders the organization row-scope predicate for its
// own alias and registers its own bind positions, the same shape the
// next-steps section uses for its two link sub-selects.
//
// The cap is applied in SQL, and it counts COMPANIES, because that is what
// graphOrgCap means. A row limit would count edges instead: one company holding
// many partner edges to this account would fill the budget and starve the
// others, drawing three companies where the card allows ten — and silently,
// since the total comes off the whole set either way. Choosing the companies
// first bounds the result properly too, at graphOrgCap × the distinct ways one
// company can attach.
//
// Distinct rows, for the same reason: the partner edges carry no uniqueness
// constraint, so the same relationship recorded twice would otherwise draw as
// two identical lines.
func (g *graphAssembly) readRelatedOrganizations() ([]graphRelatedOrg, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(g.orgID)
	parentScope, err := scopeClause(g.ctx, "organization", "o", arg)
	if err != nil {
		return nil, err
	}
	childScope, err := scopeClause(g.ctx, "organization", "o", arg)
	if err != nil {
		return nil, err
	}
	partnerScope, err := scopeClause(g.ctx, "organization", "o", arg)
	if err != nil {
		return nil, err
	}
	// The arms live in a CTE so the company count, the chosen companies and the
	// rows all come off ONE evaluation — a second copy of the union would be a
	// second chance for the total and the rows to disagree.
	rows, err := g.tx.Query(g.ctx, fmt.Sprintf(`
		WITH related AS (
			SELECT o.id, o.display_name, o.logo_object_key, '%[2]s' AS relation,
			       NULL::text AS partner_kind, NULL::uuid AS edge_owner
			FROM organization o
			JOIN organization root ON root.parent_org_id = o.id
			WHERE root.id = $%[1]d AND o.archived_at IS NULL AND (%[5]s)
			UNION ALL
			SELECT o.id, o.display_name, o.logo_object_key, '%[3]s', NULL::text, NULL::uuid
			FROM organization o
			WHERE o.parent_org_id = $%[1]d AND o.archived_at IS NULL AND (%[6]s)
			UNION ALL
			SELECT o.id, o.display_name, o.logo_object_key, '%[4]s', r.kind, r.organization_id
			FROM relationship r
			JOIN organization o ON o.id = CASE WHEN r.organization_id = $%[1]d
			                                   THEN r.counterparty_org_id ELSE r.organization_id END
			WHERE r.kind IN ('partner_of','referred_by','co_sell_with')
			  AND r.archived_at IS NULL AND r.ended_at IS NULL
			  AND $%[1]d IN (r.organization_id, r.counterparty_org_id)
			  AND o.archived_at IS NULL AND (%[7]s)
		), companies AS (
			SELECT DISTINCT id, display_name, logo_object_key FROM related
		), chosen AS (
			SELECT id FROM companies ORDER BY display_name, id LIMIT %[8]d
		)
		SELECT DISTINCT related.id, related.display_name, related.logo_object_key, related.relation,
		       related.partner_kind, related.edge_owner,
		       (SELECT count(*) FROM companies)
		FROM related JOIN chosen ON chosen.id = related.id
		ORDER BY related.display_name, related.id, related.relation`,
		orgPos, graphRelationParent, graphRelationChild, graphRelationPartner,
		parentScope, childScope, partnerScope, graphOrgCap), args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (graphRelatedOrg, error) {
		var related graphRelatedOrg
		// Every row carries the same total; the last write wins and they agree.
		err := row.Scan(&related.orgID, &related.displayName, &related.logoObjectKey, &related.relation,
			&related.partnerKind, &related.edgeOwner, &g.relatedTotal)
		return related, err
	})
}
