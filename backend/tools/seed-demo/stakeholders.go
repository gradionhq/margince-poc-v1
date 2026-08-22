// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Who is involved, on the deal and on the conversation.
//
// Both are derived from the people already at the company rather than named
// in the dataset, for the same reason ownership is: 20 companies are ingested
// and 180 are not, and a rule covers the ones nobody has met yet.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// buyingRoles is the shape of a buying committee, in the order it is filled.
//
// The seniority of the title decides who gets which seat: the most senior
// person on the account is the economic buyer, and so on down. That is a
// heuristic and it will be wrong about individuals — but a demo whose deals
// all have an unnamed committee is wrong about everything, and the alternative
// is hand-listing a committee per company for companies nobody has read yet.
var buyingRoles = []string{"economic_buyer", "champion", "influencer", "blocker"}

// seniority ranks a printed job title. Higher is more senior. Deliberately
// small: it needs to separate a managing director from an account manager,
// not to model a career ladder.
func seniority(title string) int {
	t := strings.ToLower(title)
	switch {
	case containsAnyOf(t, "geschäftsführ", "geschaeftsführ", "geschaeftsfuehr", "managing director", "ceo", "chief executive", "founder", "gründer", "vorstand", "president"):
		return 5
	case containsAnyOf(t, "chief", "cto", "cfo", "coo", "cmo", "cro", "vp ", "vice president", "prokurist"):
		return 4
	case containsAnyOf(t, "head of", "leiter", "leiterin", "director", "principal"):
		return 3
	case containsAnyOf(t, "senior", "lead "):
		return 2
	default:
		return 1
	}
}

func containsAnyOf(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

// seedStakeholders gives every deal a buying committee drawn from the people
// its company actually employs.
//
// A deal with no stakeholders is a number with nobody attached — the page can
// show an amount and a stage but cannot answer who wants this, who signs, or
// who is in the way, which is most of what a deal record is for.
// It walks every deal the installation holds, from refs.dealsByCompany, not
// only the ones demo.json names — a generated deal needs a committee for the
// same reason, and the verify pass fails on it either way.
func seedStakeholders(c *client, _ demoConfig, refs pipelineRefs, mode runMode) (int, error) {
	created := 0
	for _, domain := range sortedCompanyDomains(refs.dealsByCompany) {
		orgID, ok := refs.orgsByDom[domain]
		if !ok {
			continue
		}
		// One read per company rather than per deal: a company with three
		// deals has one staff list, and it is the same list each time.
		staff, err := staffBySeniority(c, orgID)
		if err != nil {
			return created, err
		}
		if len(staff) == 0 {
			// The company publishes nobody. Its deals have no committee
			// because there is no one to put on one, which the verify pass
			// exempts on purpose.
			continue
		}
		for _, dealID := range refs.dealsByCompany[domain] {
			for i, personID := range staff {
				if i >= len(buyingRoles) {
					break
				}
				if mode == modeDryRun {
					created++
					continue
				}
				added, err := ensureStakeholder(c, dealID, personID, buyingRoles[i])
				if err != nil {
					return created, fmt.Errorf("deal at %s: %w", domain, err)
				}
				if added {
					created++
				}
			}
		}
	}
	return created, nil
}

// projectRoles is the shape of a delivery team, in the order it is filled.
//
// Same heuristic as buyingRoles and the same defence: the most senior person
// on the account sponsors the work, and it descends from there. It will be
// wrong about individuals; a project page with nobody on it is wrong about
// everything, and every project in the demo had exactly that.
var projectRoles = []string{"sponsor", "project_lead", "delivery_lead", "subject_matter_expert"}

// seedProjectStakeholders puts the customer's own people on their project.
//
// It reads the projects back rather than remembering what seedProjects wrote,
// because a re-run creates none — it skips every key that already exists — and
// a pass that only staffed freshly created projects would leave every project
// from an earlier run empty forever.
func seedProjectStakeholders(c *client, mode runMode) (int, error) {
	if mode == modeDryRun {
		return 0, nil
	}
	type project struct {
		ID    string `json:"id"`
		OrgID string `json:"organization_id"`
	}
	var projects []project
	if err := c.getAll("/v1/projects", nil, func(raw json.RawMessage) error {
		var rows []project
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		projects = append(projects, rows...)
		return nil
	}); err != nil {
		return 0, fmt.Errorf("listing projects: %w", err)
	}
	// Stable order, so the same input writes the same audit trail.
	sort.Slice(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })

	created := 0
	// One staff read per COMPANY, not per project: a company with two projects
	// has one staff list, and it is the same list each time.
	staffByOrg := map[string][]string{}
	for _, proj := range projects {
		if proj.OrgID == "" {
			continue
		}
		staff, ok := staffByOrg[proj.OrgID]
		if !ok {
			var err error
			staff, err = staffBySeniority(c, proj.OrgID)
			if err != nil {
				return created, err
			}
			staffByOrg[proj.OrgID] = staff
		}
		for i, personID := range staff {
			if i >= len(projectRoles) {
				break
			}
			added, err := ensureProjectStakeholder(c, proj.ID, personID, projectRoles[i])
			if err != nil {
				return created, fmt.Errorf("project %s: %w", proj.ID, err)
			}
			if added {
				created++
			}
		}
	}
	return created, nil
}

