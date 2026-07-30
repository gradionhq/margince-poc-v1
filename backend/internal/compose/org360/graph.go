// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The connections card. What it is and why it is a second read is in doc.go;
// this file is the one-hop rule and the assembly that enforces it.
//
// One hop means one edge from the account. A contact's other employers, a
// deal's other accounts and a partner's own partners are NOT walked: the
// second hop is a different read with a different cost, and a card that
// sometimes went two hops would have no honest cap.
//
// The layout is the browser's work, never the server's — this returns the set,
// not a picture.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The group names, spelled once. They are the contract's groups_omitted
// vocabulary and the keys the assembly reasons about, so a rename cannot
// leave the two halves disagreeing.
//
// There is no group for the parent, child and partner organizations: they
// need no grant beyond the organization read the whole endpoint demands, so
// they are row-scope pruned like every other node and can never be withheld
// wholesale. A value nothing can ever emit would be vocabulary a client had
// to handle and would never see.
const (
	graphGroupContacts  = crmcontracts.OrganizationGraphGroupsOmitted("contacts")
	graphGroupDeals     = crmcontracts.OrganizationGraphGroupsOmitted("deals")
	graphGroupIntroPath = crmcontracts.OrganizationGraphGroupsOmitted("intro_path")
	graphGroupOurSide   = crmcontracts.OrganizationGraphGroupsOmitted("our_side")
)

// How many nodes of each capped group one graph carries. The card is a
// picture a rep reads at a glance, so the caps are what fits one; the
// endpoints that own each collection serve the whole list.
//
// Stakeholder contacts have no cap of their own: they arrive with the deals
// already capped above, so the deal cap bounds them.
// graphUserCap counts USERS, not edges: one teammate can have emailed five of
// the account's contacts, and a row budget would let them fill it while the
// nine other colleagues who touched the account went undrawn. It is chosen in
// SQL for the same reason graphOrgCap is.
const (
	graphContactCap = 15
	graphDealCap    = 10
	graphOrgCap     = 10
	graphUserCap    = 10
)

// graphScanCap bounds the ONE read whose display order this code cannot push
// into SQL: contacts are ordered by the §4 score, which is resolved after the
// rows come back, so their slice cannot be a top-N LIMIT.
//
// What the cap bounds is the rows RETURNED and the work done per row — the §4
// fold in people.StrengthForPeople, which joins activity and activity_link per
// contact, and every node this package then builds. Both used to grow with the
// account.
//
// It does NOT bound the count: the headcount rides the same statement (see
// readEmployment for why it must), and counting means reading one account's
// employment rows through idx_rel_org_people. That cost is deliberate. An exact
// dropped_count is the contract's promise — a truncated graph reporting no
// count reads as the whole neighbourhood — and one index range scan per account
// is what keeping it exact costs.
//
// On an account past the bound the card shows the strongest of the first
// graphScanCap contacts by id rather than of all of them, which the contract
// states; at 500 against a display cap of 15 that is an account nobody has.
//
// The other two groups need no such bound. Deals are ordered in SQL, and the
// related organizations are capped in SQL by COMPANY — a row bound there would
// let one company's many partner edges starve the others.
const graphScanCap = 500

// Graph reads the account's one-hop connections inside ONE workspace
// transaction. The organization read is mandatory and its refusal is the
// whole read's refusal; every other group is attempted, and a group refused
// for lack of a grant is omitted and named rather than returned empty.
func (s *Service) Graph(ctx context.Context, orgID ids.OrganizationID) (crmcontracts.OrganizationGraph, error) {
	now := s.now().UTC()
	out := crmcontracts.OrganizationGraph{
		AsOf:          now,
		RootId:        openapi_types.UUID(orgID.UUID),
		Nodes:         []crmcontracts.OrganizationGraphNode{},
		Edges:         []crmcontracts.OrganizationGraphEdge{},
		GroupsOmitted: []crmcontracts.OrganizationGraphGroupsOmitted{},
	}
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		org, err := s.people.GetOrganizationTx(ctx, tx, orgID, storekit.LiveOnly)
		if err != nil {
			return err
		}
		g := &graphAssembly{
			ctx: ctx, tx: tx, orgID: orgID, now: now, out: &out,
			nodeIndex: map[ids.UUID]int{},
			strengths: map[ids.PersonID]people.RelationshipStrength{},
		}
		g.addNode(crmcontracts.OrganizationGraphNode{
			Id:      openapi_types.UUID(orgID.UUID),
			Kind:    crmcontracts.OrganizationGraphNodeKindOrganization,
			Label:   org.DisplayName,
			Root:    true,
			LogoUrl: org.LogoUrl,
		})
		return g.build()
	})
	if err != nil {
		return crmcontracts.OrganizationGraph{}, err
	}
	return out, nil
}

