// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// What the scheduled tick must do, and — as in record_test.go — mostly what it
// must NOT do. The absences asserted here are the product: a socket that is never
// opened, an ingest that never happens, a cursor that never moves past a message
// that did not land.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// tickRuntime is a job tick's Runtime: unattended, with this member's credential
// on deposit and their connection scripted.
func tickRuntime(t *testing.T, rows ...[]any) *fakeRuntime {
	t.Helper()
	rt := newRuntime().unattended()
	depositSession(t, rt, callerUserID, memberIMEI)
	if len(rows) > 0 {
		rt.tx.script(readFleet, rows...)
	}
	return rt
}

// scriptTurn states what one tick's multi-row reads answer: the fleet, every
// member's verdicts, and which of the echoed ids the CRM sent. Nil is an empty
// answer, and the verdicts serve BOTH reads of them — see the note on queryRows.
func scriptTurn(rt *fakeRuntime, fleet, verdicts, ours [][]any) {
	rt.tx.script(readFleet, fleet...)
	rt.tx.script(readVerdicts, verdicts...)
	rt.tx.script(readMarkers, ours...)
}

// scriptFreshVerdicts states what the SECOND read of the verdicts answers — the one
// taken after the drain, which is what a landing is actually judged against. It is
// how a test says "the member blocked this counterparty while the socket was open".
func scriptFreshVerdicts(rt *fakeRuntime, verdicts ...[]any) {
	rt.tx.script(readVerdicts, verdicts...)
}

// oneMember scripts a fleet of one connected, armed member.
func oneMember() [][]any {
	return [][]any{connectionRow(statusConnected, memberZaloUID, true)}
}

// afterTheDrain scripts the two single-row reads a turn that reaches the landing pass
// makes: the connection RE-READ, which is what the member's consent is judged against
// after the drain, and the row the bookkeeping write returns.
func afterTheDrain(consent, written []any) [][]any {
	return [][]any{consent, written}
}

// stillArmed is the ordinary answer to that re-read: the member has not withdrawn.
func stillArmed() []any {
	return connectionRow(statusConnected, memberZaloUID, true)
}

// theBookkeepingWrite is the version-guarded row write every turn ends with. Its
// arguments are (id, status, error class, version[, backoff interval]).
func theBookkeepingWrite(t *testing.T, rt *fakeRuntime) (string, []any) {
	t.Helper()
	return rt.tx.statementMentioning(t, "last_polled_at = now()")
}

// cursorsWritten is what each conversation's bookmark was moved to, read off the
// per-counterparty advance. Empty when no bookmark moved at all.
func cursorsWritten(t *testing.T, rt *fakeRuntime) map[string]string {
	t.Helper()
	moved := map[string]string{}
	for i, sql := range rt.tx.statements {
		if !strings.Contains(sql, "greatest(") {
			continue
		}
		args := rt.tx.args[i]
		counterparties, ok := args[1].([]string)
		if !ok {
			t.Fatalf("the cursor advance named no counterparties: %v", args[1])
		}
		cursors, ok := args[2].([]string)
		if !ok || len(cursors) != len(counterparties) {
			t.Fatalf("the cursor advance is misaligned: %v / %v", args[1], args[2])
		}
		for at, counterparty := range counterparties {
			moved[counterparty] = cursors[at]
		}
	}
	return moved
}

// theInbox is a session holding the two real captured frames: a message somebody
// sent this member, and one of this member's own outgoing messages coming back.
func theInbox(t *testing.T) *fakeInbox {
	t.Helper()
	echo, inbound := capturedFrames(t)
	return &fakeInbox{uid: memberZaloUID, frames: []zaloInbound{inbound, echo}}
}

// oursAlready scripts the sent-marker read as having recorded these ids, which is
// how a test says "the CRM staged this reply itself".
func oursAlready(ids ...string) [][]any {
	rows := make([][]any, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, []any{id})
	}
	return rows
}

// chosen is the ordinary verdict list: this member allowed the captured
// conversation, under the name their screen was showing, and nothing has been
// captured from it yet.
func chosen() [][]any {
	return [][]any{allowRow(entryID, counterpartyZaloUID, verdictAllow, "Chosen")}
}

// scriptCursors states how far each conversation has already been read. It is
// separate from the verdicts because the two live in separate tables for a reason —
// see cursor.go — and a fixture that conflated them could not express the case that
// forced them apart.
func scriptCursors(rt *fakeRuntime, rows ...[]any) {
	rt.tx.script(readCursors, rows...)
}

// scriptFloors states which conversations carry a consent floor, and from when. It is
// separate from both the verdicts and the cursors because a floor outlives the verdict
// it came from and means something different from a bookmark — see floor.go.
func scriptFloors(rt *fakeRuntime, rows ...[]any) {
	rt.tx.script(readFloors, rows...)
}

