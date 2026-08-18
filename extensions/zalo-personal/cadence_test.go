// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// How often a member is worth asking, and the one property in this unit that is a
// SAFETY BOUND rather than a tuning preference: a member not polled inside the
// server's retention window loses those messages permanently, so the backoff ceiling
// trades provider load against data loss.
//
// Split from poll_test.go, which owns what a turn does, because these tests are about
// when a turn happens at all — and because the ceiling's argument deserves a test
// that holds the ARGUMENT rather than the arithmetic, which is easier to find in a
// file named for it.
//
// NO CLOCK IS INJECTED ANYWHERE HERE, and that is not an oversight: every deadline in
// this feature is written and compared by the DATABASE (`poll_after = now() +
// interval`, `poll_after <= now()`). A value written from the application's clock and
// compared against the server's is a scheduling bug that shows up only when the two
// drift, and it shows up as a member who is never due. So what these tests assert is
// the interval the statement carries, which is all the Go side decides.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ADAPTIVE CADENCE. The cost a slower tick saves is the HANDSHAKE — one HTTPS
// login-info call plus a TLS and websocket handshake per member per tick, paid
// whether or not anything arrived — so the lever is fewer ticks and not smaller
// drains.
func TestADrainThatFoundNothingNewBacksTheMemberOff(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	// Armed, allowed, and everything in the drain is already below the
	// conversation's own bookmark.
	scriptTurn(rt, [][]any{withIdleStreak(connectionRow(statusConnected, memberZaloUID, true), 2)},
		chosen(), nil)
	scriptCursors(rt, cursorRow(counterpartyZaloUID, inboundMsgID))
	rt.tx.singleRows = afterTheDrain(stillArmed(), stillArmed())

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: theInbox(t)}).open()); err != nil {
		t.Fatalf("a quiet member is not a failure, and the tick answered %v", err)
	}
	if len(rt.ingested) != 0 {
		t.Fatalf("nothing new was in the drain and %d record(s) reached capture", len(rt.ingested))
	}
	sql, args := rt.tx.statementMentioning(t, "idle_streak = idle_streak + 1")
	if !strings.Contains(sql, "poll_after = now() + $5::interval") {
		t.Fatalf("the backoff is not written from the database's own clock:\n%s", sql)
	}
	// The wait is derived from the streak the row already held, so a third empty
	// drain waits longer than the second did.
	if want := backoffFor(3).String(); args[4] != want {
		t.Fatalf("the member waits %v; after three empty drains it is %v", args[4], want)
	}
}

// A conversation that just started moving is the one worth watching, so ANY record
// puts the member back on the base cadence immediately.
func TestADrainThatLandedSomethingClearsTheBackoff(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, [][]any{withIdleStreak(connectionRow(statusConnected, memberZaloUID, true), 9)}, chosen(), nil)
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: theInbox(t)}).open()); err != nil {
		t.Fatalf("the tick failed: %v", err)
	}
	sql, _ := theBookkeepingWrite(t, rt)
	if !strings.Contains(sql, duePromptly) {
		t.Fatalf("a productive drain left the member backed off:\n%s", sql)
	}
}

// A member inside their backoff is not polled AT ALL — no row read, no credential
// unsealed, no handshake — and the database is what enforces that. The predicate is
// asserted on the statement because that is where the enforcement lives: a check in
// Go after the read would run over rows already fetched, and the handshake this
// feature exists to avoid would already have been decided on.
func TestAMemberInsideTheirBackoffIsNeverEvenEnumerated(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	// The read returns nobody, which is what a fleet of backed-off members looks
	// like from here.
	scriptTurn(rt, nil, nil, nil)
	opened := theInbox(t)

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: opened}).open()); err != nil {
		t.Fatalf("a fleet with nobody due is not a failure, and it answered %v", err)
	}
	sql, _ := rt.tx.statementMentioning(t, "capture_enabled")
	if !strings.Contains(sql, "(poll_after IS NULL OR poll_after <= now())") {
		t.Fatalf("the fleet read does not skip members inside their backoff:\n%s", sql)
	}
	// The ordering survives the new predicate: it applies among those who ARE due.
	if !strings.Contains(sql, "ORDER BY last_polled_at ASC NULLS FIRST, created_at") {
		t.Fatalf("the fairness order was lost:\n%s", sql)
	}
	if opened.drains != 0 || rt.secrets.gets != 0 {
		t.Fatalf("a fleet with nobody due opened %d socket(s) and read %d credential(s)", opened.drains, rt.secrets.gets)
	}
}

