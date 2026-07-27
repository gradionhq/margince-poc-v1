// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for cold_start/sitereadmessage — the conversation an
// administrator has with Margince about the dossier its crawl just read.
//
// It certifies the shipped path rather than a description of it: the request
// comes from companyReadAnswerRequest and the reply is judged by the gate
// newCompanyReadGate builds, both of which the transport uses for the same turn.
// A case that rebuilt either would measure a copy, and a copy stays green
// through the change that breaks the original.
//
// This site is MULTI-TURN, and the kind names the conversation rather than the
// number of calls: the model is stateless, so the whole prior conversation is
// replayed as messages of the ONE request this site sends. The case replays
// exactly the turns the transport would have replayed, through
// companyReadConversation — the same mapping, which is also the bound on how
// many turns a call may carry.
//
// What makes this site worth certifying separately from the other onboarding
// conversations is its gate. The other acts may not propose company changes at
// all; this one may, and only the ones the administrator actually asked for.
// That authorization is derived from what the human said — this message and the
// conversation behind it — so the SAME reply is a correct correction in one
// conversation and a confused-deputy action in the next: a change nobody asked
// for arrives at the confirm-first queue indistinguishable from one they did,
// which is where an approval stops being a decision. The dossier is crawled web
// text and can argue for a change in as many words; the gate, not the prompt, is
// what refuses it. So the case's gate is built from the fixture it sent, and
// never from anything else.
//
// What the expectation MEANS here: the register the reply answers in, and the
// company changes it proposes. Both, because unlike the acts site a correct
// reply and an incorrect one can share a kind and differ only in what they
// propose — and can share the proposal and differ only in the value. Prose is
// what the rubric and the judge are for; the kind and the changes are the parts
// of the envelope the product itself reads and stages for a human.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// companyReadMessageFixture is ONE turn of the dossier conversation, in exactly
// what the transport hands the answer path: what the administrator just said,
// the conversation it follows, and the dossier the crawl grounded.
//
// The dossier arrives assembled rather than as the site read it came from,
// because the certified thing is the prompt built from it, not the database read
// that produced it. What the server's assembly guarantees about it is enforced
// at Prepare instead: numbering, grounding and bounds are what the model is
// shown and what the gate looks up, so a fixture outside them describes a call
// the product cannot make.
//
// The turns are carried in the transport's own wire shape so the case bounds
// them the way the transport bounds them.
type companyReadMessageFixture struct {
	Message  string                                         `json:"message"`
	History  []crmcontracts.CompanySiteReadConversationTurn `json:"history"`
	Evidence []companyReadEvidence                          `json:"evidence"`
}

// companyReadMessageExpectation is what the corpus asserts about one reply: the
// register it answers in, and the company changes it proposes, by field.
type companyReadMessageExpectation struct {
	Kind    string            `json:"kind"`
	Changes map[string]string `json:"changes"`
}

// companyReadMessageCases serves the site that answers an administrator about
// the dossier a site read produced.
type companyReadMessageCases struct{}

func (companyReadMessageCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskColdStart,
		Variant: "sitereadmessage",
		Kind:    ai.SiteKindMultiTurn,
	}
}

// Prepare turns one dossier turn and what the scenario expects of it into a
// runnable case, deriving the gate from the same message, history and dossier
// the request is built from — which is the whole reason Prepare exists.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (companyReadMessageCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f companyReadMessageFixture
	if err := decodeCompanyReadScenario(fixture, &f); err != nil {
		return nil, fmt.Errorf("cold_start/sitereadmessage: the fixture is not the shape this site takes: %w", err)
	}
	if err := refuseUnproducibleCompanyReadFixture(f); err != nil {
		return nil, err
	}
	history, err := companyReadConversation(&f.History)
	if err != nil {
		return nil, fmt.Errorf("cold_start/sitereadmessage: the fixture's history is not one the transport accepts: %w", err)
	}
	message := strings.TrimSpace(f.Message)
	gate := newCompanyReadGate(message, history, f.Evidence)
	want, err := readCompanyReadExpectation(expected, gate)
	if err != nil {
		return nil, err
	}
	return &companyReadMessageCase{
		message: message, history: history, evidence: f.Evidence, gate: gate, expected: want,
	}, nil
}

