// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the reply-draft case owes the certification lane: it drives the drafter
// the product drives, it records EVERY request that drafter issued — the voiced
// attempt, the critic retry and the plain fallback are three calls, not one —
// and it judges the draft that would actually be served.
//
// The three-request shape is the reason several tests below script more than one
// reply. A case that recorded only the first would report a prompt the model was
// shown while grading an answer it gave to a different one, and the canary gate
// reads the same trace.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The replies a run is about. Written as text rather than marshalled so a
// malformed reply is as expressible as a well-formed one.
const (
	replyDraftCleanReply = `{"subject":"Re: Heat recovery commissioning",` +
		`"body":"September works. We commission on the 14th and hand over on the 16th."}`
	// Three deterministic tells at once — a canned opener, the not-X-but-Y
	// reframe and a generic engagement question — none of which the sanitizer
	// can mechanically remove, so this reply survives every pass the drafter
	// makes over it.
	replyDraftViolatingReply = `{"subject":"Re: Heat recovery commissioning",` +
		`"body":"Here's the thing: it's not about dates, but transformation. What do you think?"}`
)

// replyDraftVoicedFixture is a workspace with a built Voice DNA profile asking
// for a reply — the variant that can issue all three calls.
func replyDraftVoicedFixture() replyDraftFixture {
	return replyDraftFixture{
		Activity: replyActivityData{
			Subject: "Heat recovery commissioning",
			Body:    "We need commissioning in September. Can you confirm the window?",
			Intent:  "Confirm the delivery window",
		},
		Voice: &replyDraftVoiceArtifact{
			PersonalityMD:  "Blunt, never hedges.",
			VoiceProfileMD: "# Voice DNA\n\n## How you think\n\nVerdict first.",
			Exemplars:      []ai.VoiceExemplar{{Register: "email", Kind: "email", Text: "We ship Monday."}},
			Stats:          ai.VoiceStats{MeanSentenceWords: 9, EmDashPer100Words: 0},
			ProfileVersion: 3,
		},
	}
}

// replyDraftPlainFixture is the same activity in a workspace that has no
// profile yet. Same site, other system variant.
func replyDraftPlainFixture() replyDraftFixture {
	fixture := replyDraftVoicedFixture()
	fixture.Voice = nil
	return fixture
}

// replyDraftScript answers with the canned replies a run is about, in order;
// extra calls repeat the last. It is a plain completer rather than a validating
// brain because the case sends its requests bare: production wraps the same
// request in the shape-retry when the brain supports one, and a case that
// retried would certify the answer a model gives after being told to try again.
func replyDraftScript(replies []string) *sequencedBrainStub {
	responses := make([]model.Response, 0, len(replies))
	for _, reply := range replies {
		responses = append(responses, model.Response{Text: reply})
	}
	return &sequencedBrainStub{responses: responses}
}

func replyDraftFixtureJSON(t *testing.T, f replyDraftFixture) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return raw
}

func replyDraftExpectationJSON(t *testing.T, register string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(register)
	if err != nil {
		t.Fatalf("encoding the expectation: %v", err)
	}
	return raw
}

func runReplyDraftCase(
	t *testing.T, fixture replyDraftFixture, register string, replies []string,
) (aitasks.Outcome, aitasks.Trace) {
	t.Helper()
	prepared, err := replyDraftCases{}.Prepare(
		replyDraftFixtureJSON(t, fixture), replyDraftExpectationJSON(t, register))
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	trace, err := prepared.Run(context.Background(), replyDraftScript(replies))
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	return prepared.Evaluate(trace), trace
}

// replyDraftOutcomeCase is one scripted conversation with the model and the
// verdict the case owes it.
type replyDraftOutcomeCase struct {
	name       string
	fixture    replyDraftFixture
	register   string
	replies    []string
	wantResult string
	wantDetail string
}

func runReplyDraftOutcomeCases(t *testing.T, cases []replyDraftOutcomeCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, _ := runReplyDraftCase(t, tc.fixture, tc.register, tc.replies)
			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if !strings.Contains(outcome.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to name %q", outcome.Detail, tc.wantDetail)
			}
		})
	}
}

