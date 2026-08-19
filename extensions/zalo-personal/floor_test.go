// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// THE PER-CONVERSATION CONSENT FLOOR, and the asymmetry that is the whole of it:
// lifting an exclusion does NOT hand over the excluded period, while naming somebody
// who was never excluded DOES hand over their backlog. Get one arm wrong and this
// installation publishes a week of conversation a member had deliberately hidden; get
// the other wrong and "I forgot to include Bob" silently loses everything Zalo was
// still holding for him.
//
// So the two arms are asserted SIDE BY SIDE in one tick where that is expressible,
// and the rest are whole ticks rather than assertions against a predicate — for the
// same reason mode_test.go's are: a test that checked a refusal reason while the
// record still landed would pass on exactly the bug that matters.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// blockedPeriodFrames is the story this whole file exists for, as frames: a message
// that arrived WHILE the member had this conversation on their leave-out list, and one
// that arrived after they took it off.
//
// Both are built from the real captured inbound frame, so only the id and the time
// differ from something Zalo actually sent. The blocked one keeps the lower id, which
// is what a provider whose ids order by time would issue.
func blockedPeriodFrames(t *testing.T) (lifted time.Time, hidden, afterwards zaloInbound) {
	t.Helper()
	_, base := capturedFrames(t)
	lifted = base.OccurredAt.Add(time.Hour)
	afterwards = base
	afterwards.MsgID = "8161098009500"
	afterwards.OccurredAt = lifted.Add(time.Minute)
	afterwards.Content = "the first message after the member stopped hiding this thread"
	return lifted, base, afterwards
}

// excludingEveryoneExcept is a member capturing everyone except the people they leave
// out, with the mode chosen before any of the frames here.
func excludingEveryoneExcept() []any {
	return withMode(connectionRow(statusConnected, memberZaloUID, true), captureEveryoneExcept)
}

// THE DEFECT. A member leaves one conversation out, messages arrive for a week and are
// correctly refused — advancing nothing, because a refusal lands no record and moves no
// bookmark — and then the member takes that person off the list. Before the floor, the
// whole hidden week landed on the very next tick: capture_mode_since was from before
// the block, the cursor row was from before the block, and the verdict row that
// recorded the block had just been deleted by the act of lifting it.
//
// TWO TICKS, because one cannot state the promise: the first has to establish that the
// blocked period really was refused and really did advance nothing, which is what makes
// the second tick's silence about it a decision rather than a bookmark.
func TestLiftingAnExclusionDoesNotHandOverTheBlockedPeriod(t *testing.T) {
	t.Parallel()
	lifted, hidden, afterwards := blockedPeriodFrames(t)
	armed := excludingEveryoneExcept()

	// TICK ONE, while the exclusion is in force. Nothing lands and nothing advances,
	// which is the state that leaves the whole period in Zalo's queue.
	blocked := tickRuntime(t)
	scriptTurn(blocked, [][]any{armed},
		[][]any{allowRow(entryID, counterpartyZaloUID, verdictBlock, "Hidden")}, nil)
	blocked.tx.singleRows = afterTheDrain(armed, armed)
	if err := pollFleet(context.Background(), blocked, newProvider(map[string]*fakeInbox{
		memberIMEI: {uid: memberZaloUID, frames: []zaloInbound{hidden}},
	}).open()); err != nil {
		t.Fatalf("the tick during the exclusion failed: %v", err)
	}
	if len(blocked.ingested) != 0 {
		t.Fatalf("an excluded conversation was captured: %v", keysOf(blocked.ingested))
	}
	if moved := cursorsWritten(t, blocked); len(moved) != 0 {
		t.Fatalf("an excluded conversation moved a bookmark to %v, which would hide the defect", moved)
	}

	// TICK TWO, after the member lifted the exclusion. The verdict row is GONE — that
	// is what lifting it does — and the floor is the only thing left that says the
	// period existed. Zalo is still holding the hidden message and offers it again.
	after := tickRuntime(t)
	scriptTurn(after, [][]any{armed}, nil, nil)
	scriptFloors(after, floorRow(counterpartyZaloUID, lifted))
	after.tx.singleRows = afterTheDrain(armed, armed)
	if err := pollFleet(context.Background(), after, newProvider(map[string]*fakeInbox{
		memberIMEI: {uid: memberZaloUID, frames: []zaloInbound{hidden, afterwards}},
	}).open()); err != nil {
		t.Fatalf("the tick after the exclusion was lifted failed: %v", err)
	}
	if len(after.ingested) != 1 {
		t.Fatalf("lifting an exclusion captured %v; only the message after the lift may land — the rest is the period the member was hiding",
			keysOf(after.ingested))
	}
	if got := after.ingested[0].Key; got != memberZaloUID+":"+afterwards.MsgID {
		t.Fatalf("the message that landed is %q; the one after the lift is expected", got)
	}
	// AND THE FLOOR IS A FLOOR AND NOT A BLOCK: the conversation is being captured
	// again, so its bookmark moves — to the message that landed and no further.
	if moved := cursorsWritten(t, after); moved[counterpartyZaloUID] != afterwards.MsgID {
		t.Fatalf("the bookmarks moved to %v after the lift", moved)
	}
}

