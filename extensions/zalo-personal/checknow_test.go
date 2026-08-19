// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// What "check now" must do, and — as everywhere in this unit — rather more about
// what it must not.
//
// THE ASSERTIONS ARE ON THE STATEMENT, not on a row read back, and that is what the
// level allows: a unit's own suite has no database. It is also where the behaviour
// actually lives. Both columns are named explicitly on purpose — clearing poll_after
// alone leaves the idle counter at, say, nine, so the FIRST empty drain after the
// press puts the member straight back on the fifteen-minute rung, which is
// indistinguishable from the control having done nothing.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// armedAndBackedOff is the state the control exists for: capture on, a long idle
// history, and a wait the member's own screen is naming at them.
func armedAndBackedOff() []any {
	return backedOffUntil(
		withIdleStreak(connectionRow(statusConnected, memberZaloUID, true), 9),
		time.Date(2026, time.August, 18, 10, 15, 0, 0, time.UTC))
}

// theCheckNowWrite is the statement the operation issued, or a failure naming what it
// did issue instead.
func theCheckNowWrite(t *testing.T, rt *fakeRuntime) (string, []any) {
	t.Helper()
	return rt.tx.statementMentioning(t, "UPDATE "+connectionTable)
}

func TestCheckNowClearsBothHalvesOfTheBackoff(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.singleRows = [][]any{armedAndBackedOff()}

	out, err := checkNow(context.Background(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("a member asking for a check sooner was refused: %v", err)
	}
	if got := jsonOf[struct {
		WasWaiting bool `json:"was_waiting"`
	}](t, out).WasWaiting; !got {
		t.Fatal("the member was inside a wait and the answer says they were not")
	}
	sql, args := theCheckNowWrite(t, rt)
	// BOTH, named separately. The wait is what the screen shows and the streak is
	// what decides the NEXT one, so a write that cleared only the first would look
	// fixed for one tick and be back on the ceiling rung after it.
	if !strings.Contains(sql, "idle_streak = 0") {
		t.Fatalf("the idle streak survived the press, so the next empty drain returns to the capped rung:\n%s", sql)
	}
	if !strings.Contains(sql, "poll_after = NULL") {
		t.Fatalf("the wait was not cleared, so nothing happens sooner:\n%s", sql)
	}
	if len(args) != 1 || args[0] != callerUserID {
		t.Fatalf("the write is not addressed to the caller alone: %v", args)
	}
}

// The clause is the SHARED one. A second spelling of "poll this member on the next
// tick" is the drift duePromptly exists to prevent, and this is the fourth caller.
func TestCheckNowUsesTheOneSpellingOfDueOnTheNextTick(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.singleRows = [][]any{armedAndBackedOff()}

	if _, err := checkNow(context.Background(), rt, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("bringing the check forward: %v", err)
	}
	sql, _ := theCheckNowWrite(t, rt)
	if !strings.Contains(sql, duePromptly) {
		t.Fatalf("this write spells the shared clause its own way:\n%s", sql)
	}
	// NO APPLICATION CLOCK. Every deadline in this unit is written and compared by
	// the database; a poll_after computed here and compared against the server's
	// now() is a scheduling bug that surfaces only once the two drift, as a member
	// who is never due again.
	if strings.Contains(sql, "$2") || strings.Contains(sql, "interval") {
		t.Fatalf("the write carries a deadline this process computed:\n%s", sql)
	}
}

// Idempotent: a member whose next check is already due has asked for something that
// is already true, and that is a reasonable thing to ask.
func TestCheckNowSucceedsWhenTheNextCheckIsAlreadyDue(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	// connectionRow carries no wait at all, which is what the database answers for a
	// poll_after that is NULL or already past.
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}

	out, err := checkNow(context.Background(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("asking for a check that was already due was treated as a mistake: %v", err)
	}
	if got := jsonOf[struct {
		WasWaiting bool `json:"was_waiting"`
	}](t, out).WasWaiting; got {
		t.Fatal("nothing was waiting and the answer claims a wait was cleared")
	}
	// The write still happens: the outcome asked for is "be due", and the row is
	// left in it either way rather than the handler deciding it knew better.
	sql, _ := theCheckNowWrite(t, rt)
	if !strings.Contains(sql, duePromptly) {
		t.Fatalf("an already-due member's press wrote something else:\n%s", sql)
	}
}

// The ROW-SCOPE MISS, which is a different answer from a permission refusal: 404,
// and it says nothing about whether anybody else has a connection.
func TestCheckNowAnswersNotFoundForAMemberWhoHasConnectedNothing(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.noRows = map[int]bool{1: true}

	_, err := checkNow(context.Background(), rt, json.RawMessage(`{}`))
	if !errors.Is(err, extension.ErrNotFound) {
		t.Fatalf("a member with no connection was answered %v, want the not-found sentinel", err)
	}
	if len(rt.tx.statements) != 1 {
		t.Fatalf("the handler wrote after finding no connection; it issued:\n%s", strings.Join(rt.tx.statements, "\n---\n"))
	}
}

// The PERMISSION refusal, asserted separately because it is a 403 and the case above
// is a 404. Object RBAC is the core's gate and lives in api/crm.yaml; what this unit
// itself refuses is an invocation with nobody behind it, which is the only principal
// question it can answer — and it must refuse before touching anything.
func TestCheckNowRefusesAnInvocationWithNobodyBehindIt(t *testing.T) {
	t.Parallel()
	rt := newRuntime().unattended()

	_, err := checkNow(context.Background(), rt, json.RawMessage(`{}`))
	if !errors.Is(err, extension.ErrForbidden) {
		t.Fatalf("a tick with no principal was answered %v, want the forbidden sentinel", err)
	}
	if len(rt.tx.statements) != 0 {
		t.Fatalf("a refused invocation still reached the database:\n%s", strings.Join(rt.tx.statements, "\n---\n"))
	}
}

// The two states where a cleared backoff would advertise a check that provably will
// not happen: the fleet read admits only `connected` members with capture armed, so
// answering "on its way" for either would be the lie this operation exists to remove.
func TestCheckNowRefusesAConnectionNoScheduledCheckWouldVisit(t *testing.T) {
	t.Parallel()
	for name, row := range map[string][]any{
		"a session zalo stopped accepting":                             connectionRow(statusNeedsReconnect, memberZaloUID, true),
		"an account nobody has withdrawn but nothing is captured from": connectionRow(statusConnected, memberZaloUID, false),
		"a withdrawn account":                                          connectionRow(statusDisconnected, memberZaloUID, false),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt := newRuntime()
			rt.tx.singleRows = [][]any{row}

			_, err := checkNow(context.Background(), rt, json.RawMessage(`{}`))
			if !errors.Is(err, extension.ErrInvalid) {
				t.Fatalf("%s was answered %v, want the refusal that says what to do instead", name, err)
			}
			if len(rt.tx.statements) != 1 {
				t.Fatalf("the schedule was written for a connection no tick visits:\n%s",
					strings.Join(rt.tx.statements, "\n---\n"))
			}
		})
	}
}

