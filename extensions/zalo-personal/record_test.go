// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The three filters and the mapping, and MOST OF THIS FILE ASSERTS AN ABSENCE.
// That is not a stylistic choice: "an unallowed conversation produces no record"
// is the whole consent property of this unit, and the only way to state it is to
// show that nothing came out. A test that checked a returned reason string while
// the record still landed would pass on exactly the bug that matters.

import (
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// pickingTheCounterparty is only_chosen with the captured conversation on the pick
// list, which is the mode the unit's first model was the whole of.
func pickingTheCounterparty() consent {
	return consent{
		mode:     captureOnlyChosen,
		verdicts: map[string]verdict{counterpartyZaloUID: verdictAllow},
	}
}

// picking is only_chosen with an arbitrary list.
func picking(verdicts map[string]verdict) consent {
	return consent{mode: captureOnlyChosen, verdicts: verdicts}
}

// theModeChosenAt is the floor everyone_except measures a never-mentioned
// conversation from. It sits BEFORE the captured frames on purpose, so the ordinary
// everyone_except case admits them and a test about the floor moves the floor rather
// than the messages.
func theModeChosenAt() time.Time {
	return time.UnixMilli(1786940000000).UTC()
}

// everyoneExcept is the other mode, with a leave-out list.
func everyoneExcept(verdicts map[string]verdict) consent {
	return consent{mode: captureEveryoneExcept, since: theModeChosenAt(), verdicts: verdicts}
}

func TestARealInboundFrameBecomesARepliableMessage(t *testing.T) {
	t.Parallel()
	_, inbound := capturedFrames(t)

	rec, err := recordFor(inbound, memberZaloUID, nil)
	if err != nil {
		t.Fatalf("a real captured message could not be mapped: %v", err)
	}
	// The published grammar, run here rather than waved through: the core refuses
	// what this cannot pass, and a suite that skipped it would find out in
	// production.
	if err := rec.Validate(); err != nil {
		t.Fatalf("the record capture would be handed is refused: %v", err)
	}
	if rec.System != ingressSystem {
		t.Fatalf("the record names source %q; the declared one is %q", rec.System, ingressSystem)
	}
	if rec.Key != memberZaloUID+":"+inboundMsgID {
		t.Fatalf("the natural key is %q; it must be the provider's own id namespaced by the member's account", rec.Key)
	}
	if rec.ThreadKey != provider+":"+memberZaloUID+":"+counterpartyZaloUID {
		t.Fatalf("the thread key is %q, which is not namespaced by provider and member", rec.ThreadKey)
	}
	if rec.Activity.Kind != extension.ActivityKindMessage || rec.Activity.ChannelProvider != provider {
		t.Fatalf("the activity is %q on %q; a message on this transport is expected", rec.Activity.Kind, rec.Activity.ChannelProvider)
	}
	if rec.Activity.Direction != extension.DirectionInbound {
		t.Fatalf("a message somebody sent this member reads as %q", rec.Activity.Direction)
	}
	if rec.Activity.Body != "Test B" {
		t.Fatalf("the body is %q; the captured message says %q", rec.Activity.Body, "Test B")
	}
	// The PROVIDER's time, taken from the frame's own unix-millisecond `ts`, never
	// the moment the poll noticed.
	if want := time.UnixMilli(1786940459649).UTC(); !rec.Activity.OccurredAt.Equal(want) {
		t.Fatalf("the message is timed %s; the frame says %s", rec.Activity.OccurredAt, want)
	}
	if len(rec.Raw) == 0 {
		t.Fatal("the provider's own frame was not kept as evidence")
	}
}

// What makes the captured message answerable, and the reason this is a test of
// its own: a record that lands with no channel identity reads perfectly ordinary
// on the timeline and has no reply box, which is indistinguishable from a
// provider that does not identify its senders.
func TestTheCounterpartyIsNamedByAccountAndCarriesNoAddress(t *testing.T) {
	t.Parallel()
	_, inbound := capturedFrames(t)

	rec, err := recordFor(inbound, memberZaloUID, nil)
	if err != nil {
		t.Fatalf("mapping the captured message: %v", err)
	}
	identity := rec.Counterparty.ChannelIdentity
	if identity.Provider != provider || identity.ChannelUserID != counterpartyZaloUID {
		t.Fatalf("the counterparty is bound as %+v; the pair is what a reply is routed on", identity)
	}
	if rec.Counterparty.DisplayName == "" || identity.DisplayName != rec.Counterparty.DisplayName {
		t.Fatalf("the counterparty is unnamed or named twice differently: %+v", rec.Counterparty)
	}
	// Empty EMAIL, DOMAIN and ADDRESSES, asserted together because they are one
	// decision: Zalo reports no address anywhere, and an empty Domain is not
	// opting out of the core's suppression gates — it is failing to answer, which
	// those gates read as "keep". Inventing one to satisfy a gate would be worse
	// than the silence.
	if rec.Counterparty.Email != "" || rec.Counterparty.Domain != "" || len(rec.Addresses) != 0 {
		t.Fatalf("the record invents contact details Zalo does not report: %+v / %v", rec.Counterparty, rec.Addresses)
	}
}

// Two reps, one message id: the key must not collide, because this unit's records
// share ONE provenance namespace and a collision would land one member's
// conversation as the other's.
func TestTwoMembersCannotCollideOnOneMessageID(t *testing.T) {
	t.Parallel()
	_, inbound := capturedFrames(t)

	mine, err := recordFor(inbound, memberZaloUID, nil)
	if err != nil {
		t.Fatalf("mapping for the first member: %v", err)
	}
	theirs, err := recordFor(inbound, "1800000000000000003", nil)
	if err != nil {
		t.Fatalf("mapping for the second member: %v", err)
	}
	if mine.Key == theirs.Key || mine.ThreadKey == theirs.ThreadKey {
		t.Fatalf("two members share a key: %q / %q", mine.Key, mine.ThreadKey)
	}
}

func TestTheRosterOnlyImprovesTheNameAndNeverDecidesWhetherARecordLands(t *testing.T) {
	t.Parallel()
	_, inbound := capturedFrames(t)
	roster := map[string]string{counterpartyZaloUID: "Saved Contact Name"}

	named, err := recordFor(inbound, memberZaloUID, roster)
	if err != nil {
		t.Fatalf("mapping with a roster: %v", err)
	}
	if named.Counterparty.DisplayName != "Saved Contact Name" {
		t.Fatalf("the roster name was not used: %q", named.Counterparty.DisplayName)
	}
	// A first-time prospect is by definition not on the roster, and the frame's
	// own name is already enough — so an absent entry changes the NAME and
	// nothing else about whether this lands.
	stranger, err := recordFor(inbound, memberZaloUID, map[string]string{})
	if err != nil {
		t.Fatalf("a counterparty absent from the roster could not be mapped: %v", err)
	}
	if stranger.Counterparty.DisplayName != inbound.DName || stranger.Key != named.Key {
		t.Fatalf("an unrostered counterparty produced a different record: %+v", stranger.Counterparty)
	}
}

// A frame whose own timestamp did not decode must refuse LOUDLY rather than
// default to now(): the capture seam rejects a zero time, and a timeline stamped
// with when a poll noticed is a timeline of this system's own scheduling.
func TestAFrameThisUnitCannotRepresentIsRefusedRatherThanGuessedAt(t *testing.T) {
	t.Parallel()
	_, inbound := capturedFrames(t)

	for name, frame := range map[string]zaloInbound{
		"no time of its own":      {MsgID: inboundMsgID, UIDFrom: counterpartyZaloUID, Content: "x"},
		"no message id":           {UIDFrom: counterpartyZaloUID, OccurredAt: inbound.OccurredAt},
		"nobody at the other end": {MsgID: inboundMsgID, IDTo: selfUID, OccurredAt: inbound.OccurredAt},
		"more evidence than capture keeps": {
			MsgID: inboundMsgID, UIDFrom: counterpartyZaloUID, OccurredAt: inbound.OccurredAt,
			Raw: make([]byte, extension.MaxRawBytes+1),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := recordFor(frame, memberZaloUID, nil); err == nil {
				t.Fatalf("%s was mapped into a record anyway", name)
			}
		})
	}
	if _, err := recordFor(inbound, "", nil); err == nil {
		t.Fatal("a connection that does not say which account it is still produced a keyed record")
	}
}

