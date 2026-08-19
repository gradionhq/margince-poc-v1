// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// THE TWO MODES, which are the consent boundary of this unit: a member either captures
// everything except the people they leave out, or only the people they choose.
// Inverting either arm is the worst defect this code can have — it is not a bug that
// loses a message, it is one that captures a conversation somebody said no to — so the
// tests here are whole ticks rather than assertions against a predicate, and most of
// them assert an ABSENCE.
//
// THE TWO ARMS ARE NOT MIRROR IMAGES, and that asymmetry is the mode-switch decision
// this file also pins. Under only_chosen the member NAMED the conversation, so its
// queued history is what they asked for and there is no floor. Under everyone_except
// they named nobody, so a conversation nothing has ever been captured from starts at
// the moment the mode was chosen — otherwise switching modes would silently sweep in
// every conversation the member had already looked at and decided against.

import (
	"context"
	"testing"
	"time"
)

// unchosenCounterparty is somebody the member has not decided about, whose message
// sits UNDER an allowed one in the drain.
const unchosenCounterparty = "1900000000000000042"

// unchosenMsgID is BELOW inboundMsgID on purpose. That single fact is the whole
// interleaving case: a per-member cursor is a maximum, so landing the higher id
// buries this one.
const unchosenMsgID = "8161098001400"

// twoConversations is a drain holding one message from a conversation the member has
// not chosen and one from a conversation they have, with the UNCHOSEN one carrying
// the LOWER message id. Both are built from the real captured inbound frame, so only
// the counterparty and the id differ from something Zalo actually sent.
func twoConversations(t *testing.T) []zaloInbound {
	t.Helper()
	_, base := capturedFrames(t)
	unchosen := base
	unchosen.UIDFrom, unchosen.MsgID = unchosenCounterparty, unchosenMsgID
	unchosen.Content = "the message a per-member cursor used to bury"
	return []zaloInbound{unchosen, base}
}

// THE DEFECT TWO REVIEWS FOUND, and the case the old fixture could not express: a
// drain with a blocked conversation UNDER an allowed one.
//
// The suppression happens BEFORE Ingest, so capture's natural-key idempotency never
// gets a chance to save the buried message — it is not landed-twice-and-deduped, it
// is never ingested at all, and it is gone permanently once it ages out of Zalo's
// queue. Which is silent data loss on the exact workflow the design promises: "I
// forgot to include Bob, let me allow him now."
//
// TWO TICKS, because one tick cannot state the promise. The first lands the allowed
// conversation; the second, after the member allows the other one, has to still let
// the older message through.
//
// This is the only_chosen form. TestExcludingAConversationLaterStopsCapturingIt is its
// everyone_except sibling, where the member's act is the opposite one.
func TestAllowingAConversationLaterStillGetsWhatZaloIsHolding(t *testing.T) {
	t.Parallel()

	// TICK ONE. Only the higher-numbered conversation is allowed.
	first := tickRuntime(t)
	scriptTurn(first, oneMember(), chosen(), nil)
	first.tx.singleRows = afterTheDrain(stillArmed(), stillArmed())
	drain := &fakeInbox{uid: memberZaloUID, frames: twoConversations(t)}
	if err := pollFleet(context.Background(), first,
		newProvider(map[string]*fakeInbox{memberIMEI: drain}).open()); err != nil {
		t.Fatalf("the first tick failed: %v", err)
	}
	if len(first.ingested) != 1 || first.ingested[0].Key != memberZaloUID+":"+inboundMsgID {
		t.Fatalf("the first tick landed %v; only the chosen conversation may land", keysOf(first.ingested))
	}
	// THE BOOKMARK IS THE CHOSEN CONVERSATION'S ALONE. A single per-member cursor
	// would have been set to inboundMsgID here, which is above unchosenMsgID — and
	// that is exactly what buried the other conversation.
	moved := cursorsWritten(t, first)
	if len(moved) != 1 || moved[counterpartyZaloUID] != inboundMsgID {
		t.Fatalf("the first tick moved bookmarks %v; only the chosen conversation's may move", moved)
	}
	if _, buried := moved[unchosenCounterparty]; buried {
		t.Fatalf("a bookmark was written for a conversation the member had not chosen: %v", moved)
	}

	// TICK TWO. The member allows the other conversation. Zalo is still holding its
	// message, and it MUST land.
	second := tickRuntime(t)
	scriptTurn(second, oneMember(), [][]any{
		allowRow(entryID, counterpartyZaloUID, verdictAllow, "Chosen"),
		allowRow(secondEntryID, unchosenCounterparty, verdictAllow, "Bob"),
	}, nil)
	scriptCursors(second, cursorRow(counterpartyZaloUID, inboundMsgID))
	second.tx.singleRows = afterTheDrain(stillArmed(), stillArmed())
	again := &fakeInbox{uid: memberZaloUID, frames: twoConversations(t)}
	if err := pollFleet(context.Background(), second,
		newProvider(map[string]*fakeInbox{memberIMEI: again}).open()); err != nil {
		t.Fatalf("the second tick failed: %v", err)
	}
	if len(second.ingested) != 1 {
		t.Fatalf("the second tick landed %v; the newly allowed message and nothing else", keysOf(second.ingested))
	}
	if got := second.ingested[0].Key; got != memberZaloUID+":"+unchosenMsgID {
		t.Fatalf("the newly allowed conversation landed %q; a conversation somebody just chose must not start empty", got)
	}
}