// A refused draft is reported in the drafter's own words, because those words
// are what turns a reliability drop into a diagnosis. Every one of these is a
// draft the product would not put in front of a human — the opposite fix from a
// draft written in the wrong register, which is why the two are never one
// number.
func TestReplyDraftCaseReportsWhatTheDrafterRefused(t *testing.T) {
	runReplyDraftOutcomeCases(t, []replyDraftOutcomeCase{
		{
			name:       "a reply that is not the required JSON",
			fixture:    replyDraftPlainFixture(),
			register:   replyDraftRegisterPlain,
			replies:    []string{"I have drafted the reply for you."},
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "reply draft response is not valid JSON",
		},
		{
			// A subject carrying a line break is a forged header the moment the
			// draft is sent, which is why the drafter refuses it rather than
			// trimming it.
			name:       "a subject that would forge a header",
			fixture:    replyDraftPlainFixture(),
			register:   replyDraftRegisterPlain,
			replies:    []string{`{"subject":"Re: plan\nBcc: audit@example.test","body":"September works."}`},
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "subject contains a line break",
		},
		{
			name:       "a draft with nothing in it",
			fixture:    replyDraftPlainFixture(),
			register:   replyDraftRegisterPlain,
			replies:    []string{`{"subject":"Re: plan","body":"   "}`},
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "body is empty",
		},
		{
			// Every attempt trips the floor, the retry cannot clean it and the
			// plain fallback is no better: a workspace that asked for its own
			// voice got nothing the product promises, in three calls.
			name:     "a voiced call with no clean draft in it",
			fixture:  replyDraftVoicedFixture(),
			register: replyDraftRegisterVoiced,
			replies: []string{
				replyDraftViolatingReply, replyDraftViolatingReply, replyDraftViolatingReply,
			},
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "anti-AI floor",
		},
	})
}

// The other half: drafts the product would serve, judged against the register
// the scenario says this workspace should have been answered in. A wrong
// register here is a measurement of the model, not a defect in the draft.
func TestReplyDraftCaseSeparatesTheRightRegisterFromAServableWrongOne(t *testing.T) {
	runReplyDraftOutcomeCases(t, []replyDraftOutcomeCase{
		{
			name:       "the voiced draft the profile asked for",
			fixture:    replyDraftVoicedFixture(),
			register:   replyDraftRegisterVoiced,
			replies:    []string{replyDraftCleanReply},
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			name:       "a voiced draft the critic retry cleaned",
			fixture:    replyDraftVoicedFixture(),
			register:   replyDraftRegisterVoiced,
			replies:    []string{replyDraftViolatingReply, replyDraftCleanReply},
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			// Servable, clean and not what the workspace asked for: the profile
			// was loaded and the product still had to serve the plain draft.
			name:       "the plain fallback, where the scenario expects the voice",
			fixture:    replyDraftVoicedFixture(),
			register:   replyDraftRegisterVoiced,
			replies:    []string{replyDraftViolatingReply, replyDraftViolatingReply, replyDraftCleanReply},
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: `served the plain draft where the scenario expects "voiced"`,
		},
		{
			name:       "a workspace with no Voice DNA state",
			fixture:    replyDraftPlainFixture(),
			register:   replyDraftRegisterPlain,
			replies:    []string{replyDraftCleanReply},
			wantResult: aitasks.OutcomeAccepted,
		},
	})
}

// Trace.Requests is a slice because this site's drafter issues up to three
// calls, and each one is a different prompt: the profile block, the block plus
// the critic's list of what the last attempt broke, and the plain prompt with no
// profile in it at all. A case that recorded only the first would publish a
// prompt the served draft never came from.
func TestReplyDraftCaseRecordsEveryRequestTheDrafterIssued(t *testing.T) {
	fixture := replyDraftVoicedFixture()

	_, trace := runReplyDraftCase(t, fixture, replyDraftRegisterVoiced,
		[]string{replyDraftViolatingReply, replyDraftViolatingReply, replyDraftCleanReply})

	if len(trace.Requests) != 3 {
		t.Fatalf("the trace carries %d requests, want the voiced attempt, the critic retry and the plain fallback",
			len(trace.Requests))
	}
	voiced, retry, fallback := trace.Requests[0], trace.Requests[1], trace.Requests[2]
	if !strings.HasPrefix(voiced.System, replyDraftVoiceSystem) {
		t.Errorf("the first request is not in the voice variant: %q", voiced.System)
	}
	for _, fragment := range []string{"Voice profile:", "Blunt, never hedges.", "We ship Monday."} {
		if !strings.Contains(voiced.Messages[0].Content, fragment) {
			t.Errorf("the voiced request misses %q", fragment)
		}
	}
	if !strings.Contains(retry.Messages[0].Content, "violated these hard rules") {
		t.Error("the retry does not tell the model what the last attempt broke")
	}
	if !strings.HasPrefix(fallback.System, replyDraftSystem) {
		t.Errorf("the fallback is not in the plain variant: %q", fallback.System)
	}
	if strings.Contains(fallback.Messages[0].Content, "Voice profile:") {
		t.Error("the plain fallback still carries the voice block")
	}
	// The activity is the counterparty's own text, and it belongs on the data
	// side of every one of these calls — including the two the product issues to
	// itself.
	for i, req := range trace.Requests {
		if strings.Contains(req.System, "commissioning in September") {
			t.Errorf("request %d put the activity in the instruction frame: %q", i, req.System)
		}
		if !strings.Contains(req.Messages[0].Content, "commissioning in September") {
			t.Errorf("request %d never carried the activity: %q", i, req.Messages[0].Content)
		}
	}
	if trace.Output != replyDraftCleanReply {
		t.Errorf("the trace records %q, want the reply the served draft was read from", trace.Output)
	}
}

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

