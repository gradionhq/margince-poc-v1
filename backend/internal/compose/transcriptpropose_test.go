// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/schema"
)

// meetingLines is a short transcript with one plain commitment on line 3.
func meetingLines() []string {
	return []string{
		"Dana: Thanks for walking us through the rollout plan.",
		"Priya: Any concerns from the security side?",
		"Priya: I'll send the revised pricing over by Friday.",
		"Dana: Perfect, we'll review it then.",
	}
}

func payloadOf(t *testing.T, steps ...proposedStep) transcriptPayload {
	t.Helper()
	list := steps
	return transcriptPayload{Proposals: &list}
}

func TestAReadingIsRefusedWhenItCitesALineTheTranscriptDoesNotHave(t *testing.T) {
	lines := meetingLines()
	for _, cited := range []int{0, -1, len(lines) + 1} {
		payload := payloadOf(t, proposedStep{
			Summary: "Send the revised pricing", Owner: "Priya",
			SourceLines: []int{cited}, Confidence: 0.9,
		})
		msg := validateTranscriptPayload(payload, len(lines))
		if msg == "" {
			t.Fatalf("citing line %d of a %d-line transcript must be refused; an uncheckable citation reads as corroboration",
				cited, len(lines))
		}
		if !strings.Contains(msg, "lines 1 to") {
			t.Errorf("the refusal must name the range that does exist, got %q", msg)
		}
	}
}

func TestAReadingIsRefusedWhenItProposesSomethingItCannotPointAt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		step    proposedStep
		wantMsg string
	}{
		{
			name:    "no lines cited at all",
			step:    proposedStep{Summary: "Send pricing", Owner: "Priya", Confidence: 0.9},
			wantMsg: "cites no line",
		},
		{
			name: "more lines than a located claim needs",
			step: proposedStep{
				Summary: "Send pricing", Owner: "Priya", Confidence: 0.9,
				SourceLines: []int{1, 1, 1, 1, 1, 1, 1},
			},
			wantMsg: "at most",
		},
		{
			name:    "no summary",
			step:    proposedStep{Owner: "Priya", SourceLines: []int{3}, Confidence: 0.9},
			wantMsg: "no summary",
		},
		{
			name:    "no owner",
			step:    proposedStep{Summary: "Send pricing", SourceLines: []int{3}, Confidence: 0.9},
			wantMsg: "no owner",
		},
		{
			name: "confidence outside the unit interval",
			step: proposedStep{
				Summary: "Send pricing", Owner: "Priya", SourceLines: []int{3}, Confidence: 1.5,
			},
			wantMsg: "outside [0,1]",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := validateTranscriptPayload(payloadOf(t, tc.step), len(meetingLines()))
			if msg == "" {
				t.Fatal("want a refusal")
			}
			if !strings.Contains(msg, tc.wantMsg) {
				t.Errorf("want the refusal to contain %q, got %q", tc.wantMsg, msg)
			}
		})
	}
}

func TestATranscriptThatStatesNothingIsAnAnswerAndAMissingKeyIsNot(t *testing.T) {
	empty := payloadOf(t)
	if msg := validateTranscriptPayload(empty, 4); msg != "" {
		t.Errorf("an empty list is the CORRECT answer for a transcript with no commitments, got refusal %q", msg)
	}
	if msg := validateTranscriptPayload(transcriptPayload{}, 4); msg == "" {
		t.Error("a reply with no proposals key did not answer the question and must be refused, not read as empty")
	}
}

func TestAReadingIsRefusedWhenItProposesMoreThanAMeetingCanHold(t *testing.T) {
	steps := make([]proposedStep, 0, maxTranscriptProposals+1)
	for range maxTranscriptProposals + 1 {
		steps = append(steps, proposedStep{
			Summary: "Send pricing", Owner: "Priya", SourceLines: []int{3}, Confidence: 0.9,
		})
	}
	msg := validateTranscriptPayload(payloadOf(t, steps...), len(meetingLines()))
	if !strings.Contains(msg, "at most") {
		t.Errorf("over the cap must be refused with the cap named, got %q", msg)
	}
}

func TestTheConfidenceFloorDropsAnUnsureReadingRatherThanRefusingIt(t *testing.T) {
	sure := proposedStep{Summary: "Send pricing", Owner: "Priya", SourceLines: []int{3}, Confidence: transcriptConfidenceFloor}
	unsure := proposedStep{Summary: "Maybe review", Owner: "Dana", SourceLines: []int{4}, Confidence: transcriptConfidenceFloor - 0.01}

	// Both are VALID: the floor is a question about whether a well-formed
	// reading earns a human's attention, not about whether it may be read.
	if msg := validateTranscriptPayload(payloadOf(t, sure, unsure), len(meetingLines())); msg != "" {
		t.Fatalf("an unsure but well-formed reading is valid, got refusal %q", msg)
	}
	kept := aboveFloor([]proposedStep{sure, unsure})
	if len(kept) != 1 || kept[0].Summary != sure.Summary {
		t.Errorf("only the reading at or above the floor is staged, got %+v", kept)
	}
}

