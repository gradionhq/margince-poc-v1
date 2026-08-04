// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for signal_extract/thread_events.
//
// It certifies the shipped path: the request comes from extractRequest and the
// reply is judged by validateExtractPayload, the same builder and the same
// validator the engine uses. A case that rebuilt either would measure a copy,
// and a copy stays green through the change that breaks the original.
//
// What the expectation MEANS here: the set of events the conversation states,
// each named by the KIND and the POSITION of the message it is stated in.
// Position is the identity the corpus can name, because the ids are minted
// here and never supplied — production takes them from real activity rows no
// model has seen, so an answer can only carry one by having read this call's
// prompt, which is exactly what the citation check exists to prove.
//
// The empty expectation is the important one. Most conversations state no
// material event, and a site that invents one on a quiet thread is worse than
// a silent site — it puts a card in front of a rep for nothing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// signalExtractFixture is ONE conversation, oldest first, as the thread read
// hands it to the engine.
type signalExtractFixture []signalExtractMessage

// signalExtractMessage is one message in exactly the fields the prompt
// consumes. It carries no id — see Prepare.
type signalExtractMessage struct {
	Direction string `json:"direction"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
}

// signalExtractExpectation is one event the scenario says the conversation
// states: its kind, and the 1-based position of the message stating it.
type signalExtractExpectation struct {
	Kind    string `json:"kind"`
	Message int    `json:"message"`
}

// signalExtractCases serves the one site that reads material events out of a
// settled conversation.
type signalExtractCases struct{}

func (signalExtractCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskSignalExtract,
		Variant: "thread_events",
		Kind:    ai.SiteKindOneShot,
	}
}

// CertifiedScope is the site's own kind: reading a conversation IS one call.
// There is no solo re-ask here — an event under the floor is dropped, not
// asked about again — so the call this case makes is the whole path.
func (signalExtractCases) CertifiedScope() string { return aitasks.ScopeFullInvocation }

// Prepare turns one conversation and the events the scenario expects into a
// runnable case, MINTING an activity id per message rather than reading one
// from either.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (signalExtractCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var messages signalExtractFixture
	if err := json.Unmarshal(fixture, &messages); err != nil {
		return nil, fmt.Errorf("signal_extract/thread_events: the fixture is not the shape this site takes: %w", err)
	}
	if err := refuseUnreadableThread(messages); err != nil {
		return nil, err
	}
	var want []signalExtractExpectation
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf(
			"signal_extract/thread_events: the expected answer is not a list of events: %w", err)
	}
	if err := refuseUnreachableEvents(want, len(messages)); err != nil {
		return nil, err
	}
	thread := settledThread{Key: "cert-thread", OrganizationID: ids.NewV7()}
	for _, message := range messages {
		thread.Messages = append(thread.Messages, threadMessage{
			ID:        ids.NewV7(),
			Direction: message.Direction,
			Subject:   message.Subject,
			Body:      message.Body,
		})
	}
	return &signalExtractCase{thread: thread, expected: want}, nil
}

// refuseUnreadableThread names a conversation the thread read could never have
// returned: it takes at most extractThreadMessages and truncates every body to
// extractBodyLimit, so a fixture beyond either bound would certify a prompt the
// product cannot send.
func refuseUnreadableThread(messages signalExtractFixture) error {
	if len(messages) == 0 {
		return errors.New(
			"signal_extract/thread_events: the fixture supplies no message, so there is no conversation to read")
	}
	if len(messages) > extractThreadMessages {
		return fmt.Errorf(
			"signal_extract/thread_events: the fixture carries %d messages, but one call reads at most %d",
			len(messages), extractThreadMessages)
	}
	for i, message := range messages {
		if n := utf8.RuneCountInString(message.Body); n > extractBodyLimit {
			return fmt.Errorf(
				"signal_extract/thread_events: message %d carries a body of %d characters, but the read truncates every body to %d",
				i+1, n, extractBodyLimit)
		}
		if message.Direction != "inbound" && message.Direction != "outbound" && message.Direction != "" {
			return fmt.Errorf(
				"signal_extract/thread_events: message %d is directed %q, and a captured email is inbound or outbound",
				i+1, message.Direction)
		}
	}
	return nil
}

// refuseUnreachableEvents names an expectation the validator can never satisfy,
// which would measure nothing for as long as it stayed in the corpus. An EMPTY
// expectation is not one of them — it is the abstention scenario, and it is the
// one most conversations deserve.
func refuseUnreachableEvents(want []signalExtractExpectation, messages int) error {
	if len(want) > extractMaxEvents {
		return fmt.Errorf(
			"signal_extract/thread_events: the scenario expects %d events, but this site reports at most %d",
			len(want), extractMaxEvents)
	}
	for i, event := range want {
		if _, ok := extractKinds[event.Kind]; !ok {
			return fmt.Errorf(
				"signal_extract/thread_events: expectation %d names kind %q, which this site never records",
				i+1, event.Kind)
		}
		if event.Message < 1 || event.Message > messages {
			return fmt.Errorf(
				"signal_extract/thread_events: expectation %d cites message %d, and the conversation has %d",
				i+1, event.Message, messages)
		}
	}
	return nil
}

// signalExtractCase is one conversation ready to be read, closed over the
// minted ids and the events the scenario expects.
type signalExtractCase struct {
	thread   settledThread
	expected []signalExtractExpectation
}

// Run issues the one request this site sends, bare: production wraps the same
// request in the shape-retry when the brain supports one, and a case that did
// too would certify the answer a model gives after being told to try again.
func (c *signalExtractCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := extractRequest(c.thread)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("signal_extract/thread_events: %w", err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate applies the engine's own checks in the engine's own order — parse,
// then validateExtractPayload against the conversation that was asked about —
// and only then asks whether the events are the ones the scenario expects. A
// reply that fails the validator has no events to disagree with.
//
// The confidence floor is deliberately not applied. It is the engine's
// decision about what to do with an answer it already believes, not a
// judgement on whether the answer is usable, and folding it in would report a
// hedged correct reading as a broken reply.
func (c *signalExtractCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	var payload extractPayload
	if err := json.Unmarshal([]byte(ai.Unfence(trace.Output)), &payload); err != nil {
		return aitasks.Outcome{
			Result: aitasks.OutcomeInvalid,
			Detail: fmt.Sprintf("unparseable model output: %v", err),
		}
	}
	if msg := validateExtractPayload(payload, c.thread); msg != "" {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: msg}
	}
	if disagreements := c.disagreements(payload); len(disagreements) > 0 {
		return aitasks.Outcome{Result: aitasks.OutcomeWrongAnswer, Detail: strings.Join(disagreements, "; ")}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}

// disagreements names every event the scenario expects and the reply missed,
// and every event the reply reports and the scenario does not — all of them,
// because a reading that found one of three is not the near miss one line
// would read as.
//
// Events are named by kind and by the message's position in the fixture, which
// is where the expectation was written. The minted id names nothing a scenario
// author could look up, and the mail's own text has no business being echoed
// into a record.
func (c *signalExtractCase) disagreements(payload extractPayload) []string {
	position := make(map[string]int, len(c.thread.Messages))
	for i, message := range c.thread.Messages {
		position[message.ID.String()] = i + 1
	}
	reported := map[string]bool{}
	for _, event := range payload.events() {
		reported[fmt.Sprintf("%s@%d", event.Kind, position[event.MessageID])] = true
	}
	wanted := map[string]bool{}
	for _, event := range c.expected {
		wanted[fmt.Sprintf("%s@%d", event.Kind, event.Message)] = true
	}
	var out []string
	for key := range wanted {
		if !reported[key] {
			out = append(out, fmt.Sprintf("the conversation states %s and the reply does not report it", key))
		}
	}
	for key := range reported {
		if !wanted[key] {
			out = append(out, fmt.Sprintf("the reply reports %s, which the conversation does not state", key))
		}
	}
	// Map iteration is unordered, and a detail line that reshuffles between
	// runs reads as a different failure each time.
	sort.Strings(out)
	return out
}
