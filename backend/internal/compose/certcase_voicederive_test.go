// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the voice-derive case owes the certification lane: it runs the build the
// product runs — ai.DeriveVoice itself, not a re-creation of its request — it
// reports what the build refused in the build's own words, and it separates a
// profile the build would keep from one that grounds itself somewhere other than
// where the scenario says the style lives.
//
// The parity test at the bottom is the one that can catch this case becoming a
// copy: every other test here would stay green through exactly that drift.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The two openings a signature move can quote, one per corpus source, so a
// scenario about where the style lives has two answers to choose between.
const (
	voiceDeriveEmailOpening = "We commission on the 14th and hand over on the 16th."
	voiceDeriveChatOpening  = "Short answer: yes, the window holds."
)

// voiceDeriveSample builds one stored corpus source. The opening is the
// distinctive fragment; the padding is what makes the source the length a real
// one is, since the build refuses a corpus under the starter floor before it
// calls a model.
func voiceDeriveSample(id, register, opening string) ai.VoiceSample {
	text := opening + "\n" + strings.TrimSpace(strings.Repeat("We ship on the date we gave you. ", 60))
	return ai.VoiceSample{
		ID: id, Kind: "email", Register: register, Weight: 1, Text: text, WordCount: ai.WordCount(text),
	}
}

// voiceDeriveCorpusFixture is one author's corpus: the same voice in two
// registers, each with an opening a profile can point at.
func voiceDeriveCorpusFixture() voiceDeriveFixture {
	return voiceDeriveFixture{
		Personality: "Blunt, never hedges.",
		Samples: []ai.VoiceSample{
			voiceDeriveSample("S1", "email", voiceDeriveEmailOpening),
			voiceDeriveSample("S2", "chat", voiceDeriveChatOpening),
		},
	}
}

// voiceDeriveProfile is a profile the build accepts, grounded in the moves it is
// given. Written through the build's own inference type because a valid reply
// carries twelve fields, and hand-writing all of them per case would bury the
// one the case is about.
func voiceDeriveProfile(moves ...ai.VoiceSignatureMove) ai.VoiceInference {
	return ai.VoiceInference{
		IdentitySummary: "Writes like an engineer confirming a date.",
		ThinkingPattern: "States the verdict, then the two facts that hold it up.",
		Directness:      "Answers in the first sentence.",
		Structure:       "Two short paragraphs, no preamble.",
		SignatureMoves:  moves,
	}
}

func voiceDeriveReply(t *testing.T, inference ai.VoiceInference) string {
	t.Helper()
	raw, err := json.Marshal(inference)
	if err != nil {
		t.Fatalf("encoding the reply: %v", err)
	}
	return string(raw)
}

// voiceDeriveGroundedReply is the reply this file is mostly about: a profile
// whose one signature move quotes the email source verbatim and names it.
func voiceDeriveGroundedReply(t *testing.T, sampleID, quote string) string {
	t.Helper()
	return voiceDeriveReply(t, voiceDeriveProfile(ai.VoiceSignatureMove{
		Move: "Commits to a date in the opening line", Quote: quote, SampleID: sampleID,
	}))
}

func voiceDeriveFixtureJSON(t *testing.T, f voiceDeriveFixture) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return raw
}

func voiceDeriveExpectationJSON(t *testing.T, sampleIDs ...string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(sampleIDs)
	if err != nil {
		t.Fatalf("encoding the expectation: %v", err)
	}
	return raw
}

func runVoiceDeriveCase(
	t *testing.T, fixture voiceDeriveFixture, expected json.RawMessage, reply string,
) (aitasks.Outcome, aitasks.Trace) {
	t.Helper()
	prepared, err := voiceDeriveCases{}.Prepare(voiceDeriveFixtureJSON(t, fixture), expected)
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	trace, err := prepared.Run(context.Background(), &replyBrainStub{response: model.Response{Text: reply}})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	return prepared.Evaluate(trace), trace
}

