// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert_test

// The stand-in certification case the black-box tests in this package load and
// run corpora against, on its own file because two concerns share it: the
// corpus format's tests and Run's own plumbing tests.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// stubVariant names the stand-in site every scenario in this package's tests
// certifies. These tests pin the corpus FORMAT and the runner's own plumbing,
// so naming a shipped site would tie them to that site's prompt and validator
// — a different claim, and one its own tests already make.
const stubVariant = "widget"

// stubCases is a certification case in miniature: it issues one request built
// from its fixture, and its validator accepts a reply that carries the word the
// scenario expects.
type stubCases struct{ site aitasks.Site }

func (s stubCases) Site() aitasks.Site { return s.site }

func (s stubCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
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
	return stubCase{subject: f.Subject, want: want}, nil
}

type stubCase struct{ subject, want string }

func (c stubCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
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

func (c stubCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	if strings.TrimSpace(trace.Output) == "" {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: "the reply carries no text to read"}
	}
	if !strings.Contains(trace.Output, c.want) {
		return aitasks.Outcome{Result: aitasks.OutcomeWrongAnswer, Detail: "the description never names " + c.want}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}

// censusFor builds the registry a corpus is validated (and run) against: one
// stub site per task named, each with its case bound.
func censusFor(t *testing.T, tasks ...ai.Task) *aitasks.Registry {
	t.Helper()
	r := aitasks.NewRegistry()
	for _, task := range tasks {
		site := aitasks.Site{Task: task, Variant: stubVariant, Kind: ai.SiteKindOneShot}
		r.Register(site)
		r.BindCase(stubCases{site: site})
	}
	return r
}
