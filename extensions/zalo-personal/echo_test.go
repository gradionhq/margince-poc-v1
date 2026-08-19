// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The echo, which is the hardest thing this unit has to get right and the reason two
// of its three tables exist.
//
// Zalo delivers a member's OWN outgoing messages back to their own socket as ordinary
// inbound frames carrying the same msgId the send returned. Two different things
// arrive that way: a reply the CRM staged, which is already on the timeline and must
// not land twice, and a reply the rep typed in the Zalo app on their phone, which
// nothing here has ever seen and which is the half of every conversation the timeline
// was missing. Only the send markers separate them.
//
// AND THE TRAP: on an outgoing frame `dName` is the MEMBER's own display name. Passing
// it through binds the rep's name to the customer's channel identity in the core,
// after which every future message from that customer arrives under the rep's name.
// The tests below assert on the real value from the captured frame, so a regression
// that passes it through fails loudly rather than quietly corrupting a person record.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// TEST 1 OF THE FOUR THAT MATTER. The echo of a reply the CRM itself staged must
// reach capture NOT AT ALL. The core already wrote that activity, so a second copy
// puts the rep's own words on the customer's timeline twice — and attributed to
// whichever direction the second row happened to carry.
func TestTheEchoOfAReplyTheCRMStagedIsNeverCapturedAgain(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, oneMember(), chosen(), oursAlready(echoMsgID))
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}
	opened := theInbox(t)

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: opened}).open()); err != nil {
		t.Fatalf("the tick failed: %v", err)
	}
	if len(rt.ingested) != 1 {
		t.Fatalf("capture was handed %d record(s); the CRM's own reply must not land a second time: %+v",
			len(rt.ingested), keysOf(rt.ingested))
	}
	if got := rt.ingested[0].Key; got != memberZaloUID+":"+inboundMsgID {
		t.Fatalf("the record that landed is %q; the id the CRM sent is %q", got, echoMsgID)
	}
	if rt.ingestedOn[0] != extension.UserID(callerUserID) {
		t.Fatalf("the record landed on %q rather than on the member whose credential produced it", rt.ingestedOn[0])
	}
	// The marker read asks about the ECHOED id only: an inbound message was never
	// sent by this CRM, so asking about it would widen the query for no answer.
	sql, args := rt.tx.statementMentioning(t, "provider_message_id = ANY")
	ids, ok := args[1].([]string)
	if !ok || len(ids) != 1 || ids[0] != echoMsgID {
		t.Fatalf("the marker read asked about %v:\n%s", args[1], sql)
	}
	// The bookmark is moved for the ONE conversation that landed something, to the
	// ONE id that landed, after the ingest.
	if moved := cursorsWritten(t, rt); len(moved) != 1 || moved[counterpartyZaloUID] != inboundMsgID {
		t.Fatalf("the bookmarks moved to %v", moved)
	}
}

// keysOf names what landed, so a count mismatch says WHICH records were handed
// over rather than only how many.
func keysOf(records []extension.Record) []string {
	keys := make([]string, 0, len(records))
	for _, rec := range records {
		keys = append(keys, rec.Key+" "+rec.Activity.Direction)
	}
	return keys
}

// TEST 2 OF THE FOUR, AND THE SHARPEST. A reply the rep typed in the Zalo app on
// their phone arrives as the same shape of echo with NO marker, and it is the half
// of the conversation the timeline was missing — for exactly the usage this unit's
// own consent copy recommends.
//
// THE TRAP IS THE DISPLAY NAME. On an outgoing frame `dName` is the MEMBER's own
// name, and passing it through would bind the rep's name to the customer's channel
// identity in the core — after which every future message from that customer
// arrives under the rep's name. The assertion below names the real value from the
// captured frame, so a regression that passes it through fails loudly.
func TestAReplySentFromTheRepsPhoneIsCapturedAsTheirsWithoutStealingTheirName(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	// No markers: this echo is not one the CRM staged.
	scriptTurn(rt, oneMember(), chosen(), nil)
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}
	echo, _ := capturedFrames(t)
	opened := &fakeInbox{uid: memberZaloUID, frames: []zaloInbound{echo}}

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: opened}).open()); err != nil {
		t.Fatalf("the tick failed: %v", err)
	}
	if len(rt.ingested) != 1 {
		t.Fatalf("a reply sent from the phone landed %d record(s): %v", len(rt.ingested), keysOf(rt.ingested))
	}
	rec := rt.ingested[0]
	if rec.Activity.Direction != extension.DirectionOutbound {
		t.Fatalf("the rep's own reply landed as %q", rec.Activity.Direction)
	}
	// The counterparty is idTo. uidFrom on this frame is the literal "0".
	if rec.Counterparty.ChannelIdentity.ChannelUserID != counterpartyZaloUID {
		t.Fatalf("the counterparty resolved to %q; on an outgoing frame it is idTo",
			rec.Counterparty.ChannelIdentity.ChannelUserID)
	}
	// The member's OWN name, verbatim from the capture, must appear nowhere on the
	// counterparty.
	if echo.DName == "" {
		t.Fatal("the captured echo carries no dName, so this test proves nothing")
	}
	if rec.Counterparty.DisplayName == echo.DName || rec.Counterparty.ChannelIdentity.DisplayName == echo.DName {
		t.Fatalf("the member's own name %q was written onto the customer's identity: %+v", echo.DName, rec.Counterparty)
	}
	// What it IS is the name the member's own screen showed when they chose the
	// conversation — the only name this unit honestly has here.
	if rec.Counterparty.DisplayName != "Chosen" {
		t.Fatalf("the counterparty is named %q; the saved verdict says %q", rec.Counterparty.DisplayName, "Chosen")
	}
	if err := rec.Validate(); err != nil {
		t.Fatalf("the outbound record is refused by capture: %v", err)
	}
}