// THE FILTERS, and every case here is a case where NOTHING may be captured. The
// self-echo is the one whose absence a customer would see: without it every rep
// reply lands a second time on that customer's timeline, attributed inbound, so
// the customer appears to have said what the rep said.
func TestOnlyAConversationTheMemberChoseIsAdmitted(t *testing.T) {
	t.Parallel()
	echo, inbound := capturedFrames(t)

	for name, tc := range map[string]struct {
		frame  zaloInbound
		by     consent
		cursor string
		ours   map[string]bool
		keep   bool
		reason string
	}{
		"a conversation on the pick list": {
			frame: inbound, by: pickingTheCounterparty(), keep: true,
		},
		// The echo of a reply the CRM itself staged: the core already wrote that
		// activity, so capturing it would post the rep's words to the customer
		// twice.
		"the echo of a reply the CRM staged": {
			frame:  echo,
			by:     pickingTheCounterparty(),
			ours:   map[string]bool{echoMsgID: true},
			reason: "own_send_already_recorded",
		},
		// The echo of a reply the rep typed on their PHONE. Nothing here has seen
		// it, and it is the half of the conversation the timeline was missing.
		"the echo of a reply sent from the rep's phone": {
			frame: echo, by: pickingTheCounterparty(), keep: true,
		},
		// The consent story from the OUTBOUND side: a rep messaging somebody they
		// never allowed must not pull that person into the CRM.
		"a phone reply to somebody who is not on the pick list": {
			frame: echo, by: picking(map[string]verdict{}), reason: "not_included",
		},
		"a conversation left off the pick list by a block": {
			frame: inbound, by: picking(map[string]verdict{counterpartyZaloUID: verdictBlock}), reason: "not_included",
		},
		"a conversation nobody has mentioned, under only_chosen": {
			frame: inbound, by: picking(map[string]verdict{}), reason: "not_included",
		},
		"a member who picked somebody else": {
			frame:  inbound,
			by:     picking(map[string]verdict{"1900000000000000009": verdictAllow}),
			reason: "not_included",
		},
		// THE OTHER MODE. Absence means the opposite here, which is the whole point
		// of there being a mode at all.
		"a conversation nobody has mentioned, under everyone_except": {
			frame: inbound, by: everyoneExcept(map[string]verdict{}), keep: true,
		},
		"a conversation on the leave-out list": {
			frame:  inbound,
			by:     everyoneExcept(map[string]verdict{counterpartyZaloUID: verdictBlock}),
			reason: "excluded_or_before_the_mode",
		},
		// An `allow` row is INERT under everyone_except rather than wrong in it.
		"a pick-list row under everyone_except": {
			frame: inbound, by: everyoneExcept(map[string]verdict{counterpartyZaloUID: verdictAllow}), keep: true,
		},
		"a phone reply under everyone_except": {
			frame: echo, by: everyoneExcept(map[string]verdict{}), keep: true,
		},
		"a phone reply to somebody left out": {
			frame:  echo,
			by:     everyoneExcept(map[string]verdict{counterpartyZaloUID: verdictBlock}),
			reason: "excluded_or_before_the_mode",
		},
		// NO MODE CAPTURES NOTHING. Unreachable through any writer — the database
		// refuses "armed with no mode" — and the safe direction anyway.
		"a member who has not answered yet": {
			frame: inbound, by: consent{verdicts: map[string]verdict{counterpartyZaloUID: verdictAllow}},
			reason: "no_mode_chosen",
		},
		"a message at the bookmark": {
			frame: inbound, by: pickingTheCounterparty(), cursor: inboundMsgID, reason: "already_landed",
		},
		"a message below the bookmark": {
			frame: inbound, by: pickingTheCounterparty(), cursor: "9161098001435", reason: "already_landed",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			keep, reason := admits(tc.frame, tc.by, tc.cursor, tc.ours)
			if keep != tc.keep {
				t.Fatalf("%s was %v, want %v (reason %q)", name, keep, tc.keep, reason)
			}
			if reason != tc.reason {
				t.Fatalf("%s was refused as %q, want %q", name, reason, tc.reason)
			}
		})
	}
}

