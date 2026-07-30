// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The connections card: the account's one-hop neighbourhood as an explicit
// node/edge set the client lays out. It is the same page as the 360 and the
// same posture — one transaction, one instant, per-group authorization, and
// a cap that reports what it left out — but a separate read, because a
// client that wants the profile does not always want the graph and the
// layout is the browser's work, never the server's.
//
// One hop means one edge from the account. A contact's other employers, a
// deal's other accounts and a partner's own partners are NOT walked: the
// second hop is a different read with a different cost, and a card that
// sometimes went two hops would have no honest cap.

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
)

// How many nodes of each capped group one graph carries. The card is a
// picture a rep reads at a glance, so the caps are what fits one; the
// endpoints that own each collection serve the whole list.
//
// Stakeholder contacts have no cap of their own: they arrive with the deals
// already capped above, so the deal cap bounds them.
const (
	graphContactCap = 15
	graphDealCap    = 10
	graphOrgCap     = 10
)

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
			Id:    openapi_types.UUID(orgID.UUID),
			Kind:  crmcontracts.OrganizationGraphNodeKindOrganization,
			Label: org.DisplayName,
			Root:  true,
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

	omitted   map[crmcontracts.OrganizationGraphGroupsOmitted]bool
	employees []graphPersonEdge
	openDeals []graphDeal
	seats     []graphSeat
	routeIn   []signals.RouteInEdge
	signalID  *ids.UUID
	strengths map[ids.PersonID]people.RelationshipStrength
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

// build reads every group, scores the people once, then places the nodes.
func (g *graphAssembly) build() error {
	g.omitted = map[crmcontracts.OrganizationGraphGroupsOmitted]bool{}
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
	if err := g.scorePeople(); err != nil {
		return err
	}
	g.placeContacts()
	g.placeDeals()
	if err := g.placeRelatedOrganizations(); err != nil {
		return err
	}
	g.markIntroPath()
	return nil
}

// group runs one group's read and records it as omitted when the caller's
// grants refuse it. Any other error fails the whole graph, because a group
// that broke for a real reason must never be reported as one the caller may
// not see.
func (g *graphAssembly) group(name crmcontracts.OrganizationGraphGroupsOmitted, read func() error) error {
	err := read()
	if errors.Is(err, apperrors.ErrPermissionDenied) {
		g.out.GroupsOmitted = append(g.out.GroupsOmitted, name)
		g.omitted[name] = true
		return nil
	}
	return err
}
