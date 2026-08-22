// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The invented pipeline: leads, deals, and the activities that make a company
// record look worked rather than merely filled in.
//
// Dates in the dataset are OFFSETS IN DAYS from the run, so a demo seeded
// today reads as current and one seeded three months ago does not look
// abandoned.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// pipelineRefs is what the seeded pipeline needs to point at: which user owns
// what, which organization a deal hangs off, and the stage ids of the
// workspace's own Sales pipeline.
type pipelineRefs struct {
	usersByRef map[string]string // demo.json ref -> app_user id
	orgsByDom  map[string]string // company domain -> organization id
	stagesByNm map[string]string // stage name  -> stage id
	firstStage string            // the stage a deal is born in
	// contractsByRef lets the renewal read its successor's terms: a renewal
	// inherits nothing, so every field is restated from the dataset.
	contractsByRef []demoContract
	// dealsByCompany is filled after the deals are seeded, so an activity can
	// link to the deal it moved.
	dealsByCompany map[string][]string
	// anchorName and orgNameByID name the parties a document prints.
	anchorName  string
	orgNameByID map[string]string
	// domainByOrgID is orgsByDom backwards, so a phase holding only an id can
	// still ask what LANGUAGE the account's paper is written in — the domain
	// is what decides that.
	domainByOrgID map[string]string
	// ownerRefByDomain is who holds each account — the ONE answer ownership
	// and activity authorship both read, so they cannot drift apart.
	ownerRefByDomain map[string]string
	pipelineID       string
	now              time.Time
}

// dayOffset turns a dataset offset into a date. Negative is the past.
func (r pipelineRefs) dayOffset(days int) time.Time {
	return r.now.AddDate(0, 0, days)
}

func (r pipelineRefs) date(days int) string {
	return r.dayOffset(days).Format("2006-01-02")
}

func (r pipelineRefs) timestamp(days int) string {
	return r.dayOffset(days).Format(time.RFC3339)
}

// loadPipelineRefs resolves everything the pipeline phases need to reference,
// once, so each phase is a straight write rather than a search.
func loadPipelineRefs(c *client, cfg demoConfig, now time.Time) (pipelineRefs, error) {
	refs := pipelineRefs{
		contractsByRef:   cfg.Contracts,
		dealsByCompany:   map[string][]string{},
		ownerRefByDomain: map[string]string{},
		orgNameByID:      map[string]string{},
		domainByOrgID:    map[string]string{},
		anchorName:       cfg.Anchor.LegalName,
		usersByRef:       map[string]string{},
		orgsByDom:        map[string]string{},
		stagesByNm:       map[string]string{},
		now:              now,
	}

	var users struct {
		Data []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"data"`
	}
	if err := c.get("/v1/users", url.Values{"limit": {"100"}}, &users); err != nil {
		return refs, fmt.Errorf("listing seats: %w", err)
	}
	byEmail := map[string]string{}
	for _, u := range users.Data {
		byEmail[strings.ToLower(u.Email)] = u.ID
	}
	for _, u := range cfg.Users {
		if id, ok := byEmail[strings.ToLower(u.Email)]; ok {
			refs.usersByRef[u.Ref] = id
		}
	}

	if err := refs.loadOrganizations(c); err != nil {
		return refs, err
	}

	var pipelines struct {
		Data []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Stages []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"stages"`
		} `json:"data"`
	}
	if err := c.get("/v1/pipelines", nil, &pipelines); err != nil {
		return refs, fmt.Errorf("listing pipelines: %w", err)
	}
	if len(pipelines.Data) == 0 {
		return refs, fmt.Errorf("the workspace has no pipeline — deals have nowhere to go")
	}
	refs.resolveOwners(cfg)
	first := pipelines.Data[0]
	refs.pipelineID = first.ID
	for i, s := range first.Stages {
		refs.stagesByNm[s.Name] = s.ID
		if i == 0 {
			refs.firstStage = s.Name
		}
	}
	return refs, nil
}

