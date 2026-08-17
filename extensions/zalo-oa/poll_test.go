// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The scheduled pull. What every test here is really about is the same rule: the
// cursor moves after the ingest and never before it, and a tick that failed part
// way moves nothing — because for this provider a message the cursor skipped is
// gone from the API in nine days with no depth left to page to.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// connectedRuntime is a tick's starting state: a live credential on deposit for
// the authorizing admin, an app secret, and the connection row the tick reads.
func connectedRuntime(t *testing.T, from cursor) *fakeRuntime {
	t.Helper()
	rt := newRuntime().unattended()
	// The credential belongs to the AUTHORIZING ADMIN, and the tick has no caller
	// of its own — which is the whole custody arrangement in one fixture.
	if err := sealTokens(t.Context(), rt, adminUserID, livePair(at(20*time.Hour))); err != nil {
		t.Fatalf("sealing the credential: %v", err)
	}
	rt.secrets.stored["workspace//"+appSecretKey] = []byte("secret")
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, nil, from)}
	return rt
}

// A tick lands what it read, on the AUTHORIZING ADMIN's authority, and only then
// writes the cursor.
func TestATickLandsWhatItReadOnTheAuthorizingAdminsAuthority(t *testing.T) {
	rt := connectedRuntime(t, cursor{})
	rt.tx.singleRows = append(rt.tx.singleRows, connectionRow(statusConnected, nil, cursor{floor: 1002}))
	fake := newZaloFake(t)
	fake.chatPages = [][]map[string]any{{
		message("m2", 1002, srcUserToOA, "hai"),
		message("m1", 1001, srcUserToOA, "một"),
	}}

	if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("pollConnection: %v", err)
	}
	if len(rt.ingested) != 2 {
		t.Fatalf("%d records were handed to the core, want 2", len(rt.ingested))
	}
	for _, on := range rt.ingestedOn {
		if string(on) != adminUserID {
			t.Fatalf("a record was landed on %q; every one runs on the authorizing administrator's live authority", on)
		}
	}
	// Oldest first, so what is decided about is a contiguous run upward from the
	// floor — which is what makes a tick that stops half way safe.
	if rt.ingested[0].Key != fixtureOAID+":m1" {
		t.Fatalf("the first record landed was %q, want the oldest", rt.ingested[0].Key)
	}
	_, args := rt.tx.statementMentioning(t, "high_water_mark = $2")
	if args[1] != int64(1002) {
		t.Fatalf("the floor was written as %v, want the newest message decided about", args[1])
	}
}

// The ingest happens with NO transaction of this unit's open. The pipeline opens
// its own, so calling it from inside one takes a second connection while holding
// one — which on a small pool does not fail, it hangs.
func TestATickNeverIngestsFromInsideItsOwnTransaction(t *testing.T) {
	rt := connectedRuntime(t, cursor{})
	rt.tx.singleRows = append(rt.tx.singleRows, connectionRow(statusConnected, nil, cursor{floor: 1000}))
	fake := newZaloFake(t)
	fake.chatPages = [][]map[string]any{{message("m1", 1000, srcUserToOA, "một")}}

	if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		// The fake answers ErrNestedIngest exactly as the core does, so a handler
		// that nested would fail here rather than in production.
		t.Fatalf("pollConnection: %v", err)
	}
	if len(rt.ingested) == 0 {
		t.Fatal("nothing was ingested, so the nesting rule was not actually exercised")
	}
}

// A tick that fails part way through the ingest writes NO cursor. The region is
// walked again next time, where everything that already landed is a deduplicated
// no-op on its natural key.
func TestATickThatFailsPartWayMovesNothing(t *testing.T) {
	rt := connectedRuntime(t, cursor{floor: 900})
	rt.ingestErr, rt.ingestFrom = extension.ErrForbidden, 2
	fake := newZaloFake(t)
	fake.chatPages = [][]map[string]any{{
		message("m3", 1003, srcUserToOA, "ba"),
		message("m2", 1002, srcUserToOA, "hai"),
		message("m1", 1001, srcUserToOA, "một"),
	}}

	err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0)))
	if err == nil {
		t.Fatal("a tick whose ingest was refused reported success")
	}
	for _, sql := range rt.tx.statements {
		if strings.Contains(sql, "high_water_mark = $2") {
			t.Fatalf("the cursor was written by a tick that did not finish: %s", sql)
		}
	}
}