// THE OTHER ARM, IN THE SAME TICK, which is the only way to state that these are two
// rules and not one: a conversation the member once excluded is captured from the lift
// forward, and a conversation they simply never mentioned before naming it collects
// everything Zalo is still holding.
//
// It is the property TestAllowingAConversationLaterStillGetsWhatZaloIsHolding pins for
// the whole unit, restated here against a member who ALSO has a floor — because the
// obvious wrong fix is a floor that applies to every conversation, and that fix passes
// every test in this file except this one.
func TestAConversationThatWasNeverExcludedCarriesNoFloor(t *testing.T) {
	t.Parallel()
	lifted, hidden, _ := blockedPeriodFrames(t)
	// The same old message, in a conversation nobody has ever decided against. Its id
	// is the lower of the two, so a per-member high-water mark would bury it too.
	neverExcluded := hidden
	neverExcluded.UIDFrom, neverExcluded.MsgID = unchosenCounterparty, unchosenMsgID

	rt := tickRuntime(t)
	// only_chosen, and the MODE was chosen after both of these messages arrived — so a
	// floor borrowed from the mode would refuse them both, and only a floor that
	// belongs to one named conversation can separate them.
	named := withModeChosenAt(connectionRow(statusConnected, memberZaloUID, true),
		lifted.Add(time.Hour))
	scriptTurn(rt, [][]any{named}, [][]any{
		allowRow(entryID, counterpartyZaloUID, verdictAllow, "Once hidden"),
		allowRow(secondEntryID, unchosenCounterparty, verdictAllow, "Bob"),
	}, nil)
	scriptFloors(rt, floorRow(counterpartyZaloUID, lifted))
	rt.tx.singleRows = afterTheDrain(named, named)

	if err := pollFleet(context.Background(), rt, newProvider(map[string]*fakeInbox{
		memberIMEI: {uid: memberZaloUID, frames: []zaloInbound{hidden, neverExcluded}},
	}).open()); err != nil {
		t.Fatalf("the tick failed: %v", err)
	}
	if len(rt.ingested) != 1 {
		t.Fatalf("the tick captured %v; the never-excluded conversation's backlog and nothing from the hidden period",
			keysOf(rt.ingested))
	}
	if got := rt.ingested[0].Key; got != memberZaloUID+":"+unchosenMsgID {
		t.Fatalf("the message that landed is %q; a conversation nobody excluded must still get its backlog", got)
	}
}

// A CONVERSATION WITH NEITHER A BOOKMARK NOR A VERDICT NOR A FLOOR BEHAVES EXACTLY AS
// IT DID, in both modes: everyone_except captures it from the mode's own floor forward,
// only_chosen captures nothing from it at all.
//
// It is the anti-regression arm of this change rather than a test of it — it passes
// against the implementation this fix replaced, and it must. What it catches is the
// over-application: a floor applied to conversations nothing ever narrowed, which
// would silence the ordinary everyone_except case here.
func TestAConversationNobodyHasDecidedAboutIsUnchanged(t *testing.T) {
	t.Parallel()
	_, base := capturedFrames(t)

	for name, tc := range map[string]struct {
		by       consent
		admitted bool
	}{
		"everyone_except captures it from the mode floor forward": {
			by:       everyoneExcept(nil),
			admitted: true,
		},
		"only_chosen captures nothing from a conversation nobody named": {
			by:       picking(nil),
			admitted: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// No bookmark, no verdict, no floor: the three absences together.
			keep, why := admits(base, tc.by, bookmark{}, nil)
			if keep != tc.admitted {
				t.Fatalf("a conversation nothing has ever decided about was %v (%q); %v is what this mode has always answered",
					keep, why, tc.admitted)
			}
		})
	}
}