// seedLeads files the unqualified names at the top of the funnel, and
// promotes the one that converted so the promote path is exercised rather
// than only described.
func seedLeads(c *client, cfg demoConfig, refs pipelineRefs, mode runMode) (int, error) {
	created := 0
	// One read for the whole phase, rather than a full lead listing per lead.
	leadsBySource := map[string]string{}
	if mode != modeDryRun {
		loaded, err := loadLeadsBySource(c)
		if err != nil {
			return 0, err
		}
		leadsBySource = loaded
	}
	for _, lead := range cfg.Leads {
		if mode == modeDryRun {
			created++
			continue
		}
		// source_system + source_id make the create idempotent server-side:
		// re-running finds the same lead rather than filing a second one.
		body := jsonBody{
			"source":        seedSource,
			"source_system": "seed",
			"source_id":     lead.Ref,
			"status":        lead.Status,
		}
		addIfSet(body, "full_name", lead.FullName)
		addIfSet(body, "email", lead.Email)
		addIfSet(body, "title", lead.Title)
		addIfSet(body, "company_name", lead.Company)
		if owner, ok := refs.usersByRef[lead.Owner]; ok {
			body["owner_id"] = owner
		}

		// The lead API is idempotent on source_system+source_id, so a re-run
		// answers with the SAME lead rather than refusing. That is
		// convergence, but the reply cannot be told apart from a create — so
		// what is counted is whether this ref was already on file.
		//
		// Promotion does NOT consume the lead: the contract says it marks the
		// row `status=promoted` and archives it. So an already-promoted lead is
		// found by its source_id like any other, as long as the lookup includes
		// archived rows — which loadLeadsBySource does.
		before := leadsBySource[lead.Ref]
		var out struct {
			ID string `json:"id"`
		}
		if err := c.post("/v1/leads", body, &out); err != nil {
			if _, ok := conflictingID(err); ok {
				continue
			}
			return created, fmt.Errorf("lead %s: %w", lead.Ref, err)
		}
		if before == "" {
			created++
		}

		if lead.Promote && out.ID != "" {
			// A lead the previous run already promoted answers 409, which for
			// a converging seeder is the desired state reported as a refusal.
			if err := c.post("/v1/leads/"+out.ID+"/promote", jsonBody{"trigger": "human_qualify"}, nil); err != nil && !isConflict(err) {
				return created, fmt.Errorf("promoting lead %s: %w", lead.Ref, err)
			}
			// Promotion mints a person but no employment, so the new contact
			// belongs to nobody's company — an orphan the company page cannot
			// show. The lead named an employer; the person inherits it.
			if err := employPromoted(c, lead, refs); err != nil {
				return created, fmt.Errorf("employing the promoted %s: %w", lead.FullName, err)
			}
		}
	}
	return created, nil
}

// seedDeals opens each deal and, when its target stage is terminal, advances
// it there.
//
// A deal cannot be CREATED won or lost — the product refuses it outright
// ("create open, then advance"), because winning is an event with a date and
// a reason rather than a column you can be born in. So the two closed deals
// are opened at the first stage and closed through the real advance, which is
// also what puts a lost_reason on the record.
//
// The expected close date is constrained the same way: a deal is born open,
// so a date already past is refused (INV-CLOSE-PAST), and the closed deals
// carry none.
func seedDeals(c *client, cfg demoConfig, refs pipelineRefs, mode runMode) (int, error) {
	created := 0
	for _, deal := range cfg.Deals {
		orgID, ok := refs.orgsByDom[strings.ToLower(deal.Company)]
		if !ok {
			return created, fmt.Errorf("deal %s names company %q, which is not seeded", deal.Ref, deal.Company)
		}
		stageID, ok := refs.stagesByNm[deal.Stage]
		if !ok {
			return created, fmt.Errorf("deal %s names stage %q, which this pipeline does not have", deal.Ref, deal.Stage)
		}
		openAt := stageID
		if terminal := terminalStatus(deal.Stage); terminal != "" {
			openAt = refs.stagesByNm[refs.firstStage]
		}
		if mode == modeDryRun {
			created++
			continue
		}

		existing, err := findDeal(c, deal.Name, orgID)
		if err != nil {
			return created, err
		}
		if existing != "" {
			continue
		}

		body, err := dealBody(deal, refs, orgID, openAt)
		if err != nil {
			return created, err
		}

		var out struct {
			ID string `json:"id"`
		}
		if err := c.post("/v1/deals", body, &out); err != nil {
			return created, fmt.Errorf("deal %s: %w", deal.Ref, err)
		}
		created++

		if err := closeIfTerminal(c, deal, out.ID, stageID); err != nil {
			return created, err
		}
	}
	return created, nil
}

// wonWithoutContractReason is what EVERY win the seeder closes reports.
//
// ADR-0109 §6 (deals/win_evidence.go) refuses a won deal that has neither a
// signed contract with its paper ATTACHED nor a stated reason. The seeder
// cannot satisfy the first half at close time whatever the dataset says: deals
// close in the pipeline phase and contracts and their PDFs are written after
// it, so the evidence does not exist yet when the deal is won.
//
// The field is a CLOSED vocabulary — imported, purchase_order, verbal,
// renewal_by_email, other — because a free-text answer cannot be counted, and
// counting them is why the exit is allowed at all. Free text is a 422.
// purchase_order is the honest member here.
//
// The contracts phase still files the real agreement afterwards, so the
// account shows its paper; what this records is that the WIN was booked before
// that paper existed, which is true of how the seeder works.
const wonWithoutContractReason = "purchase_order"