// A message this unit cannot represent is DROPPED rather than retried forever:
// it will never land, so stopping on it would park the connection on one
// malformed message. The drop is announced, because a provider change that made
// every message unrepresentable would otherwise look exactly like a quiet account.
func TestAnUnrepresentableMessageIsDroppedAndAnnouncedRatherThanRetried(t *testing.T) {
	rt := connectedRuntime(t, cursor{floor: 900})
	rt.tx.singleRows = append(rt.tx.singleRows, connectionRow(statusConnected, nil, cursor{floor: 1000}))
	fake := newZaloFake(t)
	broken := message("", 1000, srcUserToOA, "no id at all")
	fake.chatPages = [][]map[string]any{{broken}}

	if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("pollConnection: %v", err)
	}
	if len(rt.ingested) != 0 {
		t.Fatal("a message with no provider id was handed to the core, where it would land a second copy on every poll")
	}
	if !published(rt, eventRecordDropped) {
		t.Fatalf("the drop was not announced; the events were %v", verbs(rt))
	}
	// And the cursor still moves past it: a cursor that only advanced past LANDED
	// messages would re-page a feed of malformed ones forever.
	_, args := rt.tx.statementMentioning(t, "high_water_mark = $2")
	if args[1] != int64(1000) {
		t.Fatalf("the floor was written as %v, want it past the message that was dropped", args[1])
	}
}

// A record the CORE refuses as invalid is the same case: this unit built
// something the core will never accept, and retrying it on a cadence parks the
// connection on one message.
func TestARecordTheCoreRefusesAsInvalidIsDroppedRatherThanRetried(t *testing.T) {
	rt := connectedRuntime(t, cursor{floor: 900})
	rt.tx.singleRows = append(rt.tx.singleRows, connectionRow(statusConnected, nil, cursor{floor: 1000}))
	rt.ingestErr, rt.ingestFrom = extension.ErrInvalid, 1
	fake := newZaloFake(t)
	fake.chatPages = [][]map[string]any{{message("m1", 1000, srcUserToOA, "một")}}

	if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("pollConnection: %v", err)
	}
	if !published(rt, eventRecordDropped) {
		t.Fatalf("a refused record was not announced as dropped; the events were %v", verbs(rt))
	}
}

// A tick with no connection, or one that is parked, does nothing and reports
// nothing. The parked states already say what a human must do — a tick that
// failed on them would fill a log with the fact that somebody has not repaired
// something yet.
func TestATickOverAConnectionThatIsNotWorkingDoesNothingQuietly(t *testing.T) {
	for name, arm := range map[string]struct{ status string }{
		"no connection at all":  {""},
		"one awaiting reauth":   {statusReauth},
		"one whose tier lapsed": {statusTierLapse},
	} {
		t.Run(name, func(t *testing.T) {
			rt := newRuntime().unattended()
			if arm.status == "" {
				rt.tx.noRows = map[int]bool{1: true}
			} else {
				rt.tx.singleRows = [][]any{connectionRow(arm.status, nil, cursor{})}
			}
			fake := newZaloFake(t)

			if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
				t.Fatalf("pollConnection: %v", err)
			}
			if len(fake.calls) != 0 {
				t.Fatalf("the provider was reached %v for a connection that cannot be polled", fake.calls)
			}
		})
	}
}