// The same trap where this unit knows NO name for the counterparty, which is the
// case the frame's own dName is most tempting to reach for. An unnamed counterparty
// is honest and the core still resolves them by account; the member's name on the
// customer's identity is a corrupted person record nothing downstream can spot.
func TestAPhoneReplyToSomebodyThisUnitCannotNameIsLeftUnnamed(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, oneMember(), [][]any{allowRow(entryID, counterpartyZaloUID, verdictAllow, "")}, nil)
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}
	echo, _ := capturedFrames(t)
	opened := &fakeInbox{uid: memberZaloUID, frames: []zaloInbound{echo}}

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: opened}).open()); err != nil {
		t.Fatalf("the tick failed: %v", err)
	}
	if len(rt.ingested) != 1 {
		t.Fatalf("a phone reply landed %d record(s): %v", len(rt.ingested), keysOf(rt.ingested))
	}
	party := rt.ingested[0].Counterparty
	if party.DisplayName != "" || party.ChannelIdentity.DisplayName != "" {
		t.Fatalf("an unnameable counterparty was given the name %q; the member's own is %q",
			party.DisplayName, echo.DName)
	}
	if party.ChannelIdentity.ChannelUserID != counterpartyZaloUID {
		t.Fatalf("an unnamed counterparty is still resolved by account, and this one is %q",
			party.ChannelIdentity.ChannelUserID)
	}
}

// TEST 3 OF THE FOUR. The consent story from the side nobody was watching: a rep
// messaging somebody they never allowed must not pull that person into the CRM
// through the outbound door.
func TestAPhoneReplyToSomebodyTheMemberNeverAllowedIsNeverCaptured(t *testing.T) {
	t.Parallel()
	for name, verdicts := range map[string][][]any{
		"a counterparty with no verdict":    {allowRow(entryID, "1900000000000000009", verdictAllow, "Somebody else")},
		"a counterparty the member blocked": {allowRow(entryID, counterpartyZaloUID, verdictBlock, "Ruled out")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt := tickRuntime(t)
			scriptTurn(rt, oneMember(), verdicts, nil)
			rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}
			echo, _ := capturedFrames(t)
			opened := &fakeInbox{uid: memberZaloUID, frames: []zaloInbound{echo}}

			if err := pollFleet(context.Background(), rt,
				newProvider(map[string]*fakeInbox{memberIMEI: opened}).open()); err != nil {
				t.Fatalf("%s is not a failure, and it answered %v", name, err)
			}
			if len(rt.ingested) != 0 {
				t.Fatalf("%s reached capture through the outbound door: %v", name, keysOf(rt.ingested))
			}
		})
	}
}

// TEST 4 OF THE FOUR. Both halves of one conversation on ONE thread key. Two keys
// is two monologues, and neither of them reads as a conversation.
func TestBothHalvesOfOneConversationLandOnOneThread(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, oneMember(), chosen(), nil)
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: theInbox(t)}).open()); err != nil {
		t.Fatalf("the tick failed: %v", err)
	}
	if len(rt.ingested) != 2 {
		t.Fatalf("one exchange landed %d record(s): %v", len(rt.ingested), keysOf(rt.ingested))
	}
	first, second := rt.ingested[0], rt.ingested[1]
	if first.ThreadKey != second.ThreadKey {
		t.Fatalf("the two halves landed on %q and %q", first.ThreadKey, second.ThreadKey)
	}
	if first.ThreadKey != provider+":"+memberZaloUID+":"+counterpartyZaloUID {
		t.Fatalf("the thread key is %q", first.ThreadKey)
	}
	// One of each direction, and the same counterparty on both.
	if first.Activity.Direction == second.Activity.Direction {
		t.Fatalf("both halves landed as %q", first.Activity.Direction)
	}
	if first.Counterparty.ChannelIdentity.ChannelUserID != second.Counterparty.ChannelIdentity.ChannelUserID {
		t.Fatal("the two halves of one conversation name two different people")
	}
}