// The echo is a DIRECTION test and cannot be a dedupe-on-id job, because the echo
// carries the same id the send returned. This states the fact the capture proves,
// so a later change that "simplified" the filter to an id comparison fails here.
func TestTheEchoAndTheInboundAreToldApartByDirectionAndNotByID(t *testing.T) {
	t.Parallel()
	echo, inbound := capturedFrames(t)

	if !echo.selfSent() || inbound.selfSent() {
		t.Fatalf("direction read wrong: echo uidFrom=%q idTo=%q, inbound uidFrom=%q idTo=%q",
			echo.UIDFrom, echo.IDTo, inbound.UIDFrom, inbound.IDTo)
	}
	if echo.counterparty() != counterpartyZaloUID || inbound.counterparty() != counterpartyZaloUID {
		t.Fatalf("the other end of the conversation is read as %q / %q", echo.counterparty(), inbound.counterparty())
	}
}

// The cursor compares NUMBERS. Text order would put "999" above "1000", which on
// a rolling id parks a member's capture and drops everything after it.
func TestTheCursorComparesMessageIdsAsNumbers(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		msgID, cursor string
		below         bool
	}{
		"a shorter id that is numerically larger": {msgID: "1000", cursor: "999", below: false},
		"a longer id that is numerically smaller": {msgID: "999", cursor: "1000", below: true},
		"the same id":      {msgID: "1000", cursor: "1000", below: true},
		"no cursor at all": {msgID: "1", cursor: "", below: false},
		// UNSEEN is the safe direction for an id neither side can parse: read as
		// seen it loses the message permanently, read as unseen it costs one
		// replay that capture deduplicates away.
		"an id this unit cannot parse":     {msgID: "not-a-number", cursor: "1000", below: false},
		"a cursor this unit cannot parse":  {msgID: "1000", cursor: "not-a-number", below: false},
		"an id past what a uint64 can say": {msgID: strings.Repeat("9", 25), cursor: "1000", below: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := atOrBelow(tc.msgID, tc.cursor); got != tc.below {
				t.Fatalf("%s: atOrBelow(%q, %q) = %v", name, tc.msgID, tc.cursor, got)
			}
		})
	}
	if higher("999", "1000") != "1000" || higher("1000", "999") != "1000" || higher("", "5") != "5" {
		t.Fatal("the cursor advanced to the wrong one of two ids")
	}
}

