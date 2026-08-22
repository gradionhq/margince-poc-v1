// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// What a seeded deal IS, and what happens to it once it exists.
//
// Split from pipeline.go at the 500-line cap, along the seam seedDeals already
// had: that file decides WHICH deals there are, this one decides what each one
// looks like when it is born and how it reaches a terminal stage.

import (
	"fmt"
	"strings"
)

// dealBody is the deal as it is BORN — open, at the first stage, with whatever
// of amount, owner and partner the fixture named.
//
// Split from seedDeals at the 80-line cap, and along the seam the function
// already had: this half decides what a deal IS, the loop around it decides
// which deals there are and what happens to them next.
func dealBody(deal demoDeal, refs pipelineRefs, orgID, openAt string) (jsonBody, error) {
	body := jsonBody{
		"name":            deal.Name,
		"pipeline_id":     refs.pipelineID,
		"stage_id":        openAt,
		"organization_id": orgID,
		"source":          seedSource,
	}
	if deal.AmountMinor > 0 {
		body["amount_minor"] = deal.AmountMinor
		body["currency"] = deal.Currency
	}
	if owner, ok := refs.usersByRef[deal.Owner]; ok {
		body["owner_id"] = owner
	}
	// Attributed at BIRTH rather than patched afterwards: the commission
	// ledger accrues off the deal's won event, and a deal that was already
	// won before the attribution landed would never produce one.
	if deal.Partner != "" {
		partnerID, ok := refs.orgsByDom[strings.ToLower(deal.Partner)]
		if !ok {
			return nil, fmt.Errorf("deal %s is attributed to partner %q, which is not seeded",
				deal.Ref, deal.Partner)
		}
		body["partner_org_id"] = partnerID
		if deal.PartnerAttribution != "" {
			body["partner_attribution"] = deal.PartnerAttribution
		}
	}
	// A deal is born open, so a close date already past is refused. A closed
	// deal gets no date here; its close is the event that matters.
	if deal.CloseInDays > 0 {
		body["expected_close_date"] = refs.date(deal.CloseInDays)
	}
	return body, nil
}

// closeIfTerminal walks a deal to its won or lost stage, or leaves an open one
// alone. The CLOSE is a second call on purpose: a deal is born open and
// advanced, which is the path a rep takes and the one that raises the events
// the rest of the demo reads.
func closeIfTerminal(c *client, deal demoDeal, dealID, stageID string) error {
	terminal := terminalStatus(deal.Stage)
	if terminal == "" {
		return nil
	}
	advance := jsonBody{"to_stage_id": stageID, "status": terminal}
	if terminal == "lost" {
		// The product requires a reason for a loss, and a demo that shrugs at
		// "why did we lose?" teaches the wrong habit.
		reason := deal.LostReason
		if reason == "" {
			reason = "Kein Grund erfasst"
		}
		advance["lost_reason"] = reason
	}
	if terminal == "won" {
		advance["won_without_contract_reason"] = wonWithoutContractReason
	}
	if err := c.post("/v1/deals/"+dealID+"/advance", advance, nil); err != nil {
		return fmt.Errorf("closing deal %s as %s: %w", deal.Ref, terminal, err)
	}
	return nil
}
