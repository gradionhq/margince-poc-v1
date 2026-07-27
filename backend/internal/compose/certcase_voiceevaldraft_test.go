// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the held-out drafting case owes the certification lane: it issues the
// request the evaluation issues — the variation index included, because that
// index is part of the prompt — it reads the reply with the evaluation's own
// reading of a draft, and it separates the three things a reply can be. A draft
// the evaluation cannot read and a draft that reads nothing like the author fail
// for opposite reasons: the first leaves the candidate unscored, the second is
// the measurement this site exists to take.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The two floors every test below asserts against. voiceEvalClearedFloor is
// under the proximity a corpus-shaped draft reaches and voiceEvalMissedFloor is
// above it, so the same reply is an accepted answer under one and a wrong one
// under the other — which is the expectation doing the work rather than the
// reply.
const (
	voiceEvalClearedFloor = 0.6
	voiceEvalMissedFloor  = 0.9
)

// The three replies. The corpus every test builds from is written in one
// sentence rhythm, so a draft in that rhythm sits close to its fingerprint and a
// staccato one sits far from it.
const (
	voiceEvalCorpusShapedReply = `{"subject":"Re: the work",` +
		`"body":"Useful sentence about the work. We ship Monday and the plan holds."}`
	voiceEvalStaccatoReply = `{"subject":"Re: the work",` +
		`"body":"Why?! Really?! No way! Stop! Now! Go! Yes! What?! How?! Never!"}`
	// The tell survives sanitizing: the sanitizer only rewrites the hard
	// punctuation rule, and corporate AI vocabulary is reported, never guessed
	// away.
	voiceEvalTellReply = `{"subject":"Re: the work",` +
		`"body":"Useful sentence about the work. We delve into the plan and it holds."}`
)

// voiceEvalCorpus is one built candidate and the sample reserved to draft
// against, assembled the way a build assembles them: the stats and the exemplars
// come from the build half through the product's own analyzer and selector, so
// the block this case sends is the block a build sends.
func voiceEvalCorpus(t *testing.T) (ai.VoiceArtifact, ai.VoiceSample) {
	t.Helper()
	heldOut, build := splitVoiceHeldOut(evalSamples(8), "hash-cert")
	if len(heldOut) == 0 {
		t.Fatal("the split reserved no held-out sample, so there is nothing to draft against")
	}
	stats := ai.AnalyzeVoice(build)
	return ai.VoiceArtifact{
		Markdown:  "# Voice DNA\n\n## Identity\n\ndirect operator, short sentences",
		Stats:     stats,
		Exemplars: ai.SelectExemplars(build, stats),
	}, heldOut[0]
}

// voiceEvalDraftFixtureAt is one held-out drafting call at one repeat.
func voiceEvalDraftFixtureAt(t *testing.T, repeat int) json.RawMessage {
	t.Helper()
	artifact, sample := voiceEvalCorpus(t)
	raw, err := json.Marshal(voiceEvalDraftFixture{
		Personality:    "Writes short. States the verdict first.",
		VoiceProfileMD: artifact.Markdown,
		Exemplars:      artifact.Exemplars,
		Stats:          artifact.Stats,
		HeldOut:        voiceEvalHeldOutSample{Register: sample.Register, Text: sample.Text},
		Repeat:         repeat,
	})
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return raw
}

// voiceEvalDraftFloor is what the corpus asserts, encoded as the corpus will
// carry it — beside the fixture, never inside it.
func voiceEvalDraftFloor(t *testing.T, floor float64) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(floor)
	if err != nil {
		t.Fatalf("encoding the expectation: %v", err)
	}
	return raw
}

func runVoiceEvalDraftCase(t *testing.T, floor float64, reply string) (aitasks.Outcome, aitasks.Trace) {
	t.Helper()
	prepared, err := voiceEvalDraftCases{}.Prepare(voiceEvalDraftFixtureAt(t, 0), voiceEvalDraftFloor(t, floor))
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	trace, err := prepared.Run(context.Background(), &replyBrainStub{response: model.Response{Text: reply}})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	return prepared.Evaluate(trace), trace
}