// THE everyone_except SIBLING OF THE C1 TEST, and the mode's own promise: a
// conversation nobody has mentioned IS captured, and one the member later leaves out
// STOPS being captured.
//
// It is the mirror of the only_chosen case rather than a copy of it, because the
// member's act is the opposite one: there they name who comes IN, here they name who
// stays OUT. Both promises live in admitsUnderMode, which is why both are asserted
// through a whole tick rather than against the predicate.
func TestExcludingAConversationLaterStopsCapturingIt(t *testing.T) {
	t.Parallel()
	armed := withMode(connectionRow(statusConnected, memberZaloUID, true), captureEveryoneExcept)

	// TICK ONE. Nobody is on the leave-out list, so BOTH conversations are captured —
	// including the one no verdict has ever mentioned, which under only_chosen would
	// have been dropped. Their frames are after the mode's floor.
	first := tickRuntime(t)
	scriptTurn(first, [][]any{armed}, nil, nil)
	first.tx.singleRows = afterTheDrain(armed, armed)
	drain := &fakeInbox{uid: memberZaloUID, frames: twoConversations(t)}
	if err := pollFleet(context.Background(), first,
		newProvider(map[string]*fakeInbox{memberIMEI: drain}).open()); err != nil {
		t.Fatalf("the first tick failed: %v", err)
	}
	if len(first.ingested) != 2 {
		t.Fatalf("everyone_except captured %v; both conversations are expected", keysOf(first.ingested))
	}
	// A bookmark per conversation, in a mode where neither has a verdict row to hang
	// one on — which is the whole reason the positions live in their own table.
	moved := cursorsWritten(t, first)
	if moved[counterpartyZaloUID] != inboundMsgID || moved[unchosenCounterparty] != unchosenMsgID {
		t.Fatalf("the bookmarks moved to %v", moved)
	}

	// TICK TWO. The member puts one of them on the leave-out list. It must stop being
	// captured, and the other must carry on.
	second := tickRuntime(t)
	scriptTurn(second, [][]any{armed},
		[][]any{allowRow(entryID, unchosenCounterparty, verdictBlock, "Left out")}, nil)
	second.tx.singleRows = afterTheDrain(armed, armed)
	newer := newerInBothConversations(t)
	again := &fakeInbox{uid: memberZaloUID, frames: newer}
	if err := pollFleet(context.Background(), second,
		newProvider(map[string]*fakeInbox{memberIMEI: again}).open()); err != nil {
		t.Fatalf("the second tick failed: %v", err)
	}
	if len(second.ingested) != 1 {
		t.Fatalf("after one exclusion the tick captured %v", keysOf(second.ingested))
	}
	if got := second.ingested[0].Counterparty.ChannelIdentity.ChannelUserID; got != counterpartyZaloUID {
		t.Fatalf("the excluded conversation was captured anyway: %q", got)
	}
}

// newerInBothConversations is a later message in each of the two conversations, above
// the bookmarks the first tick wrote. It is what makes the second tick a test of the
// EXCLUSION rather than of the bookmark.
func newerInBothConversations(t *testing.T) []zaloInbound {
	t.Helper()
	_, base := capturedFrames(t)
	kept, excluded := base, base
	kept.MsgID = "8161098009000"
	excluded.UIDFrom, excluded.MsgID = unchosenCounterparty, "8161098009001"
	return []zaloInbound{kept, excluded}
}