// A press is a scheduling change and not a consent change, so it leaves NO ledger row
// and NO event — the same judgement recordTurn makes for a turn that moved nothing. A
// row per press would put one field-history entry over a counter that is deliberately
// off the audit image, and tell a later reader nothing they could act on.
func TestCheckNowRecordsNoLedgerRow(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.singleRows = [][]any{armedAndBackedOff()}

	if _, err := checkNow(context.Background(), rt, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("bringing the check forward: %v", err)
	}
	if len(rt.tx.audited) != 0 || len(rt.tx.published) != 0 {
		t.Fatalf("a press wrote %d ledger row(s) and published %d event(s)", len(rt.tx.audited), len(rt.tx.published))
	}
	// The version still moves, and that is what makes a drain finishing after the
	// press decline to re-apply the backoff it just cleared: a turn's bookkeeping is
	// written under the version its fleet read saw.
	sql, _ := theCheckNowWrite(t, rt)
	if !strings.Contains(sql, "version = version + 1") {
		t.Fatalf("the press left the row's version where it was, so a turn in flight can overwrite it:\n%s", sql)
	}
}

// IT FETCHES NOTHING, which is the whole shape of this operation and the thing the
// copy on the screen promises. No credential is unsealed, no socket is opened, and
// nothing reaches capture — the scheduled tick does all of that.
func TestCheckNowSpendsNoCredentialAndFetchesNothing(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.singleRows = [][]any{armedAndBackedOff()}

	if _, err := checkNow(context.Background(), rt, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("bringing the check forward: %v", err)
	}
	if rt.secrets.gets != 0 {
		t.Fatalf("a press unsealed %d credential(s); it moves a schedule and nothing else", rt.secrets.gets)
	}
	if rt.ingestCalls != 0 {
		t.Fatalf("a press handed %d record(s) to capture; the scheduled tick is what drains", rt.ingestCalls)
	}
}

// NEITHER HALF OF THE WRITE MAY FAIL SILENTLY. A member told their check was
// brought forward when nothing was written stops watching for the message that is
// still not arriving — so a read that could not be performed is not "there is no
// connection", and a write that was refused is not a check brought forward.
func TestCheckNowReportsAFailureRatherThanAnUnwrittenSuccess(t *testing.T) {
	t.Parallel()
	t.Run("the connection could not be read", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime()
		rt.tx.rowErr = map[int]error{1: errors.New("the read could not be performed")}

		_, err := checkNow(context.Background(), rt, json.RawMessage(`{}`))
		if err == nil {
			t.Fatal("a read that failed was reported as a member with no connection")
		}
		if errors.Is(err, extension.ErrNotFound) {
			t.Fatalf("a read that failed was answered as a row-scope miss: %v", err)
		}
	})
	t.Run("the schedule could not be written", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime()
		rt.tx.singleRows = [][]any{armedAndBackedOff()}
		rt.tx.execErr = errors.New("the schedule could not be written")

		if _, err := checkNow(context.Background(), rt, json.RawMessage(`{}`)); err == nil {
			t.Fatal("a refused write was reported as a check brought forward")
		}
	})
}

// A transaction the core would not open is propagated rather than answered over: a
// member told their check was brought forward when nothing was written would stop
// watching for the messages that are not going to arrive.
func TestCheckNowPropagatesAFailureToOpenATransaction(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.txErr = errors.New("the runtime would not open a transaction")

	if _, err := checkNow(context.Background(), rt, json.RawMessage(`{}`)); err == nil {
		t.Fatal("a press that wrote nothing was reported as a check brought forward")
	}
}
