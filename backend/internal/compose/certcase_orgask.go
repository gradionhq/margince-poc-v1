// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for summarize/org_ask — the company view's prepared
// questions.
//
// It shares everything with summarize/org_brief except the request: the same
// account fixture, the same minted-id discipline, and the same production
// grounding filter deciding which sentences survive. What differs is the one
// thing this site adds — the answer must be grounded in the records the ASKED
// question is about, not merely in the account somewhere.
//
// That is why the scenario names both a question and the labelled records a
// correct answer cites. A `whats_open` answer that cites only last month's
// email is grounded and wrong, and a case that checked grounding alone would
// score it as correct.

import (
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/compose/orgbrief"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// orgAskFixture is one account plus the question asked about it. The account
// half is the brief's fixture, unchanged: two shapes would let the two sites
// drift into certifying different accounts.
type orgAskFixture struct {
	orgBriefFixture
	Question crmcontracts.OrganizationQuestion `json:"question"`
}

type orgAskCases struct{}

func (orgAskCases) Site() aitasks.Site {
	return aitasks.Site{Task: ai.TaskSummarize, Variant: "org_ask", Kind: ai.SiteKindOneShot}
}

// Prepare turns one account, one prepared question and the records a correct
// answer must cite into a runnable case.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (orgAskCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f orgAskFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("summarize/org_ask: the fixture is not the shape this site takes: %w", err)
	}
	// The question runs through the production validator, so a scenario cannot
	// certify a question the endpoint would refuse to answer.
	question, err := orgbrief.ParseQuestion(f.Question)
	if err != nil {
		return nil, fmt.Errorf("summarize/org_ask: %w", err)
	}
	var want []string
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf(
			"summarize/org_ask: the expected answer is not a list of record labels the answer must cite: %w", err)
	}
	in, label, err := orgBriefInput(f.orgBriefFixture)
	if err != nil {
		return nil, fmt.Errorf("summarize/org_ask: %w", err)
	}
	if err := refuseUngroundableBrief(want, label); err != nil {
		return nil, fmt.Errorf("summarize/org_ask: %w", err)
	}
	return &orgBriefCase{
		site: "summarize/org_ask",
		request: func(in orgbrief.Input) model.Request {
			return orgbrief.AskRequest(question, in)
		},
		in: in, orgID: ids.NewV7().String(), label: label, expected: want,
	}, nil
}