// WHAT A RUN WITH NOBODY DUE ACTUALLY COSTS, asserted rather than assumed — because a
// 60s dispatcher is only affordable if this is the floor, and the whole argument for
// shortening the cadence rests on it.
//
// TWO INDEXED STATEMENTS AND NOTHING ELSE: the fleet read, whose due predicate is
// evaluated in the database against ext_zalo_personal_connection_due, and the send-marker
// sweep, an indexed range delete that normally matches nothing. No credential is
// unsealed, no socket is opened, no roster is read, and no per-member row is written.
//
// It is asserted on the STATEMENT LIST rather than on a count, so a third statement
// added to this path names itself in the failure instead of moving a number somebody
// then updates.
func TestARunWithNobodyDueCostsTwoIndexedStatementsAndNothingElse(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, nil, nil, nil)
	opened := theInbox(t)

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: opened}).open()); err != nil {
		t.Fatalf("a run with nobody due answered %v", err)
	}
	if len(rt.tx.statements) != 2 {
		t.Fatalf("a run with nobody due issued %d statement(s):\n%s",
			len(rt.tx.statements), strings.Join(rt.tx.statements, "\n---\n"))
	}
	// The order matters as much as the count: the due check is the FIRST thing, so
	// nothing expensive can precede it.
	if !strings.Contains(rt.tx.statements[0], "poll_after IS NULL OR poll_after <= now()") {
		t.Fatalf("the first statement of a run is not the due check:\n%s", rt.tx.statements[0])
	}
	if !strings.Contains(rt.tx.statements[1], "created_at < now()") {
		t.Fatalf("the second statement is not the marker sweep:\n%s", rt.tx.statements[1])
	}
	// Nothing that costs a handshake, a credential or a provider call happened.
	switch {
	case opened.drains != 0:
		t.Fatalf("a socket was drained %d time(s)", opened.drains)
	case rt.secrets.gets != 0:
		t.Fatalf("a credential was unsealed %d time(s)", rt.secrets.gets)
	case len(rt.ingested) != 0:
		t.Fatalf("%d record(s) reached capture", len(rt.ingested))
	case len(rt.tx.audited) != 0:
		t.Fatalf("%d ledger row(s) were written to say a schedule ran", len(rt.tx.audited))
	}
}

// The due predicate has an INDEX behind it, or a 60s dispatcher turns into a table scan
// of every connection in the installation once a minute — which is the cost this whole
// split exists to avoid, moved into the database.
//
// The index is asserted against the migration that creates it, because that file is the
// only place the two facts can be held together: the columns the read filters and orders
// by, and the columns the index leads with.
func TestTheDuePredicateHasAnIndexBehindIt(t *testing.T) {
	t.Parallel()
	migration, err := os.ReadFile("migrations/0003_tick_state.up.sql")
	if err != nil {
		t.Fatalf("reading the tick-state migration: %v", err)
	}
	ddl := strings.Join(strings.Fields(string(migration)), " ")
	if !strings.Contains(ddl, "ON ext.ext_zalo_personal_connection (workspace_id, poll_after, last_polled_at)") {
		t.Fatal("the connection table has no index leading with the columns the fleet read filters and orders by, " +
			"so a dispatcher run scans every connection in the installation")
	}
}

// A turn that FAILED touches neither column. Incrementing the idle counter on a
// failure would conflate "nothing to say" with "cannot ask", so a member whose
// session died and was repaired would be polled slowly for hours after they fixed
// it — while the case that genuinely must not be retried on a cadence is already
// fully backed off by leaving the fleet as needs_reconnect.
func TestAFailedTurnDoesNotCountAsAQuietOne(t *testing.T) {
	t.Parallel()
	rt := tickRuntime(t)
	scriptTurn(rt, [][]any{withIdleStreak(connectionRow(statusConnected, memberZaloUID, true), 3)}, chosen(), nil)
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true)}
	opened := theInbox(t)
	opened.drainErr = errors.New("the socket closed early")

	if err := pollFleet(context.Background(), rt,
		newProvider(map[string]*fakeInbox{memberIMEI: opened}).open()); err == nil {
		t.Fatal("a drain that failed was reported as a successful tick")
	}
	// The ASSIGNMENT forms, not the bare names: the projection this statement
	// returns carries idle_streak, so a substring check on the name alone would pass
	// for the wrong reason.
	sql, _ := theBookkeepingWrite(t, rt)
	if strings.Contains(sql, "idle_streak =") || strings.Contains(sql, "poll_after =") {
		t.Fatalf("a failure was recorded as a quiet drain:\n%s", sql)
	}
}

