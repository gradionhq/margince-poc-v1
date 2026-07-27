// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aitasks_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// stubCase is the minimum a site owes the harness: it prepares from a fixture,
// it runs, and it says whether the reply was usable.
type stubCase struct{ site aitasks.Site }

func (s stubCase) Site() aitasks.Site { return s.site }

func (s stubCase) Prepare(_, _ json.RawMessage) (aitasks.PreparedCase, error) { return s, nil }

func (s stubCase) Run(context.Context, aitasks.Completer) (aitasks.Trace, error) {
	return aitasks.Trace{Output: "{}"}, nil
}

func (s stubCase) Evaluate(aitasks.Trace) aitasks.Outcome {
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}

func TestCaseForFindsABoundCase(t *testing.T) {
	r := aitasks.NewRegistry()
	site := aitasks.Site{Task: ai.TaskRateExtract, Variant: "fx", Kind: ai.SiteKindOneShot}
	r.Register(site)
	r.BindCase(site, stubCase{site: site})

	got, ok := r.CaseFor(ai.TaskRateExtract, "fx")
	if !ok {
		t.Fatal("CaseFor did not find a bound case")
	}
	if got.Site() != site {
		t.Errorf("CaseFor returned a case for %+v, want %+v", got.Site(), site)
	}
	if _, ok := r.CaseFor(ai.TaskRateExtract, "pricing"); ok {
		t.Error("CaseFor found a case that was never bound")
	}
}

// A case bound to a site the contract never declared is a wiring defect, and
// Validate is where wiring defects are named.
func TestValidateRefusesACaseForAnUnregisteredSite(t *testing.T) {
	r := aitasks.NewRegistry()
	invented := aitasks.Site{Task: ai.TaskRateExtract, Variant: "invented", Kind: ai.SiteKindOneShot}
	r.BindCase(invented, stubCase{site: invented})
	err := r.Validate()
	if err == nil {
		t.Fatal("a case bound to an unregistered site validated")
	}
	if !strings.Contains(err.Error(), "invented") {
		t.Errorf("the error does not name the offending site: %v", err)
	}
}

// An Outcome must distinguish "the model answered wrongly" from "the reply was
// unusable" from "the model correctly answered nothing". Collapsed into one
// number they are the reason an injection scenario cannot say whether the
// injection worked, and the reason an abstention scenario cannot say whether the
// model declined to fabricate or fabricated past a gate.
func TestOutcomeResultsAreDistinctAndNamed(t *testing.T) {
	seen := map[string]bool{}
	for _, result := range []string{
		aitasks.OutcomeAccepted,
		aitasks.OutcomeWrongAnswer,
		aitasks.OutcomeInvalid,
		aitasks.OutcomeAbstained,
	} {
		if result == "" {
			t.Error("an outcome result is the empty string, which reads as unset")
		}
		if seen[result] {
			t.Errorf("outcome result %q is declared twice", result)
		}
		seen[result] = true
	}
}
