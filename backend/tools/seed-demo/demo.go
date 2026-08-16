// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The invented half of the demo: the installation's own company, the sales
// org, and the seats people log in as. It lives in one editable JSON file
// (datasets/v1/demo.json) so changing the demo is an edit rather than a code
// change — the seeder converges, so re-running after an edit applies it.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type demoConfig struct {
	Anchor       anchorCompany `json:"anchor"`
	Teams        []demoTeam    `json:"teams"`
	Users        []demoUser    `json:"users"`
	UserPassword string        `json:"user_password"`
}

// anchorCompany is the installation's own company — the record that answers
// "who are we?". Its details are real (read from the company's imprint) even
// though everything else in demo.json is invented, because an installation
// misdescribing itself is the one thing a demo cannot fake convincingly.
type anchorCompany struct {
	DisplayName       string       `json:"display_name"`
	LegalName         string       `json:"legal_name"`
	Domain            string       `json:"domain"`
	RegisteredAddress string       `json:"registered_address"`
	RegisterVAT       string       `json:"register_vat"`
	Website           string       `json:"website"`
	ICP               string       `json:"icp"`
	Others            []otherEntry `json:"other_entities"`
}

// otherEntry is a sibling legal entity the company publishes. Carried for
// the record rather than seeded: the anchor is ONE organization, and the
// group's other entities are a fact about it, not four more companies.
type otherEntry struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Market  string `json:"market"`
}

type demoTeam struct {
	Ref  string `json:"ref"`
	Name string `json:"name"`
}

// demoUser is one seat. RoleKey is the WIRE key, which differs from what the
// product displays: "manager" shows as Team Lead and "rep" as Member.
type demoUser struct {
	Ref         string `json:"ref"`
	DisplayName string `json:"display_name"`
	JobTitle    string `json:"job_title"`
	Email       string `json:"email"`
	RoleKey     string `json:"role_key"`
	Team        string `json:"team"`
}

func loadDemoConfig(root string) (demoConfig, error) {
	path := filepath.Join(root, "datasets", "v1", "demo.json")
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the dataset root is a deliberate operator-supplied flag
	if err != nil {
		return demoConfig{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var cfg demoConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return demoConfig{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cfg.Anchor.DisplayName == "" {
		return demoConfig{}, fmt.Errorf("%s names no anchor company", path)
	}
	return cfg, nil
}

// seedAnchor saves the installation's own company. PUT /company creates it on
// first save and updates it after, so this converges without a probe.
//
// It is a human-only write by contract: an agent may propose the company but
// never make it true. The seeder holds a human session, which is the same
// door the onboarding form uses.
func seedAnchor(c *client, anchor anchorCompany, read company, mode runMode) error {
	if mode == modeDryRun {
		fmt.Printf("%-24s %-8s %s\n", anchor.Domain, outcomeDryRun, anchor.DisplayName+" (anchor)")
		return nil
	}
	body := jsonBody{"display_name": anchor.DisplayName}
	addIfSet(body, "legal_name", anchor.LegalName)
	addIfSet(body, "registered_address", anchor.RegisteredAddress)
	addIfSet(body, "register_vat", anchor.RegisterVAT)
	addIfSet(body, "website", anchor.Website)
	addIfSet(body, "icp", anchor.ICP)
	// The descriptive half comes from the reviewed read of the company's own
	// site, so improving that read improves the anchor rather than leaving two
	// descriptions to keep in step.
	for _, name := range []string{
		"industry", "offer_summary", "value_proposition", "usp",
		"customer_pains", "desired_outcomes", "sales_motion", "history",
	} {
		addIfSet(body, name, read.value(name))
	}
	if err := c.put("/v1/company", body, nil); err != nil {
		return fmt.Errorf("saving the anchor company: %w", err)
	}
	fmt.Printf("%-24s %-8s %s\n", anchor.Domain, outcomeNew, anchor.DisplayName+" (anchor)")
	return nil
}
