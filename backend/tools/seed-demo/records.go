// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The rest of the demo record set: what happened on a company (activities),
// what it signed (contracts), what it was quoted (products and offers), and
// what it agreed to be contacted about (consent).

import (
	"fmt"
	"net/url"
	"strings"
)

// seedActivities files the correspondence, calls, meetings and tasks.
//
// Every write carries source_system + source_id, which the activity API
// treats as an idempotency key — so this phase converges without a probe of
// its own, and a re-run neither duplicates a thread nor re-opens a task.
func seedActivities(c *client, cfg demoConfig, refs pipelineRefs, mode runMode) (int, error) {
	created := 0
	for i, act := range cfg.Activities {
		orgID, ok := refs.orgsByDom[strings.ToLower(act.Company)]
		if !ok {
			return created, fmt.Errorf("activity %d names company %q, which is not seeded", i, act.Company)
		}
		if mode == modeDryRun {
			created++
			continue
		}

		// One of the two offsets is set: DaysAgo for something that happened,
		// DaysIn for something still to come.
		occurred := -act.DaysAgo
		if act.DaysIn > 0 {
			occurred = act.DaysIn
		}
		// An activity links to the records it touched rather than belonging
		// to one, so a mail that names a company and a deal is one row on
		// both timelines instead of two copies.
		body := jsonBody{
			"kind":          act.Kind,
			"occurred_at":   refs.timestamp(occurred),
			"source":        seedSource,
			"source_system": "seed",
			"source_id":     fmt.Sprintf("act-%d", i),
			"links":         []jsonBody{{"entity_type": "organization", "entity_id": orgID}},
		}
		addIfSet(body, "subject", act.Subject)
		addIfSet(body, "body", act.Body)
		addIfSet(body, "direction", act.Direction)
		addIfSet(body, "meeting_status", act.MeetingStatus)
		if act.DurationSeconds > 0 {
			body["duration_seconds"] = act.DurationSeconds
		}
		// assignee_id and due_at belong to a TASK and to nothing else — the
		// activity_task_fields CHECK refuses them on a mail or a meeting,
		// because those record what happened rather than what somebody owes.
		// Who handled the others is carried by the record's owner instead.
		if act.Kind == "task" {
			if assignee, ok := refs.usersByRef[act.Assignee]; ok {
				body["assignee_id"] = assignee
			}
			if act.DaysIn > 0 {
				body["due_at"] = refs.timestamp(act.DaysIn)
			}
		}

		// Idempotent on source_system+source_id, so a re-run replays the same
		// row and the reply cannot tell a create from a convergence. Probe
		// first, and count only what was genuinely absent.
		before, err := findActivityBySource(c, fmt.Sprintf("act-%d", i))
		if err != nil {
			return created, err
		}
		if err := c.post("/v1/activities", body, nil); err != nil {
			if _, ok := conflictingID(err); ok {
				continue
			}
			return created, fmt.Errorf("activity %d (%s on %s): %w", i, act.Kind, act.Company, err)
		}
		if !before {
			created++
		}
	}
	return created, nil
}

func findActivityBySource(c *client, sourceID string) (bool, error) {
	var page struct {
		Data []struct {
			SourceID string `json:"source_id"`
		} `json:"data"`
	}
	if err := c.get("/v1/activities", url.Values{"limit": {"200"}}, &page); err != nil {
		return false, fmt.Errorf("listing activities: %w", err)
	}
	for _, row := range page.Data {
		if row.SourceID == sourceID {
			return true, nil
		}
	}
	return false, nil
}

// seedContracts files the agreements a company has signed — the domain that
// answers "what are we already committed to?" separately from "what are we
// selling?".
func seedContracts(c *client, cfg demoConfig, refs pipelineRefs, mode runMode) (int, error) {
	created := 0
	for _, contract := range cfg.Contracts {
		orgID, ok := refs.orgsByDom[strings.ToLower(contract.Company)]
		if !ok {
			return created, fmt.Errorf("contract %s names company %q, which is not seeded", contract.Ref, contract.Company)
		}
		if mode == modeDryRun {
			created++
			continue
		}

		existing, err := findContract(c, orgID, contract.Title)
		if err != nil {
			return created, err
		}
		if existing != "" {
			continue
		}

		body := jsonBody{
			"organization_id": orgID,
			"title":           contract.Title,
			"auto_renew":      contract.AutoRenew,
		}
		addIfSet(body, "contract_number", contract.ContractNumber)
		addIfSet(body, "value_basis", contract.ValueBasis)
		if contract.ValueMinor > 0 {
			body["value_minor"] = contract.ValueMinor
			body["currency"] = contract.Currency
		}
		if contract.StartsInDays != 0 {
			body["starts_on"] = refs.date(contract.StartsInDays)
		}
		if contract.EndsInDays != 0 {
			body["ends_on"] = refs.date(contract.EndsInDays)
		}
		if contract.RenewalInDays != 0 {
			body["renewal_on"] = refs.date(contract.RenewalInDays)
		}
		if contract.SignedInDays != 0 {
			body["signed_on"] = refs.date(contract.SignedInDays)
		}
		if contract.NoticePeriodDays > 0 {
			body["notice_period_days"] = contract.NoticePeriodDays
		}

		if err := c.post("/v1/contracts", body, nil); err != nil {
			return created, fmt.Errorf("contract %s: %w", contract.Ref, err)
		}
		created++
	}
	return created, nil
}

