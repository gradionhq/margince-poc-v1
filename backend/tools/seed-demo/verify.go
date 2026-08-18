// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The check that runs after every seed.
//
// Each rule here exists because the gap it catches was real, shipped, and
// invisible until somebody went looking. A seeded database that is missing
// half its edges looks exactly like a complete one from the record counts —
// 91 people and 20 companies read as success whether or not anybody owns
// them.
//
// It fails loudly rather than warning. A demo with a silent hole is worse
// than no demo: it teaches the wrong thing about the product and nobody finds
// out until it is in front of somebody.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// verifyFinding is one thing that is wrong, phrased so the reader knows what
// to do about it rather than only what the number was.
type verifyFinding struct {
	Rule   string
	Detail string
}

// verifySeed re-reads the installation and reports every rule it breaks.
func verifySeed(c *client, cfg demoConfig, mode runMode) error {
	if mode == modeDryRun {
		return nil
	}
	var findings []verifyFinding
	for _, check := range []func(*client, demoConfig) ([]verifyFinding, error){
		checkEverythingIsOwned,
		checkPeopleAreEmployed,
		checkActivitiesReachPeople,
		checkDealsHaveStakeholders,
		checkLifecycleIsSet,
		checkCoverage,
		checkTheSurfacesAreNotEmpty,
		checkPartnersArePromoted,
	} {
		found, err := check(c, cfg)
		if err != nil {
			return fmt.Errorf("verifying: %w", err)
		}
		findings = append(findings, found...)
	}

	if len(findings) == 0 {
		fmt.Printf("\nverify:        OK — every seeded record is owned, employed, linked and staged\n")
		return nil
	}
	fmt.Printf("\nverify:        %d rule(s) broken\n", len(findings))
	for _, f := range findings {
		fmt.Printf("  %-28s %s\n", f.Rule, f.Detail)
	}
	return fmt.Errorf("the seed is incomplete — see the rules above")
}