// terminalStatus maps a stage name to the close status it represents, or ""
// for the open stages that need no advance.
func terminalStatus(stage string) string {
	switch strings.ToLower(stage) {
	case "won":
		return "won"
	case "lost":
		return "lost"
	default:
		return ""
	}
}

// employPromoted ties a promoted lead's person to the company the lead named.
func employPromoted(c *client, lead demoLead, refs pipelineRefs) error {
	orgID, ok := refs.orgsByDom[strings.ToLower(lead.Company)]
	if !ok {
		return nil // the lead named a company outside this dataset
	}
	return employPromotedPerson(c, lead.FullName, lead.Title, orgID)
}

// employPromotedPerson gives the person a promotion minted the job the lead
// described.
//
// Promotion creates a person and NOTHING else: no employment edge, so the new
// contact belongs to no company. They then show on no company page, and the
// ownership pass leaves them unowned because ownership is inherited from the
// employer — and an unowned row is workspace-shared, visible at every scope.
// Both verify rules catch it, which is how the generated leads were found
// doing exactly this.
func employPromotedPerson(c *client, fullName, title, orgID string) error {
	personID, found, err := findPersonByName(c, fullName)
	if err != nil || !found {
		return err
	}
	_, err = ensureEmployment(c, personID, orgID, title, false)
	return err
}

// loadOrganizations indexes the accounts twice: by domain, which is how the
// dataset names them, and by id with the name a document would print.
func (r *pipelineRefs) loadOrganizations(c *client) error {
	type orgRow struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		LegalName   string `json:"legal_name"`
		Domains     []struct {
			Domain string `json:"domain"`
		} `json:"domains"`
	}
	err := c.getAll("/v1/organizations", nil, func(raw json.RawMessage) error {
		var rows []orgRow
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, o := range rows {
			// The legal name is what paper names as a party; the display name
			// is what people call them, and only one of those belongs in a
			// contract.
			name := o.LegalName
			if name == "" {
				name = o.DisplayName
			}
			r.orgNameByID[o.ID] = name
			for _, dom := range o.Domains {
				domain := strings.ToLower(dom.Domain)
				r.orgsByDom[domain] = o.ID
				// First domain wins: a company with several is reached by any
				// of them, but its paper needs one settled answer.
				if _, seen := r.domainByOrgID[o.ID]; !seen {
					r.domainByOrgID[o.ID] = domain
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("listing organizations: %w", err)
	}
	return nil
}

// loadDeals records the id of every seeded deal, keyed by its company, so the
// phases that point at a deal do not each re-search for it.
// It reads the installation's OWN deals rather than walking demo.json,
// because the generated deals are deals too: a phase that only knew the
// dataset's own left every generated deal without a buying committee, and
// left activities unable to link to one.
func (r *pipelineRefs) loadDeals(c *client, _ demoConfig) error {
	domainByOrg := map[string]string{}
	for domain, orgID := range r.orgsByDom {
		domainByOrg[orgID] = domain
	}
	return c.getAll("/v1/deals", nil, func(raw json.RawMessage) error {
		var rows []struct {
			ID             string `json:"id"`
			OrganizationID string `json:"organization_id"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			domain, ok := domainByOrg[row.OrganizationID]
			if !ok {
				continue
			}
			r.dealsByCompany[domain] = append(r.dealsByCompany[domain], row.ID)
		}
		return nil
	})
}

// loadLeadsBySource reads every seeded lead ONCE into a map, keyed by the
// source_id the seeder minted.
//
// It replaces a per-lead search that listed all leads on every lookup, which
// is O(leads²) over a run. It also reads the statuses that search left out:
// a promoted or disqualified lead is still a lead the seeder created, and not
// finding it made the run create a second one — the opposite of converging.
// A disqualified lead is archived, so it only appears with include_archived.
func loadLeadsBySource(c *client) (map[string]string, error) {
	type leadRow struct {
		ID       string `json:"id"`
		SourceID string `json:"source_id"`
	}
	bySource := map[string]string{}
	query := url.Values{"include_archived": {"true"}}
	err := c.getAll("/v1/leads", query, func(raw json.RawMessage) error {
		var rows []leadRow
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			if row.SourceID != "" {
				bySource[row.SourceID] = row.ID
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing leads: %w", err)
	}
	return bySource, nil
}

func findDeal(c *client, name, orgID string) (string, error) {
	var page struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	query := url.Values{"organization_id": {orgID}, "limit": {"100"}}
	if err := c.get("/v1/deals", query, &page); err != nil {
		return "", fmt.Errorf("listing deals for %s: %w", orgID, err)
	}
	for _, row := range page.Data {
		if strings.EqualFold(row.Name, name) {
			return row.ID, nil
		}
	}
	return "", nil
}
