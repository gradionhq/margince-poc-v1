// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The contracts domain: the agreements a company has already signed, which is
// a different question from what it is being sold.
//
// Three of the five states cannot simply be given. A contract is born draft
// and everything past that is ASSERTED, because the product is explicit that
// a status moves when a human says so and never because a date passed.
// `cancelled` needs the cancellation recorded and then asserted — recording
// the dates alone does not end the agreement. `superseded` is reachable only
// by renewing, which writes the successor and the pointer in one transaction,
// so asserting it directly is refused outright.

import (
	"fmt"
	"net/url"
	"strings"
)

// seedContracts files the agreements a company has signed — the domain that
// answers "what are we already committed to?" separately from "what are we
// selling?".
//
// The three states a contract cannot simply be given are handled by doing the
// thing that produces them: a cancellation is recorded, a renewal writes the
// successor and supersedes its predecessor in one transaction, and everything
// else is asserted through the status endpoint. Setting `superseded` directly
// is refused outright, because it would leave the pointer and the status
// disagreeing.
func seedContracts(c *client, cfg demoConfig, refs pipelineRefs, mode runMode) (int, error) {
	// A successor named by renews_into is NOT created here: renewing writes
	// its row, and creating it first would leave two.
	successors := map[string]bool{}
	for _, contract := range cfg.Contracts {
		if contract.RenewsInto != "" {
			successors[contract.RenewsInto] = true
		}
	}

	created := 0
	ids := map[string]string{}
	for _, contract := range cfg.Contracts {
		if successors[contract.Ref] {
			continue
		}
		id, isNew, err := ensureContract(c, contract, refs, mode)
		if err != nil {
			return created, err
		}
		if isNew {
			created++
		}
		ids[contract.Ref] = id
	}
	// The lifecycle runs after every contract exists, because a renewal names
	// a successor that may be later in the list.
	for _, contract := range cfg.Contracts {
		if successors[contract.Ref] {
			continue
		}
		if err := driveContract(c, contract, ids, refs, mode); err != nil {
			return created, fmt.Errorf("contract %s: %w", contract.Ref, err)
		}
	}
	return created, nil
}

func ensureContract(c *client, contract demoContract, refs pipelineRefs, mode runMode) (id string, isNew bool, err error) {
	orgID, ok := refs.orgsByDom[strings.ToLower(contract.Company)]
	if !ok {
		return "", false, fmt.Errorf("contract %s names company %q, which is not seeded", contract.Ref, contract.Company)
	}
	if mode == modeDryRun {
		return "", true, nil
	}

	existing, err := findContract(c, orgID, contract.Title)
	if err != nil {
		return "", false, err
	}
	if existing != "" {
		return existing, false, nil
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

	var out struct {
		ID string `json:"id"`
	}
	if err := c.post("/v1/contracts", body, &out); err != nil {
		return "", false, fmt.Errorf("contract %s: %w", contract.Ref, err)
	}
	return out.ID, true, nil
}

// driveContract moves one contract to the state the dataset asks for. Each
// step is skipped when it is already there, so a re-run changes nothing.
func driveContract(c *client, contract demoContract, ids map[string]string, refs pipelineRefs, mode runMode) error {
	if mode == modeDryRun {
		return nil
	}
	id := ids[contract.Ref]
	if id == "" {
		return nil
	}
	current, err := contractStatus(c, id)
	if err != nil {
		return err
	}
	// A terminal contract is finished with: re-asserting anything on it is
	// refused, and a re-run must not try.
	if current == "cancelled" || current == "superseded" || current == "expired" {
		return nil
	}

	// A contract is born draft. Anything the dataset wants beyond that is
	// asserted first, because a cancellation records dates without moving the
	// status and a renewal only makes sense on a live agreement.
	if contract.Status != "" && contract.Status != current {
		if err := c.post("/v1/contracts/"+id+"/status", jsonBody{"status": contract.Status}, nil); err != nil {
			return fmt.Errorf("asserting status %q: %w", contract.Status, err)
		}
	}

	switch {
	case contract.RenewsInto != "":
		return renewContract(c, contract, ids, refs)
	case contract.Cancel != nil:
		body := jsonBody{
			"cancellation_notice_on":    refs.date(contract.Cancel.NoticeInDays),
			"cancellation_effective_on": refs.date(contract.Cancel.EffectiveInDays),
		}
		if err := c.post("/v1/contracts/"+id+"/cancellation", body, nil); err != nil {
			return fmt.Errorf("recording the cancellation: %w", err)
		}
		// Recording the terms is not the same as ending the agreement: the
		// status is a separate assertion, which is the product's point that
		// no date moves a status by itself.
		if err := c.post("/v1/contracts/"+id+"/status", jsonBody{"status": "cancelled"}, nil); err != nil {
			return fmt.Errorf("asserting the cancellation: %w", err)
		}
	}
	return nil
}

// renewContract supersedes a contract by renewing it. The successor's terms
// come from the dataset entry it points at — a renewal freezes its own rate
// and inherits nothing, so every field is restated.
//
// The successor was already created by ensureContract, which is a duplicate
// the renewal replaces: renewing writes its OWN successor row, so the
// placeholder is archived first and the dataset's second entry is what the
// renewal is filled from.
func renewContract(c *client, contract demoContract, ids map[string]string, refs pipelineRefs) error {
	var terms demoContract
	for _, candidate := range refs.contractsByRef {
		if candidate.Ref == contract.RenewsInto {
			terms = candidate
			break
		}
	}
	if terms.Ref == "" {
		return fmt.Errorf("renews_into names %q, which is not in this dataset", contract.RenewsInto)
	}
	body := jsonBody{"title": terms.Title, "value_basis": terms.ValueBasis, "auto_renew": terms.AutoRenew}
	addIfSet(body, "contract_number", terms.ContractNumber)
	if terms.ValueMinor > 0 {
		body["value_minor"] = terms.ValueMinor
		body["currency"] = terms.Currency
	}
	if terms.StartsInDays != 0 {
		body["starts_on"] = refs.date(terms.StartsInDays)
	}
	if terms.EndsInDays != 0 {
		body["ends_on"] = refs.date(terms.EndsInDays)
	}
	if terms.RenewalInDays != 0 {
		body["renewal_on"] = refs.date(terms.RenewalInDays)
	}
	if terms.NoticePeriodDays > 0 {
		body["notice_period_days"] = terms.NoticePeriodDays
	}
	var out struct {
		SuccessorID string `json:"successor_id"`
		ID          string `json:"id"`
	}
	if err := c.post("/v1/contracts/"+ids[contract.Ref]+"/renewal", body, &out); err != nil {
		return fmt.Errorf("renewing: %w", err)
	}
	// The successor is born draft like any contract. The dataset says what it
	// should be, and nothing else will assert it: the main loop skips a
	// successor precisely because the renewal owns its row.
	successorID := out.SuccessorID
	if successorID == "" {
		successorID = out.ID
	}
	if terms.Status != "" && terms.Status != "draft" && successorID != "" {
		if err := c.post("/v1/contracts/"+successorID+"/status", jsonBody{"status": terms.Status}, nil); err != nil {
			return fmt.Errorf("asserting the successor's status: %w", err)
		}
	}
	return nil
}

func contractStatus(c *client, id string) (string, error) {
	var out struct {
		Status string `json:"status"`
	}
	if err := c.get("/v1/contracts/"+id, nil, &out); err != nil {
		return "", fmt.Errorf("reading contract %s: %w", id, err)
	}
	return out.Status, nil
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