func TestVoiceEvalDraftCaseSeparatesTheThreeThingsAReplyCanBe(t *testing.T) {
	cases := []struct {
		name       string
		floor      float64
		reply      string
		wantResult string
		wantDetail string
	}{
		{
			name: "a draft in the corpus's own rhythm", floor: voiceEvalClearedFloor,
			reply: voiceEvalCorpusShapedReply, wantResult: aitasks.OutcomeAccepted,
			wantDetail: "of the corpus fingerprint",
		},
		{
			name: "a draft nothing like the author", floor: voiceEvalClearedFloor,
			reply: voiceEvalStaccatoReply, wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: "the scenario expects at least",
		},
		{
			// Well formed, in the right rhythm, and still short of what the
			// scenario claims this profile reaches — a measurement of the model,
			// not a defect in the reply.
			name: "a draft under the floor the scenario claims", floor: voiceEvalMissedFloor,
			reply: voiceEvalCorpusShapedReply, wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: "the scenario expects at least 0.9000",
		},
		{
			// The evaluation keeps a draft that trips the floor and scores it, so
			// the measurement still stands; what the tell costs is the candidate's
			// activation, and this record's job is to say it was earned.
			name: "a draft carrying a tell the sanitizer cannot remove", floor: voiceEvalClearedFloor,
			reply: voiceEvalTellReply, wantResult: aitasks.OutcomeAccepted,
			wantDetail: "ai_ese",
		},
		{
			name: "a reply that is not the required JSON", floor: voiceEvalClearedFloor,
			reply: "I could not write that.", wantResult: aitasks.OutcomeInvalid,
			wantDetail: `is not {"subject":"...","body":"..."}`,
		},
		{
			name: "a draft with no body", floor: voiceEvalClearedFloor,
			reply: `{"subject":"Re: the work","body":"   "}`, wantResult: aitasks.OutcomeInvalid,
			wantDetail: "empty subject or body",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, _ := runVoiceEvalDraftCase(t, tc.floor, tc.reply)
			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if !strings.Contains(outcome.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to name %q", outcome.Detail, tc.wantDetail)
			}
		})
	}
}

// The repeat index is the one field of this fixture that changes nothing but the
// prompt, which is exactly why it is carried: the evaluation asks the same
// question voiceEvalRepeatsPerPrompt times and distinguishes the calls by that
// index alone. A case that dropped it would certify a prompt the product sends
// once out of three times.
func TestVoiceEvalDraftCaseSendsTheVariationItWasGiven(t *testing.T) {
	seen := map[string]bool{}
	for repeat := 0; repeat < voiceEvalRepeatsPerPrompt; repeat++ {
		prepared, err := voiceEvalDraftCases{}.Prepare(
			voiceEvalDraftFixtureAt(t, repeat), voiceEvalDraftFloor(t, voiceEvalClearedFloor))
		if err != nil {
			t.Fatalf("preparing repeat %d: %v", repeat, err)
		}
		trace, err := prepared.Run(context.Background(),
			&replyBrainStub{response: model.Response{Text: voiceEvalCorpusShapedReply}})
		if err != nil {
			t.Fatalf("running repeat %d: %v", repeat, err)
		}
		if len(trace.Requests) != 1 {
			t.Fatalf("the trace carries %d requests, want the one call this site issues", len(trace.Requests))
		}
		suffix := fmt.Sprintf("\n(variation %d)", repeat+1)
		content := trace.Requests[0].Messages[0].Content
		if !strings.HasSuffix(content, suffix) {
			t.Errorf("repeat %d does not end in %q", repeat, suffix)
		}
		if seen[content] {
			t.Errorf("repeat %d sends a prompt an earlier repeat already sent", repeat)
		}
		seen[content] = true
	}
}

