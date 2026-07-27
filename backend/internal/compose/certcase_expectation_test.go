// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Three sites' verdicts now rest on this one comparison, so it owes its own
// proof: it is a subset claim, it names every disagreement rather than the
// first, it forgives presentation and nothing else, and it says the same thing
// in the same order on every run.

import (
	"reflect"
	"strings"
	"testing"
)

func TestExpectationDisagreementsNamesWhatTheScenarioDidNotGet(t *testing.T) {
	cases := []struct {
		name     string
		expected map[string]string
		grounded map[string]string
		want     []string
	}{
		{
			name:     "everything the scenario named, with the values it named",
			expected: map[string]string{"legal_name": "Acme Robotics GmbH"},
			grounded: map[string]string{"legal_name": "Acme Robotics GmbH"},
		},
		{
			// The subset claim: a real page grounds more than a scenario cares to
			// pin, and a case that demanded exhaustiveness would fail a read for
			// being richer than its author imagined.
			name:     "a grounded field the scenario never named",
			expected: map[string]string{"legal_name": "Acme Robotics GmbH"},
			grounded: map[string]string{"legal_name": "Acme Robotics GmbH", "industry": "robotics"},
		},
		{
			name:     "a field the reply never grounded",
			expected: map[string]string{"legal_name": "Acme Robotics GmbH"},
			grounded: map[string]string{},
			want:     []string{`no surviving legal_name, which the scenario expects`},
		},
		{
			name:     "a field grounded with another value",
			expected: map[string]string{"legal_name": "Acme Robotics GmbH"},
			grounded: map[string]string{"legal_name": "Acme Robotics"},
			want:     []string{`legal_name reads "Acme Robotics" where the scenario expects "Acme Robotics GmbH"`},
		},
		{
			// All of them, not the first: a run that read one field right and two
			// wrong is not the near miss one line would read as. Sorted, so the same
			// run says the same thing every time.
			name: "two ways of being wrong at once",
			expected: map[string]string{
				"legal_name": "Acme Robotics GmbH",
				"industry":   "robotics",
				"usp":        "in-house engineering",
			},
			grounded: map[string]string{"usp": "in-house engineering", "legal_name": "Globex SE"},
			want: []string{
				`no surviving industry, which the scenario expects`,
				`legal_name reads "Globex SE" where the scenario expects "Acme Robotics GmbH"`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expectationDisagreements(tc.expected, tc.grounded)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("disagreements = %q, want %q", got, tc.want)
			}
		})
	}
}

// Presentation is forgiven because the gates forgive it in evidence, and nothing
// else is: a scenario that failed on a straightened apostrophe would measure
// typography, and one that passed on a reworded value would measure nothing.
func TestExpectationDisagreementsForgivesPresentationAndNothingElse(t *testing.T) {
	cases := []struct {
		name        string
		expectation string
		grounded    string
		wantAgreed  bool
	}{
		{name: "a curly apostrophe", expectation: "Acme's promise", grounded: "Acme’s promise", wantAgreed: true},
		{name: "collapsed whitespace", expectation: "Acme Robotics GmbH", grounded: "Acme  Robotics\tGmbH", wantAgreed: true},
		{name: "a different case", expectation: "Acme Robotics GmbH", grounded: "ACME ROBOTICS GMBH", wantAgreed: true},
		{name: "a reworded value", expectation: "cut picking time in half", grounded: "halve picking time"},
		{name: "a reformatted phone", expectation: "+49 30 1234567", grounded: "+49-30-1234567"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expectationDisagreements(
				map[string]string{"value_proposition": tc.expectation},
				map[string]string{"value_proposition": tc.grounded})
			if agreed := len(got) == 0; agreed != tc.wantAgreed {
				t.Errorf("agreed = %v, want %v (%q)", agreed, tc.wantAgreed, got)
			}
		})
	}
}

// An expectation is what the corpus asserts, so a scenario asserting nothing has
// nothing to disagree with. The cases refuse that at Prepare; the comparison
// still has to be honest about it rather than inventing a complaint.
func TestExpectationDisagreementsSaysNothingAboutAnEmptyExpectation(t *testing.T) {
	if got := expectationDisagreements(nil, map[string]string{"legal_name": "Acme Robotics GmbH"}); got != nil {
		t.Errorf("disagreements = %q, want none", got)
	}
}

// The comparison asks about named fields, so a gate's result is keyed by name
// before it gets there.
func TestGroundedValuesKeysTheGateResultByFieldName(t *testing.T) {
	grounded := groundedValues([]evidencedField{
		{Field: "legal_name", Value: "Acme Robotics GmbH", EvidenceSnippet: "Impressum: Acme Robotics GmbH"},
		{Field: "industry", Value: "robotics", SourceURL: "https://acme.example"},
	})

	want := map[string]string{"legal_name": "Acme Robotics GmbH", "industry": "robotics"}
	if !reflect.DeepEqual(grounded, want) {
		t.Errorf("groundedValues = %v, want %v", grounded, want)
	}
	if got := expectationDisagreements(map[string]string{"industry": "robotics"}, grounded); len(got) != 0 {
		t.Errorf("a matching expectation disagreed: %q", got)
	}
}

// A gate that let nothing through is not a comparison that agrees with
// everything — it is one where every expected field is missing, and the run that
// reports it must say so.
func TestGroundedValuesOfNothingDisagreesWithEveryExpectation(t *testing.T) {
	got := expectationDisagreements(
		map[string]string{"legal_name": "Acme Robotics GmbH", "industry": "robotics"},
		groundedValues(nil))
	if len(got) != 2 {
		t.Fatalf("disagreements = %q, want one per expected field", got)
	}
	for _, line := range got {
		if !strings.HasPrefix(line, "no surviving ") {
			t.Errorf("disagreement %q does not say the field never survived", line)
		}
	}
}