// checkEverythingIsOwned is the rule whose absence made the access model
// undemonstrable: an ownerless row is workspace-shared, visible at EVERY row
// scope, so with nothing owned both reps saw every company and the difference
// between a rep's view and a team lead's was nil.
func checkEverythingIsOwned(c *client, _ demoConfig) ([]verifyFinding, error) {
	var findings []verifyFinding

	type orgRow struct {
		DisplayName string `json:"display_name"`
		OwnerID     string `json:"owner_id"`
		IsAnchor    bool   `json:"is_anchor"`
	}
	var unowned []string
	err := c.getAll("/v1/organizations", nil, func(raw json.RawMessage) error {
		var rows []orgRow
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			if row.OwnerID == "" && !row.IsAnchor {
				unowned = append(unowned, row.DisplayName)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(unowned) > 0 {
		findings = append(findings, verifyFinding{
			Rule:   "organizations are owned",
			Detail: fmt.Sprintf("%d without an owner (%s) — an ownerless row is visible at every row scope", len(unowned), sample(unowned)),
		})
	}

	type personRow struct {
		FullName string `json:"full_name"`
		OwnerID  string `json:"owner_id"`
	}
	unowned = nil
	err = c.getAll("/v1/people", nil, func(raw json.RawMessage) error {
		var rows []personRow
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			if row.OwnerID == "" {
				unowned = append(unowned, row.FullName)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(unowned) > 0 {
		findings = append(findings, verifyFinding{
			Rule:   "people are owned",
			Detail: fmt.Sprintf("%d without an owner (%s)", len(unowned), sample(unowned)),
		})
	}
	return findings, nil
}

// checkPeopleAreEmployed catches the contact who belongs to no company: they
// appear in a search and on no company page, which is how a promoted lead
// vanished from the account it came from.
func checkPeopleAreEmployed(c *client, _ demoConfig) ([]verifyFinding, error) {
	// Every employment edge in one paginated read, rather than one request per
	// person. At 151 people the N+1 was merely slow; at the 800+ this dataset
	// is heading for it dominated the whole verify pass.
	employed := map[string]bool{}
	err := c.getAll("/v1/relationships", url.Values{"kind": {"employment"}}, func(raw json.RawMessage) error {
		var rows []struct {
			PersonID string `json:"person_id"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			employed[row.PersonID] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	var orphans []string
	err = c.getAll("/v1/people", nil, func(raw json.RawMessage) error {
		var rows []struct {
			ID       string `json:"id"`
			FullName string `json:"full_name"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, person := range rows {
			if !employed[person.ID] {
				orphans = append(orphans, person.FullName)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(orphans) == 0 {
		return nil, nil
	}
	return []verifyFinding{{
		Rule:   "people work somewhere",
		Detail: fmt.Sprintf("%d employed nowhere (%s) — they show on no company page", len(orphans), sample(orphans)),
	}}, nil
}

// checkActivitiesReachPeople catches correspondence filed against a company
// and nobody: the company timeline fills, every person's stays empty, and a
// rep opening a contact sees no history of talking to them.
func checkActivitiesReachPeople(c *client, _ demoConfig) ([]verifyFinding, error) {
	conversations, withPerson := 0, 0
	err := c.getAll("/v1/activities", nil, func(raw json.RawMessage) error {
		var rows []struct {
			ID    string `json:"id"`
			Kind  string `json:"kind"`
			Links []struct {
				EntityType string `json:"entity_type"`
			} `json:"links"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, act := range rows {
			// A note or a task is internal — about an account, not with anybody.
			if act.Kind != "email" && act.Kind != "call" && act.Kind != "meeting" {
				continue
			}
			conversations++
			for _, link := range act.Links {
				if link.EntityType == "person" {
					withPerson++
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if conversations == 0 || withPerson > 0 {
		return nil, nil
	}
	return []verifyFinding{{
		Rule:   "conversations name a person",
		Detail: fmt.Sprintf("%d mails/calls/meetings link to no person — every contact's timeline is empty", conversations),
	}}, nil
}

// checkDealsHaveStakeholders catches the deal that is a number with nobody
// attached: no champion, no economic buyer, nobody in the way.
//
// A deal at a company that publishes NO staff is exempt, and the distinction
// matters: awin.com names nobody on its site, so its deal has no committee
// because there is nobody to put on one — not because the seeder forgot.
// Failing on that would train the reader to ignore this check.
func checkDealsHaveStakeholders(c *client, _ demoConfig) ([]verifyFinding, error) {
	// Three whole-table reads instead of two requests per deal: which deals
	// have a committee, and which companies employ anybody at all.
	hasCommittee := map[string]bool{}
	err := c.getAll("/v1/relationships", url.Values{"kind": {"deal_stakeholder"}}, func(raw json.RawMessage) error {
		var rows []struct {
			DealID string `json:"deal_id"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			hasCommittee[row.DealID] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	hasStaff := map[string]bool{}
	err = c.getAll("/v1/relationships", url.Values{"kind": {"employment"}}, func(raw json.RawMessage) error {
		var rows []struct {
			OrganizationID string `json:"organization_id"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			hasStaff[row.OrganizationID] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	var bare []string
	err = c.getAll("/v1/deals", nil, func(raw json.RawMessage) error {
		var rows []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			OrganizationID string `json:"organization_id"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, deal := range rows {
			if hasCommittee[deal.ID] || !hasStaff[deal.OrganizationID] {
				continue
			}
			bare = append(bare, deal.Name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(bare) == 0 {
		return nil, nil
	}
	return []verifyFinding{{
		Rule:   "deals have a committee",
		Detail: fmt.Sprintf("%d with no stakeholder (%s)", len(bare), sample(bare)),
	}}, nil
}

// checkLifecycleIsSet catches the account left at the default: a filter whose
// whole job is "who are our customers?" returns everything when nothing has
// been staged.
func checkLifecycleIsSet(c *client, _ demoConfig) ([]verifyFinding, error) {
	var unknown []string
	err := c.getAll("/v1/organizations", nil, func(raw json.RawMessage) error {
		var rows []struct {
			DisplayName string `json:"display_name"`
			Lifecycle   string `json:"lifecycle"`
			IsAnchor    bool   `json:"is_anchor"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			if !row.IsAnchor && (row.Lifecycle == "" || row.Lifecycle == "unknown") {
				unknown = append(unknown, row.DisplayName)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(unknown) == 0 {
		return nil, nil
	}
	return []verifyFinding{{
		Rule:   "accounts have a lifecycle",
		Detail: fmt.Sprintf("%d still unknown (%s) — add them to demo.json's lifecycle map", len(unknown), sample(unknown)),
	}}, nil
}

// checkTheSurfacesAreNotEmpty catches a phase that silently created nothing.
//
// Tags, lists, quotas, offers and products are each a SCREEN, and an empty one
// looks identical to a broken one: a Tags page with no rows teaches a viewer
// that the product has no tags. The seeder reports "0 new" for a phase that
// converged AND for a phase that failed to write anything, so the count in the
// run's own output cannot tell those apart — only reading the installation
// back can.
//
// It asserts presence rather than a number. How many tags is a demo decision
// the coverage matrix owns; whether the phase ran at all is a correctness one.
func checkTheSurfacesAreNotEmpty(c *client, _ demoConfig) ([]verifyFinding, error) {
	var findings []verifyFinding
	for _, surface := range []struct {
		what  string
		path  string
		query url.Values
		why   string
	}{
		{"tags", "/v1/tags", nil, "the Tags screen reads as a product without tags"},
		{"lists", "/v1/lists", nil, "no saved segment to open"},
		{"quotas", "/v1/quotas", nil, "attainment has nothing to be a percentage of"},
		{"products", "/v1/products", nil, "an offer has no rate card behind it"},
	} {
		count := 0
		err := c.getAll(surface.path, surface.query, func(raw json.RawMessage) error {
			var rows []json.RawMessage
			if err := json.Unmarshal(raw, &rows); err != nil {
				return err
			}
			count += len(rows)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("counting %s: %w", surface.what, err)
		}
		if count == 0 {
			findings = append(findings, verifyFinding{
				Rule:   "the surfaces carry rows",
				Detail: fmt.Sprintf("no %s were written — %s", surface.what, surface.why),
			})
		}
	}
	return findings, nil
}

// checkPartnersArePromoted reads the list the Partners screen reads.
//
// This rule exists because the screen shipped fully built and showing
// nothing: demo.json named three partners, no code wrote them, and every
// other count in the seeder's report was correct — so the hole was invisible
// until somebody opened the screen.
//
// It asks /v1/partners rather than counting organizations, because that is
// the question the screen asks. An org that was created but never promoted
// answers this list with nothing, which is exactly the failure to catch.
func checkPartnersArePromoted(c *client, cfg demoConfig) ([]verifyFinding, error) {
	if len(cfg.Partners) == 0 {
		return nil, nil
	}
	promoted := map[string]bool{}
	err := c.getAll("/v1/partners", nil, func(raw json.RawMessage) error {
		var rows []struct {
			OrganizationID string `json:"organization_id"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			promoted[row.OrganizationID] = true
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing partners: %w", err)
	}
	orgIDs, err := orgIDsByDomain(c)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, p := range cfg.Partners {
		id, seeded := orgIDs[strings.ToLower(p.Domain)]
		if !seeded || !promoted[id] {
			missing = append(missing, p.DisplayName)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	return []verifyFinding{{
		Rule:   "the partners are promoted",
		Detail: fmt.Sprintf("%d of %d named partners are absent from /v1/partners (%s) — the Partners screen renders empty", len(missing), len(cfg.Partners), sample(missing)),
	}}, nil
}

// sample names the first few offenders, because a bare count sends the reader
// back to the database to find out which ones.
func sample(names []string) string {
	const show = 3
	if len(names) <= show {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:show], ", ") + fmt.Sprintf(", +%d more", len(names)-show)
}