// THE MODE SWITCH, and the call this makes: only_chosen -> everyone_except captures
// from the moment of the switch FORWARD, not back through Zalo's queue.
//
// A member switching to "everyone" has named nobody. Reaching back would sweep in the
// conversations they had looked at and deliberately not picked under the previous
// mode — "the CRM captured my doctor" is the exact outcome this unit exists to
// prevent, and it would happen silently, on a schedule, with no screen showing it.
//
// The cost of the floor is at most a retention window's worth of history for
// conversations nobody has ever mentioned, and only on the tick after the switch. A
// member who wants one of them can name it, and naming it is what removes the floor.
func TestSwitchingToEveryoneExceptCapturesFromTheSwitchForwardAndNotBackwards(t *testing.T) {
	t.Parallel()
	_, base := capturedFrames(t)
	// The mode was chosen AFTER this message arrived: it belongs to the period the
	// member had already decided about under their previous answer.
	switchedAt := base.OccurredAt.Add(time.Minute)
	armed := withModeChosenAt(
		withMode(connectionRow(statusConnected, memberZaloUID, true), captureEveryoneExcept),
		switchedAt)

	rt := tickRuntime(t)
	scriptTurn(rt, [][]any{armed}, nil, nil)
	rt.tx.singleRows = afterTheDrain(armed, armed)
	older := &fakeInbox{uid: memberZaloUID, frames: []zaloInbound{base}}

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: older}).open()); err != nil {
		t.Fatalf("the tick failed: %v", err)
	}
	if len(rt.ingested) != 0 {
		t.Fatalf("switching mode reached back through Zalo's queue and captured %v", keysOf(rt.ingested))
	}

	// And a message that arrives AFTER the switch is captured, in the same
	// conversation — so the floor is a floor and not a block.
	newer := base
	newer.MsgID, newer.OccurredAt = "8161098009100", switchedAt.Add(time.Minute)
	after := tickRuntime(t)
	scriptTurn(after, [][]any{armed}, nil, nil)
	after.tx.singleRows = afterTheDrain(armed, armed)
	if err := pollFleet(context.Background(), after, newProvider(map[string]*fakeInbox{
		memberIMEI: {uid: memberZaloUID, frames: []zaloInbound{newer}},
	}).open()); err != nil {
		t.Fatalf("the second tick failed: %v", err)
	}
	if len(after.ingested) != 1 {
		t.Fatalf("a message sent after the switch was not captured: %v", keysOf(after.ingested))
	}
}

// THE FLOOR IS ONLY FOR CONVERSATIONS NOTHING HAS EVER BEEN CAPTURED FROM. One with a
// bookmark is past that question — capture has been reading it, and where it got to is
// what the bookmark says, not when a mode was chosen.
func TestTheModeFloorDoesNotReachAConversationAlreadyBeingCaptured(t *testing.T) {
	t.Parallel()
	_, base := capturedFrames(t)
	switchedAt := base.OccurredAt.Add(time.Minute)
	armed := withModeChosenAt(
		withMode(connectionRow(statusConnected, memberZaloUID, true), captureEveryoneExcept),
		switchedAt)

	rt := tickRuntime(t)
	scriptTurn(rt, [][]any{armed}, nil, nil)
	// A bookmark BELOW this message: the conversation is already being captured.
	scriptCursors(rt, cursorRow(counterpartyZaloUID, echoMsgID))
	rt.tx.singleRows = afterTheDrain(armed, armed)

	if err := pollFleet(context.Background(), rt, newProvider(map[string]*fakeInbox{
		memberIMEI: {uid: memberZaloUID, frames: []zaloInbound{base}},
	}).open()); err != nil {
		t.Fatalf("the tick failed: %v", err)
	}
	if len(rt.ingested) != 1 {
		t.Fatalf("a conversation already being captured was cut off by the mode floor: %v", keysOf(rt.ingested))
	}
}

// Under only_chosen there is NO floor, which is the asymmetry between the two modes
// stated as a test: the member named this conversation, so its queued history is what
// they asked for.
func TestOnlyChosenHasNoFloorBecauseTheMemberNamedTheConversation(t *testing.T) {
	t.Parallel()
	_, base := capturedFrames(t)
	// The mode was chosen long after the message arrived, and it still lands.
	armed := withModeChosenAt(connectionRow(statusConnected, memberZaloUID, true),
		base.OccurredAt.Add(time.Hour))

	rt := tickRuntime(t)
	scriptTurn(rt, [][]any{armed}, chosen(), nil)
	rt.tx.singleRows = afterTheDrain(armed, armed)

	if err := pollFleet(context.Background(), rt, newProvider(map[string]*fakeInbox{
		memberIMEI: {uid: memberZaloUID, frames: []zaloInbound{base}},
	}).open()); err != nil {
		t.Fatalf("the tick failed: %v", err)
	}
	if len(rt.ingested) != 1 {
		t.Fatalf("a conversation the member named did not get its queued messages: %v", keysOf(rt.ingested))
	}
}