// sortedCompanyDomains keeps the write order stable across runs, so the same
// input produces the same audit trail.
func sortedCompanyDomains(byCompany map[string][]string) []string {
	out := make([]string, 0, len(byCompany))
	for domain := range byCompany {
		out = append(out, domain)
	}
	sort.Strings(out)
	return out
}

// staffBySeniority lists a company's employees, most senior first.
func staffBySeniority(c *client, orgID string) ([]string, error) {
	type employment struct {
		PersonID string `json:"person_id"`
		Role     string `json:"role"`
	}
	var rows []employment
	query := url.Values{"kind": {"employment"}, "organization_id": {orgID}}
	err := c.getAll("/v1/relationships", query, func(raw json.RawMessage) error {
		var page []employment
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		rows = append(rows, page...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing employments: %w", err)
	}
	// A stable sort on a stable list keeps the committee identical across
	// runs, which is what stops a re-seed from reshuffling who the champion is.
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && seniority(rows[j].Role) > seniority(rows[j-1].Role); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.PersonID != "" {
			out = append(out, row.PersonID)
		}
	}
	return out, nil
}

func ensureStakeholder(c *client, dealID, personID, role string) (bool, error) {
	return ensureStakeholderOn(c, "deal_stakeholder", "deal_id", dealID, personID, role)
}

// ensureProjectStakeholder is the same edge on a project. Same endpoint, same
// idempotency: the relationship kind and the id field are what differ.
func ensureProjectStakeholder(c *client, projectID, personID, role string) (bool, error) {
	return ensureStakeholderOn(c, "project_stakeholder", "project_id", projectID, personID, role)
}

// ensureStakeholderOn attaches one person to one record, once.
//
// The list-then-write is not a race guard — the seeder is the only writer —
// but a re-run must not add a second edge for somebody already on the record,
// and the conflict arm covers the case where an edge exists under a role this
// run would not have chosen.
func ensureStakeholderOn(c *client, kind, idField, recordID, personID, role string) (bool, error) {
	var page struct {
		Data []struct {
			PersonID string `json:"person_id"`
		} `json:"data"`
	}
	query := url.Values{"kind": {kind}, idField: {recordID}, "limit": {"50"}}
	if err := c.get("/v1/relationships", query, &page); err != nil {
		return false, fmt.Errorf("listing %s: %w", kind, err)
	}
	for _, row := range page.Data {
		if row.PersonID == personID {
			return false, nil
		}
	}
	body := jsonBody{
		"kind":      kind,
		"person_id": personID,
		idField:     recordID,
		"role":      role,
		"source":    seedSource,
	}
	if err := c.post("/v1/relationships", body, nil); err != nil {
		if isConflict(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