// THE FLEET READ ITSELF, asserted on the statement, because both properties live
// in the SQL and nowhere else: a member who has not chosen is never enumerated, so
// no credential of theirs is unsealed and no socket of theirs is opened, and the
// order is what stops a busy installation from never reaching the end of its list.
func TestTheFleetReadSkipsUnarmedMembersAndTakesTheLongestWaitingFirst(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, nil, nil, nil)

	if err := pollFleet(context.Background(), rt, newProvider(nil).open()); err != nil {
		t.Fatalf("an installation with nobody armed is not a failure, and it answered %v", err)
	}
	sql, args := rt.tx.statementMentioning(t, "capture_enabled")
	if !strings.Contains(sql, "ORDER BY last_polled_at ASC NULLS FIRST, created_at") {
		t.Fatalf("the fleet is not read longest-waiting-first:\n%s", sql)
	}
	if len(args) != 1 || args[0] != statusConnected {
		t.Fatalf("the fleet read is not restricted to working connections: %v", args)
	}
	if len(rt.ingested) != 0 {
		t.Fatal("an empty fleet reached capture")
	}
}

// A member who is ARMED but has admitted nobody: the socket is not opened either,
// for the same reason. Nothing this drain returned could be kept, so reading it
// would be this installation taking a copy of somebody's private conversation in
// order to throw it away.
func TestAMemberWhoHasAdmittedNobodyHasNoSocketOpened(t *testing.T) {
	t.Parallel()
	for name, verdicts := range map[string][][]any{
		"a list that is empty":            {},
		"a list with nothing but a block": {allowRow(entryID, counterpartyZaloUID, verdictBlock, "Blocked")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt := tickRuntime(t)
			scriptTurn(rt, oneMember(), verdicts, nil)
			rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}
			opened := theInbox(t)
			provider := newProvider(map[string]*fakeInbox{memberIMEI: opened})

			if err := pollFleet(context.Background(), rt, provider.open()); err != nil {
				t.Fatalf("%s is not a failure, and it answered %v", name, err)
			}
			if provider.opens != 0 || opened.drains != 0 {
				t.Fatalf("%s still opened %d session(s) and drained %d time(s)", name, provider.opens, opened.drains)
			}
			if len(rt.ingested) != 0 {
				t.Fatalf("%s landed %d record(s)", name, len(rt.ingested))
			}
		})
	}
}

// The three filters, at the tick's level, asserted on the ABSENCE of an ingest.
// This is the security property: a record that never reaches capture never
// becomes a row on a shared timeline, in an audit trail and in an outbox event —
// none of which a later cleanup can undo.
func TestNothingUnallowedEverReachesCapture(t *testing.T) {
	t.Parallel()
	for name, verdicts := range map[string][][]any{
		"a counterparty the member blocked": {
			allowRow(entryID, counterpartyZaloUID, verdictBlock, "Blocked"),
		},
		"a counterparty with no verdict at all": {
			allowRow(entryID, "1900000000000000009", verdictAllow, "Somebody else"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt := tickRuntime(t)
			scriptTurn(rt, oneMember(), verdicts, nil)
			rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}
			opened := theInbox(t)

			err := pollFleet(context.Background(), rt,
				newProvider(map[string]*fakeInbox{memberIMEI: opened}).open())
			if err != nil {
				t.Fatalf("%s is not a failure, and it answered %v", name, err)
			}
			if len(rt.ingested) != 0 {
				t.Fatalf("%s reached capture: %+v", name, rt.ingested)
			}
		})
	}
}

// READ, CLOSE, INGEST. Ingest opens its own transaction, so calling it inside one
// of ours takes a second connection while holding one — which on a small pool does
// not fail, it hangs. The fake refuses it exactly as the core does, so a tick that
// reverted to reading and ingesting in one transaction fails here.
func TestNothingIsEverIngestedFromInsideThisUnitsOwnTransaction(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, oneMember(), chosen(), nil)
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}

	err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: theInbox(t)}).open())
	if err != nil {
		t.Fatalf("the tick failed, and an ingest taken inside our own transaction reads as %v: %v", extension.ErrNestedIngest, err)
	}
	if len(rt.ingested) != 2 {
		t.Fatalf("capture was handed %d record(s)", len(rt.ingested))
	}
}

