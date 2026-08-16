// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Checking that every state exists, by reading the installation back.
//
// The other verify rules catch a record that is WRONG. This one catches a
// state that is ABSENT — a contract status nobody can click, a lost reason
// the filter has nothing to filter by, a disqualified lead hiding behind
// include_archived. An empty screen and a correct-but-empty screen look
// identical until somebody needs to test against one.
//
// It counts what the installation actually holds rather than what the planner
// intended, so a state the API silently refused is caught here rather than
// assumed present.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// checkCoverage reads the installation back and asserts the coverage matrix:
// every state the product can hold exists at least the number of times the
// matrix asks for.
//
// This is the rule that makes the demo usable as TEST data rather than only as
// a demo. The other rules catch a record that is wrong; this catches a state
// that is ABSENT — a contract status nobody can click, a lost reason the
// filter has nothing to filter by. An empty screen and a correct-but-empty
// screen look identical until somebody needs to test against one.
//
// It counts what the installation actually holds rather than what the planner
// intended, so a planned state the API silently refused is caught here.
func checkCoverage(c *client, _ demoConfig) ([]verifyFinding, error) {
	counts := map[coverageAxis]map[string]int{}
	tally := func(axis coverageAxis, value string) {
		if value == "" {
			return
		}
		if counts[axis] == nil {
			counts[axis] = map[string]int{}
		}
		counts[axis][value]++
	}

	for _, count := range []func(*client, tallyFunc) error{
		tallyLifecycles,
		tallyDeals,
		tallyLeads,
		tallyContractsAndPaper,
	} {
		if err := count(c, tally); err != nil {
			return nil, err
		}
	}

	short := coverageShortfall(coverageMatrix, counts)
	if len(short) == 0 {
		return nil, nil
	}
	return []verifyFinding{{
		Rule:   "every state is represented",
		Detail: fmt.Sprintf("%d state(s) below the coverage matrix:\n      %s", len(short), strings.Join(short, "\n      ")),
	}}, nil
}

// tallyFunc records one observed state against a coverage axis.
type tallyFunc func(coverageAxis, string)

// tallyLifecycles counts where each account stands. The anchor is skipped: it
// is the installation's own company, not somebody we sell to.
func tallyLifecycles(c *client, tally tallyFunc) error {
	return c.getAll("/v1/organizations", nil, func(raw json.RawMessage) error {
		var rows []struct {
			Lifecycle string `json:"lifecycle"`
			IsAnchor  bool   `json:"is_anchor"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			if !row.IsAnchor {
				tally(axisLifecycle, row.Lifecycle)
			}
		}
		return nil
	})
}

// tallyDeals counts a deal by its STAGE while it is open and by its STATUS
// once closed, which is how the matrix names them: a board column is a stage,
// but "won" and "lost" are not columns anybody works.
func tallyDeals(c *client, tally tallyFunc) error {
	return c.getAll("/v1/deals", nil, func(raw json.RawMessage) error {
		var rows []struct {
			Status string `json:"status"`
			Stage  struct {
				Name string `json:"name"`
			} `json:"stage"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			value := row.Stage.Name
			if row.Status == "won" || row.Status == "lost" {
				value = row.Status
			}
			tally(axisDeal, strings.ToLower(value))
		}
		return nil
	})
}

// tallyLeads counts the funnel, archived rows included.
//
// A disqualified lead IS archived — that is what DELETE /v1/leads/{id} does —
// so a plain list omits it and the cell would read as permanently empty no
// matter how many the seeder created.
func tallyLeads(c *client, tally tallyFunc) error {
	return c.getAll("/v1/leads", url.Values{"include_archived": {"true"}}, func(raw json.RawMessage) error {
		var rows []struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			tally(axisLead, row.Status)
		}
		return nil
	})
}

// tallyContractsAndPaper counts contract statuses and the documents attached
// to them. Contracts are listed per organization because there is no
// installation-wide contract list endpoint.
func tallyContractsAndPaper(c *client, tally tallyFunc) error {
	var orgIDs []string
	err := c.getAll("/v1/organizations", nil, func(raw json.RawMessage) error {
		var rows []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			orgIDs = append(orgIDs, row.ID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, orgID := range orgIDs {
		err := c.getAll("/v1/organizations/"+orgID+"/contracts", nil, func(raw json.RawMessage) error {
			var rows []struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(raw, &rows); err != nil {
				return err
			}
			for _, row := range rows {
				tally(axisContract, row.Status)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}

	// Paper: an attachment carrying a contract_id is filed under its
	// contract; one without is an account document. The difference is the
	// whole point of the document cells — a PDF that lost its contract_id
	// floats loose in Documents and its contract shows no paper at all.
	return c.getAll("/v1/attachments", nil, func(raw json.RawMessage) error {
		var rows []struct {
			ContractID string `json:"contract_id"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			if row.ContractID != "" {
				tally(axisDocument, "contract_pdf")
			} else {
				tally(axisDocument, "loose")
			}
		}
		return nil
	})
}