// The claim this case makes is that it certifies the shipped path. The proof is
// running the shipped path beside it: the same fixture, answered by the same
// scripted model, must issue the same requests in the same order and reach the
// same verdict in the drafter's real method as in the case.
//
// Production is driven from completeVoiced rather than from
// DraftEmailWithProvenance because everything above it is a database read — the
// activity row and the voice profile — and neither reaches a prompt. What that
// leaves uncrossed is the voice state itself, which production loads from the
// voice store; Prepare is held to the artifact instead, by re-reading the block
// it built through ai.VersionExemplars.
func TestReplyDraftCaseRunsWhatProductionRuns(t *testing.T) {
	cases := []replyDraftParityCase{
		{
			name: "a voiced draft the profile accepts", fixture: replyDraftVoicedFixture(),
			register: replyDraftRegisterVoiced, replies: []string{replyDraftCleanReply},
			wantResult: aitasks.OutcomeAccepted, wantVoiced: true,
		},
		{
			name: "a voiced draft the critic retry cleans", fixture: replyDraftVoicedFixture(),
			register:   replyDraftRegisterVoiced,
			replies:    []string{replyDraftViolatingReply, replyDraftCleanReply},
			wantResult: aitasks.OutcomeAccepted, wantVoiced: true,
		},
		{
			name: "a voiced call that falls back to the plain draft", fixture: replyDraftVoicedFixture(),
			register: replyDraftRegisterVoiced,
			replies: []string{
				replyDraftViolatingReply, replyDraftViolatingReply, replyDraftCleanReply,
			},
			wantResult: aitasks.OutcomeWrongAnswer, wantVoiced: false,
		},
		{
			name: "a workspace with no Voice DNA state", fixture: replyDraftPlainFixture(),
			register: replyDraftRegisterPlain, replies: []string{replyDraftCleanReply},
			wantResult: aitasks.OutcomeAccepted, wantVoiced: false,
		},
		{
			name: "a reply the drafter refuses", fixture: replyDraftPlainFixture(),
			register: replyDraftRegisterPlain, replies: []string{"I have drafted the reply for you."},
			wantResult: aitasks.OutcomeInvalid, wantVoiced: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertReplyDraftMatchesProduction(t, tc)
		})
	}
}

// replyDraftParityCase is one scripted conversation run twice — once through the
// drafter's own method, once through the case — and what the two must agree on.
type replyDraftParityCase struct {
	name       string
	fixture    replyDraftFixture
	register   string
	replies    []string
	wantResult string
	// wantVoiced is whether production stamped a voice profile version, which it
	// does exactly when it served the voice-styled draft. The case reads the same
	// fact off the last request it recorded, and the two must agree or the
	// register the record reports is the case's own invention.
	wantVoiced bool
}

func assertReplyDraftMatchesProduction(t *testing.T, tc replyDraftParityCase) {
	t.Helper()
	voice, err := replyDraftVoiceState(tc.fixture.Voice)
	if err != nil {
		t.Fatalf("reading the fixture's voice state: %v", err)
	}
	script := replyDraftScript(tc.replies)
	draft, version, _, productionErr := replyDrafter{brain: script}.completeVoiced(
		context.Background(), ids.NewV7(), tc.fixture.Activity, voice)

	outcome, trace := runReplyDraftCase(t, tc.fixture, tc.register, tc.replies)

	if len(script.requests) != len(trace.Requests) {
		t.Fatalf("production issued %d requests, the case recorded %d",
			len(script.requests), len(trace.Requests))
	}
	for i, req := range script.requests {
		assertSameCompanyReadRequest(t, req, trace.Requests[i])
	}
	if outcome.Result != tc.wantResult {
		t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
	}
	if productionErr != nil && outcome.Detail != productionErr.Error() {
		t.Errorf("the case reports %q, want the drafter's own words %q", outcome.Detail, productionErr)
	}
	if productionErr == nil && draft.Body == "" {
		t.Error("production served a draft with no body, which it must never do")
	}
	register, err := replyDraftServedRegister(trace)
	if err != nil {
		t.Fatalf("reading the served register: %v", err)
	}
	if voiced := register == replyDraftRegisterVoiced; voiced != tc.wantVoiced {
		t.Errorf("the case read the register %q, want voiced = %v", register, tc.wantVoiced)
	}
	if (version != nil) != tc.wantVoiced {
		t.Errorf("production stamped version %v, want a voice stamp = %v", version, tc.wantVoiced)
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
