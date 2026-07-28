// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the held-out drafting case refuses before a paid run spends anything on
// it: a fixture the evaluation could not have produced, and an expectation no
// reply could satisfy. Both are corpus defects rather than model failures, and
// naming them here costs a parse where finding them later costs a run.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func TestVoiceEvalDraftCaseRefusesAnUnreachableExpectation(t *testing.T) {
	cases := []struct {
		name     string
		expected json.RawMessage
		wantMsg  string
	}{
		{name: "an expectation shaped like something else", expected: json.RawMessage(`{"min":0.6}`), wantMsg: "not a stylometric floor"},
		{name: "no expectation at all", expected: nil, wantMsg: "not a stylometric floor"},
		{name: "a floor every draft clears", expected: voiceEvalDraftFloor(t, 0), wantMsg: "asserts nothing"},
		{name: "a negative floor", expected: voiceEvalDraftFloor(t, -0.2), wantMsg: "asserts nothing"},
		{name: "a floor above the measure's ceiling", expected: voiceEvalDraftFloor(t, 1.5), wantMsg: "at most 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := voiceEvalDraftCases{}.Prepare(voiceEvalDraftFixtureAt(t, 0), tc.expected)
			if err == nil {
				t.Fatal("an unreachable expectation prepared")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("the refusal does not say what is unreachable: %v", err)
			}
		})
	}
}

// A fixture the evaluation could never have been handed would certify a call the
// product does not make: a build below the corpus floor never produces an
// artifact, the selector keeps at most two exemplars, and the loop runs a fixed
// number of repeats.
func TestVoiceEvalDraftCaseRefusesAFixtureTheEvaluationCouldNotRun(t *testing.T) {
	base := func(t *testing.T) voiceEvalDraftFixture {
		t.Helper()
		var f voiceEvalDraftFixture
		if err := json.Unmarshal(voiceEvalDraftFixtureAt(t, 0), &f); err != nil {
			t.Fatalf("decoding the fixture: %v", err)
		}
		return f
	}
	cases := []struct {
		name    string
		mutate  func(*voiceEvalDraftFixture)
		wantMsg string
	}{
		{
			name:    "a candidate with no derived profile",
			mutate:  func(f *voiceEvalDraftFixture) { f.VoiceProfileMD = "  " },
			wantMsg: "no derived voice profile",
		},
		{
			name: "more verbatim examples than a build keeps",
			mutate: func(f *voiceEvalDraftFixture) {
				f.Exemplars = []ai.VoiceExemplar{
					{Register: "email", Kind: "email", Text: "one"},
					{Register: "spoken", Kind: "transcript", Text: "two"},
					{Register: "long_form", Kind: "document", Text: "three"},
				}
			},
			wantMsg: "keeps at most",
		},
		{
			name: "a verbatim example with no text",
			mutate: func(f *voiceEvalDraftFixture) {
				f.Exemplars = []ai.VoiceExemplar{{Register: "email", Kind: "email", Text: " "}}
			},
			wantMsg: "example with no text",
		},
		{
			name:    "a corpus below the build floor",
			mutate:  func(f *voiceEvalDraftFixture) { f.Stats.WordCount = ai.StarterVoiceWords - 1 },
			wantMsg: "own-authored words",
		},
		{
			name:    "a held-out sample with nothing in it",
			mutate:  func(f *voiceEvalDraftFixture) { f.HeldOut.Text = "   " },
			wantMsg: "nothing to reply to",
		},
		{
			name:    "a held-out sample with no register",
			mutate:  func(f *voiceEvalDraftFixture) { f.HeldOut.Register = "" },
			wantMsg: "names no register",
		},
		{
			name:    "a repeat the loop never reaches",
			mutate:  func(f *voiceEvalDraftFixture) { f.Repeat = voiceEvalRepeatsPerPrompt },
			wantMsg: "repeats each held-out prompt",
		},
		{
			name:    "a repeat before the first",
			mutate:  func(f *voiceEvalDraftFixture) { f.Repeat = -1 },
			wantMsg: "repeats each held-out prompt",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := base(t)
			tc.mutate(&fixture)
			raw, err := json.Marshal(fixture)
			if err != nil {
				t.Fatalf("encoding the fixture: %v", err)
			}
			_, err = voiceEvalDraftCases{}.Prepare(raw, voiceEvalDraftFloor(t, voiceEvalClearedFloor))
			if err == nil {
				t.Fatal("a fixture the evaluation could not run prepared")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("the refusal does not name what is wrong: %v", err)
			}
		})
	}
}

// voiceEvalBrainRecorder is the brain a whole evaluation runs through: it
// records every request and answers the drafting calls with one canned reply and
// the judge call with a usable score set, so what the evaluation asked can be
// compared against what the case asks.
type voiceEvalBrainRecorder struct {
	draftReply string
	requests   []model.Request
}

func (r *voiceEvalBrainRecorder) Complete(_ context.Context, req model.Request) (model.Response, error) {
	r.requests = append(r.requests, req)
	if strings.HasPrefix(req.System, voiceEvalJudgeSystem) {
		scores := make([]float64, voiceEvalRepeatsPerPrompt)
		for i := range scores {
			scores[i] = 0.9
		}
		payload, err := json.Marshal(map[string]any{"scores": scores})
		if err != nil {
			return model.Response{}, err
		}
		return model.Response{Text: string(payload)}, nil
	}
	return model.Response{Text: r.draftReply}, nil
}

// draftRequests keeps the drafting calls of a whole evaluation; the judge call
// belongs to the sibling site.
func (r *voiceEvalBrainRecorder) draftRequests() []model.Request {
	var out []model.Request
	for _, req := range r.requests {
		if strings.HasPrefix(req.System, voiceEvalDraftSystem) {
			out = append(out, req)
		}
	}
	return out
}

// The claim this case makes is that it certifies the shipped path. The proof is
// running the shipped path beside it: a whole evaluation over the same candidate
// and the same held-out sample must issue the case's request for every repeat,
// and must take the same reading of the same reply.
//
// That reading has two halves, and they decide different things. Whether the
// evaluation could READ the draft decides whether there is a measurement at all,
// and it is the case's invalid/not verdict. Whether the draft trips the anti-AI
// floor decides nothing here: the evaluation counts the tell, KEEPS the draft
// and scores it, and the count is spent once the whole candidate is folded
// together. So the tell belongs in what the case reports, never in whether it
// reports a measurement — a run refused here would be a draft the evaluation
// went on to grade.