// THE ASYMMETRY THAT IS THE WHOLE SAFETY ARGUMENT: a cursor not advanced past a
// message that landed costs one deduplicated retry, while a cursor advanced past
// one that did not land costs the message.
func TestTheCursorDoesNotAdvancePastAMessageThatDidNotLand(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, oneMember(), [][]any{allowRow(entryID, counterpartyZaloUID, verdictAllow, "Chosen")}, nil)
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}
	// Capture refuses on this unit's STANDING, which is not a message-level
	// problem: the turn stops and nothing is written past it.
	rt.ingestErr, rt.ingestFrom = extension.ErrForbidden, 1

	err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: theInbox(t)}).open())
	if err == nil {
		t.Fatal("a tick whose only member failed answered success")
	}
	if moved := cursorsWritten(t, rt); len(moved) != 0 {
		t.Fatalf("a bookmark moved to %v past a message capture never took", moved)
	}
	// last_polled_at IS still written, or this member sits at the front of the
	// fairness order forever and starves everybody behind them.
	sql, args := theBookkeepingWrite(t, rt)
	if !strings.Contains(sql, "last_polled_at = now()") {
		t.Fatalf("a failed turn wrote no poll time:\n%s", sql)
	}
	if args[2] == "" {
		t.Fatal("a failed turn recorded no failure class for the member's screen")
	}
}

// A skip is a SUCCESS: the core drops a wholly-internal message deliberately and
// commits a breadcrumb saying so, and treating it as a failure would retry a
// deliberate drop forever.
func TestADeliberateSkipAdvancesTheCursorExactlyAsAnAcceptanceDoes(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	rt.skips = true
	scriptTurn(rt, oneMember(), [][]any{allowRow(entryID, counterpartyZaloUID, verdictAllow, "Chosen")}, nil)
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: theInbox(t)}).open()); err != nil {
		t.Fatalf("a skipped record is a success, and the tick answered %v", err)
	}
	if moved := cursorsWritten(t, rt); moved[counterpartyZaloUID] != inboundMsgID {
		t.Fatalf("a skip left the bookmarks at %v", moved)
	}
}

// A message the ALLOWLIST dropped must not move the cursor, and this is the
// subtlest rule in the file: a cursor that jumped over an unchosen conversation
// would have quietly decided that the moment a member allows it, it starts empty.
func TestAMessageTheMembersChoiceDroppedDoesNotMoveTheCursor(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, oneMember(), [][]any{allowRow(entryID, "1900000000000000009", verdictAllow, "Somebody else")}, nil)
	rt.tx.singleRows = afterTheDrain(stillArmed(), stillArmed())

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: theInbox(t)}).open()); err != nil {
		t.Fatalf("the tick failed: %v", err)
	}
	if moved := cursorsWritten(t, rt); len(moved) != 0 {
		t.Fatalf("a bookmark moved to %v for a conversation the member has not chosen", moved)
	}
}

// The member withdraws, or disarms capture, while the socket is open. The verdicts
// that opened it were read seconds earlier, and landing on them would capture a
// private conversation somebody had just explicitly withdrawn from.
func TestAMemberWhoWithdrawsWhileTheSocketIsOpenHasNothingCaptured(t *testing.T) {
	t.Parallel()
	for name, consent := range map[string][]any{
		"an account withdrawn mid-drain": connectionRow(statusDisconnected, memberZaloUID, false),
		"capture disarmed mid-drain":     connectionRow(statusConnected, memberZaloUID, false),
		"a session that died mid-drain":  connectionRow(statusNeedsReconnect, memberZaloUID, true),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt := tickRuntime(t)
			scriptTurn(rt, oneMember(), chosen(), nil)
			rt.tx.singleRows = afterTheDrain(consent, stillArmed())

			if err := pollFleet(context.Background(), rt,
				newProvider(map[string]*fakeInbox{memberIMEI: theInbox(t)}).open()); err != nil {
				t.Fatalf("%s is a withdrawal and not a failure, and it answered %v", name, err)
			}
			if len(rt.ingested) != 0 {
				t.Fatalf("%s still captured %v", name, keysOf(rt.ingested))
			}
			if moved := cursorsWritten(t, rt); len(moved) != 0 {
				t.Fatalf("%s recorded a position the member no longer consents to: %v", name, moved)
			}
		})
	}
}

// The member's row is gone entirely — the same answer, and not a failure: there is
// nothing to capture for and nothing to record.
func TestAConnectionDeletedWhileTheSocketWasOpenCapturesNothing(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, oneMember(), chosen(), nil)
	// The consent re-read matches nothing.
	rt.tx.noRows = map[int]bool{1: true}

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: theInbox(t)}).open()); err != nil {
		t.Fatalf("a deleted connection is not a failure, and it answered %v", err)
	}
	if len(rt.ingested) != 0 {
		t.Fatalf("a connection that no longer exists captured %v", keysOf(rt.ingested))
	}
}

