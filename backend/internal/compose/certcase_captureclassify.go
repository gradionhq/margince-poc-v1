// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for capture_classify/classify.
//
// It certifies the shipped path rather than a description of it: the request
// comes from classifyRequest, the same builder the engine calls, and the reply
// is judged by validateClassifyPayload, the same validator the engine applies. A
// case that rebuilt either would measure a copy, and a copy stays green through
// the change that breaks the original.
//
// What the expectation MEANS here: one label per fixture message, in fixture
// order. Production asks for exactly one label per message, so a per-message
// claim is the only expectation that says as much as the site answers. Position
// is the identity the corpus can name because the ids are minted here and the
// fixture supplies no key of its own — and the obvious candidate, the subject,
// is not unique in a real backlog, so a keyed expectation would be a second
// thing to keep true and one that silently stops matching.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// captureClassifyFixture is ONE batch, as the backlog read hands it to the
// engine: a list, because this site labels several messages in one call and the
// prompt's whole security question is what one of them can say about another.
type captureClassifyFixture []captureClassifyMessage

// captureClassifyMessage is one backlog row in exactly the fields the prompt
// consumes. It carries nothing the corpus asserts and no id — see Prepare for
// both.
type captureClassifyMessage struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// captureClassifyCases serves the one site that labels captured mail for
// attention routing.
type captureClassifyCases struct{}

func (captureClassifyCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskCaptureClassify,
		Variant: "classify",
		Kind:    ai.SiteKindOneShot,
	}
}

// Prepare turns one batch and the labels the scenario expects into a runnable
// case, MINTING an activity id per message rather than reading it from either.
//
// Production takes those ids from the backlog rows, which no model has ever
// seen, so the only way an answer can carry one is to have read it out of this
// call's prompt. A fixture that supplied them would put them in the hands of
// whoever authored the expected reply, and a model echoing back ids it was
// handed would then be indistinguishable from one that labeled the right
// messages — which is exactly the confusion validateClassifyPayload exists to
// prevent.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (captureClassifyCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var messages captureClassifyFixture
	if err := json.Unmarshal(fixture, &messages); err != nil {
		return nil, fmt.Errorf("capture_classify/classify: the fixture is not the shape this site takes: %w", err)
	}
	if err := refuseUnreadableBatch(messages); err != nil {
		return nil, err
	}
	// A correct reply differs from an incorrect one in the label tokens alone, so
	// the expectation IS those tokens rather than a wrapper carrying them.
	var want []string
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf("capture_classify/classify: the expected answer is not a list of labels: %w", err)
	}
	if err := refuseUnreachableLabels(want, len(messages)); err != nil {
		return nil, err
	}
	batch := make([]unlabeledMessage, len(messages))
	for i, m := range messages {
		batch[i] = unlabeledMessage{ID: ids.NewV7(), Subject: m.Subject, Body: m.Body}
	}
	return &captureClassifyCase{batch: batch, expected: want}, nil
}

// refuseUnreadableBatch names a batch the backlog read could never have
// returned. The read caps a call at classifyBatchSize rows and truncates every
// body to classifyBodyLimit, so a fixture beyond either bound would certify a
// prompt the product cannot send — and it is truncated in the read rather than
// in the builder, which is why the bound is checked here and not applied.
func refuseUnreadableBatch(messages captureClassifyFixture) error {
	if len(messages) == 0 {
		return errors.New("capture_classify/classify: the fixture supplies no message, so there is nothing to label")
	}
	if len(messages) > classifyBatchSize {
		return fmt.Errorf(
			"capture_classify/classify: the fixture carries %d messages, but one call labels at most %d",
			len(messages), classifyBatchSize)
	}
	for i, m := range messages {
		if n := utf8.RuneCountInString(m.Body); n > classifyBodyLimit {
			return fmt.Errorf(
				"capture_classify/classify: message %d carries a body of %d characters, but the backlog read truncates every body to %d",
				i+1, n, classifyBodyLimit)
		}
	}
	return nil
}

// refuseUnreachableLabels names an expectation the validator can never satisfy:
// a label outside the closed set is refused on every reply, and a count that is
// not one label per message asserts about a message this batch does not carry or
// leaves one unasserted. Each would measure nothing for as long as it stayed in
// the corpus. Naming it here costs a parse; finding it later costs a paid run.
func refuseUnreachableLabels(want []string, messages int) error {
	if len(want) != messages {
		return fmt.Errorf(
			"capture_classify/classify: the scenario expects %d labels for %d messages, and this site answers one label per message",
			len(want), messages)
	}
	for i, label := range want {
		if !classifyLabels[label] {
			return fmt.Errorf(
				"capture_classify/classify: the scenario expects %q for message %d, which is not commitment|meeting|noise",
				label, i+1)
		}
	}
	return nil
}

// captureClassifyCase is one batch ready to be labeled, closed over the minted
// ids and the label the scenario expects for each message.
type captureClassifyCase struct {
	batch    []unlabeledMessage
	expected []string
}

// Run issues the one request this site sends. It sends it bare: production wraps
// the same request in the shape-retry when the brain supports one, and re-asks a
// below-floor message solo on the next rung of the routing ladder. A case that
// did either would certify the answer a model gives after being told to try
// again rather than the answer it gives.
func (c *captureClassifyCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := classifyRequest(c.batch)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("capture_classify/classify: %w", err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate applies the engine's own checks in the engine's own order — parse,
// then validateClassifyPayload against the batch that was asked about — and only
// then asks whether the labels are the ones the scenario expects. The order is
// the meaning: a reply that fails the validator has no labels to disagree with,
// and every way a batch reply can break the id contract is unusable rather than
// wrong.
//
// The confidence floor is deliberately not applied. It is the engine's decision
// about what to do with an answer it already believes — re-ask it solo — not a
// judgement on whether the answer is usable, and a case that folded it in would
// report a hedged correct label as a broken reply.
func (c *captureClassifyCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	var payload classifyPayload
	if err := json.Unmarshal([]byte(ai.Unfence(trace.Output)), &payload); err != nil {
		return aitasks.Outcome{
			Result: aitasks.OutcomeInvalid,
			Detail: fmt.Sprintf("unparseable model output: %v", err),
		}
	}
	if msg := validateClassifyPayload(payload, c.batch); msg != "" {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: msg}
	}
	// validateClassifyPayload admits exactly one result per requested id: every
	// result carries a requested id, none repeats one, and none is missing.
	if disagreements := c.disagreements(payload); len(disagreements) > 0 {
		return aitasks.Outcome{Result: aitasks.OutcomeWrongAnswer, Detail: strings.Join(disagreements, "; ")}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}

// disagreements names every message the reply labels otherwise than the scenario
// expects, in fixture order. All of them, not the first: a run that labeled one
// message right and two wrong is not the near miss one line would read as.
//
// Messages are named by their position in the fixture, which is where their
// expectation was written. The minted id would name nothing a scenario author
// could look up, and the subject is captured text that has no business being
// echoed into a record.
func (c *captureClassifyCase) disagreements(payload classifyPayload) []string {
	labeled := make(map[string]string, len(payload.Results))
	for _, r := range payload.Results {
		labeled[r.ID] = r.Label
	}
	var out []string
	for i, m := range c.batch {
		if answered := labeled[m.ID.String()]; answered != c.expected[i] {
			out = append(out, fmt.Sprintf(
				"message %d is labeled %q where the scenario expects %q", i+1, answered, c.expected[i]))
		}
	}
	return out
}