// C4: A MESSAGE ID THIS UNIT CANNOT ORDER MUST NEVER BECOME A BOOKMARK.
//
// Treated as unseen it costs one deduplicated replay, which is what the comparison
// does and is safe. STORED it is a different failure entirely: a value that sits
// above and below nothing makes every numeric id read as unseen, so the whole
// conversation is re-offered on every tick forever and the bookmark stops working.
func TestAnUnorderableMessageIdNeverBecomesABookmark(t *testing.T) {
	t.Parallel()
	// higher NEVER returns what it could not order, in either position — and the
	// mixed cases are the ones that matter, because a drain can hold both kinds.
	for name, tc := range map[string]struct{ cursor, candidate, want string }{
		"a candidate nothing can order":           {cursor: inboundMsgID, candidate: "abc", want: inboundMsgID},
		"an unorderable candidate, no cursor":     {cursor: "", candidate: "abc", want: ""},
		"a stored cursor nothing can order":       {cursor: "abc", candidate: inboundMsgID, want: inboundMsgID},
		"neither side orderable":                  {cursor: "abc", candidate: "def", want: "abc"},
		"an ordinary advance":                     {cursor: echoMsgID, candidate: inboundMsgID, want: inboundMsgID},
		"a candidate below the cursor":            {cursor: inboundMsgID, candidate: echoMsgID, want: inboundMsgID},
		"a shorter id that is numerically larger": {cursor: "999", candidate: "1000", want: "1000"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := higher(tc.cursor, tc.candidate); got != tc.want {
				t.Fatalf("%s: higher(%q, %q) = %q, want %q", name, tc.cursor, tc.candidate, got, tc.want)
			}
			if _, ok := orderable(higher(tc.cursor, tc.candidate)); !ok && tc.want != "" && tc.want != "abc" {
				t.Fatalf("%s produced an unorderable bookmark", name)
			}
		})
	}
	// And the frame is refused before it can ever reach a cursor. The msgId is the
	// provider's own stable key and has been a decimal integer in every frame anybody
	// has captured; one that is not is a shape this unit cannot order.
	_, inbound := capturedFrames(t)
	for _, hostile := range []string{"abc", "12a", " 42", "42 ", "-1", "0x2a", "1e3", "١٢٣"} {
		frame := inbound
		frame.MsgID = hostile
		if _, err := recordFor(frame, memberZaloUID, nil); err == nil {
			t.Fatalf("message id %q was mapped into a record, and would then have become a bookmark", hostile)
		}
	}
}

// earlier is the SORT ordering, and it has to be strict: `a <= b` answers true for a
// value against itself, which is not a valid `less` and misbehaves exactly when two
// frames share an id.
func TestTheDrainOrderIsAStrictOrdering(t *testing.T) {
	t.Parallel()
	if earlier(inboundMsgID, inboundMsgID) {
		t.Fatal("a message sorts before itself, which is not an ordering")
	}
	if !earlier(echoMsgID, inboundMsgID) || earlier(inboundMsgID, echoMsgID) {
		t.Fatalf("the two captured frames sort wrongly: %s then %s", echoMsgID, inboundMsgID)
	}
	if earlier("abc", inboundMsgID) || earlier(inboundMsgID, "abc") {
		t.Fatal("an unorderable id claims an order")
	}
}
