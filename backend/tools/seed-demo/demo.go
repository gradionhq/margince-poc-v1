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
	Anchor           anchorCompany       `json:"anchor"`
	Teams            []demoTeam          `json:"teams"`
	Users            []demoUser          `json:"users"`
	UserPassword     string              `json:"user_password"`
	Deals            []demoDeal          `json:"deals"`
	Leads            []demoLead          `json:"leads"`
	Activities       []demoActivity      `json:"activities"`
	Contracts        []demoContract      `json:"contracts"`
	Products         []demoProduct       `json:"products"`
	Offers           []demoOffer         `json:"offers"`
	Consent          []demoConsent       `json:"consent"`
	FinanceCustomers []string            `json:"finance_customers"`
	Lifecycle        map[string][]string `json:"lifecycle"`
	Partners         []demoPartner       `json:"partners"`
	PartnerEdges     []demoPartnerEdge   `json:"partner_edges"`
	DualRolePartners []demoDualPartner   `json:"dual_role_partners"`
	InventedStaff    []demoInventedStaff `json:"invented_staff"`
	RelTypes         demoRelTypes        `json:"relationship_types"`
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

// demoDeal is one invented opportunity. Company is a domain under
// siteresults/; Owner is a user ref. Money is in minor units, which is what
// the API takes. CloseInDays is an offset so the demo stays current.
type demoDeal struct {
	Ref         string `json:"ref"`
	Company     string `json:"company"`
	Name        string `json:"name"`
	Stage       string `json:"stage"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Owner       string `json:"owner"`
	CloseInDays int    `json:"close_in_days"`
	LostReason  string `json:"lost_reason"`
	// Partner is the DOMAIN of the partner this deal is attributed to, and
	// PartnerAttribution is what they did: `sourced` (brought it) or
	// `influenced` (helped). Only `sourced` accrues a commission, and only
	// when the deal is WON — so a dataset that attributes none of its won
	// deals leaves commission_entry empty and the partner program's story
	// stops at "attributed" without ever reaching "earned".
	Partner            string `json:"partner"`
	PartnerAttribution string `json:"partner_attribution"`
}

type demoLead struct {
	Ref      string `json:"ref"`
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	Title    string `json:"title"`
	Company  string `json:"company"`
	Status   string `json:"status"`
	Owner    string `json:"owner"`
	Promote  bool   `json:"promote"`
}

// demoActivity is one thing that happened on a company. DaysAgo puts it in
// the past; DaysIn puts it in the future, which is what an open task or a
// booked meeting is.
type demoActivity struct {
	Company         string `json:"company"`
	Kind            string `json:"kind"`
	Direction       string `json:"direction"`
	Subject         string `json:"subject"`
	Body            string `json:"body"`
	DaysAgo         int    `json:"days_ago"`
	DaysIn          int    `json:"days_in"`
	MeetingStatus   string `json:"meeting_status"`
	DurationSeconds int    `json:"duration_seconds"`
	Assignee        string `json:"assignee"`
}

// demoContract is one agreement. Status is ASSERTED where it can be — the
// product is explicit that a status moves because a human said so, never
// because a date passed. Two states are not assertable and are reached by
// doing the thing instead: Cancel records a cancellation, and RenewsInto
// renews this contract into another in the list, which is the only way a
// contract becomes superseded.
type demoContract struct {
	Ref              string           `json:"ref"`
	Company          string           `json:"company"`
	Deal             string           `json:"deal"`
	Title            string           `json:"title"`
	ContractNumber   string           `json:"contract_number"`
	ValueMinor       int64            `json:"value_minor"`
	Currency         string           `json:"currency"`
	ValueBasis       string           `json:"value_basis"`
	StartsInDays     int              `json:"starts_in_days"`
	EndsInDays       int              `json:"ends_in_days"`
	RenewalInDays    int              `json:"renewal_in_days"`
	AutoRenew        bool             `json:"auto_renew"`
	NoticePeriodDays int              `json:"notice_period_days"`
	SignedInDays     int              `json:"signed_in_days"`
	Status           string           `json:"status"`
	Cancel           *demoCancelTerms `json:"cancel"`
	RenewsInto       string           `json:"renews_into"`
}

type demoCancelTerms struct {
	NoticeInDays    int `json:"notice_in_days"`
	EffectiveInDays int `json:"effective_in_days"`
}

type demoProduct struct {
	Ref            string `json:"ref"`
	Name           string `json:"name"`
	SKU            string `json:"sku"`
	Unit           string `json:"unit"`
	UnitPriceMinor int64  `json:"unit_price_minor"`
	Currency       string `json:"currency"`
	Description    string `json:"description"`
}

type demoOffer struct {
	Ref         string          `json:"ref"`
	Deal        string          `json:"deal"`
	Currency    string          `json:"currency"`
	ValidInDays int             `json:"valid_in_days"`
	State       string          `json:"state"`
	IntroText   string          `json:"intro_text"`
	Lines       []demoOfferLine `json:"lines"`
}

type demoOfferLine struct {
	Product  string `json:"product"`
	Quantity int    `json:"quantity"`
	// UnitPriceMinor overrides the product's own price, in the OFFER's
	// currency. Required when the two differ: a line snapshots its price
	// from the product, and the API refuses to convert one currency into
	// another rather than fabricate a rate (ProductCurrencyMismatchError).
	// Zero means "take the product's price", which is right whenever the
	// offer and the product agree on currency.
	UnitPriceMinor int64 `json:"unit_price_minor"`
}

// demoConsent is a consent state for one person, addressed by their position
// in the company's accepted people list — the dataset holds real people whose
// names may be re-read, so an index is stabler than a name copied twice.
type demoConsent struct {
	Company     string `json:"company"`
	PersonIndex int    `json:"person_index"`
	Purpose     string `json:"purpose"`
	State       string `json:"state"`
}

// demoPartner is one channel partner: a company the installation SELLS WITH
// rather than sells to.
//
// Invented, and marked Synthetic so nothing mistakes one for a crawled
// prospect: a partnership is a commercial agreement, and no amount of reading
// a website reveals one. The three exist to fill the Partners screen, which is
// built and would otherwise be demonstrated empty.
//
// PartnerRole, CertStatus, MarginTier and RelationshipStage are the product's
// own enums (UpsertPartnerRequest in crm.yaml), not free text — the dataset
// gives each partner a different value for all four so the screen shows range.
type demoPartner struct {
	Domain            string            `json:"domain"`
	DisplayName       string            `json:"display_name"`
	LegalName         string            `json:"legal_name"`
	Industry          string            `json:"industry"`
	Locale            string            `json:"locale"`
	Synthetic         bool              `json:"synthetic"`
	PartnerRole       string            `json:"partner_role"`
	CertStatus        string            `json:"cert_status"`
	MarginTier        string            `json:"margin_tier"`
	RelationshipStage string            `json:"relationship_stage"`
	NextStep          string            `json:"next_step"`
	OwnerRef          string            `json:"owner_ref"`
	People            []demoPartnerPers `json:"people"`
}

// demoPartnerPers is somebody at a partner company. Invented like the company
// itself, so unlike a crawled person there is no published-versus-synthesized
// distinction to preserve: the address is built from the partner's own
// .example domain, which RFC 2606 reserves and nothing can deliver to.
type demoPartnerPers struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// demoPartnerEdge ties a partner to an account it works on.
//
// Direction is not symmetric and the field names carry it: Organization is
// the ACCOUNT and Partner is the counterparty, which is the shape the
// contract states for partner_of / referred_by / co_sell_with. Reversing the
// two would file the partner as somebody else's account.
type demoPartnerEdge struct {
	Partner      string `json:"partner"`      // a partner's domain
	Organization string `json:"organization"` // the account's domain
	Kind         string `json:"kind"`         // partner_of | referred_by | co_sell_with
}

// demoDualPartner promotes a REAL crawled company to a partner while it
// stays a customer.
//
// The three synthetic <Country>Partner companies prove the Partners screen
// renders; this proves a partner need not be a separate record from the
// customer, which ADR-0079 calls the basis of the partner program. The
// company keeps its invoices, deals, contracts and contacts and gains a
// partner row on top.
type demoDualPartner struct {
	Company           string `json:"company"` // a domain under siteresults/
	PartnerRole       string `json:"partner_role"`
	CertStatus        string `json:"cert_status"`
	MarginTier        string `json:"margin_tier"`
	RelationshipStage string `json:"relationship_stage"`
	NextStep          string `json:"next_step"`
}

// demoInventedStaff is people invented for a real company that publishes
// none — the dataset's one deliberate exception to "real people come only
// from the website reader".
//
// It is confined to five Asian customers that carry signed contracts and no
// contacts, and every person it writes is marked: source is
// inventedPersonSource rather than seedSource, so a query can always tell
// them from the people the reader actually found.
type demoInventedStaff struct {
	Company string            `json:"company"`
	People  []demoPartnerPers `json:"people"`
}

// demoRelTypes says what each company IS to us, as against where it stands.
//
// Only the exceptions are listed. The base type is derived from lifecycle so
// a company ingested next month is covered with no edit — see
// relationshipTypesFor.
type demoRelTypes struct {
	Overrides map[string][]string `json:"overrides"`
}