// A member who has NOT answered captures nothing, and no socket is opened for them.
// The database refuses "armed with no mode", so this is defence in depth — but the
// safe direction for an unreachable branch in a consent filter is deny.
func TestAMemberWithNoModeHasNoSocketOpened(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	// Armed, with no mode: a row the database would refuse, scripted to prove the Go
	// does not fall back to capturing everything.
	unanswered := connectionRow(statusConnected, memberZaloUID, true)
	unanswered[6], unanswered[7] = "", nil
	scriptTurn(rt, [][]any{unanswered}, chosen(), nil)
	rt.tx.singleRows = [][]any{unanswered}
	opened := theInbox(t)

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: opened}).open()); err != nil {
		t.Fatalf("a member with no mode is not a failure, and it answered %v", err)
	}
	if opened.drains != 0 || len(rt.ingested) != 0 {
		t.Fatalf("a member who has not answered was drained %d time(s) and captured %v",
			opened.drains, keysOf(rt.ingested))
	}
}

// A BOOKMARK OLDER THAN THE MODE DOES NOT SILENCE THE FLOOR, and the round trip is what
// makes that reachable: everyone_except captures a conversation up to some id,
// only_chosen then refuses everything above it while leaving the bookmark exactly where
// it was, and switching BACK to everyone_except re-stamps the floor. The bookmark still
// exists, so reading its mere presence as "capture has been reading this conversation"
// hands over every message Zalo is still holding above it — from precisely the window
// the member had left that conversation out of.
//
// It is the sibling of TestTheModeFloorDoesNotReachAConversationAlreadyBeingCaptured:
// there the bookmark was written under this mode and rightly answers the question, here
// it predates the mode and cannot.
func TestABookmarkOlderThanTheModeDoesNotSilenceTheFloor(t *testing.T) {
	t.Parallel()
	_, base := capturedFrames(t)
	switchedAt := base.OccurredAt.Add(time.Minute)
	armed := withModeChosenAt(
		withMode(connectionRow(statusConnected, memberZaloUID, true), captureEveryoneExcept),
		switchedAt)

	rt := tickRuntime(t)
	scriptTurn(rt, [][]any{armed}, nil, nil)
	// A bookmark BELOW this message, written BEFORE the switch: where the previous
	// answer got to, and no statement about the answer in force now.
	scriptCursors(rt, cursorRowWrittenAt(counterpartyZaloUID, echoMsgID, switchedAt.Add(-time.Minute)))
	rt.tx.singleRows = afterTheDrain(armed, armed)

	if err := pollFleet(context.Background(), rt, newProvider(map[string]*fakeInbox{
		memberIMEI: {uid: memberZaloUID, frames: []zaloInbound{base}},
	}).open()); err != nil {
		t.Fatalf("the tick failed: %v", err)
	}
	if len(rt.ingested) != 0 {
		t.Fatalf("a bookmark from before the mode was chosen reached back through Zalo's queue and captured %v", keysOf(rt.ingested))
	}
	if moved := cursorsWritten(t, rt); len(moved) != 0 {
		t.Fatalf("a message the floor refused still moved a bookmark to %v", moved)
	}

	// AND THE STALE BOOKMARK IS NOT A BLOCK: a message that arrives after the switch
	// lands in that same conversation, which is what keeps this a floor.
	newer := base
	newer.MsgID, newer.OccurredAt = "8161098009200", switchedAt.Add(time.Minute)
	after := tickRuntime(t)
	scriptTurn(after, [][]any{armed}, nil, nil)
	scriptCursors(after, cursorRowWrittenAt(counterpartyZaloUID, echoMsgID, switchedAt.Add(-time.Minute)))
	after.tx.singleRows = afterTheDrain(armed, armed)
	if err := pollFleet(context.Background(), after, newProvider(map[string]*fakeInbox{
		memberIMEI: {uid: memberZaloUID, frames: []zaloInbound{newer}},
	}).open()); err != nil {
		t.Fatalf("the second tick failed: %v", err)
	}
	if len(after.ingested) != 1 {
		t.Fatalf("a message sent after the switch was not captured: %v", keysOf(after.ingested))
	}
}
