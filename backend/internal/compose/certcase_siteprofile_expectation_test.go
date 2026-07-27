// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What this site's expectation vocabulary means, and what it refuses.
//
// The profile lane is the one site whose corpus makes a NEGATIVE claim — that a
// crawl grounds no value for a named field — so it is the one site whose
// expectation has two spellings: the bare field-to-value map every other
// grounding site uses, and an explicit grounded/not_grounded mapping. Both are
// tested here rather than beside the reply grading, because what they assert is
// a different question from what a reply does with it.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
)

// A scenario shaped like something else asserts nothing about the reply, and a
// case that ran it anyway would report a number nobody wrote a claim for.
func TestSiteProfileCaseRefusesAnExpectationItCannotRead(t *testing.T) {
	for _, expected := range []json.RawMessage{nil, json.RawMessage(`["legal_name"]`), json.RawMessage(`7`)} {
		_, err := siteProfileCases{}.Prepare(siteProfileFixtureJSON(t), expected)
		if err == nil {
			t.Fatalf("a scenario expecting %s prepared", expected)
		}
		if !strings.Contains(err.Error(), "not a mapping") {
			t.Errorf("the refusal does not say what an expectation must be: %v", err)
		}
	}
}

// The explicit form's two keys are the whole vocabulary. A mistyped one would
// otherwise load as an expectation that forbids nothing and pass whatever the
// model fabricated — the exact failure the negative claim exists to prevent.
func TestSiteProfileCaseRefusesAnExplicitExpectationWithAnUnknownKey(t *testing.T) {
	_, err := siteProfileCases{}.Prepare(siteProfileFixtureJSON(t),
		json.RawMessage(`{"grounded":{},"not_ground":["legal_name"]}`))
	if err == nil {
		t.Fatal("an expectation with a mistyped key prepared")
	}
	if !strings.Contains(err.Error(), "not_ground") {
		t.Errorf("the refusal does not name the offending key: %v", err)
	}
}

// A field both required and forbidden is a contradiction no reply can satisfy,
// so it is named at Prepare rather than measured as a permanent zero.
func TestSiteProfileCaseRefusesAFieldItBothExpectsAndForbids(t *testing.T) {
	_, err := siteProfileCases{}.Prepare(siteProfileFixtureJSON(t), json.RawMessage(
		`{"grounded":{"legal_name":"Acme Robotics GmbH"},"not_grounded":["legal_name"]}`))
	if err == nil {
		t.Fatal("an expectation requiring and forbidding one field prepared")
	}
	if !strings.Contains(err.Error(), "both expects and forbids") {
		t.Errorf("the refusal does not say what is contradictory: %v", err)
	}
}

// A forbidden field outside the prompt's vocabulary is one no model can propose,
// so the claim could never be violated and would measure nothing.
func TestSiteProfileCaseRefusesAForbiddenFieldThePromptNeverOffers(t *testing.T) {
	_, err := siteProfileCases{}.Prepare(siteProfileFixtureJSON(t),
		json.RawMessage(`{"not_grounded":["favourite_colour"]}`))
	if err == nil {
		t.Fatal("an expectation forbidding an unofferable field prepared")
	}
	if !strings.Contains(err.Error(), "favourite_colour") {
		t.Errorf("the refusal does not name the offending field: %v", err)
	}
}

// The two spellings must mean the same thing where they say the same thing, or a
// scenario's claim would depend on which form its author reached for.
func TestSiteProfileCaseReadsBothSpellingsOfOneExpectation(t *testing.T) {
	reply := siteProfileReply(siteProfileLegalNameClaim(t, "Acme Robotics GmbH"))
	bare := siteProfileExpectationJSON(t, siteProfileWantLegalName)
	explicit := json.RawMessage(`{"grounded":{"legal_name":"Acme Robotics GmbH"}}`)

	for name, expected := range map[string]json.RawMessage{"bare": bare, "explicit": explicit} {
		t.Run(name, func(t *testing.T) {
			prepared, err := siteProfileCases{}.Prepare(siteProfileFixtureJSON(t), expected)
			if err != nil {
				t.Fatalf("preparing the case: %v", err)
			}
			trace, err := prepared.Run(context.Background(), &siteProfileCompleterStub{reply: reply})
			if err != nil {
				t.Fatalf("running the case: %v", err)
			}
			if outcome := prepared.Evaluate(trace); outcome.Result != aitasks.OutcomeAccepted {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, aitasks.OutcomeAccepted)
			}
		})
	}
}