// THE TIER CAN LAPSE UNDER A WORKING CONNECTION. The token is fine and the
// account's package is not, so the poll parks at a state that sends an operator
// to renew the package rather than to re-authorize a credential that works.
func TestAPackageThatLapsesUnderAWorkingConnectionParksAsATierProblem(t *testing.T) {
	rt := connectedRuntime(t, cursor{floor: 900})
	rt.tx.singleRows = append(rt.tx.singleRows, connectionRow(statusTierLapse, nil, cursor{floor: 900}))
	fake := newZaloFake(t)
	fake.errorCode = codeTierTooLow

	if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("pollConnection: %v", err)
	}
	_, args := rt.tx.statementMentioning(t, "last_error_class = $3")
	if args[1] != statusTierLapse {
		t.Fatalf("the connection was parked as %q, want %q — the credential is not the problem", args[1], statusTierLapse)
	}
	if args[2] != "package_too_low" {
		t.Fatalf("the class recorded was %q, want it to name the package", args[2])
	}
	if !published(rt, eventTierLapsed) {
		t.Fatalf("the lapse was not announced; the events were %v", verbs(rt))
	}
}

// A credential the provider rejects parks at reauth_required instead, because the
// remedy is a different one and it is somebody else's afternoon.
func TestACredentialTheProviderRejectsParksAsACredentialProblem(t *testing.T) {
	rt := connectedRuntime(t, cursor{floor: 900})
	rt.tx.singleRows = append(rt.tx.singleRows, connectionRow(statusReauth, nil, cursor{floor: 900}))
	fake := newZaloFake(t)
	fake.errorCode = codeTokenExpired

	if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("pollConnection: %v", err)
	}
	_, args := rt.tx.statementMentioning(t, "last_error_class = $3")
	if args[1] != statusReauth || args[2] != "token_rejected" {
		t.Fatalf("the connection was parked as %v, want reauth_required naming the credential", args)
	}
}

// A provider that is merely unreachable does NOT park: the tick records the class
// so the screen can say so, and reports the failure, and the next tick tries
// again with the cursor exactly where it was.
func TestAnUnreachableProviderRecordsAClassWithoutParkingTheConnection(t *testing.T) {
	rt := connectedRuntime(t, cursor{floor: 900})
	fake := newZaloFake(t)
	fake.errorCode = codeRateLimited

	err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0)))
	if err == nil {
		t.Fatal("a tick that could not reach the provider reported success")
	}
	sql, args := rt.tx.statementMentioning(t, "last_error_class = $2")
	if strings.Contains(sql, "status = ") {
		t.Fatalf("an unreachable provider parked the connection: %s", sql)
	}
	if args[1] != "provider_unavailable" {
		t.Fatalf("the class recorded was %q", args[1])
	}
}

// Another caller holding the renewal lease is not a failed tick: the work is
// being done, elsewhere, right now, and the next tick finds a fresh token.
func TestATickThatLosesTheRenewalLeaseReportsNoFailure(t *testing.T) {
	rt := newRuntime().unattended()
	if err := sealTokens(t.Context(), rt, adminUserID, livePair(at(-time.Hour))); err != nil {
		t.Fatalf("sealing the credential: %v", err)
	}
	rt.secrets.stored["workspace//"+appSecretKey] = []byte("secret")
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, nil, cursor{})}
	rt.tx.noRows = map[int]bool{2: true}
	fake := newZaloFake(t)

	if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("a tick that found a renewal in flight reported %v; the work is being done elsewhere", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("the provider was reached with a credential this tick had not renewed: %v", fake.calls)
	}
}

// A credential that is simply gone parks with a class that says so, rather than
// failing the tick forever against a deposit nobody is going to restore on its
// own.
func TestATickWithNoCredentialOnDepositParksWithTheReasonNamed(t *testing.T) {
	rt := newRuntime().unattended()
	rt.tx.singleRows = [][]any{
		connectionRow(statusConnected, nil, cursor{}),
		connectionRow(statusReauth, nil, cursor{}),
	}

	if err := pollConnection(t.Context(), rt, newZaloFake(t).dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("pollConnection: %v", err)
	}
	_, args := rt.tx.statementMentioning(t, "last_error_class = $3")
	if args[2] != "credential_missing" {
		t.Fatalf("the class recorded was %q, want it to name the missing deposit", args[2])
	}
}

