// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// Our side of the account: the colleague who owns it, and the colleagues who
// have actually dealt with its people. Kept apart from the account-side reads
// in graphreads.go because it answers the mirror-image question — not who
// works there, but who here already has a way in — and because it is the one
// group whose candidates depend on which contacts the card ended up drawing.

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// readOurSide reads who on OUR side is connected to the account: the member
// who owns it, and the colleagues who have actually been in touch with its
// people. Without it the card answers "who works there" and leaves out the
// half a rep opens it for — which of us already has a way in.
//
// It carries BOTH its gates itself, and asks for them up front rather than per
// edge, because the group is one answer: an interaction edge names one of the
// account's contacts, so the group is a person read, and every one of those
// edges is derived from an activity, so it is an activity read too. The owner
// edge names the organization the caller is already reading and a colleague
// from the workspace roster, so it needs neither grant of its own — it is held
// to the same pair because a partial answer to "who here is connected" is a
// misleading one.
//
// Neither gate is inferred from whether another group reported itself omitted:
// a group list reordered for any reason must not be able to turn a gated read
// into an ungated one.
func (g *graphAssembly) readOurSide() error {
	if err := auth.Require(g.ctx, "person", principal.ActionRead); err != nil {
		return err
	}
	if err := auth.Require(g.ctx, "activity", principal.ActionRead); err != nil {
		return err
	}
	if err := g.readAccountOwner(); err != nil {
		return err
	}
	return g.readInContactWith(g.drawnContactIDs())
}

// drawnContactIDs is the contacts the card has already PLACED — the set the
// interaction read correlates against, and therefore the set its user cap is
// chosen over.
//
// Correlating on already-placed rows is what keeps the graph one hop: an
// interaction with someone who does not work at this account is not a
// connection INTO it. It is also what keeps our_side and its dropped_count
// describing the people the card actually shows. Correlating against the
// contacts merely READ would let a colleague whose only contact the contact cap
// dropped take a user slot, be discarded again at placement for having no
// surviving edge, and push a colleague of a displayed contact out of the graph.
func (g *graphAssembly) drawnContactIDs() []ids.UUID {
	var out []ids.UUID
	for _, node := range g.out.Nodes {
		if node.Kind == crmcontracts.OrganizationGraphNodeKindPerson {
			out = append(out, ids.UUID(node.Id))
		}
	}
	return out
}

// liveMemberWhere is the ONE spelling of "someone who still works here", for a
// query that aliases app_user as u. It is the same pair the roster (GET /users)
// lists by, so this card can never name a colleague the roster has already
// dropped.
//
// Both halves matter, and neither implies the other: a suspended or deactivated
// account keeps archived_at NULL, and owner_id and captured_by both survive the
// day someone leaves. Every user node the graph draws renders this predicate —
// the card exists to tell a rep who to ask for an intro, and a former colleague
// is not an answer to that question.
const liveMemberWhere = `u.status = 'active' AND u.archived_at IS NULL`

// readAccountOwner reads the live workspace member the account is assigned to.
// It needs no row-scope clause of its own: the caller has already read the
// organization row this owner_id comes off, and the roster (GET /users) is
// readable by any authenticated member, so naming the owner discloses nothing
// the account page does not.
//
// An owner who no longer works here leaves the account simply unowned: the
// group keeps its other edges and only the owns edge is missing.
func (g *graphAssembly) readAccountOwner() error {
	var owner graphUser
	err := g.tx.QueryRow(g.ctx, `
		SELECT u.id, u.display_name
		FROM organization o
		JOIN app_user u ON u.id = o.owner_id AND `+liveMemberWhere+`
		WHERE o.id = $1`, g.orgID).Scan(&owner.userID, &owner.displayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // an unassigned account, or an owner who has left
	}
	if err != nil {
		return err
	}
	g.accountOwner = &owner
	return nil
}

// readInContactWith reads which colleagues have REAL recorded contact with the
// account's people, and with whom.
//
// It reads the interaction projection (CG-DDL-1), which is folded from the
// participant rows capture stamps — who was actually IN each conversation.
//
// What it replaced is worth stating, because the failure was invisible. This
// used to match `a.captured_by = 'human:' || u.id::text` on the activity row.
// Connector-captured mail is stamped `connector:gmail`, so the overwhelming
// majority of a real workspace's timeline matched nothing and this group came
// back empty — on precisely the accounts with the most correspondence. A rep
// opening a busy account saw "nobody here has been in touch", and there was no
// error anywhere to suggest otherwise.
//
// Real contact still means a real exchange: the projection counts only direct
// participant roles, so a cc never becomes a connection, and a task or a note
// is not an interaction at all.
//
// On gating: the contacts passed in are the ones the card has already PLACED,
// which means they have already passed the person row-scope gate — and capture
// privacy with it, so an unpromoted contact cannot appear here. The old query
// additionally carried the activity scope clause; it is subsumed rather than
// dropped. An activity's visibility derives from its links, so an activity
// linked to a person the caller can see is one the caller can read under the
// same any-link rule the timeline uses. There is no edge here whose underlying
// activity the caller could not open.
//
// The cap picks distinct USERS — most recent contact first — and the
// distinct-user total rides the SAME statement as the rows. Two statements
// would each take their own snapshot, so a concurrent interaction between them
// could make dropped_count NEGATIVE, a response violating the contract's own
// `minimum: 0`.
func (g *graphAssembly) readInContactWith(contactIDs []ids.UUID) error {
	if len(contactIDs) == 0 {
		return nil // no contacts to have been in touch with
	}
	rows, err := g.tx.Query(g.ctx, fmt.Sprintf(`
		WITH touch AS (
			SELECT u.id AS user_id, u.display_name, e.person_id, e.last_at
			FROM graph_interaction_edge e
			JOIN app_user u ON u.id = e.user_id AND `+liveMemberWhere+`
			WHERE e.person_id = ANY($1)
		), colleagues AS (
			SELECT user_id, max(last_at) AS last_touch FROM touch GROUP BY user_id
		), chosen AS (
			SELECT user_id FROM colleagues ORDER BY last_touch DESC, user_id LIMIT %d
		)
		SELECT touch.user_id, touch.display_name, touch.person_id,
		       (SELECT count(*) FROM colleagues)
		FROM touch JOIN chosen ON chosen.user_id = touch.user_id
		ORDER BY touch.user_id, touch.person_id`, graphUserCap), contactIDs)
	if err != nil {
		return err
	}
	g.ourSide, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ourSideEdge, error) {
		var edge ourSideEdge
		// Every row carries the same total; they agree because they come from
		// one statement.
		err := row.Scan(&edge.user.userID, &edge.user.displayName, &edge.personID, &g.ourSideTotal)
		return edge, err
	})
	return err
}

// readRouteIn reads the account's most recent open signal and the contact
// edges the warm room would rank as ways in. It asks the signals module for
// the candidates rather than gathering them here: the warm/cold join owns
// what "anchors this account" means, and a second spelling would let the
// card and the warm room propose different people to ask for an intro.
//
// The intro path names a person, so it needs the person grant as well as the
// signal one. Both are asked here, not inferred from whether the contacts
// group happened to run first — a group list reordered for any reason must not
// be able to name a contact to a caller who may not read people.