// A member who blocks ONE counterparty while the socket is open. The verdicts are
// re-read after the drain, so the block takes effect on the messages this very tick
// drained rather than only on the next one.
func TestACounterpartyBlockedWhileTheSocketWasOpenIsNotCaptured(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	// TWO answers to the verdict read: the drain was opened on an allow, and the
	// landing is judged on the block that replaced it.
	scriptTurn(rt, oneMember(), chosen(), nil)
	scriptFreshVerdicts(rt, allowRow(entryID, counterpartyZaloUID, verdictBlock, "Chosen"))
	rt.tx.singleRows = afterTheDrain(stillArmed(), stillArmed())

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: theInbox(t)}).open()); err != nil {
		t.Fatalf("a block mid-drain is not a failure, and it answered %v", err)
	}
	if len(rt.ingested) != 0 {
		t.Fatalf("a conversation blocked while the socket was open still captured %v", keysOf(rt.ingested))
	}
}

// A session Zalo no longer accepts is not a transient fault: only that human
// scanning a QR with their phone fixes it, which is why the row is parked in a
// state their screen explains rather than retried on a cadence.
func TestADeadSessionParksTheConnectionForAHumanWithAPhone(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, oneMember(), [][]any{allowRow(entryID, counterpartyZaloUID, verdictAllow, "Chosen")}, nil)
	rt.tx.singleRows = [][]any{connectionRow(statusNeedsReconnect, memberZaloUID, true)}

	// No inbox for this credential: the provider refuses to resume it.
	if err := pollFleet(context.Background(), rt, newProvider(nil).open()); err == nil {
		t.Fatal("a tick whose only member could not resume answered success")
	}
	_, args := theBookkeepingWrite(t, rt)
	if args[1] != statusNeedsReconnect {
		t.Fatalf("the connection was left as %v rather than needing a re-scan", args[1])
	}
	if verb := rt.tx.published[len(rt.tx.published)-1].Verb; verb != eventReconnectNeeded {
		t.Fatalf("the bus was told %q; a session needing a human is announced separately", verb)
	}
}

// A member whose credential was withdrawn under the tick. It is the same parking
// answer, and the class is this unit's own word rather than a provider's prose.
func TestAMemberWhoseCredentialIsGoneIsParkedRatherThanRetried(t *testing.T) {
	t.Parallel()
	rt := newRuntime().unattended()
	scriptTurn(rt, oneMember(), [][]any{allowRow(entryID, counterpartyZaloUID, verdictAllow, "Chosen")}, nil)
	rt.tx.singleRows = [][]any{connectionRow(statusNeedsReconnect, memberZaloUID, true)}

	if err := pollFleet(context.Background(), rt, newProvider(nil).open()); err == nil {
		t.Fatal("a member with no credential on deposit was reported as polled")
	}
	_, args := theBookkeepingWrite(t, rt)
	if args[2] != "session_withdrawn" {
		t.Fatalf("the failure class is %v; it must be this unit's own word", args[2])
	}
}

// One member's failure is theirs. A token that stopped working this morning must
// not be the reason nobody else's messages arrive.
func TestOneMembersFailureDoesNotStopTheFleet(t *testing.T) {
	t.Parallel()
	rt := newRuntime().unattended()
	depositSession(t, rt, secondUserID, "another-imei")
	scriptTurn(rt, [][]any{
		connectionRow(statusConnected, memberZaloUID, true),
		forMember(connectionRow(statusConnected, "1800000000000000009", true),
			secondUserID, secondConnectionID),
	}, chosen(), nil)
	// The first member fails before the drain, so their turn makes ONE single-row
	// read (the bookkeeping write); the second reaches the landing pass and makes
	// two, the re-read of their consent and then the write.
	rt.tx.singleRows = [][]any{
		connectionRow(statusNeedsReconnect, memberZaloUID, true),
		forMember(connectionRow(statusConnected, "1800000000000000009", true), secondUserID, secondConnectionID),
		forMember(connectionRow(statusConnected, "1800000000000000009", true), secondUserID, secondConnectionID),
	}
	// The FIRST member has no credential on deposit; the second one works.
	working := theInbox(t)
	working.uid = "1800000000000000009"

	err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{"another-imei": working}).open())
	if err != nil {
		t.Fatalf("one member failing failed the whole tick: %v", err)
	}
	if working.drains != 1 {
		t.Fatalf("the second member was drained %d time(s)", working.drains)
	}
	if len(rt.ingested) != 2 {
		t.Fatalf("the working member landed %d record(s): %v", len(rt.ingested), keysOf(rt.ingested))
	}
}

