// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for transcript_propose/next_steps.
//
// It certifies the shipped path: the request comes from transcriptRequest and
// the reply is judged by validateTranscriptPayload, the same builder and the
// same validator the engine uses. A case that rebuilt either would measure a
// copy, and a copy stays green through the change that breaks the original.
//
// What the expectation MEANS here: the set of next steps the transcript states,
// each named by the LINE it is stated on. The line number is an identity the
// corpus can name because the transcript itself supplies it — unlike the thread
// read, this site mints nothing, so a scenario author counts lines in the
// fixture and writes the number down. An answer can only carry a line number by
// having read this call's prompt, which is what the citation check exists to
// prove.
//
// The empty expectation is the important one. Most meetings state no
// commitment anyone could act on, and a site that invents one puts a question in
// front of a rep that the transcript never asked.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// transcriptProposeFixture is ONE transcript, in order, as the transcript read
// hands it to the engine: one string per line, numbered from 1 by the prompt.
type transcriptProposeFixture []string

// transcriptProposeExpectation is one next step the scenario says the
// transcript states, named by the line it is stated on.
//
// Only the line is asserted. The summary and the owner are prose the model
// writes, and a scenario that demanded particular words would grade phrasing
// rather than reading — the rubric is where phrasing is judged.
type transcriptProposeExpectation struct {
	Line int `json:"line"`
}

// transcriptProposeCases serves the one site that reads next steps out of a
// meeting transcript.
type transcriptProposeCases struct{}

func (transcriptProposeCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskTranscriptPropose,
		Variant: "next_steps",
		Kind:    ai.SiteKindOneShot,
	}
}

// CertifiedScope is the site's own kind: reading a transcript IS one call.
// There is no solo re-ask here — a proposal under the floor is dropped, not
// asked about again — so the call this case makes is the whole path.
func (transcriptProposeCases) CertifiedScope() string { return aitasks.ScopeFullInvocation }

// Prepare turns one transcript and the next steps the scenario expects into a
// runnable case.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (transcriptProposeCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var lines transcriptProposeFixture
	if err := json.Unmarshal(fixture, &lines); err != nil {
		return nil, fmt.Errorf("transcript_propose/next_steps: the fixture is not the shape this site takes: %w", err)
	}
	if err := refuseUnreadableTranscript(lines); err != nil {
		return nil, err
	}
	var want []transcriptProposeExpectation
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf(
			"transcript_propose/next_steps: the expected answer is not a list of next steps: %w", err)
	}
	if err := refuseUnreachableSteps(want, len(lines)); err != nil {
		return nil, err
	}
	return &transcriptProposeCase{lines: lines, expected: want}, nil
}

// refuseUnreadableTranscript names a transcript the read could never have been
// given: the engine refuses one past withinReadingBounds outright, and every
// line is citable evidence the approval quotes back — a blank one would be
// offered to a human as the text a commitment was read from.
func refuseUnreadableTranscript(lines transcriptProposeFixture) error {
	if len(lines) == 0 {
		return errors.New(
			"transcript_propose/next_steps: the fixture supplies no line, so there is no transcript to read")
	}
	if err := withinReadingBounds(lines); err != nil {
		return fmt.Errorf("transcript_propose/next_steps: %w", err)
	}
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			return fmt.Errorf(
				"transcript_propose/next_steps: line %d is blank, and a proposal citing it would quote nothing as its evidence",
				i+1)
		}
	}
	return nil
}

// refuseUnreachableSteps names an expectation the validator can never satisfy,
// which would measure nothing for as long as it stayed in the corpus. An EMPTY
// expectation is not one of them — it is the abstention scenario, and it is the
// one most transcripts deserve.
func refuseUnreachableSteps(want []transcriptProposeExpectation, lineCount int) error {
	if len(want) > maxTranscriptProposals {
		return fmt.Errorf(
			"transcript_propose/next_steps: the scenario expects %d next steps, but this site proposes at most %d",
			len(want), maxTranscriptProposals)
	}
	for i, step := range want {
		if step.Line < 1 || step.Line > lineCount {
			return fmt.Errorf(
				"transcript_propose/next_steps: expectation %d cites line %d, and this transcript has lines 1 to %d",
				i+1, step.Line, lineCount)
		}
	}
	return nil
}

// transcriptProposeCase is one transcript ready to be read, closed over the
// lines that were asked about and the next steps the scenario expects.
type transcriptProposeCase struct {
	lines    []string
	expected []transcriptProposeExpectation
}

// Run issues the one request this site sends, bare: production wraps the same
// request in the shape-retry when the brain supports one, and a case that did
// too would certify the answer a model gives after being told to try again.
func (c *transcriptProposeCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := transcriptRequest(c.lines)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("transcript_propose/next_steps: %w", err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate applies the engine's own checks in the engine's own order — parse,
// then validateTranscriptPayload against the transcript that was asked about —
// and only then asks whether the next steps are the ones the scenario expects. A
// reply that fails the validator has no proposals to disagree with.
//
// The confidence floor is deliberately not applied. It is the engine's decision
// about what to do with a reading it already believes, not a judgement on
// whether the reading is usable, and folding it in would report a hedged correct
// reading as a broken reply.
func (c *transcriptProposeCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	var payload transcriptPayload
	if err := json.Unmarshal([]byte(ai.Unfence(trace.Output)), &payload); err != nil {
		return aitasks.Outcome{
			Result: aitasks.OutcomeInvalid,
			Detail: fmt.Sprintf("unparseable model output: %v", err),
		}
	}
	if msg := validateTranscriptPayload(payload, len(c.lines)); msg != "" {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: msg}
	}
	if disagreements := c.disagreements(payload); len(disagreements) > 0 {
		return aitasks.Outcome{Result: aitasks.OutcomeWrongAnswer, Detail: strings.Join(disagreements, "; ")}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}

// disagreements names every next step the scenario expects and the reply
// missed, and every next step the reply proposes and the scenario does not — all
// of them, because a reading that found one of three is not the near miss one
// line would read as.
//
// A proposal answers an expectation when it cites the expected line, whatever
// else it cites alongside: a commitment stated across two lines is the same
// commitment. Only line numbers are named in the detail — the transcript's own
// words are a speaker's, and they have no business being echoed into a record.
func (c *transcriptProposeCase) disagreements(payload transcriptPayload) []string {
	wanted := make(map[int]bool, len(c.expected))
	for _, step := range c.expected {
		wanted[step.Line] = true
	}
	answered := map[int]bool{}
	var out []string
	for _, step := range payload.proposals() {
		matched := false
		for _, line := range step.SourceLines {
			if wanted[line] {
				answered[line] = true
				matched = true
			}
		}
		if !matched {
			out = append(out, fmt.Sprintf(
				"the reply proposes a next step from %s, which the transcript does not state", citedLines(step)))
		}
	}
	for line := range wanted {
		if !answered[line] {
			out = append(out, fmt.Sprintf(
				"the transcript states a next step on line %d and the reply does not propose it", line))
		}
	}
	// Map iteration is unordered, and a detail line that reshuffles between
	// runs reads as a different failure each time.
	sort.Strings(out)
	return out
}

// citedLines renders one proposal's citation as the reader of a record needs to
// see it: the lines to go and look at, in the order the reply gave them.
func citedLines(step proposedStep) string {
	rendered := make([]string, 0, len(step.SourceLines))
	for _, line := range step.SourceLines {
		rendered = append(rendered, strconv.Itoa(line))
	}
	return "line(s) " + strings.Join(rendered, ", ")
}