// THE MODE-SWITCH QUESTION, ANSWERED DELIBERATELY: everyone_except -> only_chosen
// excludes everyone not named, and switching back widens again — and NEITHER writes a
// per-conversation floor.
//
// The reason is the principle itself. A mode switch is a global narrowing and the
// GLOBAL instrument already records it: capture_mode_since is re-stamped by each
// switch, and admitsUnderMode reads it together with the bookmark's own timestamp,
// which is what TestABookmarkOlderThanTheModeDoesNotSilenceTheFloor pins. Writing
// per-conversation floors for it would mean writing a row for every conversation the
// member has ever had — a set this unit does not know — to record a fact one column
// already holds.
func TestAModeSwitchLeavesNoPerConversationFloor(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.singleRows = [][]any{
		// The member was capturing everyone except their leave-out list…
		excludingEveryoneExcept(),
		// …and is now naming who comes in instead. The save re-states one pick.
		connectionRow(statusConnected, memberZaloUID, true),
		allowRow(entryID, counterpartyZaloUID, verdictAllow, "Chosen"),
		allowRow(entryID, counterpartyZaloUID, verdictAllow, "Chosen"),
	}

	if _, err := saveAllowlist(context.Background(), rt, json.RawMessage(oneVerdict)); err != nil {
		t.Fatalf("switching mode: %v", err)
	}
	for _, statement := range rt.tx.statements {
		if strings.Contains(statement, "conversation_floor") {
			t.Fatalf("a mode switch wrote a per-conversation floor; the mode's own floor is what records a global narrowing:\n%s", statement)
		}
	}
}

// AND THE COROLLARY THE SAME PRINCIPLE FORCES: naming into capture a conversation the
// member had EXPLICITLY excluded still leaves that conversation's floor.
//
// This is the case where the two rules pull against each other, and the call is that
// the explicit exclusion wins. "Name somebody and they bring their backlog" is a
// statement about the MODE's floor — the coarse guess about conversations nobody ever
// decided about. It cannot erase a decision the member made about one named person: the
// week they spent hiding this thread is not something a later `allow` retroactively
// consents to, and the cost of being wrong the other way is publishing it.
func TestNamingAnExcludedConversationIntoCaptureStillLeavesItsFloor(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.singleRows = [][]any{
		excludingEveryoneExcept(),                                      // read
		connectionRow(statusConnected, memberZaloUID, true),            // brought forward, now only_chosen
		allowRow(entryID, counterpartyZaloUID, verdictBlock, "Hidden"), // what was stored: an exclusion
		allowRow(entryID, counterpartyZaloUID, verdictAllow, "Hidden"), // what the upsert returned
		storedFloor(floorEntryID, counterpartyZaloUID, theModeChosenAt(), 1),
	}
	// No floor stored yet: this is the first time this conversation's exclusion ends.
	rt.tx.noRows = map[int]bool{5: true}

	if _, err := saveAllowlist(context.Background(), rt, json.RawMessage(oneVerdict)); err != nil {
		t.Fatalf("naming a previously excluded conversation: %v", err)
	}
	assertFloorRaised(t, rt, extension.AuditCreate)
}

// LIFTING AN EXCLUSION BY REMOVING IT FROM THE LIST ENTIRELY is the other half of the
// same act — the picker's `none` — and it is the one that made the defect unfixable
// with the timestamps that existed: the DELETE destroys the only row that said the
// period was excluded, so the floor has to be written in the same transaction.
func TestRemovingAnExclusionRecordsWhenItWasLifted(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.singleRows = [][]any{
		excludingEveryoneExcept(),
		excludingEveryoneExcept(),
		allowRow(entryID, counterpartyZaloUID, verdictBlock, "Hidden"),
		storedFloor(floorEntryID, counterpartyZaloUID, theModeChosenAt(), 1),
	}
	rt.tx.noRows = map[int]bool{4: true}
	args := json.RawMessage(`{"capture_mode":"everyone_except","entries":[{"channel_user_id":"` +
		counterpartyZaloUID + `","mode":"none"}]}`)

	if _, err := saveAllowlist(context.Background(), rt, args); err != nil {
		t.Fatalf("taking an excluded conversation off the list: %v", err)
	}
	assertFloorRaised(t, rt, extension.AuditCreate)
	// THE FLOOR IS WRITTEN IN THE SAME TRANSACTION AS THE DELETE, and after it: the
	// row that recorded the exclusion is gone by then, so a floor written in a
	// transaction of its own could be lost while the deletion stood.
	rt.trace.before(t, "sql delete", "sql insert")
}

// Removing an INCLUSION is not lifting an exclusion, and leaves no mark. Under
// only_chosen it narrows — the conversation stops being captured — and under
// everyone_except it widens onto a conversation that was never explicitly excluded,
// which is exactly the case the mode's own floor is for.
func TestRemovingAnInclusionLeavesNoFloor(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.singleRows = [][]any{
		connectionRow(statusConnected, memberZaloUID, true),
		connectionRow(statusConnected, memberZaloUID, true),
		allowRow(entryID, counterpartyZaloUID, verdictAllow, "Chosen"),
	}
	args := json.RawMessage(`{"capture_mode":"only_chosen","entries":[{"channel_user_id":"` +
		counterpartyZaloUID + `","mode":"none"}]}`)

	if _, err := saveAllowlist(context.Background(), rt, args); err != nil {
		t.Fatalf("taking a chosen conversation off the list: %v", err)
	}
	for _, statement := range rt.tx.statements {
		if strings.Contains(statement, "conversation_floor") {
			t.Fatalf("removing an inclusion wrote a floor; only an explicit exclusion leaves a mark:\n%s", statement)
		}
	}
}