// voiceDeriveOutcomeCase is one reply and the verdict the case owes it.
type voiceDeriveOutcomeCase struct {
	name       string
	expected   []string
	reply      string
	wantResult string
	wantDetail string
}

func runVoiceDeriveOutcomeCases(t *testing.T, cases []voiceDeriveOutcomeCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, _ := runVoiceDeriveCase(t, voiceDeriveCorpusFixture(),
				voiceDeriveExpectationJSON(t, tc.expected...), tc.reply)
			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if !strings.Contains(outcome.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to name %q", outcome.Detail, tc.wantDetail)
			}
		})
	}
}

// A refused profile is reported in the build's own words, because those words
// are what turns a reliability drop into a diagnosis. Every one of these is a
// profile the product would throw away — the opposite fix from a profile that is
// well-formed and grounded in the wrong place, which is why the two are never
// one number.
func TestVoiceDeriveCaseReportsWhatTheBuildRefused(t *testing.T) {
	runVoiceDeriveOutcomeCases(t, []voiceDeriveOutcomeCase{
		{
			name:       "a reply that is not the required JSON",
			expected:   []string{"S1"},
			reply:      "The voice profile is ready.",
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "voice build returned invalid JSON",
		},
		{
			name:     "a profile with nothing said about how the author thinks",
			expected: []string{"S1"},
			reply: voiceDeriveReply(t, func() ai.VoiceInference {
				inference := voiceDeriveProfile()
				inference.ThinkingPattern = "   "
				return inference
			}()),
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "thinking_pattern is empty",
		},
		{
			// The whole point of the citation rules: a move that names a source
			// the author never wrote is invention, and the build refuses the
			// profile rather than the move.
			name:       "a move citing a source that is not in the corpus",
			expected:   []string{"S1"},
			reply:      voiceDeriveGroundedReply(t, "S9", voiceDeriveEmailOpening),
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: `cited unknown sample "S9"`,
		},
		{
			name:       "a move quoting words the cited source does not contain",
			expected:   []string{"S1"},
			reply:      voiceDeriveGroundedReply(t, "S1", "I will circle back next week."),
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: `quote is not verbatim in sample "S1"`,
		},
	})
}

// The other half: profiles the build would keep, judged against the source the
// scenario says carries the style. Grounding somewhere else is a measurement of
// the model, not a defect in the profile.
func TestVoiceDeriveCaseSeparatesAGroundedProfileFromOneThatMissedIt(t *testing.T) {
	runVoiceDeriveOutcomeCases(t, []voiceDeriveOutcomeCase{
		{
			name:       "a profile grounded where the style lives",
			expected:   []string{"S1"},
			reply:      voiceDeriveGroundedReply(t, "S1", voiceDeriveEmailOpening),
			wantResult: aitasks.OutcomeAccepted,
			wantDetail: "grounds its signature moves in S1",
		},
		{
			name:     "a profile that read both registers",
			expected: []string{"S1", "S2"},
			reply: voiceDeriveReply(t, voiceDeriveProfile(
				ai.VoiceSignatureMove{Move: "Dates first", Quote: voiceDeriveEmailOpening, SampleID: "S1"},
				ai.VoiceSignatureMove{Move: "Verdict first", Quote: voiceDeriveChatOpening, SampleID: "S2"},
			)),
			wantResult: aitasks.OutcomeAccepted,
			wantDetail: "S1, S2",
		},
		{
			name:       "a profile grounded in the other register",
			expected:   []string{"S2"},
			reply:      voiceDeriveGroundedReply(t, "S1", voiceDeriveEmailOpening),
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: "grounds no signature move in S2",
		},
		{
			// The citation rules have nothing to check on a profile with no
			// signature move, so the build keeps it: it is prose about an author
			// with no proof it was ever read.
			name:       "a profile that points at nothing",
			expected:   []string{"S1"},
			reply:      voiceDeriveReply(t, voiceDeriveProfile()),
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: "it cites no sample",
		},
	})
}