// A fixture is what PRODUCTION is given; an expectation is what the CORPUS
// asserts. Keeping them apart is what lets a gate rewrite the fixture's free
// text — the canary sweep does exactly that — without rewriting an assertion.
func TestVoiceEvalDraftFixtureCarriesOnlyWhatProductionIsGiven(t *testing.T) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(voiceEvalDraftFixtureAt(t, 1), &fields); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	given := map[string]bool{
		"personality": true, "voice_profile_md": true, "exemplars": true,
		"stats": true, "held_out": true, "repeat": true,
	}
	for name := range fields {
		if !given[name] {
			t.Errorf("the fixture carries %q, which the evaluation does not hand the drafting call", name)
		}
	}
	for name := range given {
		if _, present := fields[name]; !present {
			t.Errorf("the fixture drops %q, which the evaluation always supplies", name)
		}
	}
}

// An expectation no reply could satisfy would measure nothing for as long as it
// stayed in the corpus. Prepare is where that gets named, while it is still a
// wiring error rather than a paid run of zeros.
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
func TestVoiceEvalDraftCaseRunsWhatProductionRuns(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		// wantReason is the evaluation's own account of this reply, empty when
		// the evaluation has nothing to say against it.
		wantReason string
		// wantTell is the anti-AI code this reply trips, empty when it trips
		// none.
		wantTell   string
		wantResult string
	}{
		{name: "a draft in the corpus's own rhythm", reply: voiceEvalCorpusShapedReply, wantResult: aitasks.OutcomeAccepted},
		{
			name: "a draft the evaluation cannot read", reply: "I could not write that.",
			wantReason: "the model returned malformed drafts during evaluation", wantResult: aitasks.OutcomeInvalid,
		},
		{
			name: "a draft carrying a tell", reply: voiceEvalTellReply,
			wantReason: "anti-AI hard failures survived sanitizing", wantTell: "ai_ese",
			wantResult: aitasks.OutcomeAccepted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			artifact, sample := voiceEvalCorpus(t)
			brain := &voiceEvalBrainRecorder{draftReply: tc.reply}
			result, err := evaluateVoiceCandidate(context.Background(), brain, artifact,
				"Writes short. States the verdict first.", []ai.VoiceSample{sample}, nil)
			if err != nil {
				t.Fatalf("the evaluation did not complete: %v", err)
			}
			readable := voiceEvalRecordFlag(t, result, "structured_output_valid")
			tellsCounted := voiceEvalRecordCount(t, result, "anti_ai_hard_failures")

			production := brain.draftRequests()
			if len(production) != voiceEvalRepeatsPerPrompt {
				t.Fatalf("the evaluation issued %d drafting calls, want %d", len(production), voiceEvalRepeatsPerPrompt)
			}
			for repeat, sent := range production {
				prepared, err := voiceEvalDraftCases{}.Prepare(
					voiceEvalDraftFixtureAt(t, repeat), voiceEvalDraftFloor(t, voiceEvalClearedFloor))
				if err != nil {
					t.Fatalf("preparing repeat %d: %v", repeat, err)
				}
				trace, err := prepared.Run(context.Background(),
					&replyBrainStub{response: model.Response{Text: tc.reply}})
				if err != nil {
					t.Fatalf("running repeat %d: %v", repeat, err)
				}
				assertSameCompanyReadRequest(t, sent, trace.Requests[0])
				outcome := prepared.Evaluate(trace)
				if outcome.Result != tc.wantResult {
					t.Fatalf("repeat %d Result = %q (%s), want %q", repeat, outcome.Result, outcome.Detail, tc.wantResult)
				}
				// The parity claim itself, derived from the evaluation's own
				// record rather than restated: the case refuses exactly the
				// drafts the evaluation could not read.
				if refused := outcome.Result == aitasks.OutcomeInvalid; refused == readable {
					t.Errorf("repeat %d reports %q while the evaluation reads this draft = %t",
						repeat, outcome.Result, readable)
				}
				assertVoiceEvalTellReported(t, outcome.Detail, tc.wantTell)
			}

			if (tellsCounted > 0) != (tc.wantTell != "") {
				t.Errorf("the evaluation counted %d tells, want a tell = %t", tellsCounted, tc.wantTell != "")
			}
			// A draft the evaluation kept is one it can show a reviewer; a draft
			// it could not read leaves the sample list empty.
			if kept := len(result.SampleDrafts) > 0; kept != readable {
				t.Errorf("the evaluation kept %d sample drafts while reading this draft = %t",
					len(result.SampleDrafts), readable)
			}
			assertEvaluationReason(t, result, tc.wantReason)
		})
	}
}

