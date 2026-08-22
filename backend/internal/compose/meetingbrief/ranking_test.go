// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package meetingbrief

import (
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// The same objection used to appear in deal state, risks AND talking points,
// worded three ways. Now a claim has one home: the first section in the
// spec's order that wants it.
func TestNoClaimIsSaidTwiceAcrossTheBrief(t *testing.T) {
	in := fullInput()
	in.Commitments = append(in.Commitments,
		ClaimIn{PersonName: "Ana Roth", Kind: kindObjection, Body: "the cure period is too short", Status: statusOpen, SourceID: activityID},
		ClaimIn{PersonName: "Ana Roth", Kind: kindPriority, Body: "go-live before Q4", Status: statusOpen, SourceID: activityID},
		ClaimIn{PersonName: "Ana Roth", Kind: kindDecision, Body: "pilot on two sites first", Status: "done", SourceID: activityID},
	)
	seen := map[string]crmcontracts.MeetingBriefSectionKind{}
	for _, section := range Deterministic(in) {
		for _, line := range section.Sentences {
			// The commitments ledger is complete by design and may repeat what
			// the goal or a risk reads out of it.
			if section.Kind == crmcontracts.MeetingBriefSectionKindCommitments {
				continue
			}
			for _, body := range []string{"cure period", "go-live before Q4", "pilot on two sites", "security pack"} {
				if !contains(line.Text, body) {
					continue
				}
				if home, dup := seen[body]; dup {
					t.Errorf("%q is said in %s and again in %s", body, home, section.Kind)
				}
				seen[body] = section.Kind
			}
		}
	}
	if seen["cure period"] != crmcontracts.MeetingBriefSectionKindRisks {
		t.Errorf("an open objection's home is risks, got %s", seen["cure period"])
	}
	if seen["pilot on two sites"] != crmcontracts.MeetingBriefSectionKindDealState {
		t.Errorf("a settled decision's home is deal state, got %s", seen["pilot on two sites"])
	}
	if seen["go-live before Q4"] != crmcontracts.MeetingBriefSectionKindTalkingPoints {
		t.Errorf("a priority nobody else took is a talking point, got %s", seen["go-live before Q4"])
	}
}

// Sharpest first: an overdue promise of ours outranks an open question, which
// outranks a promise still inside its date; a settled claim trails every open one.
func TestClaimsRankByWhatTheRecordSays(t *testing.T) {
	in := fullInput()
	in.Commitments = []ClaimIn{
		{Kind: kindDecision, Body: "settled", Status: "done"},
		{Kind: kindCommitmentOurs, Body: "inside its date", Status: statusOpen, DueAt: ptr(at(18))},
		{Kind: kindOpenQuestion, Body: "a question", Status: statusOpen},
		{Kind: kindCommitmentOurs, Body: "overdue", Status: statusOpen, DueAt: ptr(at(8))},
	}
	ranked := rankClaims(in)
	want := []string{"overdue", "a question", "inside its date", "settled"}
	for i, body := range want {
		if ranked.claims[i].Body != body {
			t.Fatalf("rank %d = %q, want %q (order %v)", i, ranked.claims[i].Body, body, bodies(ranked.claims))
		}
	}
}

// A talking point is a move, not a label: evidence plus what to do with it.
func TestATalkingPointSaysWhatToDoInTheRoom(t *testing.T) {
	in := fullInput()
	in.Commitments = []ClaimIn{{PersonName: "Ana Roth", Kind: kindDecisionProcess, Body: "legal reviews after the CFO signs off", Status: statusOpen, SourceID: activityID}}
	points := sectionOf(t, Deterministic(in), crmcontracts.MeetingBriefSectionKindTalkingPoints)
	if len(points.Sentences) != 1 || !contains(points.Sentences[0].Text, "walk the next step of it in the room") {
		t.Fatalf("talking points = %+v, want the move", points.Sentences)
	}
}

func contains(text, part string) bool { return len(part) > 0 && indexOf(text, part) >= 0 }

func indexOf(text, part string) int {
	for i := 0; i+len(part) <= len(text); i++ {
		if text[i:i+len(part)] == part {
			return i
		}
	}
	return -1
}

func bodies(claims []ClaimIn) []string {
	out := make([]string, 0, len(claims))
	for _, c := range claims {
		out = append(out, c.Body)
	}
	return out
}

// Later is sharper: a promise a month overdue outranks one a day overdue.
func TestAnOlderOverduePromiseOutranksANewerOne(t *testing.T) {
	in := fullInput()
	in.Commitments = []ClaimIn{
		{Kind: kindCommitmentOurs, Body: "a day late", Status: statusOpen, DueAt: ptr(at(9))},
		{Kind: kindCommitmentOurs, Body: "a month late", Status: statusOpen, DueAt: ptr(at(-20))},
	}
	ranked := rankClaims(in)
	if ranked.claims[0].Body != "a month late" {
		t.Fatalf("rank = %v, want the later promise first", bodies(ranked.claims))
	}
}