// An expectation the site could never satisfy, and a corpus the build could never
// have been handed, both measure nothing for as long as they stay in the corpus.
// Naming them costs a parse; finding them later costs a paid run.
func TestVoiceDeriveCaseRefusesWhatThisSiteCannotMeasure(t *testing.T) {
	cases := []struct {
		name     string
		fixture  voiceDeriveFixture
		expected json.RawMessage
		want     string
	}{
		{
			name:     "an expectation that is not a list of sample ids",
			fixture:  voiceDeriveCorpusFixture(),
			expected: json.RawMessage(`{"sample_id":"S1"}`),
			want:     "not a list of corpus sample ids",
		},
		{
			name:     "an expectation that names no sample",
			fixture:  voiceDeriveCorpusFixture(),
			expected: voiceDeriveExpectationJSON(t),
			want:     "asserts nothing",
		},
		{
			name:     "a sample the corpus never carried",
			fixture:  voiceDeriveCorpusFixture(),
			expected: voiceDeriveExpectationJSON(t, "S9"),
			want:     "never supplies",
		},
		{
			name:     "a sample the prompt's word cap drops",
			fixture:  voiceDeriveCrowdedFixture(),
			expected: voiceDeriveExpectationJSON(t, "S1"),
			want:     "word cap drops that sample",
		},
		{
			name:     "a corpus under the floor the build refuses at",
			fixture:  voiceDeriveStarterShortFixture(),
			expected: voiceDeriveExpectationJSON(t, "S1"),
			want:     "at least 800",
		},
		{
			name:     "the same source id twice",
			fixture:  voiceDeriveVariant(func(f *voiceDeriveFixture) { f.Samples[1].ID = f.Samples[0].ID }),
			expected: voiceDeriveExpectationJSON(t, "S1"),
			want:     "twice",
		},
		{
			name:     "a source whose declared length is not its own",
			fixture:  voiceDeriveVariant(func(f *voiceDeriveFixture) { f.Samples[0].WordCount += 40 }),
			expected: voiceDeriveExpectationJSON(t, "S1"),
			want:     "budgets the prompt on the declared count",
		},
		{
			name:     "a source with no id to cite",
			fixture:  voiceDeriveVariant(func(f *voiceDeriveFixture) { f.Samples[0].ID = " " }),
			expected: voiceDeriveExpectationJSON(t, "S2"),
			want:     "no id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := voiceDeriveCases{}.Prepare(voiceDeriveFixtureJSON(t, tc.fixture), tc.expected)
			if err == nil {
				t.Fatal("Prepare accepted a scenario this site cannot measure")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal reads %q, want it to name %q", err, tc.want)
			}
		})
	}
}

// voiceDeriveVariant is the corpus with one thing about it broken, so a row of
// the table above reads as the thing it is about.
func voiceDeriveVariant(breakIt func(*voiceDeriveFixture)) voiceDeriveFixture {
	fixture := voiceDeriveCorpusFixture()
	breakIt(&fixture)
	return fixture
}

// voiceDeriveStarterShortFixture is a corpus of one short note — far under the
// words a build needs before it is willing to call a model at all.
func voiceDeriveStarterShortFixture() voiceDeriveFixture {
	return voiceDeriveFixture{
		Personality: "Blunt, never hedges.",
		Samples: []ai.VoiceSample{{
			ID: "S1", Kind: "email", Register: "email", Weight: 1,
			Text: voiceDeriveEmailOpening, WordCount: ai.WordCount(voiceDeriveEmailOpening),
		}},
	}
}

// voiceDeriveCrowdedFixture is a corpus whose chat source — the first the
// selector reaches — fills the prompt's whole word budget on its own, which is
// how the email source ends up outside the call the build makes.
func voiceDeriveCrowdedFixture() voiceDeriveFixture {
	whole := strings.TrimSpace(strings.Repeat("word ", 12_001))
	fixture := voiceDeriveCorpusFixture()
	fixture.Samples[1].Text = whole
	fixture.Samples[1].WordCount = ai.WordCount(whole)
	return fixture
}