func TestEvidenceQuotesTheTranscriptRatherThanTheModelsParaphrase(t *testing.T) {
	lines := meetingLines()
	activityID := ids.New[ids.ActivityKind]()
	evidence := stepEvidence(proposedStep{
		Summary: "Priya will get pricing across", Owner: "Priya", SourceLines: []int{3}, Confidence: 0.9,
	}, lines, activityID)

	if evidence.Snippet != lines[2] {
		t.Errorf("the snippet must be the transcript's own line %q so the human checks the text, not the summary; got %q",
			lines[2], evidence.Snippet)
	}
	if evidence.SourceType != "activity" || evidence.SourceID != activityID.UUID {
		t.Errorf("evidence must point back at the transcript activity, got %s/%s", evidence.SourceType, evidence.SourceID)
	}
	if len(evidence.SourceLines) != 1 || evidence.SourceLines[0] != 3 {
		t.Errorf("the cited line numbers must survive onto the evidence, got %v", evidence.SourceLines)
	}
}

func TestEvidenceTrimsAQuotationLongerThanTheApprovalsCapAccepts(t *testing.T) {
	long := strings.Repeat("y", maxEvidenceSnippetChars*2)
	evidence := stepEvidence(
		proposedStep{Summary: "s", Owner: "o", SourceLines: []int{1}, Confidence: 1},
		[]string{long}, ids.New[ids.ActivityKind]())
	if len(evidence.Snippet) > maxEvidenceSnippetChars {
		t.Errorf("a long cited line must be trimmed here rather than refused at staging, got %d bytes", len(evidence.Snippet))
	}
}

func TestTheRequestNumbersEveryLineAndFencesTheTranscript(t *testing.T) {
	req := transcriptRequest(meetingLines())
	if len(req.Messages) != 1 {
		t.Fatalf("want one user message, got %d", len(req.Messages))
	}
	prompt := req.Messages[0].Content
	for i, line := range meetingLines() {
		if !strings.Contains(prompt, strconv.Itoa(i+1)) || !strings.Contains(prompt, line) {
			t.Errorf("line %d must appear in the prompt with its number, or a citation cannot mean anything", i+1)
		}
	}
	if !strings.Contains(prompt, "between 1 and 4") {
		t.Error("the prompt must state the range of lines that exist, so an out-of-range citation is a refusal and not a surprise")
	}
	if req.System == "" {
		t.Error("the system prompt carries the fence rule; without it the transcript is not marked untrusted")
	}
	if req.ResponseSchema == nil {
		t.Error("the request must carry the generation-time shape guardrail")
	}
}

func TestTheValidatorRefusesOutputThatIsNotTheRequiredShape(t *testing.T) {
	validate := transcriptShapeValid(len(meetingLines()))
	if err := validate("not json at all"); err == nil {
		t.Error("unparseable output must be refused")
	}
	good, err := json.Marshal(map[string]any{"proposals": []map[string]any{{
		"summary": "Send the revised pricing", "owner": "Priya",
		"source_lines": []int{3}, "confidence": 0.9,
	}}})
	if err != nil {
		t.Fatalf("building the fixture reply: %v", err)
	}
	if err := validate(string(good)); err != nil {
		t.Errorf("a grounded, well-formed reading must pass, got %v", err)
	}
}

func TestATaskBodySaysWhoPromisedItAndWhereToCheck(t *testing.T) {
	one := transcriptTaskBody(TranscriptStepProposal{Owner: "Priya", SourceLines: []int{3}})
	if !strings.Contains(one, "Priya") || !strings.Contains(one, "line 3") {
		t.Errorf("a single-line citation reads as %q; want the owner and 'line 3'", one)
	}
	many := transcriptTaskBody(TranscriptStepProposal{Owner: "Dana", SourceLines: []int{3, 4}})
	if !strings.Contains(many, "lines 3, 4") {
		t.Errorf("a multi-line citation must pluralize and list them, got %q", many)
	}
}

// The floor checks above all rest on this: schema.Confidence orders against a
// plain float constant.
func TestConfidenceComparesAgainstTheFloorConstant(t *testing.T) {
	var c schema.Confidence = 0.75
	if !(c >= transcriptConfidenceFloor) {
		t.Errorf("%v must compare at or above the floor %v", c, transcriptConfidenceFloor)
	}
}