func findContract(c *client, orgID, title string) (string, error) {
	var page struct {
		Data []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"data"`
	}
	if err := c.get("/v1/organizations/"+orgID+"/contracts", url.Values{"limit": {"50"}}, &page); err != nil {
		return "", fmt.Errorf("listing contracts for %s: %w", orgID, err)
	}
	for _, row := range page.Data {
		if strings.EqualFold(row.Title, title) {
			return row.ID, nil
		}
	}
	return "", nil
}

// seedProducts fills the rate card the offers draw their line items from.
func seedProducts(c *client, cfg demoConfig, mode runMode) (map[string]string, int, error) {
	ids := map[string]string{}
	created := 0

	var page struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if mode != modeDryRun {
		if err := c.get("/v1/products", url.Values{"limit": {"100"}}, &page); err != nil {
			return nil, 0, fmt.Errorf("listing products: %w", err)
		}
	}
	byName := map[string]string{}
	for _, row := range page.Data {
		byName[strings.ToLower(row.Name)] = row.ID
	}

	for _, product := range cfg.Products {
		if id, ok := byName[strings.ToLower(product.Name)]; ok {
			ids[product.Ref] = id
			continue
		}
		if mode == modeDryRun {
			created++
			continue
		}
		body := jsonBody{
			"name":             product.Name,
			"unit_price_minor": product.UnitPriceMinor,
			"currency":         product.Currency,
			"source":           seedSource,
		}
		addIfSet(body, "sku", product.SKU)
		addIfSet(body, "unit", product.Unit)
		addIfSet(body, "description", product.Description)

		var out struct {
			ID string `json:"id"`
		}
		if err := c.post("/v1/products", body, &out); err != nil {
			return nil, created, fmt.Errorf("product %s: %w", product.Ref, err)
		}
		ids[product.Ref] = out.ID
		created++
	}
	return ids, created, nil
}

// seedOffers quotes the open deals, and drives each offer to its dataset
// state through the real send/accept/reject transitions rather than writing a
// state column: an accepted offer that was never sent is not a state the
// product can reach.
func seedOffers(c *client, cfg demoConfig, refs pipelineRefs, products map[string]string, mode runMode) (int, error) {
	created := 0
	for _, offer := range cfg.Offers {
		dealID, err := dealIDFor(c, cfg, refs, offer.Deal)
		if err != nil {
			return created, err
		}
		if dealID == "" {
			continue // the deal is not seeded (dry run, or a -limit subset)
		}
		if mode == modeDryRun {
			created++
			continue
		}
		if has, err := dealHasOffer(c, dealID); err != nil {
			return created, err
		} else if has {
			continue
		}

		lines := make([]jsonBody, 0, len(offer.Lines))
		for _, line := range offer.Lines {
			productID, ok := products[line.Product]
			if !ok {
				return created, fmt.Errorf("offer %s names product %q, which is not seeded", offer.Ref, line.Product)
			}
			lines = append(lines, jsonBody{"product_id": productID, "quantity": line.Quantity})
		}
		body := jsonBody{"currency": offer.Currency, "source": seedSource, "line_items": lines}
		addIfSet(body, "intro_text", offer.IntroText)
		if offer.ValidInDays != 0 {
			body["valid_until"] = refs.date(offer.ValidInDays)
		}

		var out struct {
			ID string `json:"id"`
		}
		if err := c.post("/v1/deals/"+dealID+"/offers", body, &out); err != nil {
			return created, fmt.Errorf("offer %s: %w", offer.Ref, err)
		}
		created++

		if err := driveOfferTo(c, out.ID, offer.State); err != nil {
			return created, fmt.Errorf("offer %s: %w", offer.Ref, err)
		}
	}
	return created, nil
}

// driveOfferTo walks an offer to its target state. Accept and reject both
// require a sent offer, so the send is not optional scaffolding.
func driveOfferTo(c *client, offerID, state string) error {
	if state == "" || state == "draft" {
		return nil
	}
	if err := c.post("/v1/offers/"+offerID+"/send", jsonBody{}, nil); err != nil {
		return fmt.Errorf("sending: %w", err)
	}
	switch state {
	case "sent":
		return nil
	case "accepted":
		if err := c.post("/v1/offers/"+offerID+"/accept", jsonBody{}, nil); err != nil {
			return fmt.Errorf("accepting: %w", err)
		}
	case "rejected":
		if err := c.post("/v1/offers/"+offerID+"/reject", jsonBody{}, nil); err != nil {
			return fmt.Errorf("rejecting: %w", err)
		}
	default:
		return fmt.Errorf("unknown offer state %q", state)
	}
	return nil
}

func dealIDFor(c *client, cfg demoConfig, refs pipelineRefs, dealRef string) (string, error) {
	for _, deal := range cfg.Deals {
		if deal.Ref != dealRef {
			continue
		}
		orgID, ok := refs.orgsByDom[strings.ToLower(deal.Company)]
		if !ok {
			return "", nil
		}
		return findDeal(c, deal.Name, orgID)
	}
	return "", fmt.Errorf("no deal has ref %q", dealRef)
}

func dealHasOffer(c *client, dealID string) (bool, error) {
	var page struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.get("/v1/deals/"+dealID+"/offers", url.Values{"limit": {"5"}}, &page); err != nil {
		return false, fmt.Errorf("listing offers for deal %s: %w", dealID, err)
	}
	return len(page.Data) > 0, nil
}
