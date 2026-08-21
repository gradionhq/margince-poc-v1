// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The rest of the demo record set: what happened on a company (activities),
// what it signed (contracts), what it was quoted (products and offers), and
// what it agreed to be contacted about (consent).

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// seedActivities files the correspondence, calls, meetings and tasks.
//
// Every write carries source_system + source_id, which the activity API
// treats as an idempotency key — so this phase converges without a probe of
// its own, and a re-run neither duplicates a thread nor re-opens a task.
func seedActivities(c *client, seats *sessions, cfg demoConfig, refs pipelineRefs, mode runMode) (int, error) {
	created := 0
	// One read for the phase: which source ids already exist. Counting what
	// this run genuinely created needs the before-state, and asking per
	// activity was the seeder's worst quadratic.
	seenSourceIDs := map[string]bool{}
	if mode != modeDryRun {
		loaded, err := loadActivitySourceIDs(c)
		if err != nil {
			return 0, err
		}
		seenSourceIDs = loaded
	}
	for i, act := range cfg.Activities {
		orgID, ok := refs.orgsByDom[strings.ToLower(act.Company)]
		if !ok {
			return created, fmt.Errorf("activity %d names company %q, which is not seeded", i, act.Company)
		}
		if mode == modeDryRun {
			created++
			continue
		}
		// WHO writes this is the point, not a detail. The product records the
		// author as a participant, and the network view reads participants to
		// answer "who on our team knows this contact". Posting everything as
		// one account makes that account know everybody.
		author := seats.as(handlerOf(act, cfg, refs))

		// One of the two offsets is set: DaysAgo for something that happened,
		// DaysIn for something still to come.
		occurred := -act.DaysAgo
		if act.DaysIn > 0 {
			occurred = act.DaysIn
		}
		// An activity links to every record it touched rather than belonging
		// to one, so a mail is one row that appears on the company, on the
		// person it was with, and on the deal it moved. Linking only the
		// company — which is what this did first — leaves every person's
		// timeline empty, which is where a rep actually looks.
		links, err := activityLinks(c, refs, act, orgID)
		if err != nil {
			return created, fmt.Errorf("activity %d on %s: %w", i, act.Company, err)
		}
		body := jsonBody{
			"kind":          act.Kind,
			"occurred_at":   refs.timestamp(occurred),
			"source":        seedSource,
			"source_system": "seed",
			"source_id":     fmt.Sprintf("act-%d", i),
			"links":         links,
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
		// row and the reply cannot tell a create from a convergence. The
		// source ids present before this phase say what was genuinely absent.
		before := seenSourceIDs[fmt.Sprintf("act-%d", i)]
		if err := author.post("/v1/activities", body, nil); err != nil {
			if _, ok := conflictingID(err); ok {
				continue
			}
			if isNotFound(err) && act.Assignee != "" {
				// Row scope hides an account from anyone outside the team that
				// owns it, and hiding it means 404 rather than 403 — existence
				// is not leaked. So a dataset entry naming a colleague on the
				// wrong team reads as a missing company, which sends the reader
				// looking for the wrong bug.
				return created, fmt.Errorf(
					"activity %d: %s cannot see %s — a colleague can only be named on an account their own team owns",
					i, act.Assignee, act.Company)
			}
			return created, fmt.Errorf("activity %d (%s on %s): %w", i, act.Kind, act.Company, err)
		}
		if !before {
			created++
		}
	}
	return created, nil
}

// handlerOf is the colleague who had this conversation.
//
// Derived rather than configured, so a company ingested next month is covered
// without anyone editing a list: the dataset may name an assignee, and
// otherwise it is whoever owns the account. Both fall back to the seeding
// account, which is what happens for a company nobody has been assigned yet.
func handlerOf(act demoActivity, cfg demoConfig, refs pipelineRefs) demoUser {
	wanted := act.Assignee
	if wanted == "" {
		wanted = refs.ownerRefByDomain[strings.ToLower(act.Company)]
	}
	for _, user := range cfg.Users {
		if user.Ref == wanted {
			return user
		}
	}
	return demoUser{}
}

// activityLinks is what one activity touched: always its company, plus the
// company's most senior contact and any open deal there.
//
// Derived rather than listed, because a dataset that names the counterpart
// per activity would have to be rewritten for every company ingested later.
// The senior contact is the one a conversation with an account is most likely
// to have been with — a heuristic, and better than an empty timeline.
func activityLinks(c *client, refs pipelineRefs, act demoActivity, orgID string) ([]jsonBody, error) {
	links := []jsonBody{{"entity_type": "organization", "entity_id": orgID}}

	// A note or a task is internal — it is about the account, not with anybody.
	if act.Kind == "email" || act.Kind == "call" || act.Kind == "meeting" {
		staff, err := staffBySeniority(c, orgID)
		if err != nil {
			return nil, err
		}
		if len(staff) > 0 {
			links = append(links, jsonBody{"entity_type": "person", "entity_id": staff[0]})
		}
	}

	for _, deal := range refs.dealsByCompany[strings.ToLower(act.Company)] {
		links = append(links, jsonBody{"entity_type": "deal", "entity_id": deal})
		break // one deal: an account with two is ambiguous, and guessing wrong is worse than not guessing
	}
	return links, nil
}

// loadActivitySourceIDs reads the source_id of every activity ONCE.
//
// It replaces a per-activity search that listed activities on every call and
// stopped at 200 rows. Both halves were bugs waiting for scale: the search was
// O(activities²) over a run, and the cap meant activity 201 was never found,
// so a converging re-run filed a duplicate of it instead of recognising it.
func loadActivitySourceIDs(c *client) (map[string]bool, error) {
	seen := map[string]bool{}
	err := c.getAll("/v1/activities", nil, func(raw json.RawMessage) error {
		var rows []struct {
			SourceID string `json:"source_id"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			if row.SourceID != "" {
				seen[row.SourceID] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing activities: %w", err)
	}
	return seen, nil
}

// seedLifecycle says where each account stands with us.
//
// It runs after the deals exist, because the two have to agree: an account
// with a won deal is a customer and one with an open deal is at least an
// opportunity. A demo where every company sits at the default teaches the
// lifecycle filter to return everything.
//
// A company the dataset does not place takes the lifecycle its PROFILE was
// planned with, which is what spreads the accounts across customer, former
// customer, opportunity, prospect and target instead of leaving everything at
// two values. demo.json still overrides whenever the story needs something
// specific, and a company with no profile at all falls back to what its
// records show — an open deal makes it an opportunity, otherwise a target.
func seedLifecycle(c *client, cfg demoConfig, refs pipelineRefs, plan map[string]profile, mode runMode) (int, error) {
	changed := 0
	placed := map[string]bool{}
	for _, domains := range cfg.Lifecycle {
		for _, domain := range domains {
			placed[strings.ToLower(domain)] = true
		}
	}
	for domain := range refs.orgsByDom {
		if placed[domain] {
			continue
		}
		stage := plan[domain].Lifecycle
		if stage == "" {
			stage = "target"
			if len(refs.dealsByCompany[domain]) > 0 {
				stage = "opportunity"
			}
		}
		cfg.Lifecycle[stage] = append(cfg.Lifecycle[stage], domain)
	}
	for stage, domains := range cfg.Lifecycle {
		for _, domain := range domains {
			orgID, ok := refs.orgsByDom[strings.ToLower(domain)]
			if !ok {
				return changed, fmt.Errorf("lifecycle names company %q, which is not seeded", domain)
			}
			if mode == modeDryRun {
				changed++
				continue
			}
			current, version, err := organizationLifecycle(c, orgID)
			if err != nil {
				return changed, err
			}
			if current == stage {
				continue
			}
			// The write is version-checked, so a concurrent edit loses rather
			// than being silently overwritten.
			body := jsonBody{"lifecycle": stage, "if_version": version}
			if err := c.patch("/v1/organizations/"+orgID, body, nil); err != nil {
				return changed, fmt.Errorf("setting %s to %s: %w", domain, stage, err)
			}
			changed++
		}
	}
	return changed, nil
}

func organizationLifecycle(c *client, orgID string) (stage string, version int, err error) {
	var out struct {
		Lifecycle string `json:"lifecycle"`
		Version   int    `json:"version"`
	}
	if err := c.get("/v1/organizations/"+orgID, nil, &out); err != nil {
		return "", 0, fmt.Errorf("reading organization %s: %w", orgID, err)
	}
	return out.Lifecycle, out.Version, nil
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
			item := jsonBody{"product_id": productID, "quantity": line.Quantity}
			// Only sent when the dataset states one. Otherwise the line takes
			// the product's own price, which is what every same-currency
			// offer wants.
			if line.UnitPriceMinor > 0 {
				item["unit_price_minor"] = line.UnitPriceMinor
			}
			lines = append(lines, item)
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
