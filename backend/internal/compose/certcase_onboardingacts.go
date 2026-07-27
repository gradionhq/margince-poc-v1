// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for cold_start/acts.
//
// It certifies the shipped path rather than a description of it: the request
// comes from onboardingActRequest, the same builder the assistant calls, and
// the reply is judged by validateOnboardingActReply, the same validator the
// assistant applies. A case that rebuilt either would measure a copy, and a
// copy stays green through the change that breaks the original.
//
// This is the census's first MULTI-TURN site, and the kind names the
// conversation rather than the number of calls: the model is stateless, so the
// whole prior conversation is replayed as messages of the ONE request this site
// sends. The case replays exactly the turns the transport would have replayed,
// through companyReadConversation — the same mapping, which is also the bound
// on how many turns a call may carry. A case that dropped them would certify a
// first turn on a scenario written about a third, which is where a follow-up
// reference lives, and where an attempt to talk the assistant out of its
// instructions over several turns lives too. What is graded is still the single
// reply that follows, which is what Site.CertifiedScope already reports.
//
// What the expectation MEANS here: the response KIND the reply must carry. The
// act answers in prose, and prose is what the rubric and the judge are for — but
// what it returns is an envelope, and the kind is the part of it the product
// itself reads and the transport hands the UI. It is a closed vocabulary, which
// is what lets an unreachable expectation be named at Prepare instead of
// measured as a zero. Pinning the sentence would fail every model that said the
// same thing differently; pinning nothing would leave a well-formed off-topic
// reminder indistinguishable from the answer the administrator asked for.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// onboardingActFixture is ONE turn of a non-company onboarding act, in exactly
// what the transport hands the assistant: which act is being spoken to, what the
// administrator just said, the conversation it follows, the server's context
// block and the locale to answer in.
//
// The context block is carried as the server serialized it rather than rebuilt
// from typed state, because that is the shape answerAct is given and because the
// act's hardening rule — that instructions inside supplied context are never
// obeyed — is only testable by a scenario that can write instructions into it.
// The turns are carried in the transport's own wire shape so the case bounds
// them the way the transport bounds them.
type onboardingActFixture struct {
	Act     string                                         `json:"act"`
	Message string                                         `json:"message"`
	History []crmcontracts.CompanySiteReadConversationTurn `json:"history"`
	Context json.RawMessage                                `json:"context"`
	Locale  string                                         `json:"locale"`
}

// onboardingActCases serves the site that answers the voice, results and
// connect acts of onboarding.
type onboardingActCases struct{}

func (onboardingActCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskColdStart,
		Variant: "acts",
		Kind:    ai.SiteKindMultiTurn,
	}
}

// Prepare turns one act turn and the kind the scenario expects into a runnable
// case, replaying the conversation through the transport's own mapping so the
// prompt this case sends is the prompt that turn would actually be answered
// with.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (onboardingActCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f onboardingActFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("cold_start/acts: the fixture is not the shape this site takes: %w", err)
	}
	if err := refuseUnsendableActTurn(f); err != nil {
		return nil, err
	}
	history, err := companyReadConversation(&f.History)
	if err != nil {
		return nil, fmt.Errorf("cold_start/acts: the fixture's history is not one the transport accepts: %w", err)
	}
	// A correct reply differs from an incorrect one in the kind token alone, so
	// the expectation IS that token rather than a wrapper carrying it.
	var want string
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf("cold_start/acts: the expected answer is not a response kind: %w", err)
	}
	// A kind outside the reply schema's closed vocabulary is unreachable: the
	// validator refuses every reply that could carry it, so the scenario would
	// measure nothing for as long as it stayed in the corpus. Naming it here costs
	// a parse; finding it later costs a paid run.
	if !companyConversationKindValid(want) {
		return nil, fmt.Errorf(
			"cold_start/acts: the scenario expects the response kind %q, which the reply schema does not offer", want,
		)
	}
	return &onboardingActCase{
		act: f.Act, message: strings.TrimSpace(f.Message), history: history,
		context: f.Context, locale: f.Locale, expected: want,
	}, nil
}

// refuseUnsendableActTurn names a turn the onboarding transport would never have
// let through, and so a prompt the product never sends. The act decides which
// role the system prompt takes and the locale decides which language it demands;
// the message is trimmed and bounded at decode time, and the context block is a
// marshalled struct, so anything else here would certify a call that cannot
// happen.
//
// The company act is refused by name rather than as an unknown one: it is a real
// act with its own site, its own prompt and its own validator, and routed here it
// would quietly take the connect act's role and be judged by a validator that
// refuses the very changes it exists to propose.
func refuseUnsendableActTurn(f onboardingActFixture) error {
	switch crmcontracts.OnboardingAct(f.Act) {
	case crmcontracts.OnboardingActVoice, crmcontracts.OnboardingActResults, crmcontracts.OnboardingActConnect:
	default:
		return fmt.Errorf(
			"cold_start/acts: the fixture speaks to the %q act, and this site answers voice, results or connect", f.Act,
		)
	}
	if !crmcontracts.OnboardingCompanyMessageRequestLocale(f.Locale).Valid() {
		return fmt.Errorf("cold_start/acts: the fixture asks for the locale %q, which onboarding never answers in", f.Locale)
	}
	if strings.TrimSpace(f.Message) == "" {
		return errors.New("cold_start/acts: the fixture carries no message, and the act answers one or nothing at all")
	}
	if n := len([]rune(strings.TrimSpace(f.Message))); n > companyReadMessageMaxRunes {
		return fmt.Errorf(
			"cold_start/acts: the fixture's message is %d characters, and the transport takes at most %d",
			n, companyReadMessageMaxRunes,
		)
	}
	var block map[string]json.RawMessage
	if err := json.Unmarshal(f.Context, &block); err != nil {
		return fmt.Errorf("cold_start/acts: the context block is not the JSON object the server assembles: %w", err)
	}
	if block == nil {
		return errors.New("cold_start/acts: the fixture supplies no context block, and every act answers from one")
	}
	return nil
}

// onboardingActCase is one act turn ready to be answered, closed over the act
// whose prompt asks the question and whose validator judges the reply.
type onboardingActCase struct {
	act      string
	message  string
	history  []model.Message
	context  json.RawMessage
	locale   string
	expected string
}

// Run issues the one request this site sends, the replayed conversation inside
// it. It sends it bare: production wraps the same request in the shape-retry
// when the brain supports one, and a case that retried would certify the answer
// a model gives after being told to try again rather than the answer it gives.
func (c *onboardingActCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := onboardingActRequest(c.act, c.message, c.history, c.context, c.locale)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("cold_start/acts: %w", err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate applies the assistant's own checks in the assistant's own order —
// parse, then validateOnboardingActReply for the act that was spoken to — and
// only then asks whether the reply answers in the register the scenario expects.
// The order is the meaning: a reply the act refuses has no kind to disagree
// with, and every way one of these replies can break the act's rule is unusable
// rather than wrong.
func (c *onboardingActCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	var reply companyReadModelReply
	if err := json.Unmarshal([]byte(ai.Unfence(trace.Output)), &reply); err != nil {
		return aitasks.Outcome{
			Result: aitasks.OutcomeInvalid,
			Detail: fmt.Sprintf("unparseable model output: %v", err),
		}
	}
	if err := validateOnboardingActReply(c.act, trace.Output); err != nil {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: err.Error()}
	}
	if reply.Kind != c.expected {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: fmt.Sprintf("the model answered as %q where the scenario expects %q", reply.Kind, c.expected),
		}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}