// The shape of the ladder, and the ceiling.
func TestTheBackoffDoublesFromTheBaseDrainIntervalAndStopsAtTheCeiling(t *testing.T) {
	t.Parallel()
	for streak, want := range map[int]time.Duration{
		0:    0,
		1:    baseDrainInterval,
		2:    2 * baseDrainInterval,
		3:    maxPollBackoff,
		50:   maxPollBackoff,
		1000: maxPollBackoff,
	} {
		if got := backoffFor(streak); got != want {
			t.Fatalf("after %d empty drains the wait is %s, want %s", streak, got, want)
		}
	}
	// A negative streak cannot happen — the column's CHECK refuses it — and it must
	// not silently become the ceiling if it ever does.
	if backoffFor(-1) != 0 {
		t.Fatalf("a negative streak waits %s", backoffFor(-1))
	}
}

// THE SAFETY BOUND, held as an invariant rather than as arithmetic. A member not
// polled inside the server's retention window loses those messages permanently, so
// this ceiling trades provider load against DATA LOSS. This test is what stops
// somebody raising the cap toward the CLAIMED three-day retention: the only window
// anybody has measured is about an hour, and it was measured once.
//
// THE GAP IS THREE TERMS, and the middle one used to be missing — the old assertion
// was optimistic by one dispatcher cadence, which at the old 300s dispatcher meant it
// asserted 20m for a real worst gap of 25m. A member waits out their own backoff, then
// waits for the next dispatcher run to notice they are due, then waits for the fairness
// order to reach them inside one tick.
func TestTheBackoffCeilingStaysWellUnderTheOnlyRetentionAnybodyHasMeasured(t *testing.T) {
	t.Parallel()
	worstGap := maxPollBackoff + declaredDispatcherCadence(t) + declaredJobTimeout(t)
	if worstGap > measuredRetentionFloor/2 {
		t.Fatalf("the longest a member can go unpolled is %s, over half the %s anybody has actually measured — "+
			"raising the cap toward the three days Zalo CLAIMS loses a quiet member's messages if the hour is the real window",
			worstGap, measuredRetentionFloor)
	}
	if maxPollBackoff <= baseDrainInterval {
		t.Fatalf("the ceiling (%s) is not above the base drain interval (%s), so the backoff buys nothing",
			maxPollBackoff, baseDrainInterval)
	}
}

// THE DISPATCHER MUST RUN AT LEAST AS OFTEN AS A MEMBER CAN BECOME DUE, and that — not
// equality — is the whole relationship between the contract's cadence and the backoff
// ladder's first rung.
//
// The two used to be asserted EQUAL, which is what tied a member's handshake interval to
// the scheduler's and made the dispatcher tick every five minutes. They measure
// different things: this one is one indexed query, the other is a TLS and WebSocket
// handshake per member. A cadence LONGER than the base interval would mean a member due
// on their own schedule waiting on the scheduler's instead; shorter costs nothing.
func TestTheDispatcherRunsAtLeastAsOftenAsAMemberCanBecomeDue(t *testing.T) {
	t.Parallel()
	declared := declaredDispatcherCadence(t)
	if declared > baseDrainInterval {
		t.Fatalf("the dispatcher ticks every %s while a member can become due every %s, so a member who is due "+
			"waits on the schedule rather than on their own backoff", declared, baseDrainInterval)
	}
	if declared <= 0 {
		t.Fatalf("the dispatcher declares a cadence of %s, so nothing ticks at all", declared)
	}
	// AND IT MUST BE FAST ENOUGH THAT A SAVE LOOKS LIKE IT DID SOMETHING. This is the
	// assertion that stops the cadence being "optimised" back to five minutes: saving
	// clears the backoff so that capture acts next, and a dispatcher slower than this
	// leaves the member watching nothing happen. It saves nothing to raise it —
	// handshake volume is bounded by poll_after either way — so the only thing a longer
	// cadence buys is a rep's first impression of a broken feature.
	if declared > saveVisibleWithin {
		t.Fatalf("the dispatcher ticks every %s, so a member who saves waits up to that long to see anything happen "+
			"(%s is the bound). Raising this does not reduce handshake volume — poll_after does that — so it costs "+
			"the first impression and saves nothing; if provider load needs cutting, the lever is maxPollBackoff",
			declared, saveVisibleWithin)
	}
}