// Every member failing is not one person's problem: it is this installation's
// egress or Zalo being down, and a tick that answered success would leave a
// fleet-wide outage with no signal anywhere but the rows.
//
// AND THE OUTAGE LEAVES EVERY ROW CONNECTED. Parking them would take the whole
// fleet out of the fleet read at once, and nothing automatic puts a member back —
// every rep would have to re-scan a QR with their phone because Zalo was down for
// one tick.
func TestEveryMemberFailingFailsTheTickAndParksNobody(t *testing.T) {
	t.Parallel()
	// BOTH credentials are on deposit: this is an outage, not a withdrawal.
	rt := tickRuntime(t)
	depositSession(t, rt, secondUserID, "another-imei")
	scriptTurn(rt, [][]any{
		connectionRow(statusConnected, memberZaloUID, true),
		forMember(connectionRow(statusConnected, "1800000000000000009", true),
			secondUserID, secondConnectionID),
	}, chosen(), nil)
	rt.tx.singleRows = [][]any{
		connectionRow(statusConnected, memberZaloUID, true),
		forMember(connectionRow(statusConnected, "1800000000000000009", true), secondUserID, secondConnectionID),
	}
	// Neither credential is at fault: nothing this installation sent reached Zalo.
	unreachable := newProvider(nil)
	unreachable.openErr = &transportError{
		Method: "GET",
		URL:    "https://wpa.chat.zalo.me/api/login/getLoginInfo",
		Err:    errors.New("dial tcp: i/o timeout"),
	}

	err := pollFleet(context.Background(), rt, unreachable.open())
	if err == nil || !strings.Contains(err.Error(), "2 connection(s)") {
		t.Fatalf("a fleet-wide outage answered %v", err)
	}
	for at, sql := range rt.tx.statements {
		if !strings.Contains(sql, "last_polled_at = now()") {
			continue
		}
		if status := rt.tx.args[at][1]; status != statusConnected {
			t.Fatalf("a fleet-wide outage parked a connection as %v", status)
		}
	}
}

// The roster is ENRICHMENT. What a failed roster call costs is a possibly-better
// display name; propagating it would cost the message.
// The roster is ENRICHMENT. What a failed roster call costs is a possibly-better
// display name; propagating it would cost the message. And with no saved name
// either, the two directions answer DIFFERENTLY on purpose: an inbound frame's own
// dName is the sender's and is used, while an outgoing frame's is the member's own
// and is left EMPTY rather than written onto the customer.
func TestARosterThatCannotBeReadCostsANameAndNeverTheMessage(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, oneMember(), [][]any{allowRow(entryID, counterpartyZaloUID, verdictAllow, "")}, nil)
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}
	opened := theInbox(t)
	opened.rosterErr = errors.New("the roster call was refused")

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: opened}).open()); err != nil {
		t.Fatalf("a failed roster call failed the tick: %v", err)
	}
	echo, inbound := capturedFrames(t)
	if len(rt.ingested) != 2 {
		t.Fatalf("a failed roster call cost a message: %v", keysOf(rt.ingested))
	}
	byDirection := map[string]extension.Record{}
	for _, rec := range rt.ingested {
		byDirection[rec.Activity.Direction] = rec
	}
	if got := byDirection[extension.DirectionInbound].Counterparty.DisplayName; got != inbound.DName {
		t.Fatalf("an inbound sender is named %q; the frame says %q", got, inbound.DName)
	}
	if got := byDirection[extension.DirectionOutbound].Counterparty.DisplayName; got != "" {
		t.Fatalf("an unnamed counterparty was given the name %q, and the member's own is %q", got, echo.DName)
	}
}

// A drain that failed transmitted the member's messages nowhere. It is an ordinary
// transient failure — the connection STAYS connected, because nothing about it is
// known to be broken.
func TestADrainThatFailedLeavesTheConnectionConnected(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, oneMember(), [][]any{allowRow(entryID, counterpartyZaloUID, verdictAllow, "Chosen")}, nil)
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}
	opened := theInbox(t)
	opened.drainErr = errors.New("the socket closed early")

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: opened}).open()); err == nil {
		t.Fatal("a drain that failed was reported as a successful tick")
	}
	_, args := theBookkeepingWrite(t, rt)
	if args[1] != statusConnected {
		t.Fatalf("a transient drain failure parked the connection as %v", args[1])
	}
	if args[2] != "poll_failed" {
		t.Fatalf("the class is %v; a drain failure is not a credential problem", args[2])
	}
}

// A member who disconnected while the tick was reading their messages. Writing
// what the turn learned would undo what they just did — the records it landed are
// theirs and stay, and the row is left exactly as they left it.
// A connection whose VERSION moved between the fleet read and the bookkeeping write
// — the member reconnected, or another writer touched the row. The write is skipped,
// because what this turn learned describes a row that no longer exists in the state
// it was read in.
//
// WHAT THIS TEST DOES NOT SAY, and used to imply: it is not a licence to capture
// after a withdrawal. Whether the messages may land at all is decided EARLIER, by
// the consent re-read after the drain — see the withdrawal tests above. This one is
// about a version race on the bookkeeping row, where the consent itself still holds.
func TestAConnectionWhoseVersionMovedSkipsTheBookkeepingWrite(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, oneMember(), chosen(), nil)
	// The consent re-read succeeds; the version-guarded write that follows it
	// matches nothing.
	rt.tx.singleRows = [][]any{stillArmed()}
	rt.tx.noRows = map[int]bool{2: true}

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: theInbox(t)}).open()); err != nil {
		t.Fatalf("a version race is not a failure, and it answered %v", err)
	}
	if len(rt.tx.audited) != 0 {
		t.Fatalf("a ledger row was written about a row that no longer exists in the state it was read in: %+v", rt.tx.audited)
	}
}

