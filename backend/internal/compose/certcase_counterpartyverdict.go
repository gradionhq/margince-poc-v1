// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for capture_counterparty_verdict/verdict.
//
// It certifies the shipped path rather than a description of it: the request
// comes from verdictRequest, the same builder the engine calls, and the reply is
// judged by validateVerdictPayload, the same validator the engine applies. A
// case that rebuilt either would measure a copy, and a copy stays green through
// the change that breaks the original.
//
// This site is where the injection corpus lives, so the distinction between a
// refused reply and a wrong one carries the whole meaning of a run: an escaped
// fence shows up as an answer about an address nobody asked about, which the
// validator refuses, while a fence that held shows up as an ordinary verdict
// that may simply disagree.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// counterpartyVerdictFixture is one first-time sender, in exactly the fields
// the ledger row hands the engine. It carries nothing the corpus asserts and no
// id — see Prepare for both.
type counterpartyVerdictFixture struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Subject     string `json:"subject"`
	Body        string `json:"body"`
}

// counterpartyVerdictCases serves the one site that judges a first-time sender.
type counterpartyVerdictCases struct{}

func (counterpartyVerdictCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskCaptureCounterpartyVerdict,
		Variant: "verdict",
		Kind:    ai.SiteKindOneShot,
	}
}

// CertifiedScope narrows the record to the one call this case makes. Judging one
// sender is up to two: a verdict under the engine's confidence floor is re-asked
// once, unbound, so the second attempt is a stronger model on the same message —
// and the verdict that gets applied is that one's, or the row is retired to a
// human. A run measures the first answer and does not weigh its confidence,
// which is the engine's decision about whether to ask again rather than a
// judgement on whether the answer is usable.
func (counterpartyVerdictCases) CertifiedScope() string { return aitasks.ScopeSingleCall }

// Prepare turns one sender and the verdict the scenario expects into a runnable
// case, MINTING the ledger row id rather than reading it from either.
//
// Production takes that id from the ledger row, which no model has ever seen, so
// the only way an answer can carry it is to have read it out of this call's
// prompt. A fixture that supplied the id would put it in the hands of whoever
// authored the expected reply, and a model echoing back an id it was handed
// would then be indistinguishable from one that answered about the right sender
// — which is exactly the confusion validateVerdictPayload exists to prevent.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (counterpartyVerdictCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f counterpartyVerdictFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("capture_counterparty_verdict/verdict: the fixture is not the shape this site takes: %w", err)
	}
	// A correct reply differs from an incorrect one in the verdict token alone,
	// so the expectation IS that token rather than a wrapper carrying it.
	var want string
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf(
			"capture_counterparty_verdict/verdict: the expected answer is not a verdict token: %w", err)
	}
	// An expectation outside the closed vocabulary is unreachable: the validator
	// refuses every reply that could satisfy it, so the scenario would measure
	// nothing for as long as it stayed in the corpus. Naming it here costs a
	// parse; finding it later costs a paid run.
	if !verdictLabels[want] {
		return nil, fmt.Errorf(
			"capture_counterparty_verdict/verdict: the scenario expects %q, which is not real|noise", want)
	}
	return &counterpartyVerdictCase{
		row: capture.PendingCounterparty{
			ID:          ids.NewV7(),
			DisplayName: f.DisplayName,
			Email:       f.Email,
			Subject:     f.Subject,
			Body:        f.Body,
		},
		expected: want,
	}, nil
}

// counterpartyVerdictCase is one fixture sender ready to be judged, closed over
// the minted row id and the verdict the scenario expects.
type counterpartyVerdictCase struct {
	row      capture.PendingCounterparty
	expected string
}

// Run issues the one request this site sends.
func (c *counterpartyVerdictCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := verdictRequest(c.row)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("capture_counterparty_verdict/verdict: %w", err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate applies the engine's own checks in the engine's own order — parse,
// then validateVerdictPayload against the row that was asked about — and only
// then asks whether the answer is the one the scenario expects. The order is the
// meaning: a reply that fails the validator has no verdict to disagree with.
func (c *counterpartyVerdictCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	var payload verdictPayload
	if err := json.Unmarshal([]byte(ai.Unfence(trace.Output)), &payload); err != nil {
		return aitasks.Outcome{
			Result: aitasks.OutcomeInvalid,
			Detail: fmt.Sprintf("unparseable model output: %v", err),
		}
	}
	if msg := validateVerdictPayload(payload, c.row); msg != "" {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: msg}
	}
	// validateVerdictPayload admits exactly one result: every result must carry
	// the one requested id, none may repeat it, and it must be present.
	answered := payload.Results[0].Verdict
	if answered != c.expected {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: fmt.Sprintf("the model answered %q where the scenario expects %q", answered, c.expected),
		}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}
