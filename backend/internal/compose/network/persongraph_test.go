// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package network

import (
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

var graphNow = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

func uuidFor(b byte) ids.UUID { return ids.UUID{b} }

func daysBefore(n int) *time.Time {
	t := graphNow.AddDate(0, 0, -n)
	return &t
}

func intp(v int) *int { return &v }

// graphWith builds a picture with one anchor, so a test states the shape it
// cares about rather than the whole assembly.
func graphWith(nodes []crmcontracts.PersonGraphNode, edges []crmcontracts.PersonGraphEdge) *crmcontracts.PersonGraph {
	anchorID := uuidFor(9)
	pid := openapi_types.UUID(anchorID)
	all := append([]crmcontracts.PersonGraphNode{{
		Id:       personNodeID(anchorID),
		Type:     crmcontracts.PersonGraphNodeTypeContact,
		Group:    crmcontracts.PersonGraphNodeGroupAnchor,
		Label:    "Anna Weber",
		PersonId: &pid,
	}}, nodes...)
	return &crmcontracts.PersonGraph{Nodes: all, Edges: edges}
}

func colleague(b byte, name string, group crmcontracts.PersonGraphNodeGroup) crmcontracts.PersonGraphNode {
	return colleagueNode(uuidFor(b), name, group)
}

func contactNode(b byte, name string) crmcontracts.PersonGraphNode {
	pid := openapi_types.UUID(uuidFor(b))
	return crmcontracts.PersonGraphNode{
		Id:       personNodeID(uuidFor(b)),
		Type:     crmcontracts.PersonGraphNodeTypeContact,
		Group:    crmcontracts.PersonGraphNodeGroupAccount,
		Label:    name,
		PersonId: &pid,
	}
}

// "She already knows you" is a different kind of claim from "he knows someone
// at her company", and no amount of volume on the second beats the first.
func TestChooseRouteAlwaysPrefersADirectRelationship(t *testing.T) {
	anchor := personNodeID(uuidFor(9))
	graph := graphWith(
		[]crmcontracts.PersonGraphNode{
			colleague(1, "Direct Dana", crmcontracts.PersonGraphNodeGroupDirect),
			colleague(2, "Busy Ben", crmcontracts.PersonGraphNodeGroupAccount),
			contactNode(3, "Their colleague"),
		},
		[]crmcontracts.PersonGraphEdge{
			// Thin but direct.
			{
				From: userNodeID(uuidFor(1)), To: anchor,
				Interactions90d: 2, Inbound90d: intp(1), Outbound90d: intp(1), LastAt: daysBefore(20),
			},
			// Far busier, but one hop away.
			{
				From: userNodeID(uuidFor(2)), To: personNodeID(uuidFor(3)),
				Interactions90d: 40, Inbound90d: intp(20), Outbound90d: intp(20), LastAt: daysBefore(1),
			},
		})

	route := chooseRoute(graph, graphNow)
	if route == nil {
		t.Fatal("two candidate routes produced no recommendation")
	}
	if route.ViaDisplayName != "Direct Dana" {
		t.Errorf("route goes via %q; a direct relationship outranks a busier indirect one", route.ViaDisplayName)
	}
	if route.ThroughDisplayName != nil {
		t.Errorf("a direct route named an intermediary (%q)", *route.ThroughDisplayName)
	}
}

// When nobody here knows the contact, the route through their colleague is
// the whole value of the account group — and it has to say who it goes
// through, or the reader cannot act on it.
func TestChooseRouteNamesTheIntermediaryWhenTheHopIsIndirect(t *testing.T) {
	graph := graphWith(
		[]crmcontracts.PersonGraphNode{
			colleague(2, "Busy Ben", crmcontracts.PersonGraphNodeGroupAccount),
			contactNode(3, "Their colleague"),
		},
		[]crmcontracts.PersonGraphEdge{{
			From: userNodeID(uuidFor(2)), To: personNodeID(uuidFor(3)),
			Interactions90d: 12, Inbound90d: intp(6), Outbound90d: intp(6), LastAt: daysBefore(2),
		}})

	route := chooseRoute(graph, graphNow)
	if route == nil {
		t.Fatal("an indirect route was available and none was recommended")
	}
	if route.ThroughDisplayName == nil || *route.ThroughDisplayName != "Their colleague" {
		t.Error("the indirect route did not name who it goes through")
	}
	if route.Why == "" {
		t.Error("a route with no proof line asks the reader to trust it")
	}
}

// A relationship that answers is worth more than one that only sends. A
// hundred unanswered mails is not a door.
func TestChooseRoutePrefersAnExchangeOverAOneSidedRelationship(t *testing.T) {
	anchor := personNodeID(uuidFor(9))
	graph := graphWith(
		[]crmcontracts.PersonGraphNode{
			colleague(1, "Talks at them", crmcontracts.PersonGraphNodeGroupDirect),
			colleague(2, "Actually converses", crmcontracts.PersonGraphNodeGroupDirect),
		},
		[]crmcontracts.PersonGraphEdge{
			{
				From: userNodeID(uuidFor(1)), To: anchor,
				Interactions90d: 30, Inbound90d: intp(0), Outbound90d: intp(30), LastAt: daysBefore(1),
			},
			{
				From: userNodeID(uuidFor(2)), To: anchor,
				Interactions90d: 6, Inbound90d: intp(3), Outbound90d: intp(3), LastAt: daysBefore(3),
			},
		})

	route := chooseRoute(graph, graphNow)
	if route == nil {
		t.Fatal("no route was chosen")
	}
	if route.ViaDisplayName != "Actually converses" {
		t.Errorf("route goes via %q; thirty unanswered sends are not a relationship", route.ViaDisplayName)
	}
	if route.Why == "" || route.Why == " " {
		t.Error("the proof line is empty")
	}
}

// A graph with nothing in it recommends nothing. Inventing a route from an
// empty picture is the failure the whole evidence posture exists to avoid.
func TestChooseRouteRecommendsNothingWhenThereIsNoEdge(t *testing.T) {
	if route := chooseRoute(graphWith(nil, nil), graphNow); route != nil {
		t.Errorf("an empty graph produced a route via %q", route.ViaDisplayName)
	}
}

// The proof line is what a rep reads before deciding whether to ask, so it has
// to distinguish the two cases rather than print one number.
func TestProofLineSaysWhetherTheRelationshipIsTwoWay(t *testing.T) {
	twoWay := proofLine(crmcontracts.PersonGraphEdge{
		Interactions90d: 6, Inbound90d: intp(3), Outbound90d: intp(3), LastAt: daysBefore(2),
	}, graphNow)
	if !strings.Contains(twoWay, "two-way") {
		t.Errorf("a mutual relationship reads %q and does not say it is mutual", twoWay)
	}
	oneSided := proofLine(crmcontracts.PersonGraphEdge{
		Interactions90d: 30, Inbound90d: intp(0), Outbound90d: intp(30), LastAt: daysBefore(2),
	}, graphNow)
	if !strings.Contains(oneSided, "one-sided") {
		t.Errorf("thirty unanswered sends read %q and do not say they went unanswered", oneSided)
	}
}

// A user and a person are different kinds of node. Bare uuids as ids would be
// ambiguous the first time the two id spaces collided, and an edge would then
// point at the wrong thing.
func TestNodeIdsKeepUsersAndContactsApart(t *testing.T) {
	same := uuidFor(7)
	if userNodeID(same) == personNodeID(same) {
		t.Error("a user and a contact with the same uuid produced the same node id")
	}
}