// declaredDispatcherCadence and declaredJobTimeout are the two schedule numbers
// api/jobs.yaml publishes, READ FROM THE CONTRACT rather than restated in Go.
//
// Both terms of the safety bound come from the document that governs them, which is the
// lesson of this unit's one real drift: a hand-copied contract value is a second source
// of truth that goes stale silently and takes the invariant's meaning with it.
func declaredDispatcherCadence(t *testing.T) time.Duration {
	t.Helper()
	return declaredSchedule(t, "cadence")
}

// declaredJobTimeout is one tick's wall clock — the time the fairness order has to reach
// a member who has just become due.
func declaredJobTimeout(t *testing.T) time.Duration {
	t.Helper()
	return declaredSchedule(t, "timeout")
}

// declaredSchedule reads the FIRST occurrence of a duration setting, which is the
// dispatcher's: the fragment declares the cadenced dispatcher before its worker child,
// and only the dispatcher carries a cadence at all.
func declaredSchedule(t *testing.T, setting string) time.Duration {
	t.Helper()
	fragment, err := os.ReadFile("api/jobs.yaml")
	if err != nil {
		t.Fatalf("reading the job contract: %v", err)
	}
	found := regexp.MustCompile(`(?m)^\s*` + setting + `:\s*(\S+)\s*$`).FindSubmatch(fragment)
	if found == nil {
		t.Fatalf("api/jobs.yaml declares no %s, and the schedule's safety bound is measured against it", setting)
	}
	declared, err := time.ParseDuration(string(found[1]))
	if err != nil {
		t.Fatalf("the declared %s %q is not a duration: %v", setting, found[1], err)
	}
	return declared
}

// WHAT THE SCREEN NEEDS TO ANSWER "IS THIS WORKING?", which is the question the whole
// cadence change is downstream of: a rep who saved a mode and saw nothing had no way to
// tell a working connector from a broken one.
//
// Three facts, and the third is the one that was missing. "Last checked" alone cannot
// distinguish a quiet minute from a quarter-hour of deliberate silence, and the second
// is what reads as a dead feature.
func TestTheStatusResponseCanExplainAQuietConnection(t *testing.T) {
	t.Parallel()
	waitingUntil := time.Date(2026, time.August, 18, 19, 51, 0, 0, time.UTC)
	polledAt := waitingUntil.Add(-15 * time.Minute)

	row := backedOffUntil(connectionRow(statusConnected, memberZaloUID, true), waitingUntil)
	row[8] = polledAt
	rt := newRuntime()
	rt.tx.singleRows = [][]any{row, {0}}

	out, err := status(context.Background(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("reading the status: %v", err)
	}
	got := jsonOf[struct {
		Connection struct {
			LastPolledAt   string `json:"last_polled_at"`
			NextCheckAfter string `json:"next_check_after"`
			LastErrorClass string `json:"last_error_class"`
		} `json:"connection"`
	}](t, out)
	if got.Connection.LastPolledAt != polledAt.Format(time.RFC3339) {
		t.Fatalf("last_polled_at rendered as %q", got.Connection.LastPolledAt)
	}
	if got.Connection.NextCheckAfter != waitingUntil.Format(time.RFC3339) {
		t.Fatalf("a member waiting out a backoff reads as %q; the screen cannot explain the wait without it",
			got.Connection.NextCheckAfter)
	}
	// THE DATABASE ANSWERS WHETHER THE WAIT IS STILL AHEAD, so the screen never has to
	// compare a timestamp against its own clock and never renders a next check in the
	// past.
	sql, _ := rt.tx.statementMentioning(t, "capture_mode_since, last_polled_at")
	if !strings.Contains(sql, "CASE WHEN poll_after > now() THEN poll_after END") {
		t.Fatalf("the read hands over a raw poll_after for a client to interpret:\n%s", sql)
	}
}

// A member who is due now says so by ABSENCE, which is the same idiom the rest of this
// response uses — no connection, no last poll, no error class are all absences too. A
// screen that sees no next check knows the answer is "on the next run".
func TestAMemberWhoIsDueNowReportsNoNextCheck(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true), {0}}

	out, err := status(context.Background(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("reading the status: %v", err)
	}
	if strings.Contains(string(out), "next_check_after") {
		t.Fatalf("a member who is due now was given a next check:\n%s", out)
	}
}
