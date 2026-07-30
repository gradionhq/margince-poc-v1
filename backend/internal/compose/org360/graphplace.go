// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// Placing the read rows as nodes and edges: the deterministic order each
// group is drawn in, the caps and what they report, the direction every edge
// runs, and which contact the warm-intro path marks. No SQL here — every rule
// below is provable from already-read rows (graph_test.go), which is why the
// caps and the edge orientation are testable without a database.

import (
	"sort"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// placeContacts adds the employees, strongest relationship first with person
// id as the tie-break, and counts the ones the cap left out.
//
// A contact whose strength the read did not resolve sorts strictly LAST,
// behind one measured at zero. The two are different facts — "we have no
// relationship here" against "we could not measure one" — and a bare map read
// would collapse them, because a missing entry and a stored 0 look the same.
func (g *graphAssembly) placeContacts() {
	kept := make([]graphPersonEdge, len(g.employees))
	copy(kept, g.employees)
	sort.SliceStable(kept, func(i, j int) bool {
		left, leftMeasured := g.strengths[kept[i].personID]
		right, rightMeasured := g.strengths[kept[j].personID]
		if leftMeasured != rightMeasured {
			return leftMeasured
		}
		if left.Strength != right.Strength {
			return left.Strength > right.Strength
		}
		return kept[i].personID.String() < kept[j].personID.String()
	})
	if len(kept) > graphContactCap {
		kept = kept[:graphContactCap]
	}
	// Counted against the account's true headcount, not against the rows this
	// read fetched: the read is bounded (graphScanCap) and a count taken from
	// what it happened to hold would understate a large account.
	g.out.DroppedCount += g.employeeTotal - len(kept)
	for _, edge := range kept {
		g.addPersonNode(edge)
		g.addEdge(g.orgID.UUID, edge.personID.UUID,
			crmcontracts.OrganizationGraphEdgeKindEmployment, edge.role)
	}
}

// keptDeals is the ONE spelling of "the open deals this graph draws" — the
// first graphDealCap of the amount-ordered read. Both readers go through it:
// the seat read, which must ask about exactly the deals that will be drawn,
// and the placement below. Two copies of that slice rule would let seats be
// read for one set and edges drawn for another, and the dangling-edge guard
// would hide the drift.
func (g *graphAssembly) keptDeals() []graphDeal {
	if len(g.openDeals) > graphDealCap {
		return g.openDeals[:graphDealCap]
	}
	return g.openDeals
}

// placeDeals adds the open deals in amount order and, under each, the
// stakeholder seats on it. A seat whose person the caller cannot read never
// arrived, so no edge here can dangle.
func (g *graphAssembly) placeDeals() {
	kept := g.keptDeals()
	g.out.DroppedCount += g.openDealTotal - len(kept)
	drawn := map[ids.UUID]bool{}
	for _, deal := range kept {
		drawn[deal.dealID] = true
		g.addNode(crmcontracts.OrganizationGraphNode{
			Id:     openapi_types.UUID(deal.dealID),
			Kind:   crmcontracts.OrganizationGraphNodeKindDeal,
			Label:  deal.name,
			Detail: deal.stageName,
		})
		g.addEdge(g.orgID.UUID, deal.dealID, crmcontracts.OrganizationGraphEdgeKindHasDeal, nil)
	}
	for _, seat := range g.seats {
		if !drawn[seat.dealID] {
			continue
		}
		g.addPersonNode(seat.person)
		g.addEdge(seat.dealID, seat.person.personID.UUID,
			crmcontracts.OrganizationGraphEdgeKindDealStakeholder, seat.role)
	}
}

// graphRelatedOrg is one organization one hop away, and how it is attached.
type graphRelatedOrg struct {
	orgID       ids.UUID
	displayName string
	// relation is the arm that found it: parent, child, or partner.
	relation string
	// partnerKind is the relationship kind on a partner arm, and edgeOwner
	// is the org that records that edge — the edge points from it to its
	// counterparty, which is a direction the two arms do not share.
	partnerKind *string
	edgeOwner   *ids.UUID
}

// The relation arms, spelled once so the SQL labels and the edge builder
// cannot drift apart.
const (
	graphRelationParent  = "parent"
	graphRelationChild   = "child"
	graphRelationPartner = "partner"
)

// placeRelated caps the account's parent, children and partner counterparties
// and draws them — all one hop, all already pruned to the caller's
// organization row scope by the read. Name order with the id as tie-break, so
// two reads of an unchanged account draw the same organizations.
func (g *graphAssembly) placeRelated(related []graphRelatedOrg) {
	// Every row given here is drawn: the cap is the READ's, applied to companies
	// rather than to rows, because one company holding many partner edges must
	// not fill a row budget and starve the others. What was left out is the
	// difference against the read's own DISTINCT company count, so a company
	// appearing on three rows is one drop rather than three.
	drawn := map[ids.UUID]bool{}
	for _, row := range related {
		drawn[row.orgID] = true
		g.addNode(crmcontracts.OrganizationGraphNode{
			Id:    openapi_types.UUID(row.orgID),
			Kind:  crmcontracts.OrganizationGraphNodeKindOrganization,
			Label: row.displayName,
		})
		from, to, kind := g.relatedEdge(row)
		g.addEdge(from, to, kind, nil)
	}
	g.out.DroppedCount += g.relatedTotal - len(drawn)
}

// relatedEdge orients one related organization's edge. The hierarchy edge
// always points parent → child; a partner edge always points from the org
// that RECORDS it to its counterparty, which is why the arm carries the
// owner rather than assuming this account is on either side.
func (g *graphAssembly) relatedEdge(row graphRelatedOrg) (ids.UUID, ids.UUID, crmcontracts.OrganizationGraphEdgeKind) {
	switch row.relation {
	case graphRelationParent:
		return row.orgID, g.orgID.UUID, crmcontracts.OrganizationGraphEdgeKindParentOf
	case graphRelationChild:
		return g.orgID.UUID, row.orgID, crmcontracts.OrganizationGraphEdgeKindParentOf
	default:
		from, to := g.orgID.UUID, row.orgID
		if row.edgeOwner != nil && *row.edgeOwner == row.orgID {
			from, to = row.orgID, g.orgID.UUID
		}
		kind := crmcontracts.OrganizationGraphEdgeKindPartnerOf
		if row.partnerKind != nil {
			kind = crmcontracts.OrganizationGraphEdgeKind(*row.partnerKind)
		}
		return from, to, kind
	}
}

// markIntroPath names the contact the warm room would route the account's
// active signal through, and marks their node.
//
// It reports nothing unless that exact contact is already a node here. The
// ranking is the warm room's own (signals.RankRouteIn), so the two surfaces
// can only ever name the same person — and when the card is not showing that
// person, because their only seat is on a deal it did not draw, saying
// nothing is the honest answer. Naming the strongest contact it happens to
// be showing would be a second, quieter ranking.
func (g *graphAssembly) markIntroPath() {
	if g.signalID == nil {
		return
	}
	ranked := signals.RankRouteIn(g.routeIn, func(personID ids.PersonID) (int, bool) {
		strength, ok := g.strengths[personID]
		return strength.Strength, ok
	})
	if len(ranked) == 0 {
		return
	}
	contactID := ranked[0].PersonID.UUID
	index, drawn := g.nodeIndex[contactID]
	if !drawn {
		return
	}
	onPath := true
	g.out.Nodes[index].IntroPath = &onPath
	g.out.IntroPath = &crmcontracts.OrganizationGraphIntroPath{
		SignalId:  openapi_types.UUID(*g.signalID),
		ContactId: openapi_types.UUID(contactID),
	}
}

// addPersonNode adds one contact, carrying the §4 score their node is
// weighted by. A person the graph already holds keeps the node it has: the
// employment title is the durable description of who they are, and a
// stakeholder seat arriving later must not overwrite it.
func (g *graphAssembly) addPersonNode(edge graphPersonEdge) {
	node := crmcontracts.OrganizationGraphNode{
		Id:     openapi_types.UUID(edge.personID.UUID),
		Kind:   crmcontracts.OrganizationGraphNodeKindPerson,
		Label:  edge.fullName,
		Detail: edge.title,
	}
	if strength, ok := g.strengths[edge.personID]; ok {
		score := strength.Strength
		bucket := crmcontracts.OrganizationGraphNodeStrengthBucket(
			people.StrengthBucketToWire(strength.Bucket))
		node.Strength = &score
		node.StrengthBucket = &bucket
	}
	g.addNode(node)
}

// addNode appends a node the graph does not already hold. The first arrival
// wins, so the node order is the deterministic order the groups were placed
// in and a client can lay the graph out the same way twice.
func (g *graphAssembly) addNode(node crmcontracts.OrganizationGraphNode) {
	id := ids.UUID(node.Id)
	if _, held := g.nodeIndex[id]; held {
		return
	}
	g.nodeIndex[id] = len(g.out.Nodes)
	g.out.Nodes = append(g.out.Nodes, node)
}

// addEdge appends one edge. Both ends are nodes by construction: every
// caller places the far node first.
func (g *graphAssembly) addEdge(from, to ids.UUID, kind crmcontracts.OrganizationGraphEdgeKind, role *string) {
	g.out.Edges = append(g.out.Edges, crmcontracts.OrganizationGraphEdge{
		From: openapi_types.UUID(from),
		To:   openapi_types.UUID(to),
		Kind: kind,
		Role: role,
	})
}