// A FIRST poll reads one page and opens no backlog: connecting an account brings
// the CRM what arrives from now on, and importing an account's history is a
// decision with a cost that an authorization click does not make.
func TestAFirstPollReadsOnePageAndImportsNoHistory(t *testing.T) {
	rt := connectedRuntime(t, cursor{})
	rt.tx.singleRows = append(rt.tx.singleRows, connectionRow(statusConnected, nil, cursor{floor: 2000}))
	fake := newZaloFake(t)
	fake.chatPages = [][]map[string]any{tenFrom(2000), tenFrom(1000), tenFrom(500)}

	if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("pollConnection: %v", err)
	}
	if fake.calls["/v2.0/oa/listrecentchat"] != 1 {
		t.Fatalf("a first poll read %d pages, want exactly one", fake.calls["/v2.0/oa/listrecentchat"])
	}
	_, args := rt.tx.statementMentioning(t, "backfill_before = NULLIF")
	if args[2] != int64(0) {
		t.Fatalf("a first poll left a backlog of %v behind it", args[2])
	}
}

// The label and the tier evidence are refreshed on every tick, because they are
// what the provider says NOW: an account renamed at oa.zalo.me, or a package
// renewed for another year, shows on the screen without anybody re-authorizing.
func TestEveryTickRefreshesTheAccountLabelAndTheTierEvidence(t *testing.T) {
	rt := connectedRuntime(t, cursor{floor: 900})
	rt.tx.singleRows = append(rt.tx.singleRows, connectionRow(statusConnected, nil, cursor{floor: 1000}))
	fake := newZaloFake(t)
	fake.chatPages = [][]map[string]any{{message("m1", 1000, srcUserToOA, "một")}}

	if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("pollConnection: %v", err)
	}
	_, args := rt.tx.statementMentioning(t, "package_valid_through = $8")
	if args[5] != "NFQ" || args[6] != "Tăng trưởng" || args[7] != "12/08/2027" {
		t.Fatalf("the tick wrote %v, want what the provider says now", args[5:8])
	}
}

// A tick that moved no cursor writes no ledger row. Recording one would write a
// row per cadence forever to say that a schedule ran.
func TestATickThatFoundNothingRecordsNothing(t *testing.T) {
	rt := connectedRuntime(t, cursor{floor: 2000})
	rt.tx.singleRows = append(rt.tx.singleRows, connectionRow(statusConnected, nil, cursor{floor: 2000}))
	fake := newZaloFake(t)
	fake.chatPages = [][]map[string]any{{message("old", 1500, srcUserToOA, "already decided")}}

	if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("pollConnection: %v", err)
	}
	if published(rt, eventPolled) {
		t.Fatalf("a quiet tick announced itself; the events were %v", verbs(rt))
	}
}

// A tick whose connection was removed or re-authorized underneath it writes
// nothing: what it learned is about a connection that no longer exists in the
// state it was read in, and writing it would undo whatever the administrator just
// did.
func TestATickWhoseConnectionMovedUnderItDoesNotWriteOverIt(t *testing.T) {
	rt := connectedRuntime(t, cursor{floor: 900})
	// The cursor write matches no row: the version moved, or the row is gone.
	rt.tx.noRows = map[int]bool{3: true}
	fake := newZaloFake(t)
	fake.chatPages = [][]map[string]any{{message("m1", 1000, srcUserToOA, "một")}}

	if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("pollConnection: %v", err)
	}
	if published(rt, eventPolled) {
		t.Fatalf("a tick announced a cursor it did not write; the events were %v", verbs(rt))
	}
	if len(rt.ingested) != 1 {
		t.Fatal("the records the tick did land must stay landed; they are the record of conversations that happened")
	}
}