// A tick that found nothing announces nothing. One ledger row per member per
// cadence forever, to say that a schedule ran, is a history nobody can read.
func TestATickThatFoundNothingWritesNoLedgerRow(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	// The conversation's own bookmark is already past both frames in the drain.
	scriptTurn(rt, oneMember(), chosen(), nil)
	scriptCursors(rt, cursorRow(counterpartyZaloUID, inboundMsgID))
	rt.tx.singleRows = afterTheDrain(stillArmed(), stillArmed())

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: theInbox(t)}).open()); err != nil {
		t.Fatalf("the tick failed: %v", err)
	}
	if len(rt.ingested) != 0 {
		t.Fatalf("a message already below its conversation's bookmark was offered again: %v", keysOf(rt.ingested))
	}
	if len(rt.tx.audited) != 0 {
		t.Fatalf("a quiet tick wrote %d ledger row(s)", len(rt.tx.audited))
	}
}

// A message that passed the member's filter and still cannot be landed. The cursor
// moves past it and a ledger row says why: parking a whole connection on one
// malformed message would stop everything behind it, and a silent skip would make
// a provider format change look exactly like a quiet inbox.
func TestAMessageThatCanNeverLandIsDroppedAndRecordedWithoutItsContents(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, oneMember(), [][]any{allowRow(entryID, counterpartyZaloUID, verdictAllow, "Chosen")}, nil)
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}
	opened := theInbox(t)
	_, inbound := capturedFrames(t)
	// A message with no readable time of its own, which the capture seam refuses
	// and no retry can fix. Its id is above the good one, so a cursor that stopped
	// at the drop would be visible as a cursor that never reached it.
	broken := zaloInbound{
		MsgID: "8161098001436", UIDFrom: counterpartyZaloUID, IDTo: selfUID,
		DName: inbound.DName, Content: "a private message this unit cannot time",
	}
	opened.frames = []zaloInbound{inbound, broken}

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: opened}).open()); err != nil {
		t.Fatalf("one unrepresentable message failed the whole turn: %v", err)
	}
	if len(rt.ingested) != 1 {
		t.Fatalf("capture was handed %d record(s); only the representable one may land", len(rt.ingested))
	}
	dropped := rt.tx.published[0]
	if dropped.Verb != eventMessageDropped {
		t.Fatalf("the drop was announced as %q", dropped.Verb)
	}
	if strings.Contains(string(dropped.Payload), "private message") {
		t.Fatalf("the drop record carries the message this unit declined to keep:\n%s", dropped.Payload)
	}
	if !strings.Contains(string(dropped.Payload), "8161098001436") {
		t.Fatalf("the drop record does not say which message it was:\n%s", dropped.Payload)
	}
}

// The drain is given a QUIET PERIOD rather than a message count, because a push
// protocol with no end-of-queue marker has no other signal that the queue is
// empty. The per-member budget is what bounds it if the socket never goes quiet.
func TestTheDrainIsBoundedByAQuietPeriodAndTheMemberSOwnBudget(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, oneMember(), [][]any{allowRow(entryID, counterpartyZaloUID, verdictAllow, "Chosen")}, nil)
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}
	opened := theInbox(t)

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: opened}).open()); err != nil {
		t.Fatalf("the tick failed: %v", err)
	}
	if opened.quiet != drainQuiet {
		t.Fatalf("the drain was given %s to go quiet, not %s", opened.quiet, drainQuiet)
	}
	// The job's own wall clock is api/jobs.yaml's 300s; one member's turn must be
	// a fraction of it or the first stalled session spends the whole tick.
	if wallClock := declaredJobTimeout(t); perMemberBudget >= wallClock {
		t.Fatalf("one member's budget (%s) is not a sub-budget of the job's own wall clock (%s)", perMemberBudget, wallClock)
	}
}

