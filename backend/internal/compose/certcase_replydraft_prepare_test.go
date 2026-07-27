// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The reply-draft case's prepare side: what it refuses before a run spends
// anything, and what a fixture is allowed to carry at all. Both are decided by
// reading the corpus rather than by calling a model, which is why they sit apart
// from the tests that script one.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// An expectation the site could never satisfy, and a fixture describing a call
// the product could never make, both measure nothing for as long as they stay in
// the corpus. Naming them costs a parse; finding them later costs a paid run.
func TestReplyDraftCaseRefusesWhatThisSiteCannotServe(t *testing.T) {
	oversized := replyDraftPlainFixture()
	oversized.Activity.Body = strings.Repeat("x", replyActivityMaxRunes+1)

	tooManyExemplars := replyDraftVoicedFixture()
	tooManyExemplars.Voice.Exemplars = []ai.VoiceExemplar{
		{Register: "email", Kind: "email", Text: "We ship Monday."},
		{Register: "chat", Kind: "chat", Text: "Confirmed."},
		{Register: "email", Kind: "email", Text: "No change."},
	}

	unbuiltProfile := replyDraftVoicedFixture()
	unbuiltProfile.Voice.VoiceProfileMD = "  "

	cases := []struct {
		name     string
		fixture  replyDraftFixture
		expected json.RawMessage
		want     string
	}{
		{
			name:     "an expectation that is not a register",
			fixture:  replyDraftPlainFixture(),
			expected: json.RawMessage(`{"register":"plain"}`),
			want:     "is not a register",
		},
		{
			name:     "a register this site does not have",
			fixture:  replyDraftPlainFixture(),
			expected: replyDraftExpectationJSON(t, "german"),
			want:     `"german"`,
		},
		{
			// The plain variant is the only prompt a workspace without Voice DNA
			// state can send, so no run of this fixture could ever answer voiced.
			name:     "the voice, asked of a workspace that has none",
			fixture:  replyDraftPlainFixture(),
			expected: replyDraftExpectationJSON(t, replyDraftRegisterVoiced),
			want:     "no Voice DNA state",
		},
		{
			name:     "an activity longer than the drafter is ever handed",
			fixture:  oversized,
			expected: replyDraftExpectationJSON(t, replyDraftRegisterPlain),
			want:     "body",
		},
		{
			name:     "more verbatim examples than drafting shows",
			fixture:  tooManyExemplars,
			expected: replyDraftExpectationJSON(t, replyDraftRegisterVoiced),
			want:     "verbatim examples",
		},
		{
			name:     "a profile with no derived artifact in it",
			fixture:  unbuiltProfile,
			expected: replyDraftExpectationJSON(t, replyDraftRegisterVoiced),
			want:     "carries no derived voice profile",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := replyDraftCases{}.Prepare(replyDraftFixtureJSON(t, tc.fixture), tc.expected)
			if err == nil {
				t.Fatal("Prepare accepted a scenario this site cannot measure")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal reads %q, want it to name %q", err, tc.want)
			}
		})
	}
}

// A fixture is what PRODUCTION is given; an expectation is what the CORPUS
// asserts. Keeping them apart is what lets a gate rewrite every free-text field
// of a fixture — the canary sweep does exactly that — without rewriting an
// assertion.
func TestReplyDraftFixtureCarriesOnlyWhatProductionIsGiven(t *testing.T) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(replyDraftFixtureJSON(t, replyDraftVoicedFixture()), &fields); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	given := map[string]bool{"activity": true, "voice": true}
	for name := range fields {
		if !given[name] {
			t.Errorf("the fixture carries %q, which the drafting path is not given", name)
		}
	}
	for name := range given {
		if _, present := fields[name]; !present {
			t.Errorf("the fixture drops %q, which the drafting path always has", name)
		}
	}
}
