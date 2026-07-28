// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the two company conversations owe their certification cases in common.
//
// The dossier conversation and the onboarding company conversation are separate
// sites — different prompts, different authorization, different bounds on what a
// fixture may say — but they answer in ONE reply vocabulary, judged by ONE
// validator behind companyReadGate. So what a scenario may assert about a reply,
// what makes an assertion unreachable, and how a run is scored are the same
// question twice, and the answer lives here rather than in whichever site
// happened to need it first. A copy of a shared answer drifts, and the copy that
// drifts is the one that stops failing.
//
// Each site supplies its own name for the refusals, because a corpus author
// reading "the fixture is not one this site takes" needs to know which site.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// companyConversationExpectation is what the corpus asserts about one reply: the
// register it answers in, and the company changes it proposes, by field.
//
// Both, because a correct reply and an incorrect one can share a kind and differ
// only in what they propose — and can share the proposal and differ only in the
// value. Prose is what the rubric and the judge are for; the kind and the changes
// are the parts of the envelope the product itself reads and stages for a human.
type companyConversationExpectation struct {
	Kind    string            `json:"kind"`
	Changes map[string]string `json:"changes"`
}

// decodeCompanyConversationScenario reads one half of a scenario — the fixture or
// the expectation — strictly. A corpus author's mistyped key is otherwise a field
// silently left at its zero value, a fixture missing its dossier or an
// expectation asserting nothing, and both read as a passing run rather than as
// the authoring mistake they are.
func decodeCompanyConversationScenario[T companyReadMessageFixture |
	onboardingCompanyMessageFixture | companyConversationExpectation](
	raw json.RawMessage, into *T,
) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(into)
}

// refuseUnsendableCompanyMessage holds a fixture's message to what the transport
// would have carried. Both conversations trim it and bound it at decode time, so
// a message outside those bounds certifies a call that cannot happen.
func refuseUnsendableCompanyMessage(site, message string) error {
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf(
			"%s: the fixture carries no message, and this conversation answers one or nothing at all", site)
	}
	if n := len([]rune(strings.TrimSpace(message))); n > companyReadMessageMaxRunes {
		return fmt.Errorf(
			"%s: the fixture's message is %d characters, and the transport takes at most %d",
			site, n, companyReadMessageMaxRunes,
		)
	}
	return nil
}

// refuseUnassemblableDossier holds a fixture's dossier to what
// companyReadEvidenceSet can produce. The numbering is what the model cites and
// what the gate looks a citation up by; a source with no URL is one the server
// drops before the model sees it; and the rune bound is what the model is shown
// of a long value. A fixture outside any of the three shows the model a dossier
// the product cannot build, and asks the gate a question it will never be asked.
func refuseUnassemblableDossier(site string, evidence []companyReadEvidence) error {
	if len(evidence) > companyReadSourceLimit {
		return fmt.Errorf(
			"%s: the fixture supplies %d dossier sources, and the server assembles at most %d",
			site, len(evidence), companyReadSourceLimit,
		)
	}
	for i, source := range evidence {
		if want := fmt.Sprintf("S%d", i+1); source.ID != want {
			return fmt.Errorf(
				"%s: dossier source %d is numbered %q, and the server numbers them %s onwards in order",
				site, i+1, source.ID, want,
			)
		}
		if strings.TrimSpace(source.URL) == "" {
			return fmt.Errorf(
				"%s: dossier source %q carries no source url, and the server drops a source it cannot cite",
				site, source.ID,
			)
		}
		if n := max(len([]rune(source.Value)), len([]rune(source.Quote))); n > companyReadSourceMaxRunes {
			return fmt.Errorf(
				"%s: dossier source %q carries %d characters, and the server bounds every value and quote at %d",
				site, source.ID, n, companyReadSourceMaxRunes,
			)
		}
	}
	return nil
}

// readCompanyConversationExpectation parses what the scenario asserts and refuses
// what this reply vocabulary could never produce. An unreachable expectation
// measures nothing for as long as it stays in the corpus: naming it here costs a
// parse, finding it later costs a paid run.
//
// The authorization check is the one only a prepared case can make. The gate is
// built from the fixture's own conversation, so Prepare already knows whether the
// change a scenario expects is one the administrator asked for — and if it is
// not, every reply proposing it is refused, whatever the model answers.
func readCompanyConversationExpectation(
	site string, expected json.RawMessage, gate companyReadGate,
) (companyConversationExpectation, error) {
	var want companyConversationExpectation
	if err := decodeCompanyConversationScenario(expected, &want); err != nil {
		return want, fmt.Errorf("%s: the expectation is not the shape this site's scenarios take: %w", site, err)
	}
	if !companyConversationKindValid(want.Kind) {
		return want, fmt.Errorf(
			"%s: the scenario expects the response kind %q, which the reply schema does not offer", site, want.Kind)
	}
	if len(want.Changes) == 0 {
		return want, nil
	}
	if want.Kind != companyConversationRecommendation && want.Kind != companyConversationCorrection {
		return want, fmt.Errorf(
			"%s: the scenario expects changes under the kind %q, which may not propose changes", site, want.Kind)
	}
	if len(want.Changes) > companyReadChangeLimit {
		return want, fmt.Errorf(
			"%s: the scenario expects %d changes, and a reply carries at most %d",
			site, len(want.Changes), companyReadChangeLimit,
		)
	}
	// Sorted so a scenario with two unreachable changes names the same one every
	// time it is prepared.
	for _, field := range slices.Sorted(maps.Keys(want.Changes)) {
		if err := refuseUnreachableCompanyChange(site, field, want.Changes[field], gate); err != nil {
			return want, err
		}
	}
	return want, nil
}

func refuseUnreachableCompanyChange(site, field, value string, gate companyReadGate) error {
	if !crmcontracts.CompanySiteReadSuggestedChangeField(field).Valid() {
		return fmt.Errorf(
			"%s: the scenario expects a change to %q, an unsupported field for this conversation", site, field)
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s: the scenario expects a change to %q with no value to compare", site, field)
	}
	if !gate.authorization.allows(companyReadProposedChange{Field: field, Value: value}) {
		return fmt.Errorf(
			"%s: the fixture's conversation authorizes no change to %q, "+
				"so the gate refuses every reply that proposes one", site, field,
		)
	}
	return nil
}

// evaluateCompanyConversationReply applies the answer path's own checks in the
// answer path's own order — parse, then the gate this turn was sent under — and
// only then asks whether the reply says what the scenario expects. The order is
// the meaning: a reply the gate refuses has no register to disagree with, and a
// change it refused is not a change the scenario can be said to have got wrong.
func evaluateCompanyConversationReply(
	gate companyReadGate, expected companyConversationExpectation, trace aitasks.Trace,
) aitasks.Outcome {
	var reply companyReadModelReply
	if err := json.Unmarshal([]byte(ai.Unfence(trace.Output)), &reply); err != nil {
		return aitasks.Outcome{
			Result: aitasks.OutcomeInvalid,
			Detail: fmt.Sprintf("unparseable model output: %v", err),
		}
	}
	if err := gate.validate(trace.Output); err != nil {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: err.Error()}
	}
	if reply.Kind != expected.Kind {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: fmt.Sprintf("the model answered as %q where the scenario expects %q", reply.Kind, expected.Kind),
		}
	}
	disagreements := expectationDisagreements(expected.Changes, proposedChangeValues(reply.ProposedChanges))
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
