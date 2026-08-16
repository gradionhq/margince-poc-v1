// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Who owns what.
//
// This is a RULE rather than a list, because the dataset grows: 20 companies
// are ingested today and 180 are not, and a demo that only scopes correctly
// for the ones somebody remembered to name in a config file is a demo that
// breaks the moment it is extended.
//
// An ownerless row is workspace-shared — visible at EVERY row scope — so
// leaving one unowned does not merely look untidy: it makes the whole access
// model undemonstrable. Before this ran, both SDRs saw all 20 companies and
// the difference between a rep's view and a team lead's was invisible.

import (
	"fmt"
	"hash/fnv"
	"net/url"
	"sort"
	"strings"
)

// assignOwners gives every organization an owner, and every person the owner
// of the company they work at.
//
// The company's owner is chosen by hashing its domain across the sellers, so
// the answer is stable (a re-run never reshuffles the book) and automatic (a
// company ingested next month lands with somebody without anyone editing a
// list). demo.json may still name an owner explicitly where the story needs
// one — the deals do exactly that — and an explicit choice always wins.
//
// A person inherits their employer's owner rather than being hashed
// separately: a rep who owns the account owns the conversation with it, and
// splitting a company's contacts across two reps is a state a real CRM only
// reaches by accident.
func assignOwners(c *client, cfg demoConfig, refs pipelineRefs, mode runMode) (orgs, people int, err error) {
	if len(refs.ownerRefByDomain) == 0 {
		return 0, 0, fmt.Errorf("no seats to own anything — seed the users first (-dsn)")
	}

	for domain, orgID := range refs.orgsByDom {
		ownerID, ok := refs.usersByRef[refs.ownerRefByDomain[domain]]
		if !ok {
			continue
		}
		if mode == modeDryRun {
			orgs++
			continue
		}
		changed, err := setOrganizationOwner(c, orgID, ownerID)
		if err != nil {
			return orgs, people, fmt.Errorf("owning %s: %w", domain, err)
		}
		if changed {
			orgs++
		}
		staff, err := setStaffOwner(c, orgID, ownerID, mode)
		if err != nil {
			return orgs, people, fmt.Errorf("owning the staff at %s: %w", domain, err)
		}
		people += staff
	}
	return orgs, people, nil
}

// resolveOwners decides who owns each company, once, so ownership and the
// activities written on an account cannot disagree about it.
//
// A company named by a deal takes that deal's owner; every other company is
// hashed across the sellers. Both halves are rules, so a company ingested
// next month lands with somebody without an edit anywhere.
func (r *pipelineRefs) resolveOwners(cfg demoConfig) {
	sellers := sellerIDs(cfg, *r)
	if len(sellers) == 0 {
		return
	}
	for _, deal := range cfg.Deals {
		if deal.Owner != "" {
			r.ownerRefByDomain[strings.ToLower(deal.Company)] = deal.Owner
		}
	}
	for domain := range r.orgsByDom {
		if _, ok := r.ownerRefByDomain[domain]; !ok {
			r.ownerRefByDomain[domain] = sellers[hashIndex(domain, len(sellers))]
		}
	}
}

// sellerIDs is the seats a record may be assigned to, in a stable order.
//
// Only the people who carry a book: the CSO sees everything already and
// giving her accounts would make her view indistinguishable from a rep's,
// which is the one thing the management role exists to show.
func sellerIDs(cfg demoConfig, refs pipelineRefs) []string {
	var refsOut []string
	for _, user := range cfg.Users {
		if user.Team == "" {
			continue
		}
		if _, ok := refs.usersByRef[user.Ref]; ok {
			refsOut = append(refsOut, user.Ref)
		}
	}
	sort.Strings(refsOut)
	return refsOut
}

// hashIndex spreads a key deterministically across n buckets. Stable across
// runs and across machines, which is what keeps a re-seed from reshuffling
// who owns what.
//
// Walking the sum down by n keeps every value an int and every step in
// range, so the bucket is provably inside the slice without a conversion
// anyone has to reason about.
func hashIndex(key string, n int) int {
	if n <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key)) //craft:ignore swallowed-errors hash.Write never returns an error, as its own contract states
	sum := h.Sum32()
	bucket := 0
	for i := 0; i < 32; i++ {
		bucket = (bucket*2 + int((sum>>(31-i))&1)) % n
	}
	return bucket
}

func setOrganizationOwner(c *client, orgID, ownerID string) (bool, error) {
	var current struct {
		OwnerID string `json:"owner_id"`
		Version int    `json:"version"`
	}
	if err := c.get("/v1/organizations/"+orgID, nil, &current); err != nil {
		return false, fmt.Errorf("reading it back: %w", err)
	}
	if current.OwnerID == ownerID {
		return false, nil
	}
	body := jsonBody{"owner_id": ownerID, "if_version": current.Version}
	if err := c.patch("/v1/organizations/"+orgID, body, nil); err != nil {
		return false, err
	}
	return true, nil
}

// setStaffOwner hands every employee of one company to that company's owner.
func setStaffOwner(c *client, orgID, ownerID string, mode runMode) (int, error) {
	staff, err := employeesOf(c, orgID)
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, personID := range staff {
		if mode == modeDryRun {
			changed++
			continue
		}
		var current struct {
			OwnerID string `json:"owner_id"`
			Version int    `json:"version"`
		}
		if err := c.get("/v1/people/"+personID, nil, &current); err != nil {
			return changed, fmt.Errorf("reading person %s: %w", personID, err)
		}
		if current.OwnerID == ownerID {
			continue
		}
		body := jsonBody{"owner_id": ownerID, "if_version": current.Version}
		if err := c.patch("/v1/people/"+personID, body, nil); err != nil {
			return changed, fmt.Errorf("owning person %s: %w", personID, err)
		}
		changed++
	}
	return changed, nil
}

// employeesOf lists the people the employment edges say work at a company.
func employeesOf(c *client, orgID string) ([]string, error) {
	var page struct {
		Data []struct {
			PersonID string `json:"person_id"`
		} `json:"data"`
	}
	query := url.Values{"kind": {"employment"}, "organization_id": {orgID}, "limit": {"200"}}
	if err := c.get("/v1/relationships", query, &page); err != nil {
		return nil, fmt.Errorf("listing employments: %w", err)
	}
	out := make([]string, 0, len(page.Data))
	for _, row := range page.Data {
		if row.PersonID != "" {
			out = append(out, row.PersonID)
		}
	}
	return out, nil
}
