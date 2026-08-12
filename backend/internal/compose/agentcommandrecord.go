// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The REST door's half of the two commands one tool serves through TWO
// contract operations (gradionhq/margince-poc-v1#928 task 7): merge_records is
// mergePerson and mergeOrganization, and enrich is scrapeCompany and
// deepReadCompany.
//
// Both are where this door was previously answering the WRONG question, and in
// two different ways. A merge's route walk read the routed {id} as the staged
// target, but that id is the record merged FROM — the one about to be archived
// — while the approval belongs on the survivor the body names. And the two
// enrich routes are one verb at two depths, distinguishable by nothing on the
// wire except which path was taken, so a target derived from the route could
// never say which of the two a human was releasing.

import (
	"net/http"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// mergeCommand decodes POST /v1/people/{id}/merge and
// POST /v1/organizations/{id}/merge.
//
// The routed {id} is the SOURCE and the body's `target_id` is the survivor:
// crm.yaml says so ("The surviving person (B). This row (A) is archived") and
// the handler does the same (people.Handlers.MergePerson passes the path id as
// the source). So this is where the two doors stop disagreeing: the tool door
// has always pinned the survivor, and this door pinned whichever row the route
// happened to name.
//
// The record type comes off the route's own policy entry rather than being
// written here a second time — the entry is generated from the contract's
// x-mcp-tool annotation, so a type spelled again in this file could disagree
// with the one the gate admitted against.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func mergeCommand(pol agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error) {
	sourceID, err := routedID(r)
	if err != nil {
		return nil, err
	}
	in, err := commandBody[struct {
		TargetID ids.UUID `json:"target_id"`
	}](body)
	if err != nil {
		return nil, err
	}
	return agents.NewMergeCall(deps.records, agents.MergeCommand{
		RecordType: string(pol.RecordType),
		SourceID:   sourceID,
		TargetID:   in.TargetID,
	}), nil
}

// scrapeCompanyCommand decodes POST /v1/organizations/{id}/enrich — the
// one-page read.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func scrapeCompanyCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error) {
	return enrichCall(deps, r, body, agents.EnrichDepthPage)
}

// deepReadCompanyCommand decodes POST /v1/organizations/{id}/deep-read — the
// whole-site crawl.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func deepReadCompanyCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error) {
	return enrichCall(deps, r, body, agents.EnrichDepthSite)
}

// enrichCall builds one enrich command at the depth its CALLER names.
//
// The depth is a parameter of this function, supplied by the two decoders
// above as a typed constant. It is never read from the request: there is no
// `depth` on either route's wire form to read it from, and a decoder that
// synthesized the string would be free to synthesize the wrong one — a
// scrapeCompany described to an approving human as a whole-site crawl, which
// no schema check could catch because "site" is a perfectly valid value.
// Passing the value structurally is what makes that unreachable rather than
// merely unlikely.
//
// The body is optional on both routes (crm.yaml's EnrichCompanyRequest: "With
// no body the org's own domain is read"), which commandBody answers with an
// empty override rather than a refusal.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func enrichCall(deps restCommandDeps, r *http.Request, body []byte, depth agents.EnrichDepth) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	in, err := commandBody[struct {
		URL string `json:"url"`
	}](body)
	if err != nil {
		return nil, err
	}
	return agents.NewEnrichCall(deps.records, agents.EnrichCommand{
		OrganizationID: id,
		URL:            in.URL,
		Depth:          depth,
	}), nil
}