// The failure classes are the vocabulary the screen renders, and the provider's
// own message is deliberately not among them.
func TestEveryFailureClassIsThisUnitsOwnVocabulary(t *testing.T) {
	for _, arm := range []struct {
		cause error
		want  string
	}{
		{errUnauthorized, "token_rejected"},
		{errTierTooLow, "package_too_low"},
		{errAPINotRegistered, "api_not_registered"},
		{errTransient, "provider_unavailable"},
		{errProvider, "provider_answer_unusable"},
		{extension.ErrForbidden, "member_not_permitted"},
		{extension.ErrInvalid, "connection_unusable"},
		{errors.New("something else entirely"), "poll_failed"},
	} {
		if got := failureClass(arm.cause); got != arm.want {
			t.Fatalf("failureClass(%v) = %q, want %q", arm.cause, got, arm.want)
		}
	}
}

// A tick reads THE NEWEST REGION FIRST and spends what is left of its budget on
// the backlog under it, so an installation catching up still sees this morning's
// messages this morning.
func TestATickReadsWhatIsNewFirstAndThenFillsTheBacklogUnderIt(t *testing.T) {
	open := cursor{floor: 100, gap: 800, top: 2000, offset: 20}
	rt := connectedRuntime(t, open)
	rt.tx.singleRows = append(rt.tx.singleRows, connectionRow(statusConnected, nil, cursor{floor: 2500}))
	fake := newZaloFake(t)
	fake.chatPages = [][]map[string]any{
		// The newest page, above everything already decided.
		{message("new", 2500, srcUserToOA, "sáng nay")},
		// And the page the backlog resumes on, under the gap.
		{message("old", 700, srcUserToOA, "tuần trước")},
	}

	if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("pollConnection: %v", err)
	}
	landed := map[string]bool{}
	for _, rec := range rt.ingested {
		landed[rec.Key] = true
	}
	for _, want := range []string{fixtureOAID + ":new", fixtureOAID + ":old"} {
		if !landed[want] {
			t.Fatalf("%q was not landed; the tick handled %v", want, landed)
		}
	}
	// The backlog closed, so the floor and the top collapse back into one number
	// and nothing is left describing a hole.
	_, args := rt.tx.statementMentioning(t, "backfill_offset = $5")
	if args[1] != int64(2500) {
		t.Fatalf("the floor was written as %v, want it at the newest message once the backlog closed", args[1])
	}
	if args[2] != int64(0) || args[3] != int64(0) || args[4] != 0 {
		t.Fatalf("a closed backlog left %v behind it, want the gap, the top and the resume hint all cleared", args[2:5])
	}
}

// A backlog that outlasts the tick's budget leaves the FLOOR where it was, so
// nothing under the hole is ever skipped.
func TestABacklogThatOutlastsTheBudgetHoldsTheFloor(t *testing.T) {
	open := cursor{floor: 100, gap: 800, top: 2000, offset: 20}
	rt := connectedRuntime(t, open)
	// A ceiling the backlog below cannot be finished inside: the account read and
	// three pages, against a hole more than thirty messages deep.
	rt.tx.singleRows[0] = withBudget(rt.tx.singleRows[0], 4)
	rt.tx.singleRows = append(rt.tx.singleRows, connectionRow(statusConnected, nil, open))
	fake := newZaloFake(t)
	pages := [][]map[string]any{{message("new", 2500, srcUserToOA, "sáng nay")}}
	// Full pages all the way down: the backlog cannot be finished inside the
	// budget this tick has left.
	for page := range 12 {
		pages = append(pages, tenFrom(790-int64(page*10)))
	}
	fake.chatPages = pages

	if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("pollConnection: %v", err)
	}
	_, args := rt.tx.statementMentioning(t, "backfill_offset = $5")
	if args[1] != int64(100) {
		t.Fatalf("the floor moved to %v while a hole was still open under it; every message in that hole would be unreachable", args[1])
	}
	if args[2] == int64(0) {
		t.Fatal("the tick reported no backlog after running out of budget with one still open")
	}
}

