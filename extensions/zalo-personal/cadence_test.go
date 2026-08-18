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
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
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
		chosenAt(inboundMsgID), nil)
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
func TestTheBackoffDoublesFromTheBaseCadenceAndStopsAtTheCeiling(t *testing.T) {
	t.Parallel()
	for streak, want := range map[int]time.Duration{
		0:    0,
		1:    basePollInterval,
		2:    2 * basePollInterval,
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
// anybody has measured is about an hour (DESIGN §9.1, issue #1692), and the worst gap
// between two drains of one member is the cap plus a whole tick's wall clock.
func TestTheBackoffCeilingStaysWellUnderTheOnlyRetentionAnybodyHasMeasured(t *testing.T) {
	t.Parallel()
	worstGap := maxPollBackoff + jobWallClock
	if worstGap > measuredRetentionFloor/2 {
		t.Fatalf("the longest a member can go unpolled is %s, over half the %s anybody has actually measured — "+
			"raising the cap toward the three days Zalo CLAIMS loses a quiet member's messages if the hour is the real window "+
			"(DESIGN §9.1, issue #1692)", worstGap, measuredRetentionFloor)
	}
	if maxPollBackoff <= basePollInterval {
		t.Fatalf("the ceiling (%s) is not above the base cadence (%s), so the backoff buys nothing",
			maxPollBackoff, basePollInterval)
	}
}

// The base cadence is declared in api/jobs.yaml and restated in Go because a
// function cannot read the contract. This is what holds the two equal — a backoff
// that started from a stale copy of the cadence would be a ladder whose first rung
// is not where the scheduler actually stands.
func TestTheBaseCadenceIsTheOneTheContractDeclares(t *testing.T) {
	t.Parallel()
	fragment, err := os.ReadFile("api/jobs.yaml")
	if err != nil {
		t.Fatalf("reading the job contract: %v", err)
	}
	found := regexp.MustCompile(`(?m)^\s*cadence:\s*(\S+)\s*$`).FindSubmatch(fragment)
	if found == nil {
		t.Fatal("api/jobs.yaml declares no cadence, and the backoff ladder starts from it")
	}
	declared, err := time.ParseDuration(string(found[1]))
	if err != nil {
		t.Fatalf("the declared cadence %q is not a duration: %v", found[1], err)
	}
	if declared != basePollInterval {
		t.Fatalf("the contract ticks every %s and the backoff ladder starts at %s", declared, basePollInterval)
	}
}

// THE ERROR CLASSES THE CONTRACT PUBLISHES ARE THE ONES THE CODE EMITS.
//
// The set is enumerated in api/crm.yaml because a class list a reader has to
// cross-check against Go drifts — and it already had: the contract described
// `session_evicted`, which nothing here has ever produced, so any client or model
// reading the contract was told about a state it would never see. This derives the
// set from the contract and holds failureClass to it, in both directions.
func TestEveryPublishedErrorClassIsOneTheCodeCanEmit(t *testing.T) {
	t.Parallel()
	fragment, err := os.ReadFile("api/crm.yaml")
	if err != nil {
		t.Fatalf("reading the contract: %v", err)
	}
	block := regexp.MustCompile(`(?s)last_error_class:.*?enum:\n((?:\s+- \w+\n)+)`).FindSubmatch(fragment)
	if block == nil {
		t.Fatal("the contract no longer enumerates last_error_class, so nothing holds the two sets equal")
	}
	published := map[string]bool{}
	for _, line := range regexp.MustCompile(`- (\w+)`).FindAllSubmatch(block[1], -1) {
		published[string(line[1])] = true
	}
	// Every class the code can produce, from the one function that produces them.
	emitted := map[string]bool{}
	for _, cause := range []error{
		extension.ErrForbidden, extension.ErrInvalid, context.DeadlineExceeded,
		errUnanswered, errors.New("something else entirely"),
	} {
		emitted[failureClass(cause)] = true
	}
	for class := range emitted {
		if !published[class] {
			t.Fatalf("the code emits %q and the contract does not publish it, so a client sees a class it was never told about", class)
		}
	}
	for class := range published {
		if !emitted[class] {
			t.Fatalf("the contract publishes %q and nothing emits it — which is the drift this test exists to catch", class)
		}
	}
}