// The DECLARED handler is the one that takes the tick. The unit's Jobs entry names
// pollInbox, and a test that only ever drove pollFleet would leave the wiring
// between them unproven — which is how a job ships that registers, ticks, and does
// nothing.
func TestTheDeclaredJobIsTheTickThatRuns(t *testing.T) {
	t.Parallel()
	rt := newRuntime().unattended()
	scriptTurn(rt, nil, nil, nil)

	var handle extension.JobHandler
	for _, job := range New().Jobs {
		if job.Name == "poll_inbox" {
			handle = job.Handle
		}
	}
	if handle == nil {
		t.Fatal("the unit declares no poll_inbox handler")
	}
	if err := handle(context.Background(), rt); err != nil {
		t.Fatalf("the declared tick failed on an installation with nobody armed: %v", err)
	}
	if _, args := rt.tx.statementMentioning(t, "capture_enabled"); len(args) != 1 {
		t.Fatalf("the declared tick did not read the fleet the way pollFleet does: %v", args)
	}
}

// The class is what a member's own screen renders, so it is this unit's word and
// never Zalo's prose — a remote party's text is not this installation's to display.
func TestAFailureClassIsThisUnitsOwnWordForWhatWentWrong(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		cause error
		class string
	}{
		"a credential that is gone":         {cause: extension.ErrForbidden, class: "session_withdrawn"},
		"a connection this unit cannot use": {cause: extension.ErrInvalid, class: "connection_unusable"},
		"a turn that ran out of time":       {cause: context.DeadlineExceeded, class: "provider_unavailable"},
		"a request Zalo never answered":     {cause: errUnanswered, class: "provider_unavailable"},
		"anything else":                     {cause: errors.New("the socket closed early"), class: "poll_failed"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := failureClass(tc.cause).Class; got != tc.class {
				t.Fatalf("%s was classed %q, want %q", name, got, tc.class)
			}
		})
	}
	if strings.Contains(failureClass(errors.New("Zalo says: mật khẩu sai")).Class, "mật khẩu") {
		t.Fatal("the provider's own message reached a class this unit renders")
	}
}

// The markers are the one thing in this unit that would otherwise grow forever: a
// row per reply, kept to answer a question that expired days ago. The sweep runs on
// the tick because the only thing that makes a marker worth keeping is a drain that
// might still read the message it names, and the drain is here.
func TestOldSendMarkersAreSweptOnTheTick(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, nil, nil, nil)

	if err := pollFleet(context.Background(), rt, newProvider(nil).open()); err != nil {
		t.Fatalf("the tick failed: %v", err)
	}
	// Swept even with nobody armed: markers age out whether or not anybody polled.
	sql, args := rt.tx.statementMentioning(t, "created_at < now()")
	if len(args) != 1 || args[0] != sentMarkerLife.String() {
		t.Fatalf("the sweep window is %v:\n%s", args, sql)
	}
	// Bounded by the PROVIDER's retention rather than chosen, and generously
	// because that window is unmeasured (DESIGN §9.1). A marker that expires while
	// an echo can still arrive costs a duplicated reply on a customer's timeline.
	if sentMarkerLife < 3*24*time.Hour {
		t.Fatalf("markers live %s, under the retention Zalo claims", sentMarkerLife)
	}
}

// A fleet-wide outage must not be reported as a housekeeping failure: the sweep is
// last, and the all-failed verdict is answered before it.
func TestAFleetWideOutageIsReportedRatherThanTheSweepThatFollowedIt(t *testing.T) {
	t.Parallel()
	rt := newRuntime().unattended()
	scriptTurn(rt, oneMember(), chosen(), nil)
	rt.tx.singleRows = [][]any{connectionRow(statusNeedsReconnect, memberZaloUID, true)}
	rt.tx.execErr = errors.New("the sweep could not run either")

	err := pollFleet(context.Background(), rt, newProvider(nil).open())
	if err == nil || !strings.Contains(err.Error(), "1 connection(s)") {
		t.Fatalf("a fleet-wide outage answered %v", err)
	}
}

// A PROVIDER THIS INSTALLATION COULD NOT REACH IS NOT A DEAD CREDENTIAL, and the two
// have opposite recoveries. needs_reconnect takes the member out of the fleet read until
// a human re-scans a QR with their own phone, so classing an egress blip or an expired
// per-member budget as one would demand that of every rep on the installation at once,
// for one tick of Zalo being unreachable. A transport failure is the same shape as a
// failed drain: the row stays connected and the turn simply failed.
func TestAProviderThatCouldNotBeReachedDoesNotDemandAReScan(t *testing.T) {
	t.Parallel()
	for name, cause := range map[string]error{
		"a session request that never reached Zalo": &transportError{
			Method: "GET",
			URL:    "https://wpa.chat.zalo.me/api/login/getLoginInfo",
			Err:    errors.New("dial tcp: connection refused"),
		},
		"a per-member budget that expired mid-handshake": context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt := tickRuntime(t)
			scriptTurn(rt, oneMember(), chosen(), nil)
			rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}
			provider := newProvider(nil)
			provider.openErr = cause

			if err := pollFleet(context.Background(), rt, provider.open()); err == nil {
				t.Fatalf("%s answered a successful tick", name)
			}
			_, args := theBookkeepingWrite(t, rt)
			if args[1] != statusConnected {
				t.Fatalf("%s parked the connection as %v, which only a human with a phone can undo", name, args[1])
			}
			if args[2] != "provider_unavailable" {
				t.Fatalf("%s was recorded as %v; the member's screen must not be told to reconnect", name, args[2])
			}
		})
	}
}