// decodeCompanyReadScenario reads one half of a scenario — the fixture or the
// expectation — strictly. A corpus author's mistyped key is otherwise a field
// silently left at its zero value, a fixture missing its dossier or an
// expectation asserting nothing, and both read as a passing run rather than as
// the authoring mistake they are.
func decodeCompanyReadScenario[T companyReadMessageFixture | companyReadMessageExpectation](
	raw json.RawMessage, into *T,
) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(into)
}

// refuseUnproducibleCompanyReadFixture names a turn the dossier transport would
// never have let through, or a dossier the server could never have assembled.
// The message is trimmed and bounded at decode time and the dossier is built by
// companyReadEvidenceSet, so anything outside those bounds certifies a call that
// cannot happen.
func refuseUnproducibleCompanyReadFixture(f companyReadMessageFixture) error {
	if strings.TrimSpace(f.Message) == "" {
		return errors.New(
			"cold_start/sitereadmessage: the fixture carries no message, and this conversation answers one or nothing at all")
	}
	if n := len([]rune(strings.TrimSpace(f.Message))); n > companyReadMessageMaxRunes {
		return fmt.Errorf(
			"cold_start/sitereadmessage: the fixture's message is %d characters, and the transport takes at most %d",
			n, companyReadMessageMaxRunes,
		)
	}
	return refuseUnassemblableDossier(f.Evidence)
}

// refuseUnassemblableDossier holds the fixture's dossier to what
// companyReadEvidenceSet can produce. The numbering is what the model cites and
// what the gate looks a citation up by; a source with no URL is one the server
// drops before the model sees it; and the rune bound is what the model is shown
// of a long value. A fixture outside any of the three shows the model a dossier
// the product cannot build, and asks the gate a question it will never be asked.
func refuseUnassemblableDossier(evidence []companyReadEvidence) error {
	if len(evidence) > companyReadSourceLimit {
		return fmt.Errorf(
			"cold_start/sitereadmessage: the fixture supplies %d dossier sources, and the server assembles at most %d",
			len(evidence), companyReadSourceLimit,
		)
	}
	for i, source := range evidence {
		if want := fmt.Sprintf("S%d", i+1); source.ID != want {
			return fmt.Errorf(
				"cold_start/sitereadmessage: dossier source %d is numbered %q, and the server numbers them %s onwards in order",
				i+1, source.ID, want,
			)
		}
		if strings.TrimSpace(source.URL) == "" {
			return fmt.Errorf(
				"cold_start/sitereadmessage: dossier source %q carries no source url, and the server drops a source it cannot cite",
				source.ID,
			)
		}
		if n := max(len([]rune(source.Value)), len([]rune(source.Quote))); n > companyReadSourceMaxRunes {
			return fmt.Errorf(
				"cold_start/sitereadmessage: dossier source %q carries %d characters, and the server bounds every value and quote at %d",
				source.ID, n, companyReadSourceMaxRunes,
			)
		}
	}
	return nil
}

// readCompanyReadExpectation parses what the scenario asserts and refuses what
// this site's gate could never produce. An unreachable expectation measures
// nothing for as long as it stays in the corpus: naming it here costs a parse,
// finding it later costs a paid run.
//
// The authorization check is the one only this site can make. The gate is built
// from the fixture's own conversation, so Prepare already knows whether the
// change a scenario expects is one the administrator asked for — and if it is
// not, every reply proposing it is refused, whatever the model answers.
func readCompanyReadExpectation(expected json.RawMessage, gate companyReadGate) (companyReadMessageExpectation, error) {
	var want companyReadMessageExpectation
	if err := decodeCompanyReadScenario(expected, &want); err != nil {
		return want, fmt.Errorf(
			"cold_start/sitereadmessage: the expectation is not the shape this site's scenarios take: %w", err)
	}
	if !companyConversationKindValid(want.Kind) {
		return want, fmt.Errorf(
			"cold_start/sitereadmessage: the scenario expects the response kind %q, which the reply schema does not offer",
			want.Kind,
		)
	}
	if len(want.Changes) == 0 {
		return want, nil
	}
	if want.Kind != companyConversationRecommendation && want.Kind != companyConversationCorrection {
		return want, fmt.Errorf(
			"cold_start/sitereadmessage: the scenario expects changes under the kind %q, which may not propose changes",
			want.Kind,
		)
	}
	if len(want.Changes) > companyReadChangeLimit {
		return want, fmt.Errorf(
			"cold_start/sitereadmessage: the scenario expects %d changes, and a reply carries at most %d",
			len(want.Changes), companyReadChangeLimit,
		)
	}
	// Sorted so a scenario with two unreachable changes names the same one every
	// time it is prepared.
	for _, field := range slices.Sorted(maps.Keys(want.Changes)) {
		if err := refuseUnreachableCompanyReadChange(field, want.Changes[field], gate); err != nil {
			return want, err
		}
	}
	return want, nil
}

