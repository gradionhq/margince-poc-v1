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

	var orgs struct {
		Data []struct {
			DisplayName string `json:"display_name"`
			OwnerID     string `json:"owner_id"`
			IsAnchor    bool   `json:"is_anchor"`
		} `json:"data"`
	}
	if err := c.get("/v1/organizations", url.Values{"limit": {"200"}}, &orgs); err != nil {
		return nil, err
	}
	var unowned []string
	for _, row := range orgs.Data {
		if row.OwnerID == "" && !row.IsAnchor {
			unowned = append(unowned, row.DisplayName)
		}
	}
	if len(unowned) > 0 {
		findings = append(findings, verifyFinding{
			Rule:   "organizations are owned",
			Detail: fmt.Sprintf("%d without an owner (%s) — an ownerless row is visible at every row scope", len(unowned), sample(unowned)),
		})
	}

	var people struct {
		Data []struct {
			FullName string `json:"full_name"`
			OwnerID  string `json:"owner_id"`
		} `json:"data"`
	}
	if err := c.get("/v1/people", url.Values{"limit": {"500"}}, &people); err != nil {
		return nil, err
	}
	unowned = nil
	for _, row := range people.Data {
		if row.OwnerID == "" {
			unowned = append(unowned, row.FullName)
		}
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
	var people struct {
		Data []struct {
			ID       string `json:"id"`
			FullName string `json:"full_name"`
		} `json:"data"`
	}
	if err := c.get("/v1/people", url.Values{"limit": {"500"}}, &people); err != nil {
		return nil, err
	}
	var orphans []string
	for _, person := range people.Data {
		var rels struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		query := url.Values{"kind": {"employment"}, "person_id": {person.ID}, "limit": {"1"}}
		if err := c.get("/v1/relationships", query, &rels); err != nil {
			return nil, err
		}
		if len(rels.Data) == 0 {
			orphans = append(orphans, person.FullName)
		}
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
	var page struct {
		Data []struct {
			ID    string `json:"id"`
			Kind  string `json:"kind"`
			Links []struct {
				EntityType string `json:"entity_type"`
			} `json:"links"`
		} `json:"data"`
	}
	if err := c.get("/v1/activities", url.Values{"limit": {"500"}}, &page); err != nil {
		return nil, err
	}
	conversations, withPerson := 0, 0
	for _, act := range page.Data {
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
	var deals struct {
		Data []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			OrganizationID string `json:"organization_id"`
		} `json:"data"`
	}
	if err := c.get("/v1/deals", url.Values{"limit": {"200"}}, &deals); err != nil {
		return nil, err
	}
	var bare []string
	for _, deal := range deals.Data {
		var rels struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		query := url.Values{"kind": {"deal_stakeholder"}, "deal_id": {deal.ID}, "limit": {"1"}}
		if err := c.get("/v1/relationships", query, &rels); err != nil {
			return nil, err
		}
		if len(rels.Data) > 0 {
			continue
		}
		staff, err := employeesOf(c, deal.OrganizationID)
		if err != nil {
			return nil, err
		}
		if len(staff) == 0 {
			continue
		}
		bare = append(bare, deal.Name)
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
	var orgs struct {
		Data []struct {
			DisplayName string `json:"display_name"`
			Lifecycle   string `json:"lifecycle"`
			IsAnchor    bool   `json:"is_anchor"`
		} `json:"data"`
	}
	if err := c.get("/v1/organizations", url.Values{"limit": {"200"}}, &orgs); err != nil {
		return nil, err
	}
	var unknown []string
	for _, row := range orgs.Data {
		if !row.IsAnchor && (row.Lifecycle == "" || row.Lifecycle == "unknown") {
			unknown = append(unknown, row.DisplayName)
		}
	}
	if len(unknown) == 0 {
		return nil, nil
	}
	return []verifyFinding{{
		Rule:   "accounts have a lifecycle",
		Detail: fmt.Sprintf("%d still unknown (%s) — add them to demo.json's lifecycle map", len(unknown), sample(unknown)),
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