// A FRAME NOTHING CAN BOOKMARK MUST NOT POISON THE WHOLE TURN'S CURSOR WRITE. The
// advance is ONE multi-row statement and both of the columns it writes are CHECKed at
// the database — a decimal message id, and a counterparty that names somebody — so a
// single unwritable entry in the batch fails the statement and NO conversation's
// bookmark moves. The frame stays in Zalo's queue, so that is every tick, forever, plus
// one ledger row per bad frame per tick.
func TestAFrameNothingCanBookmarkDoesNotPoisonTheTurnsCursorWrite(t *testing.T) {
	t.Parallel()
	armed := withMode(connectionRow(statusConnected, memberZaloUID, true), captureEveryoneExcept)
	rt := tickRuntime(t)
	scriptTurn(rt, [][]any{armed}, nil, nil)
	rt.tx.singleRows = afterTheDrain(armed, armed)

	if err := pollFleet(context.Background(), rt, newProvider(map[string]*fakeInbox{
		memberIMEI: {uid: memberZaloUID, frames: framesNothingCanBookmark(t)},
	}).open()); err != nil {
		t.Fatalf("the tick failed: %v", err)
	}
	if len(rt.ingested) != 1 {
		t.Fatalf("the landable message did not land alone: %v", keysOf(rt.ingested))
	}
	moved := cursorsWritten(t, rt)
	if len(moved) != 1 || moved[counterpartyZaloUID] != inboundMsgID {
		t.Fatalf("the bookmarks written are %v; only the conversation that landed may have one", moved)
	}
	if _, nameless := moved[""]; nameless {
		t.Fatalf("a bookmark was written for a conversation with no counterparty: %v", moved)
	}
	if dropped := eventsVerbed(rt, eventMessageDropped); dropped != 2 {
		t.Fatalf("%d drop(s) were recorded; both unbookmarkable frames must say why they went nowhere", dropped)
	}
}

// A TURN THAT ONLY DROPPED FRAMES HAS LANDED NOTHING, so it takes a backoff rung.
// Counting a drop as a landing keeps a member on the fast cadence for as long as one
// undroppable frame sits in Zalo's queue — maximum poll frequency, forever, to re-read
// a message that can never land.
func TestATurnThatOnlyDroppedFramesTakesABackoffRung(t *testing.T) {
	t.Parallel()
	armed := withMode(connectionRow(statusConnected, memberZaloUID, true), captureEveryoneExcept)
	rt := tickRuntime(t)
	scriptTurn(rt, [][]any{armed}, nil, nil)
	rt.tx.singleRows = afterTheDrain(armed, armed)
	unlandable := framesNothingCanBookmark(t)

	if err := pollFleet(context.Background(), rt, newProvider(map[string]*fakeInbox{
		memberIMEI: {uid: memberZaloUID, frames: unlandable[:2]},
	}).open()); err != nil {
		t.Fatalf("the tick failed: %v", err)
	}
	sql, args := theBookkeepingWrite(t, rt)
	if strings.Contains(sql, duePromptly) {
		t.Fatalf("a turn that landed nothing asked to be polled again at once:\n%s", sql)
	}
	if !strings.Contains(sql, "idle_streak = idle_streak + 1") || len(args) != 5 {
		t.Fatalf("a turn that landed nothing took no backoff rung:\n%s\n%v", sql, args)
	}
}

// framesNothingCanBookmark is a drain holding the two frames whose values the cursor
// table refuses — an id that is not the decimal this provider issues, and a frame naming
// nobody at the other end — followed by one ordinary message that CAN land. The good one
// is what makes the assertion "everything else still worked".
func framesNothingCanBookmark(t *testing.T) []zaloInbound {
	t.Helper()
	_, base := capturedFrames(t)
	unorderable, nameless := base, base
	unorderable.UIDFrom, unorderable.MsgID = unchosenCounterparty, "not-a-decimal-id"
	nameless.UIDFrom, nameless.MsgID = "", "8161098001500"
	return []zaloInbound{unorderable, nameless, base}
}

// eventsVerbed counts what the turn announced under one verb.
func eventsVerbed(rt *fakeRuntime, verb string) int {
	var count int
	for _, event := range rt.tx.published {
		if event.Verb == verb {
			count++
		}
	}
	return count
}
