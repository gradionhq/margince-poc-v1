// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// The stand-in certification case this package's own tests certify. It lives
// on its own file because two concerns share it — the runner pipeline's tests
// and the payload trace's — and because it is the ONE place that says what the
// runner actually requires of a case: a request built from the fixture, and a
// validator that tells an unusable reply apart from a wrong one.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// widgetVariant names the stand-in site every certifyTask test in this file
// certifies. These tests pin the RUNNER's own pipeline — repeats, the degrade
// gates, served-identity uniformity, the verdict fold — so binding them to a
// shipped site would make them fail whenever that site's prompt or validator
// changed, which is a different claim entirely.
const widgetVariant = "widget"

// containsWidget is the word this site's validator requires a correct
// description to carry, and the answer every scenario below expects.
const containsWidget = "widget"

// widgetAbstention is the reply this stand-in site treats as declining to
// describe anything — the one a scenario whose right answer is silence expects.
const widgetAbstention = "(nothing to describe)"

func widgetSite() aitasks.Site {
	return aitasks.Site{Task: ai.TaskSummarize, Variant: widgetVariant, Kind: ai.SiteKindOneShot}
}

// widgetCases is a certification case in miniature, with the two properties
// the runner actually depends on: it issues its own request from the fixture,
// and its validator tells an unusable reply apart from a wrong one.
type widgetCases struct{}

func (widgetCases) Site() aitasks.Site { return widgetSite() }

func (widgetCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f struct {
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, err
	}
	var want string
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, err
	}
	return widgetCase{subject: f.Subject, want: want}, nil
}

// widgetCase is one subject ready to be described, closed over the word the
// scenario expects the description to carry.
type widgetCase struct{ subject, want string }

func (c widgetCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := model.Request{
		System:    "Describe the subject in one sentence.",
		Messages:  []model.Message{{Role: "user", Content: c.subject}},
		MaxTokens: 1024,
	}
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, err
	}
	trace.Output = resp.Text
	return trace, nil
}

func (c widgetCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	if strings.TrimSpace(trace.Output) == "" {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: "the reply carries no text to read"}
	}
	if strings.TrimSpace(trace.Output) == widgetAbstention {
		return aitasks.Outcome{Result: aitasks.OutcomeAbstained, Detail: "the model described nothing"}
	}
	if !strings.Contains(trace.Output, c.want) {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: "the description never names " + c.want,
		}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}

// testCensus is the registry certifyTask resolves this file's scenarios
// through: one registered site, one bound case.
func testCensus(t *testing.T) *aitasks.Registry {
	t.Helper()
	r := aitasks.NewRegistry()
	r.Register(widgetSite())
	r.BindCase(widgetCases{})
	return r
}