// graphAssembly is one graph's working state.
//
// It reads in two passes on purpose. Every person the graph touches is
// scored in ONE batch, and the score decides both the contact order and
// which contact the warm-intro path routes through — so every person edge
// has to be known before any of them can be placed. Scoring per group would
// mean two passes over the same §4 inputs and a route-in ranking taken over
// a subset of the candidates the warm room ranks over, which is exactly how
// the card would come to name a different person than the warm room.
type graphAssembly struct {
	ctx   context.Context
	tx    pgx.Tx
	orgID ids.OrganizationID
	now   time.Time
	out   *crmcontracts.OrganizationGraph

	// nodeIndex maps a record id to its position in out.Nodes, so a person
	// who is both an employee and a stakeholder is one node with two edges.
	nodeIndex map[ids.UUID]int

	employees []graphPersonEdge
	openDeals []graphDeal
	seats     []graphSeat
	routeIn   []signals.RouteInEdge
	signalID  *ids.UUID
	strengths map[ids.PersonID]people.RelationshipStrength

	// Our side of the account: who owns it, and which colleagues have actually
	// been in contact with its people.
	accountOwner *graphUser
	ourSide      []ourSideEdge

	// The true size of each capped group, counted over the same predicate the
	// read used. dropped_count is derived from these rather than from the rows
	// in hand, so a read bounded by graphScanCap still reports the whole
	// remainder instead of only the part it happened to fetch.
	employeeTotal int
	openDealTotal int
	relatedTotal  int
	// ourSideTotal counts the colleagues WITH RECORDED CONTACT only. The
	// account owner is read separately and is never capped, so counting them
	// here would let dropped_count fall below zero once the owner is drawn.
	ourSideTotal int
}

// graphPersonEdge is one employment edge: who, and what they do here.
type graphPersonEdge struct {
	personID ids.PersonID
	fullName string
	title    *string
	role     *string
}

// graphDeal is one open deal of the account, with the figure it is ordered by.
type graphDeal struct {
	dealID      ids.UUID
	name        string
	stageName   *string
	amountMinor *int64
}

// graphSeat is one stakeholder seat: a person on one of the account's deals.
type graphSeat struct {
	dealID ids.UUID
	person graphPersonEdge
	role   *string
}

// graphUser is one member of THIS workspace — someone on our side of the
// account, carrying only the name a colleague is recognized by.
type graphUser struct {
	userID      ids.UUID
	displayName string
}

// ourSideEdge is one colleague's recorded contact with one of the account's
// people: who on our side, and whom they were in touch with.
type ourSideEdge struct {
	user     graphUser
	personID ids.UUID
}

// build reads the account's own groups, scores the people once, places them,
// and only then reads our side of the account against the contacts it placed.
//
// Every gate is asked inside the read it belongs to, so the order below decides
// which group is NAMED first in groups_omitted and which rows our side is
// correlated against — never what a caller is allowed to see. The related
// organizations are read outside the group loop, because they have no grant of
// their own to be refused for.
func (g *graphAssembly) build() error {
	for _, group := range []struct {
		name crmcontracts.OrganizationGraphGroupsOmitted
		read func() error
	}{
		{graphGroupContacts, g.readEmployment},
		{graphGroupDeals, g.readOpenDeals},
		{graphGroupIntroPath, g.readRouteIn},
	} {
		if err := g.group(group.name, group.read); err != nil {
			return err
		}
	}
	related, err := g.readRelatedOrganizations()
	if err != nil {
		return err
	}
	if err := g.scorePeople(); err != nil {
		return err
	}
	g.placeContacts()
	g.placeDeals()
	g.placeRelated(related)
	// Our side reads LAST, after the contact nodes exist. Its user cap picks
	// distinct colleagues in SQL, so the set it picks over has to be final: run
	// against the contacts merely READ, the cap could spend a slot on a
	// colleague whose only contact the contact cap then dropped, and
	// placeOurSide would discard them again — leaving our_side and its
	// dropped_count describing people the graph does not show.
	if err := g.group(graphGroupOurSide, g.readOurSide); err != nil {
		return err
	}
	g.placeOurSide()
	g.markIntroPath()
	return nil
}

// group runs one group's read and NAMES it as omitted when the caller's grants
// refuse it. Any other error fails the whole graph, because a group that broke
// for a real reason must never be reported as one the caller may not see.
//
// The refusal is only ever reported here. No read decides its own gate from
// whether another group was refused — each asks auth.Require itself — so this
// records an outcome rather than holding state the reads consult.
func (g *graphAssembly) group(name crmcontracts.OrganizationGraphGroupsOmitted, read func() error) error {
	err := read()
	if errors.Is(err, apperrors.ErrPermissionDenied) {
		g.out.GroupsOmitted = append(g.out.GroupsOmitted, name)
		return nil
	}
	return err
}
