// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for enrich/signature.
//
// It certifies the shipped path rather than a description of it: the request
// comes from signatureEnrichRequest, the same builder the pass calls, the window
// comes from signatureBlock, the same derivation the pass shows the model, and
// the reply is judged by gateEvidence and the §2.9 acceptance floor, the same
// two rules the pass applies before it writes anything. A case that rebuilt any
// of them would measure a copy, and a copy stays green through the change that
// breaks the original.
//
// What the expectation MEANS here: the contact fields that must reach the
// person's record, with the values they must carry. The pass proposes fields and
// fills only empty ones, so what it is right or wrong about is a field — it
// either grounds the one the scenario named, or it grounds something else, or it
// grounds nothing. It is a subset claim, never an inventory: a real signature
// carries more than a scenario cares to pin, and demanding exhaustiveness would
// fail a read for being richer than its author imagined.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// signatureEnrichFixture is ONE candidate in the fields the candidate read puts
// in this prompt: the person's own name and address, and the mail whose tail the
// window comes from. The whole body, not the window — deriving it is production's
// step, and a fixture that pre-derived it could hand the model a window the pass
// would never have built.
type signatureEnrichFixture struct {
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	Body     string `json:"body"`
}

// signatureEnrichCases serves the one site that reads contact fields off a mail
// signature.
type signatureEnrichCases struct{}

func (signatureEnrichCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskEnrich,
		Variant: "signature",
		Kind:    ai.SiteKindOneShot,
	}
}

// Prepare turns one candidate's mail and the fields the scenario expects from it
// into a runnable case, deriving the window ONCE so the gate this case runs reads
// exactly what the model this case calls was shown.
//
// The activity id is MINTED here rather than read from either side. Production
// takes it from the linked mail's ledger row, which no sender has written, so a
// fixture carrying one would put a product-side identifier in the hands of
// whoever authored the mail — and the corpus has no use for an id it cannot look
// anything up by.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (signatureEnrichCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f signatureEnrichFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("enrich/signature: the fixture is not the shape this site takes: %w", err)
	}
	lines := signatureBlock(f.Body)
	if lines == "" {
		return nil, errors.New(
			"enrich/signature: the fixture's mail leaves no signature block, and the pass calls no model without one")
	}
	var want map[string]string
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf("enrich/signature: the expected answer is not a field to value map: %w", err)
	}
	if len(want) == 0 {
		return nil, errors.New("enrich/signature: the scenario expects no field, so no reply could disagree with it")
	}
	if err := refuseUnenrichableExpectation(want); err != nil {
		return nil, err
	}
	return &signatureEnrichCase{
		// PersonID stays unset: it reaches the apply, never the prompt and never
		// the gate, and a case carries only what it certifies.
		cand: people.SignatureCandidate{
			FullName:   f.FullName,
			Email:      f.Email,
			ActivityID: ids.NewV7(),
			Body:       f.Body,
		},
		lines:    lines,
		expected: want,
	}, nil
}

// refuseUnenrichableExpectation names an expectation the pass can never satisfy:
// a field outside the §2.9 vocabulary is one no model was told exists and the
// gate drops as unknown on every reply, and an empty value is dropped as empty on
// every reply. Either would measure nothing for as long as it stayed in the
// corpus. Naming it here costs a parse; finding it later costs a paid run.
//
// Sorted so a fixture with two offences names the same one every time.
func refuseUnenrichableExpectation(want map[string]string) error {
	for _, name := range slices.Sorted(maps.Keys(want)) {
		switch {
		case !enrichFieldNames[name]:
			return fmt.Errorf(
				"enrich/signature: the scenario expects %q, which this prompt never offers the model", name)
		case strings.TrimSpace(want[name]) == "":
			return fmt.Errorf(
				"enrich/signature: the scenario expects an empty value for %q, which the gate drops", name)
		}
	}
	return nil
}

// signatureEnrichCase is one candidate ready to be read, closed over the derived
// window, the minted activity id, and the fields the scenario expects.
type signatureEnrichCase struct {
	cand     people.SignatureCandidate
	lines    string
	expected map[string]string
}

// Run issues the one request this site sends. It sends it bare: production wraps
// the same request in the shape-retry when the brain supports one, and a case
// that retried would certify the answer a model gives after being told to try
// again rather than the answer it gives.
func (c *signatureEnrichCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := signatureEnrichRequest(c.cand, c.lines)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("enrich/signature: %w", err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate applies the pass's own rules in the pass's own order — the no-guess
// gate against the window the model was shown, then the acceptance floor — and
// only then asks whether what would be written is what the scenario expects. The
// order is the meaning: a field the gate refused is not a field to disagree with.
//
// A reply is unusable only when the gate refused everything it claimed: an
// unreadable answer, or one whose every quote is invented. Claiming NOTHING is a
// different thing and is judged as a wrong answer, because omission is what this
// prompt asks for when there is nothing to quote — the pass applies nothing,
// logs nothing, and picks the person up again next cycle.
//
// The floor is applied, unlike the classify site's confidence, because it is the
// §2.9 acceptance rule and not a routing decision: a hedged field is never
// written, so a case that counted one would certify a fill the product does not
// perform.
func (c *signatureEnrichCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	gated, dropped := gateEvidence(trace.Output, c.lines, "activity:"+c.cand.ActivityID.String(),
		func(name string) bool { return enrichFieldNames[name] })
	// Every refusal reaches the Detail whatever the result: a reply that grounded
	// the expected field while fabricating evidence for three others is not the
	// clean run it would otherwise look like.
	detail := gateRefusals(dropped)
	if len(gated) == 0 && len(dropped) > 0 {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: strings.Join(detail, "; ")}
	}
	proposed := make(map[string]string, len(gated))
	for _, f := range gated {
		if float64(f.Confidence) < enrichConfidenceFloor {
			detail = append(detail, fmt.Sprintf("%s is hedged at %.2f, below the %.2f the pass applies at",
				f.Field, f.Confidence, enrichConfidenceFloor))
			continue
		}
		proposed[f.Field] = f.Value
	}
	if disagreements := c.disagreements(proposed); len(disagreements) > 0 {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: strings.Join(append(disagreements, detail...), "; "),
		}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted, Detail: strings.Join(detail, "; ")}
}

// disagreements names every expected field the proposal does not carry. All of
// them, not the first: a run that read the title right and the phone wrong is not
// the near miss one line would read as.
//
// Values compare under normalizeEvidence — the same presentation-only relaxation
// the gate applies to evidence — so a scenario neither fails on a straightened
// apostrophe nor passes on a reworded value. A phone the model reformatted still
// disagrees: its digits and separators survive the normalization.
func (c *signatureEnrichCase) disagreements(proposed map[string]string) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(c.expected)) {
		value, survived := proposed[name]
		switch {
		case !survived:
			out = append(out, fmt.Sprintf("no surviving %s, which the scenario expects", name))
		case normalizeEvidence(value) != normalizeEvidence(c.expected[name]):
			out = append(out, fmt.Sprintf("%s reads %q where the scenario expects %q", name, value, c.expected[name]))
		}
	}
	return out
}