// THE TICK'S REQUEST CEILING COMES FROM THE ROW, not from a constant compiled
// into the walk.
//
// Two installations on the same build need different numbers — one has four
// conversations and one has four hundred — and the number an operator reads on
// the status screen has to be the number the tick actually obeys, or the screen
// is telling them about something else.
func TestATickSpendsTheRequestBudgetOnItsOwnRow(t *testing.T) {
	rt := connectedRuntime(t, cursor{floor: 100})
	rt.tx.singleRows[0] = withBudget(rt.tx.singleRows[0], 4)
	rt.tx.singleRows = append(rt.tx.singleRows, connectionRow(statusConnected, nil, cursor{floor: 100}))
	fake := newZaloFake(t)
	// Full pages all the way down, so nothing but the budget can stop the walk.
	var pages [][]map[string]any
	for page := range 10 {
		pages = append(pages, tenFrom(2500-int64(page*10)))
	}
	fake.chatPages = pages

	if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("pollConnection: %v", err)
	}
	// Four requests: one read the account, and the three that were left read
	// pages. Reading the account is not free, and a budget that ignored it would
	// spend one request more than the operator authorized.
	if got := fake.calls["/v2.0/oa/getoa"]; got != 1 {
		t.Fatalf("the tick read the account %d times, want once", got)
	}
	if got := fake.calls["/v2.0/oa/listrecentchat"]; got != 3 {
		t.Fatalf("the tick read %d pages on a budget of 4 requests, want the 3 left after reading the account", got)
	}
}

// The default budget reads FURTHER than the six pages the walk used to be capped
// at, which is the whole point of moving the number onto the row.
func TestTheDefaultBudgetReadsPastTheOldSixPageCap(t *testing.T) {
	rt := connectedRuntime(t, cursor{floor: 100})
	rt.tx.singleRows = append(rt.tx.singleRows, connectionRow(statusConnected, nil, cursor{floor: 2500}))
	fake := newZaloFake(t)
	var pages [][]map[string]any
	for page := range 9 {
		pages = append(pages, tenFrom(2500-int64(page*10)))
	}
	fake.chatPages = pages

	if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("pollConnection: %v", err)
	}
	// Nine full pages and then the short page that ends the feed: a walk still
	// capped at six would have stopped inside it and left a hole behind.
	if got := fake.calls["/v2.0/oa/listrecentchat"]; got != 10 {
		t.Fatalf("the tick read %d pages, want all 9 and the short page that closes the walk", got)
	}
	_, args := rt.tx.statementMentioning(t, "backfill_offset = $5")
	if args[2] != int64(0) {
		t.Fatalf("the tick left a backlog at %v after reading the whole feed inside its budget", args[2])
	}
}

// A CREDENTIAL THAT HAS COME TO ANSWER FOR A DIFFERENT ACCOUNT parks the
// connection before a single record is built.
//
// Everything this unit writes is namespaced by the account the row claims, so
// landing under a credential that speaks for another one would key messages into
// somebody else's people and resolve every reply to a stranger holding the same
// number. None of that is visible afterwards.
func TestACredentialAnsweringForAnotherAccountParksBeforeAnythingIsLanded(t *testing.T) {
	rt := connectedRuntime(t, cursor{floor: 900})
	// The row claims an account the fake provider does not answer for.
	row := connectionRow(statusConnected, nil, cursor{floor: 900})
	row[1] = "1111111111"
	rt.tx.singleRows = [][]any{row, connectionRow(statusReauth, nil, cursor{floor: 900})}
	fake := newZaloFake(t)
	fake.chatPages = [][]map[string]any{{message("m1", 1000, srcUserToOA, "một")}}

	if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("pollConnection: %v", err)
	}
	if len(rt.ingested) != 0 {
		t.Fatal("a record was landed under an account namespace the credential does not speak for")
	}
	_, args := rt.tx.statementMentioning(t, "last_error_class = $3")
	if args[1] != statusReauth || args[2] != "account_changed" {
		t.Fatalf("the connection was parked as %v, want reauth_required naming the account", args)
	}
	if fake.calls["/v2.0/oa/listrecentchat"] != 0 {
		t.Fatal("the walk ran before the account was reconciled")
	}
}

