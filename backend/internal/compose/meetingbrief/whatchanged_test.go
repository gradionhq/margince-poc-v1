// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package meetingbrief

import (
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// The section names its baseline, lists only what happened after it, and
// takes those claims out of the set so no later section repeats them.
func TestWhatChangedListsOnlyWhatHappenedAfterTheReaderLastSpoke(t *testing.T) {
	in := fullInput()
	in.LastSpokeAt = ptr(at(5))
	in.Commitments = []ClaimIn{
		{PersonName: "Ana Roth", Kind: kindObjection, Body: "the cure period", Status: statusOpen, SourceID: activityID, OccurredAt: ptr(at(7))},
		{PersonName: "Ana Roth", Kind: kindDecision, Body: "pilot first", Status: "done", SourceID: activityID, OccurredAt: ptr(at(2))},
	}
	in.Recent = []ActIn{
		{ID: activityID, Kind: "email", Subject: "Re: redline", Direction: "inbound", At: at(8)},
		{ID: activityID, Kind: "email", Subject: "older", Direction: "outbound", At: at(3)},
	}
	sections := Deterministic(in)
	changed := sectionOf(t, sections, crmcontracts.MeetingBriefSectionKindWhatChanged)
	texts := make([]string, 0, len(changed.Sentences))
	for _, line := range changed.Sentences {
		texts = append(texts, line.Text)
	}
	joined := strings.Join(texts, " | ")
	for _, want := range []string{"You last dealt with this room 5 days ago", "Since then Ana Roth objected: the cure period", "1 conversation since then, the latest \"Re: redline\""} {
		if !strings.Contains(joined, want) {
			t.Errorf("what changed = %q, want it to say %q", joined, want)
		}
	}
	if strings.Contains(joined, "pilot first") {
		t.Errorf("a decision from before the baseline is not a change: %q", joined)
	}
	risks := sectionOf(t, sections, crmcontracts.MeetingBriefSectionKindRisks)
	for _, line := range risks.Sentences {
		if strings.Contains(line.Text, "cure period") {
			t.Errorf("the objection what-changed took is said again as a risk: %q", line.Text)
		}
	}
}

func TestFirstContactIsSaidRatherThanNothingChanged(t *testing.T) {
	in := fullInput()
	in.LastSpokeAt = nil
	changed := sectionOf(t, Deterministic(in), crmcontracts.MeetingBriefSectionKindWhatChanged)
	if len(changed.Sentences) != 1 || !strings.HasPrefix(changed.Sentences[0].Text, "First contact") {
		t.Fatalf("what changed = %+v, want the first-contact line alone", changed.Sentences)
	}
}

func TestAQuietSpellSaysNothingCapturedHasChanged(t *testing.T) {
	in := fullInput()
	in.LastSpokeAt = ptr(at(9))
	in.Commitments = nil
	in.Recent = nil
	changed := sectionOf(t, Deterministic(in), crmcontracts.MeetingBriefSectionKindWhatChanged)
	if len(changed.Sentences) != 1 || !strings.Contains(changed.Sentences[0].Text, "Nothing captured has changed since") {
		t.Fatalf("what changed = %+v, want the baseline line saying nothing changed", changed.Sentences)
	}
}