// The corpus is the author's own writing, and it belongs on the data side of the
// one call this site makes. A site that interpolated it into the instruction
// frame would be taking dictation from the text it is meant to be analysing.
func TestVoiceDeriveCaseKeepsTheCorpusOutOfTheInstructions(t *testing.T) {
	fixture := voiceDeriveCorpusFixture()

	_, trace := runVoiceDeriveCase(t, fixture, voiceDeriveExpectationJSON(t, "S1"),
		voiceDeriveGroundedReply(t, "S1", voiceDeriveEmailOpening))

	if len(trace.Requests) != 1 {
		t.Fatalf("the trace carries %d requests, want the one call this site sends", len(trace.Requests))
	}
	req := trace.Requests[0]
	for _, carried := range []string{voiceDeriveEmailOpening, voiceDeriveChatOpening, fixture.Personality} {
		if strings.Contains(req.System, carried) {
			t.Errorf("the request puts %q in the instruction frame: %q", carried, req.System)
		}
		if !strings.Contains(req.Messages[0].Content, carried) {
			t.Errorf("the request never carried %q", carried)
		}
	}
}

// The claim this case makes is that it certifies the shipped path. The proof is
// running the shipped path beside it: the same corpus, answered by the same
// canned reply, must issue the same request and reach the same verdict in
// ai.DeriveVoice as in the case — including the exact words a refusal is
// refused in.
//
// Production is driven from DeriveVoice rather than from the build worker
// because everything above it is a database read and a held-out split, and
// neither reaches this prompt.
func TestVoiceDeriveCaseRunsWhatProductionRuns(t *testing.T) {
	cases := []struct {
		name       string
		reply      string
		wantResult string
	}{
		{
			name:       "a profile the build keeps",
			reply:      voiceDeriveGroundedReply(t, "S1", voiceDeriveEmailOpening),
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			name:       "a profile the build refuses",
			reply:      voiceDeriveGroundedReply(t, "S1", "I will circle back next week."),
			wantResult: aitasks.OutcomeInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := voiceDeriveCorpusFixture()
			hash, err := voiceDeriveSourceHash(fixture.Samples)
			if err != nil {
				t.Fatalf("hashing the fixture's corpus: %v", err)
			}
			brain := &replyBrainStub{response: model.Response{Text: tc.reply}}
			artifact, productionErr := ai.DeriveVoice(
				context.Background(), brain, fixture.Personality, hash, fixture.Samples)

			outcome, trace := runVoiceDeriveCase(t, fixture, voiceDeriveExpectationJSON(t, "S1"), tc.reply)

			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if len(trace.Requests) != 1 {
				t.Fatalf("the trace carries %d requests, want the one production sent", len(trace.Requests))
			}
			assertSameCompanyReadRequest(t, brain.request, trace.Requests[0])
			if productionErr != nil && outcome.Detail != productionErr.Error() {
				t.Errorf("the case reports %q, want the build's own words %q", outcome.Detail, productionErr)
			}
			if productionErr == nil && artifact.Markdown == "" {
				t.Error("production kept a profile with no compiled markdown, which it must never do")
			}
		})
	}
}

// A fixture is what PRODUCTION is given; an expectation is what the CORPUS
// asserts. Keeping them apart is what lets a gate rewrite every free-text field
// of a fixture — the canary sweep does exactly that — without rewriting an
// assertion.
//
// The corpus snapshot hash is not among them: the build is handed one, but it
// reaches neither the prompt nor a check, so the case mints it rather than
// asking a scenario author to invent a value that changes nothing.
func TestVoiceDeriveFixtureCarriesOnlyWhatProductionIsGiven(t *testing.T) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(voiceDeriveFixtureJSON(t, voiceDeriveCorpusFixture()), &fields); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	given := map[string]bool{"personality": true, "samples": true}
	for name := range fields {
		if !given[name] {
			t.Errorf("the fixture carries %q, which the build is not given", name)
		}
	}
	for name := range given {
		if _, present := fields[name]; !present {
			t.Errorf("the fixture drops %q, which the build always has", name)
		}
	}
}