// A transient failure is RECORDED, not only written. The unit's ledger names one
// exemption — the poll's timestamp touch on an otherwise unchanged row — and the
// class a screen renders is not it: "when did this start failing" is the question
// somebody brings to it.
func TestATransientFailureIsRecordedAsWellAsWritten(t *testing.T) {
	rt := connectedRuntime(t, cursor{floor: 900})
	rt.tx.singleRows = append(rt.tx.singleRows, connectionRow(statusConnected, nil, cursor{floor: 900}))
	fake := newZaloFake(t)
	fake.errorCode = codeRateLimited

	if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err == nil {
		t.Fatal("a tick that could not reach the provider reported success")
	}
	if !published(rt, eventPolled) {
		t.Fatalf("the failure was written to the row and announced to nobody; the events were %v", verbs(rt))
	}
}

// A MESSAGE THIS INSTALLATION SENT IS NOT CAPTURED BACK. The core wrote it as an
// activity when the rep staged it, and the provider's walk includes the account's
// own outbound — so without this one real message lands on a timeline twice, with
// nothing to say which row is the duplicate.
//
// It is DECIDED ABOUT rather than skipped: the cursor moves past it exactly as it
// moves past a landed record, or the walk would meet it again forever.
func TestAMessageThisInstallationSentIsNotCapturedBack(t *testing.T) {
	rt := connectedRuntime(t, cursor{floor: 900})
	rt.tx.singleRows = append(rt.tx.singleRows, connectionRow(statusConnected, nil, cursor{floor: 1002}))
	// The marker lookup answers that the outbound message is ours.
	rt.tx.queryRows = [][]any{{"ours-1"}}
	fake := newZaloFake(t)
	fake.chatPages = [][]map[string]any{{
		message("ours-1", 1002, srcOAToUser, "the reply the CRM staged"),
		message("theirs-1", 1001, srcUserToOA, "what the customer said"),
	}}

	if err := pollConnection(t.Context(), rt, fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("pollConnection: %v", err)
	}
	landed := map[string]bool{}
	for _, rec := range rt.ingested {
		landed[rec.Key] = true
	}
	if landed[fixtureOAID+":ours-1"] {
		t.Fatal("a message this installation sent was captured back, so it is on the timeline twice")
	}
	if !landed[fixtureOAID+":theirs-1"] {
		t.Fatalf("the customer's own message was not landed: %v", landed)
	}
	// And the cursor still moved past the suppressed one.
	_, args := rt.tx.statementMentioning(t, "high_water_mark = $2")
	if args[1] != int64(1002) {
		t.Fatalf("the floor was written as %v, want it past the message that was suppressed", args[1])
	}
}

// The marker lookup asks only about OUTBOUND ids: only a message the account sent
// can be one this installation sent, so an inbound page costs no query at all.
func TestOnlyOutboundIdsAreAskedAbout(t *testing.T) {
	msgs := []chatMessage{
		{MessageID: "in-1", Src: srcUserToOA},
		{MessageID: "out-1", Src: srcOAToUser},
		{MessageID: "", Src: srcOAToUser},
	}
	got := outboundIDs(msgs)
	if len(got) != 1 || got[0] != "out-1" {
		t.Fatalf("outboundIDs = %v, want only the outbound message that has an id", got)
	}
	if len(outboundIDs(nil)) != 0 {
		t.Fatal("an empty page asked about something")
	}
}