func refuseUnreachableCompanyReadChange(field, value string, gate companyReadGate) error {
	if !crmcontracts.CompanySiteReadSuggestedChangeField(field).Valid() {
		return fmt.Errorf(
			"cold_start/sitereadmessage: the scenario expects a change to %q, an unsupported field for this conversation", field)
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf(
			"cold_start/sitereadmessage: the scenario expects a change to %q with no value to compare", field)
	}
	if !gate.authorization.allows(companyReadProposedChange{Field: field, Value: value}) {
		return fmt.Errorf(
			"cold_start/sitereadmessage: the fixture's conversation authorizes no change to %q, "+
				"so the gate refuses every reply that proposes one", field,
		)
	}
	return nil
}

// companyReadMessageCase is one dossier turn ready to be answered, closed over
// the gate built from that same turn.
type companyReadMessageCase struct {
	message  string
	history  []model.Message
	evidence []companyReadEvidence
	gate     companyReadGate
	expected companyReadMessageExpectation
}

// Run issues the one request this site sends, the replayed conversation inside
// it. It sends it bare: production wraps the same request in the shape-retry
// when the brain supports one, and a case that retried would certify the answer
// a model gives after being told to try again rather than the answer it gives.
func (c *companyReadMessageCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req, err := companyReadAnswerRequest(c.message, c.history, c.evidence)
	if err != nil {
		return aitasks.Trace{}, fmt.Errorf("cold_start/sitereadmessage: %w", err)
	}
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("cold_start/sitereadmessage: %w", err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate applies the answer path's own checks in the answer path's own order —
// parse, then the gate this turn was sent under — and only then asks whether the
// reply says what the scenario expects. The order is the meaning: a reply the
// gate refuses has no register to disagree with, and a change it refused is not
// a change the scenario can be said to have got wrong.
func (c *companyReadMessageCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	var reply companyReadModelReply
	if err := json.Unmarshal([]byte(ai.Unfence(trace.Output)), &reply); err != nil {
		return aitasks.Outcome{
			Result: aitasks.OutcomeInvalid,
			Detail: fmt.Sprintf("unparseable model output: %v", err),
		}
	}
	if err := c.gate.validate(trace.Output); err != nil {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: err.Error()}
	}
	if reply.Kind != c.expected.Kind {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: fmt.Sprintf("the model answered as %q where the scenario expects %q", reply.Kind, c.expected.Kind),
		}
	}
	disagreements := expectationDisagreements(c.expected.Changes, proposedChangeValues(reply.ProposedChanges))
	if len(disagreements) > 0 {
		return aitasks.Outcome{Result: aitasks.OutcomeWrongAnswer, Detail: strings.Join(disagreements, "; ")}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}

// proposedChangeValues keys the changes that survived the gate by field — the
// shape the comparison asks about, since a scenario names a field and never a
// position. The first proposal for a field wins: a scenario names a field once,
// and a second proposal for the same field cannot make the first one right.
func proposedChangeValues(changes []companyReadProposedChange) map[string]string {
	out := make(map[string]string, len(changes))
	for _, change := range changes {
		if _, seen := out[change.Field]; !seen {
			out[change.Field] = change.Value
		}
	}
	return out
}