// assertVoiceEvalTellReported holds the case to naming what the evaluation
// counted. The tell does not change the verdict, and that is exactly why it has
// to reach the Detail: a corpus author reading an accepted run is otherwise
// never told that this draft is one the build will refuse to activate on.
func assertVoiceEvalTellReported(t *testing.T, detail, want string) {
	t.Helper()
	if want == "" {
		if strings.Contains(detail, "anti-AI") {
			t.Errorf("Detail = %q, want no tell named for a clean draft", detail)
		}
		return
	}
	if !strings.Contains(detail, want) {
		t.Errorf("Detail = %q, want it to name the %q the evaluation counted", detail, want)
	}
}

// voiceEvalRecordFlag and voiceEvalRecordCount read the evaluation record the
// build persists. A missing or differently typed entry is a failure rather than
// a zero value, because a zero read as a claim is a parity assertion that
// asserts nothing.
func voiceEvalRecordFlag(t *testing.T, result voiceEvaluationResult, key string) bool {
	t.Helper()
	value, present := result.Evaluation[key]
	if !present {
		t.Fatalf("the evaluation record carries no %q", key)
	}
	flag, ok := value.(bool)
	if !ok {
		t.Fatalf("the evaluation record's %q is %T, want a flag", key, value)
	}
	return flag
}

func voiceEvalRecordCount(t *testing.T, result voiceEvaluationResult, key string) int {
	t.Helper()
	value, present := result.Evaluation[key]
	if !present {
		t.Fatalf("the evaluation record carries no %q", key)
	}
	count, ok := value.(int)
	if !ok {
		t.Fatalf("the evaluation record's %q is %T, want a count", key, value)
	}
	return count
}

// assertEvaluationReason holds the whole build to the same reading of the reply
// the case took: a reply the case calls unusable is one the evaluation refuses
// to activate on, and it says why in the reasons a reviewer is shown.
func assertEvaluationReason(t *testing.T, result voiceEvaluationResult, want string) {
	t.Helper()
	reasons := strings.Join(result.ReviewReasons, "; ")
	if want == "" {
		if result.Action != voiceActionAutoActivated {
			t.Errorf("the evaluation took %q on a clean candidate, and said: %s", result.Action, reasons)
		}
		return
	}
	if result.Action != voiceActionReviewRequired {
		t.Errorf("the evaluation took %q, want it to hold this candidate for review", result.Action)
	}
	if !strings.Contains(reasons, want) {
		t.Errorf("the evaluation's reasons are %q, want them to name %q", reasons, want)
	}
}

// The case must be reachable through the same registry the census validates, or
// the site is registered and served by nothing.
func TestTaskCensusBindsTheVoiceEvalDraftCase(t *testing.T) {
	registry, err := NewTaskCensus()
	if err != nil {
		t.Fatalf("the census does not validate: %v", err)
	}
	site := voiceEvalDraftCases{}.Site()
	bound, ok := registry.CaseFor(site.Task, site.Variant)
	if !ok {
		t.Fatalf("no certification case is bound to %s/%s", site.Task, site.Variant)
	}
	if bound.Site() != site {
		t.Errorf("the bound case serves %+v, want %+v", bound.Site(), site)
	}
}
