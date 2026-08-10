// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relstrength

import (
	"testing"
	"time"
)

// now is a fixed instant. Every case below states its times relative to it, so
// a test reads as "they replied 41 days after we last spoke" rather than as
// arithmetic against whatever the wall clock says.
var now = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

func daysAgo(n int) *time.Time {
	t := now.AddDate(0, 0, -n)
	return &t
}

// kindsOf lists what a derivation found, so a test can assert on the sentences
// rather than on slice positions.
func kindsOf(changes []Change) map[string]Change {
	out := map[string]Change{}
	for _, c := range changes {
		out[c.Kind] = c
	}
	return out
}

// The strongest signal this data can produce with no external source: they
// came back.
func TestChangesReportsAReplyThatEndedALongSilence(t *testing.T) {
	in := ChangeInputs{
		Current:              Inputs{LastInteraction: daysAgo(3), Count90d: 4, Inbound90d: 2, Outbound90d: 2},
		LatestInbound:        daysAgo(3),
		PrecedingInteraction: daysAgo(44),
	}
	got := kindsOf(Changes(in, now))
	c, found := got[ChangeRepliedAfterGap]
	if !found {
		t.Fatal("a reply after 41 quiet days was not reported")
	}
	if c.Days != 41 {
		t.Errorf("Days = %d, want 41 — the gap is measured to the interaction the reply broke", c.Days)
	}
}

// A reply is a normal turn in an ongoing thread until the silence before it is
// long enough to be news. Reporting every reply would make the surface noise.
func TestChangesIgnoresAReplyInAnActiveThread(t *testing.T) {
	in := ChangeInputs{
		Current:              Inputs{LastInteraction: daysAgo(1), Count90d: 12, Inbound90d: 6, Outbound90d: 6},
		LatestInbound:        daysAgo(1),
		PrecedingInteraction: daysAgo(3),
	}
	if _, found := kindsOf(Changes(in, now))[ChangeRepliedAfterGap]; found {
		t.Error("a reply two days after the last message was reported as ending a silence")
	}
}

// Their first message is a first contact, not a return — there is no silence
// behind it to have broken.
func TestChangesDoesNotCallAFirstContactAReturn(t *testing.T) {
	in := ChangeInputs{
		Current:       Inputs{LastInteraction: daysAgo(2), Count90d: 1, Inbound90d: 1},
		LatestInbound: daysAgo(2),
	}
	if _, found := kindsOf(Changes(in, now))[ChangeRepliedAfterGap]; found {
		t.Error("a first inbound message was reported as a reply after a gap")
	}
}

// A reply that itself went quiet afterwards is no longer the headline; the
// silence since is.
func TestChangesDropsAStaleReturnAndReportsTheSilenceSince(t *testing.T) {
	in := ChangeInputs{
		Current:              Inputs{LastInteraction: daysAgo(100), Count90d: 0},
		LatestInbound:        daysAgo(100),
		PrecedingInteraction: daysAgo(200),
	}
	got := kindsOf(Changes(in, now))
	if _, found := got[ChangeRepliedAfterGap]; found {
		t.Error("a reply from 100 days ago was still reported as the news about this relationship")
	}
	quiet, found := got[ChangeWentQuiet]
	if !found {
		t.Fatal("a relationship silent for 100 days was not reported as quiet")
	}
	if quiet.Days != 100 {
		t.Errorf("Days = %d, want 100", quiet.Days)
	}
}

// A contact nobody has ever spoken to has not gone quiet. Saying otherwise
// turns every dormant record into an alert.
func TestChangesSaysNothingAboutARelationshipThatNeverStarted(t *testing.T) {
	if changes := Changes(ChangeInputs{}, now); len(changes) != 0 {
		t.Errorf("a contact with no interactions produced %d changes: %+v", len(changes), changes)
	}
}

// The band move is the point; the point difference is not. The score decays
// continuously, so a surface reporting "73 became 71" would fire on every read.
func TestChangesReportsABandCrossingAndNotADriftWithinOne(t *testing.T) {
	// Busy and recent now, sparse and old a half-life ago: a real move.
	moved := ChangeInputs{
		Current:  Inputs{LastInteraction: daysAgo(1), Count90d: 20, Inbound90d: 10, Outbound90d: 10},
		Previous: Inputs{LastInteraction: daysAgo(40), Count90d: 1, Inbound90d: 1},
	}
	c, found := kindsOf(Changes(moved, now))[ChangeWarmed]
	if !found {
		t.Fatal("a relationship that went from one exchange to twenty was not reported as warming")
	}
	if c.FromBucket == c.ToBucket {
		t.Errorf("a warming change named the same band twice (%q)", c.FromBucket)
	}

	// The same counts on both sides: only decay separates the two scores, and
	// decay alone is not news.
	steady := ChangeInputs{
		Current:  Inputs{LastInteraction: daysAgo(1), Count90d: 20, Inbound90d: 10, Outbound90d: 10},
		Previous: Inputs{LastInteraction: daysAgo(31), Count90d: 20, Inbound90d: 10, Outbound90d: 10},
	}
	for _, c := range Changes(steady, now) {
		if c.Kind == ChangeWarmed || c.Kind == ChangeCooled {
			t.Errorf("a steady relationship was reported as %q (%s → %s)", c.Kind, c.FromBucket, c.ToBucket)
		}
	}
}

// A relationship that stopped is reported as cooling, in that direction.
func TestChangesReportsCoolingWhenTheBandFell(t *testing.T) {
	in := ChangeInputs{
		Current:  Inputs{LastInteraction: daysAgo(60), Count90d: 2, Inbound90d: 1, Outbound90d: 1},
		Previous: Inputs{LastInteraction: daysAgo(31), Count90d: 20, Inbound90d: 10, Outbound90d: 10},
	}
	got := kindsOf(Changes(in, now))
	if _, found := got[ChangeWarmed]; found {
		t.Error("a relationship that fell silent was reported as warming")
	}
	c, found := got[ChangeCooled]
	if !found {
		t.Fatal("a relationship that fell from twenty exchanges to two was not reported as cooling")
	}
	if c.ToBucket == "" {
		t.Error("a cooling change named no band to have fallen to")
	}
}