// A SECOND EXCLUSION OF THE SAME CONVERSATION MOVES THE FLOOR rather than creating a
// new one, and is recorded as the update it is — a create carrying no before-image
// would read as "this person was excluded for the first time" over a state that
// existed.
func TestLiftingASecondExclusionMovesTheFloorItAlreadyHad(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.singleRows = [][]any{
		excludingEveryoneExcept(),
		excludingEveryoneExcept(),
		allowRow(entryID, counterpartyZaloUID, verdictBlock, "Hidden again"),
		storedFloor(floorEntryID, counterpartyZaloUID, theModeChosenAt(), 1),
		storedFloor(floorEntryID, counterpartyZaloUID, theModeChosenAt().Add(time.Hour), 2),
	}
	args := json.RawMessage(`{"capture_mode":"everyone_except","entries":[{"channel_user_id":"` +
		counterpartyZaloUID + `","mode":"none"}]}`)

	if _, err := saveAllowlist(context.Background(), rt, args); err != nil {
		t.Fatalf("lifting a second exclusion: %v", err)
	}
	assertFloorRaised(t, rt, extension.AuditUpdate)
	change := lastFloorChange(t, rt)
	if !strings.Contains(string(change.Before), `"version":1`) {
		t.Fatalf("the ledger does not say what the floor was before it moved: %s", change.Before)
	}
}

// assertFloorRaised is the shape every lift leaves behind: a floor written from the
// DATABASE's own clock, and one ledger row plus one event naming the conversation it
// is about.
func assertFloorRaised(t *testing.T, rt *fakeRuntime, action extension.AuditAction) {
	t.Helper()
	sql, args := rt.tx.statementMentioning(t, "not_before = now()")
	if args[0] != callerUserID || args[1] != counterpartyZaloUID {
		t.Fatalf("the floor was raised for %v:\n%s", args, sql)
	}
	if !strings.Contains(sql, "not_before)\n\t\t VALUES (") || !strings.Contains(sql, "now())") {
		t.Fatalf("the floor is not written from the database's own clock:\n%s", sql)
	}
	change := lastFloorChange(t, rt)
	if change.Action != action {
		t.Fatalf("the floor was recorded as %q; %q is what this write did", change.Action, action)
	}
	if !strings.Contains(string(change.After), counterpartyZaloUID) {
		t.Fatalf("the ledger does not say whose exclusion was lifted: %s", change.After)
	}
	event := rt.tx.published[len(rt.tx.published)-1]
	if event.Verb != eventExclusionLifted || !strings.Contains(string(event.Payload), counterpartyZaloUID) {
		t.Fatalf("the lift was announced as %q carrying %s", event.Verb, event.Payload)
	}
}

// lastFloorChange is the ledger row the floor write left, failing loudly rather than
// letting an assertion run against the verdict's row beside it.
func lastFloorChange(t *testing.T, rt *fakeRuntime) extension.Change {
	t.Helper()
	change := rt.tx.audited[len(rt.tx.audited)-1]
	if change.Entity != floorEntity {
		t.Fatalf("the last ledger row is against %q; the floor's own row is expected last", change.Entity)
	}
	if change.ID != floorEntryID {
		t.Fatalf("the floor was recorded against row %q", change.ID)
	}
	return change
}

// A FLOOR THIS UNIT COULD NOT WRITE FAILS THE SAVE, rather than leaving the member
// with a lifted exclusion and no floor behind it. That combination is the defect
// itself: the verdict row is gone, nothing records the excluded period, and the whole
// of it lands on the next tick — so the save has to roll back and be retried, not
// half-succeed.
func TestASaveWhoseFloorCannotBeWrittenIsRefused(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.singleRows = [][]any{
		excludingEveryoneExcept(),
		excludingEveryoneExcept(),
		allowRow(entryID, counterpartyZaloUID, verdictBlock, "Hidden"),
	}
	// The read of whatever floor this conversation already had cannot be performed.
	rt.tx.rowErr = map[int]error{4: errors.New("the floor could not be read")}
	args := json.RawMessage(`{"capture_mode":"everyone_except","entries":[{"channel_user_id":"` +
		counterpartyZaloUID + `","mode":"none"}]}`)

	if _, err := saveAllowlist(context.Background(), rt, args); err == nil {
		t.Fatal("a save that could not record the lifted exclusion answered success; the excluded period would land on the next tick")
	}
}
