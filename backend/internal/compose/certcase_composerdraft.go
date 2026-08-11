// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The two composer drafting sites: the person page's "Write email" and the
// company page's first-touch outbound.
//
// Both existed and neither was certified. ADR-0074 requires every shipped site
// to be declared, registered and certified through its PRODUCTION path, and the
// change that gave all three drafting surfaces one shared rules block changed
// both of these prompts — so leaving them undeclared would mean two prompts
// changed inside an uncertified blind interval.
//
// They are one file because they are the same site twice: same fixture shape,
// same evaluation, differing only in which package's Write drives them and what
// a fixture is a projection OF. What they do not share with the reply site is
// the register question — neither composer has a voice variant yet, so there is
// one system prompt per site and nothing to disagree about. When Voice DNA
// reaches them, they gain the variant the way the reply site has it.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/compose/accountdraft"
	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/compose/persondraft"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The site names, as ai-tasks.yaml declares them.
const (
	personDraftSite  = "draft_reply/person"
	accountDraftSite = "draft_reply/account"
)

// composerAnswer is what a composer fixture expects. One token, because what
// separates a correct draft from an incorrect one on these sites is whether it
// was written at all — the rubric measures the prose.
const composerAnswerWritten = "written"

// personDraftCases serves the person page's composer.
type personDraftCases struct{}

func (personDraftCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskDraftReply,
		Variant: "person",
		Kind:    ai.SiteKindOneShot,
	}
}

// Prepare reads the fixture as the drafter's own input, which is the point: a
// fixture that does not decode into persondraft.Input describes a request this
// site cannot send, and it is refused here rather than scored later.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (personDraftCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var in persondraft.Input
	if err := json.Unmarshal(fixture, &in); err != nil {
		return nil, fmt.Errorf("%s: the fixture is not the shape this site takes: %w", personDraftSite, err)
	}
	if err := refuseUnanswerableComposerCase(personDraftSite, expected); err != nil {
		return nil, err
	}
	return &personDraftCase{in: in}, nil
}

// accountDraftCases serves the company page's first-touch composer.
type accountDraftCases struct{}

func (accountDraftCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskDraftReply,
		Variant: "account",
		Kind:    ai.SiteKindOneShot,
	}
}

//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (accountDraftCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var in accountdraft.Input
	if err := json.Unmarshal(fixture, &in); err != nil {
		return nil, fmt.Errorf("%s: the fixture is not the shape this site takes: %w", accountDraftSite, err)
	}
	if err := refuseUnanswerableComposerCase(accountDraftSite, expected); err != nil {
		return nil, err
	}
	return &accountDraftCase{in: in}, nil
}

// refuseUnanswerableComposerCase rejects an expectation these sites cannot
// answer, at preparation rather than as a failed score.
func refuseUnanswerableComposerCase(site string, expected json.RawMessage) error {
	var want string
	if err := json.Unmarshal(expected, &want); err != nil {
		return fmt.Errorf("%s: the expected answer is not an answer token: %w", site, err)
	}
	if want != composerAnswerWritten {
		return fmt.Errorf("%s: the only answer this site gives is %q, and the case expects %q",
			site, composerAnswerWritten, want)
	}
	return nil
}

// personDraftCase is one person-composer request ready to be answered.
type personDraftCase struct{ in persondraft.Input }

// Run drives the package's own Write, so the case exercises the prompt the
// product sends rather than a copy of it assembled here.
func (c *personDraftCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	recorder := &composerRecorder{completer: completer}
	_, _, err := persondraft.Write(ctx, recorder, c.in)
	return composerTrace(personDraftSite, recorder, err)
}

// Evaluate applies the package's own ParseDraft — the same reading the product
// gives the reply before it serves it — so the record's verdict is measured
// rather than inferred from the absence of an error in Run.
func (c *personDraftCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	return evaluateComposerDraft(trace, func(raw string) error {
		_, err := persondraft.ParseDraft(raw, c.in)
		return err
	})
}

// accountDraftCase is one account-composer request ready to be answered.
type accountDraftCase struct{ in accountdraft.Input }

func (c *accountDraftCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	recorder := &composerRecorder{completer: completer}
	_, _, err := accountdraft.Write(ctx, recorder, c.in)
	return composerTrace(accountDraftSite, recorder, err)
}

func (c *accountDraftCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	return evaluateComposerDraft(trace, func(raw string) error {
		_, err := accountdraft.ParseDraft(raw, c.in)
		return err
	})
}

// composerRecorder is the lane both composers write through: it records every
// request and the replies read back. Like the reply site's recorder it
// deliberately does not implement the shape-retry seam, so each call is sent
// bare — a case that retried would certify the answer a model gives after being
// told to try again.
type composerRecorder struct {
	completer aitasks.Completer
	requests  []model.Request
	last      string
	failed    error
}

func (r *composerRecorder) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	r.requests = append(r.requests, req)
	resp, err := r.completer.Complete(ctx, req)
	if err != nil {
		r.failed = err
		return model.Response{}, err
	}
	r.last = resp.Text
	return resp, nil
}

// composerTrace turns what the recorder saw into the lane's trace, keeping a
// failure of the CALL apart from a draft the writer would not accept: a call
// that never completed is the lane's problem, not a measurement of a reply.
func composerTrace(site string, recorder *composerRecorder, err error) (aitasks.Trace, error) {
	trace := aitasks.Trace{Requests: recorder.requests, Output: recorder.last}
	if recorder.failed != nil {
		return trace, fmt.Errorf("%s: the model call did not complete: %w", site, recorder.failed)
	}
	if err != nil {
		return trace, fmt.Errorf("%s: the drafter refused what the model returned: %w", site, err)
	}
	return trace, nil
}

// evaluateComposerDraft applies the site's own parse in the site's own order,
// and only classifies what it finds.
//
// The order carries the meaning. A reply that never happened is an abstention:
// nothing was asserted, so there is nothing to be wrong about. A reply that
// happened and the writer refused — empty text included, which its parser
// rejects like any other unreadable answer — is INVALID, because invalid means
// production's own validator turned the reply down. Classifying an empty reply
// as an abstention would move a validator refusal into the bucket that measures
// how often the model declines to answer, and quietly flatter both numbers.
func evaluateComposerDraft(trace aitasks.Trace, parse func(string) error) aitasks.Outcome {
	if len(trace.Requests) == 0 {
		return aitasks.Outcome{
			Result: aitasks.OutcomeAbstained,
			Detail: "the drafter issued no request, so no model wrote this draft",
		}
	}
	if err := parse(trace.Output); err != nil {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: err.Error()}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}
