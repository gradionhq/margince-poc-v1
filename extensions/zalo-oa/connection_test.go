// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The three operations, and the tier gate that stands between a pasted pair and
// a working connection.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// connectArgs is what the screen sends: the app, and the pair an administrator
// brought back from the authorization.
func connectArgs() json.RawMessage {
	return json.RawMessage(`{"app_id":"app-1","app_secret":"the-secret","access_token":"pasted-access","refresh_token":"pasted-refresh"}`)
}

// renewedPair is what the token endpoint hands back, with values distinct from
// anything pasted so an assertion can tell which credential was kept.
func renewedPair() tokenPair {
	return tokenPair{AccessToken: "access-2", RefreshToken: "refresh-2", ExpiresAt: at(25 * time.Hour)}
}

// connectableRuntime is an installation with nothing connected yet.
func connectableRuntime(rows ...[]any) *fakeRuntime {
	rt := newRuntime()
	rt.tx.noRows = map[int]bool{1: true}
	rt.tx.singleRows = rows
	return rt
}

// Connecting gates the account, renews the credential once, and binds the whole
// thing to the CALLER. That last part is load-bearing: their sealed credential is
// what the poll spends, and their live authority is what every captured message
// is landed on.
func TestConnectingBindsTheAccountAndTheCredentialToTheCaller(t *testing.T) {
	rt := connectableRuntime(connectionRow(statusConnected, nil, cursor{}))
	grants := &fakeGrants{rotated: renewedPair()}

	answer, err := connectVia(t.Context(), rt, connectArgs(), newZaloFake(t).dial(), grants, frozen(at(0)))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	stored := jsonOf[connection](t, answer)
	if stored.Status != statusConnected {
		t.Fatalf("status = %q, want connected", stored.Status)
	}
	if stored.AuthorizedBy != adminUserID {
		t.Fatalf("authorized_by = %q, want the caller who connected it", stored.AuthorizedBy)
	}
	// THE REPLACEMENT is what is kept, never the pasted pair: a token this
	// installation shares with whatever tool produced it is one a second use
	// rotates out from under the connection.
	onDeposit, err := unsealTokens(t.Context(), rt, adminUserID)
	if err != nil {
		t.Fatalf("the grant was not sealed under the caller: %v", err)
	}
	if onDeposit.RefreshToken != "refresh-2" {
		t.Fatalf("the sealed pair is %+v, want the one the renewal produced", onDeposit)
	}
	if grants.rotations != 1 {
		t.Fatalf("the token endpoint was reached %d times, want exactly once", grants.rotations)
	}
	// Both keys under the caller, because a unit declares one scope.
	for _, key := range []string{tokenKey, appSecretKey} {
		if _, sealed := rt.secrets.stored["user/"+adminUserID+"/"+key]; !sealed {
			t.Fatalf("%q was not sealed under the connecting administrator", key)
		}
	}
}

// A PAIR MORE THAN A DAY OLD IS THE ORDINARY CASE, because an access token lasts
// about 25 hours and a refresh token lasts three months. Gating on the pasted
// access token alone would refuse almost every pair a human ever carries between
// tools.
func TestAPairWhoseAccessHalfHasExpiredStillConnects(t *testing.T) {
	rt := connectableRuntime(connectionRow(statusConnected, nil, cursor{}))
	grants := &fakeGrants{rotated: renewedPair()}
	fake := newZaloFake(t)
	// The provider refuses the pasted token and accepts the renewed one, which is
	// exactly what a day-old pair meets.
	fake.rejectToken = "pasted-access"

	if _, err := connectVia(t.Context(), rt, connectArgs(), fake.dial(), grants, frozen(at(0))); err != nil {
		t.Fatalf("a pair with an expired access half was refused: %v", err)
	}
	if grants.rotations != 1 {
		t.Fatalf("the token endpoint was reached %d times, want once", grants.rotations)
	}
}

// A CONNECT THAT GOT AS FAR AS SEALING IS RESUMABLE. The credential is sealed
// before the row is written, so a failure between the two leaves this
// installation holding the only live pair for the account with nothing naming it
// — and the pasted token is spent, so without this the remedy is an OA
// administrator in a browser for a failure entirely on this side.
func TestAConnectThatAlreadySealedAPairResumesFromIt(t *testing.T) {
	rt := connectableRuntime(connectionRow(statusConnected, nil, cursor{}))
	// What the earlier attempt left behind.
	seal(t, rt, renewedPair())
	// The pasted refresh token is the one that attempt already spent.
	grants := &fakeGrants{rotateErr: errNoGrant}

	answer, err := connectVia(t.Context(), rt, connectArgs(), newZaloFake(t).dial(), grants, frozen(at(0)))
	if err != nil {
		t.Fatalf("a resumable connect was refused: %v", err)
	}
	if jsonOf[connection](t, answer).Status != statusConnected {
		t.Fatal("the connection did not complete from the pair already held")
	}
}

// But it is a FALLBACK and never a preference: the pasted pair is tried first, so
// connecting a different account cannot quietly complete against the one already
// held.
func TestAUsablePairAlreadyHeldDoesNotPreemptThePastedOne(t *testing.T) {
	rt := connectableRuntime(connectionRow(statusConnected, nil, cursor{}))
	seal(t, rt, tokenPair{AccessToken: "held-access", RefreshToken: "held-refresh", ExpiresAt: at(25 * time.Hour)})
	grants := &fakeGrants{rotated: renewedPair()}

	if _, err := connectVia(t.Context(), rt, connectArgs(), newZaloFake(t).dial(), grants, frozen(at(0))); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if len(grants.spent) != 1 || grants.spent[0] != "pasted-refresh" {
		t.Fatalf("the tokens spent were %v, want the pasted one — a held pair must not preempt it", grants.spent)
	}
	onDeposit, err := unsealTokens(t.Context(), rt, adminUserID)
	if err != nil {
		t.Fatalf("unsealTokens: %v", err)
	}
	if onDeposit.AccessToken != "access-2" {
		t.Fatalf("what is on deposit is %q, want what the pasted pair renewed into", onDeposit.AccessToken)
	}
}

// And a spent pasted token with nothing usable held is refused, with the reason a
// human can act on: a Zalo refresh token can only be used once.
func TestASpentRefreshTokenWithNothingHeldIsRefusedWithTheReason(t *testing.T) {
	rt := connectableRuntime()
	grants := &fakeGrants{rotateErr: errNoGrant}

	_, err := connectVia(t.Context(), rt, connectArgs(), newZaloFake(t).dial(), grants, frozen(at(0)))
	if !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("error = %v, want a refusal the caller is shown", err)
	}
	if !strings.Contains(err.Error(), "ONCE") && !strings.Contains(err.Error(), "once") {
		t.Fatalf("the refusal does not name what went wrong: %v", err)
	}
}

// A rotation whose ANSWER never came back must not invite a retry: the token may
// have rotated into a replacement nobody holds.
func TestAnUnansweredFirstRenewalRefusesRatherThanInvitingARetry(t *testing.T) {
	rt := connectableRuntime()
	grants := &fakeGrants{rotateErr: errUnanswered}

	_, err := connectVia(t.Context(), rt, connectArgs(), newZaloFake(t).dial(), grants, frozen(at(0)))
	if !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("error = %v, want a refusal the caller is shown", err)
	}
	if !strings.Contains(err.Error(), "never reported") {
		t.Fatalf("the refusal does not name the uncertainty: %v", err)
	}
}

// The member is taken from the INVOCATION. A tick and a bus delivery both answer
// the zero caller, and neither can connect an account because there is nobody
// whose grant it would be.
func TestAnInvocationWithNobodyBehindItCannotConnect(t *testing.T) {
	rt := newRuntime().unattended()
	_, err := connectVia(t.Context(), rt, connectArgs(), newZaloFake(t).dial(), &fakeGrants{}, frozen(at(0)))
	if !errors.Is(err, extension.ErrForbidden) {
		t.Fatalf("error = %v, want a refusal naming that nobody is behind the call", err)
	}
	if len(rt.tx.statements) != 0 || len(rt.secrets.stored) != 0 {
		t.Fatal("an unattended call reached the database or the custodian")
	}
}

// The arguments are decoded STRICTLY, because the contract declares
// additionalProperties: false and nothing between a client and this handler
// enforces it.
func TestArgumentsOutsideTheDeclaredShapeAreRefused(t *testing.T) {
	const good = `"app_id":"a","app_secret":"s","access_token":"at","refresh_token":"rt"`
	for name, args := range map[string]string{
		"an undeclared member":  `{` + good + `,"user_id":"someone-else"}`,
		"a repeated member":     `{` + good + `,"app_id":"b"}`,
		"a second document":     `{` + good + `} {"app_id":"b"}`,
		"a case-shifted member": `{"APP_ID":"a","app_secret":"s","access_token":"at","refresh_token":"rt"}`,
		"a missing credential":  `{"app_id":"a","app_secret":"s","access_token":"at"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rt := newRuntime()
			_, err := connectVia(t.Context(), rt, json.RawMessage(args), newZaloFake(t).dial(), &fakeGrants{}, frozen(at(0)))
			if err == nil {
				t.Fatal("the document was accepted; the published schema does not describe it")
			}
			if len(rt.secrets.stored) != 0 {
				t.Fatalf("a refused document still deposited a secret: %v", rt.secrets.stored)
			}
		})
	}
}

// THE TIER GATE IS A CAPABILITY PROBE. The package name Zalo returns is a
// localized display string it can rename or extend, so the gate asks the account
// whether it can do the thing this unit needs rather than reading what it is
// called.
func TestTheTierGateAsksTheAccountRatherThanReadingItsPackageName(t *testing.T) {
	fake := newZaloFake(t)
	label, err := probeAccount(t.Context(), fake.client("token"))
	if err != nil {
		t.Fatalf("probeAccount: %v", err)
	}
	if label.PackageName != "Tăng trưởng" {
		t.Fatalf("the package name was not carried as evidence: %+v", label)
	}
	if fake.calls["/v2.0/oa/listrecentchat"] == 0 {
		t.Fatal("the gate admitted the account without probing the API it needs, so it can only have decided on the package's name")
	}
}

// The two refusals cost different things and say different things. One is
// 2.500.000 đ a year and the other is a click, and telling an administrator to
// buy an upgrade when they need to toggle a switch is the failure the whole error
// catalog exists to prevent.
//
// Driven through connect rather than the gate alone, because what an operator
// meets is the refusal the operation returns.
func TestTheGateTellsAPurchaseApartFromAFreeConsoleToggle(t *testing.T) {
	for name, arm := range map[string]struct {
		code     int
		wantText string
		notText  string
	}{
		"a package too low":      {codeTierTooLow, "oa.zalo.me", "developers.zalo.me"},
		"an app without a group": {codeAPINotRegisterd, "developers.zalo.me", "2.500.000"},
	} {
		t.Run(name, func(t *testing.T) {
			rt := connectableRuntime()
			fake := newZaloFake(t)
			fake.errorCode = arm.code
			grants := &fakeGrants{rotated: renewedPair()}

			_, err := connectVia(t.Context(), rt, connectArgs(), fake.dial(), grants, frozen(at(0)))
			if !errors.Is(err, extension.ErrInvalid) {
				t.Fatalf("error = %v, want a refusal the caller is shown", err)
			}
			if !strings.Contains(err.Error(), arm.wantText) {
				t.Fatalf("the refusal does not say where to go: %v", err)
			}
			if strings.Contains(err.Error(), arm.notText) {
				t.Fatalf("the refusal sends an administrator to the wrong place: %v", err)
			}
			// A FRESH PAIR MEETS THE TIER REFUSAL HAVING SPENT NOTHING: the gate
			// runs before the renewal, so a free-tier account does not cost an
			// administrator their single-use token to be told no.
			if grants.rotations != 0 {
				t.Fatalf("the refresh token was spent %d times on an account the gate refused", grants.rotations)
			}
			if _, sealed := rt.secrets.stored["user/"+adminUserID+"/"+tokenKey]; sealed {
				t.Fatal("a credential was sealed for an account the gate refused")
			}
		})
	}
}

// Status is the ordinary question this screen asks, and having connected nothing
// yet is an ANSWER rather than an error.
func TestStatusReportsTheAbsenceOfAConnectionWithoutFailing(t *testing.T) {
	rt := newRuntime()
	rt.tx.noRows = map[int]bool{1: true}

	answer, err := status(t.Context(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	reported := jsonOf[struct {
		Connected  bool        `json:"connected"`
		Connection *connection `json:"connection"`
	}](t, answer)
	if reported.Connected || reported.Connection != nil {
		t.Fatalf("status reported %+v for an installation that has connected nothing", reported)
	}
}

// A parked connection is reported, and reported as NOT connected: the screen has
// to tell "nothing here" from "something a human must repair".
func TestAParkedConnectionIsReportedAndIsNotConnected(t *testing.T) {
	rt := newRuntime()
	rt.tx.singleRows = [][]any{connectionRow(statusTierLapse, nil, cursor{})}

	answer, err := status(t.Context(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	reported := jsonOf[struct {
		Connected  bool        `json:"connected"`
		Connection *connection `json:"connection"`
	}](t, answer)
	if reported.Connected {
		t.Fatal("a parked connection reported itself as connected")
	}
	if reported.Connection == nil || reported.Connection.Status != statusTierLapse {
		t.Fatalf("the row was not reported, so the screen could not say what is wrong: %+v", reported)
	}
}

// Disconnecting takes the row AND every credential, and the credentials it takes
// are the CONNECTING ADMIN's rather than the caller's: any administrator may
// disconnect, and one who deleted only their own deposit would leave a live
// credential behind for a connection that no longer exists.
func TestDisconnectingRemovesEveryCredentialWhoeverCallsIt(t *testing.T) {
	rt := newRuntime()
	rt.caller = extension.Caller{Type: extension.CallerHuman, UserID: "11111111-2222-3333-4444-555555555555"}
	seal(t, rt, livePair(at(20*time.Hour)))
	if err := rt.secrets.PutUser(t.Context(), adminUserID, appSecretKey, []byte("s")); err != nil {
		t.Fatalf("depositing the app secret: %v", err)
	}
	rt.tx.singleRows = [][]any{
		connectionRow(statusConnected, nil, cursor{}),
		connectionRow(statusConnected, nil, cursor{}),
	}

	answer, err := disconnect(t.Context(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if !jsonOf[struct {
		Disconnected bool `json:"disconnected"`
	}](t, answer).Disconnected {
		t.Fatal("nothing was reported as disconnected")
	}
	// EVERY key. A UAT found the app secret surviving a disconnect, against the
	// operation's own comment.
	for _, key := range []string{tokenKey, appSecretKey} {
		if _, still := rt.secrets.stored["user/"+adminUserID+"/"+key]; still {
			t.Fatalf("%q survived the disconnect, and the ingress port reads a deposit as live consent", key)
		}
	}
	if !published(rt, eventDisconnected) {
		t.Fatalf("the disconnect announced %v", verbs(rt))
	}
}

// Disconnecting nothing is not an error: there is no connection to remove and
// saying so is the answer the caller asked for.
func TestDisconnectingNothingIsAnAnswerRatherThanAnError(t *testing.T) {
	rt := newRuntime()
	rt.tx.noRows = map[int]bool{1: true}

	answer, err := disconnect(t.Context(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if jsonOf[struct {
		Disconnected bool `json:"disconnected"`
	}](t, answer).Disconnected {
		t.Fatal("a disconnect over nothing reported that something was removed")
	}
}

// THE ACCOUNT ID STORED IS THE TOKEN'S, NOT THE REQUEST'S — there is no longer a
// request field for it at all, which is the strongest form of that rule. It
// namespaces every person binding, every thread key and every natural key this
// unit writes.
func TestTheAccountIdStoredIsTheOneTheCredentialAnswersFor(t *testing.T) {
	rt := connectableRuntime(connectionRow(statusConnected, nil, cursor{}))

	answer, err := connectVia(t.Context(), rt, connectArgs(), newZaloFake(t).dial(),
		&fakeGrants{rotated: renewedPair()}, frozen(at(0)))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got := jsonOf[connection](t, answer).OAID; got != fixtureOAID {
		t.Fatalf("oa_id = %q, want the account the credential answers for", got)
	}
	_, args := rt.tx.statementMentioning(t, "ON CONFLICT")
	if args[0] != fixtureOAID {
		t.Fatalf("the row was written with %v as its account, want the provider's own answer", args[0])
	}
}

// THE UPSERT'S OWN COLUMN REFERENCES ARE TABLE-QUALIFIED. In an ON CONFLICT DO
// UPDATE a bare name is ambiguous between the row and EXCLUDED, and PostgreSQL
// refuses the statement with 42702 rather than picking one — which is the
// opposite of a plain UPDATE, and which reached a running installation as a 500.
func TestTheUpsertQualifiesEveryColumnItReadsBack(t *testing.T) {
	rt := connectableRuntime(connectionRow(statusConnected, nil, cursor{}))
	if _, err := connectVia(t.Context(), rt, connectArgs(), newZaloFake(t).dial(),
		&fakeGrants{rotated: renewedPair()}, frozen(at(0))); err != nil {
		t.Fatalf("connect: %v", err)
	}
	sql, _ := rt.tx.statementMentioning(t, "ON CONFLICT")
	for _, bare := range []string{
		"CASE WHEN oa_id", "THEN high_water_mark", "THEN backfill_before",
		"THEN pending_high_water_mark", "THEN backfill_offset",
	} {
		if strings.Contains(sql, bare) {
			t.Fatalf("the upsert reads %q unqualified, which is ambiguous with EXCLUDED (42702): %s", bare, sql)
		}
	}
	if !strings.Contains(sql, connectionTable+".oa_id = EXCLUDED.oa_id") {
		t.Fatalf("the cursor is not keyed on whether the account changed: %s", sql)
	}
}

// Connecting as a DIFFERENT administrator withdraws the previous one's sealed
// credentials in the same act. What would be left behind is not a stray blob: the
// ingress port reads a deposit as live consent, and no later disconnect reaches
// it because a disconnect withdraws the row's CURRENT administrator.
func TestConnectingAsSomebodyElseWithdrawsThePreviousAdminsCredentials(t *testing.T) {
	rt := newRuntime()
	previous := "22222222-3333-4444-5555-666666666666"
	if err := sealTokens(t.Context(), rt, extension.UserID(previous), livePair(at(20*time.Hour))); err != nil {
		t.Fatalf("sealing the previous administrator's credential: %v", err)
	}
	before := connectionRow(statusConnected, nil, cursor{})
	before[3] = previous
	rt.tx.singleRows = [][]any{before, connectionRow(statusConnected, nil, cursor{})}

	if _, err := connectVia(t.Context(), rt, connectArgs(), newZaloFake(t).dial(),
		&fakeGrants{rotated: renewedPair()}, frozen(at(0))); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, still := rt.secrets.stored["user/"+previous+"/"+tokenKey]; still {
		t.Fatal("the superseded administrator's credential is still on deposit")
	}
}

// And reconnecting as the SAME administrator keeps their own deposit, which they
// are in the middle of replacing.
func TestReconnectingAsTheSameAdminKeepsTheirOwnDeposit(t *testing.T) {
	rt := newRuntime()
	rt.tx.singleRows = [][]any{
		connectionRow(statusConnected, nil, cursor{}),
		connectionRow(statusConnected, nil, cursor{}),
	}

	if _, err := connectVia(t.Context(), rt, connectArgs(), newZaloFake(t).dial(),
		&fakeGrants{rotated: renewedPair()}, frozen(at(0))); err != nil {
		t.Fatalf("connect: %v", err)
	}
	onDeposit, err := unsealTokens(t.Context(), rt, adminUserID)
	if err != nil {
		t.Fatalf("the caller's own credential was withdrawn by their own reconnect: %v", err)
	}
	if onDeposit.RefreshToken != "refresh-2" {
		t.Fatalf("what is on deposit is %+v, want the pair this connect produced", onDeposit)
	}
}

// An opaque value an administrator pasted is checked for presence and size and
// nothing else: these are the provider's identifiers and its credentials, and
// this unit has no grammar for them — so a refusal says what was expected rather
// than what it should look like.
func TestAnOpaquePastedValueIsBoundedButNotSecondGuessed(t *testing.T) {
	if _, err := boundedSecretish("   ", 10, "the App ID"); !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("an empty value was accepted: %v", err)
	}
	if _, err := boundedSecretish(strings.Repeat("x", 11), 10, "the App ID"); !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("an oversized value was accepted: %v", err)
	}
	got, err := boundedSecretish("  app-1 ", 10, "the App ID")
	if err != nil {
		t.Fatalf("an ordinary value was refused: %v", err)
	}
	if got != "app-1" {
		t.Fatalf("value = %q, want it trimmed", got)
	}
}

// A Runtime the core refuses to open a transaction on is propagated rather than
// answered over: a handler that reported "not connected" for a database it could
// not read would state something it did not establish.
func TestATransactionTheCoreRefusesToOpenIsReported(t *testing.T) {
	rt := newRuntime()
	rt.txErr = extension.ErrRuntimeExpired

	if _, err := status(t.Context(), rt, json.RawMessage(`{}`)); !errors.Is(err, extension.ErrRuntimeExpired) {
		t.Fatalf("error = %v, want the refusal propagated", err)
	}
}

// THE ACCOUNT IS READ FROM THE CREDENTIAL THAT WILL BE SPENT, and a pasted pair
// whose two halves belong to different Official Accounts is refused.
//
// Nothing ties the four pasted values to one account: an access token for X and
// an app-and-refresh-token for Y are four well-formed strings that each pass
// their own check. Reading the label from the pasted token while sealing the pair
// from the exchange writes a row whose oa_id names X over a credential answering
// for Y — and that id is the namespace every key is written under and the prefix
// a reply is let through on, so a message staged before the poll reconciles them
// reaches whoever holds that number at the OTHER account.
func TestAPastedPairWhoseHalvesNameDifferentAccountsIsRefused(t *testing.T) {
	rt := connectableRuntime()
	fake := newZaloFake(t)
	fake.accountFor = map[string]string{
		"pasted-access": "1111111111", // the token the caller gated with
		"access-2":      "2222222222", // the account the renewal answers for
	}

	_, err := connectVia(t.Context(), rt, connectArgs(), fake.dial(),
		&fakeGrants{rotated: renewedPair()}, frozen(at(0)))
	if !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("error = %v, want the mismatch refused", err)
	}
	if !strings.Contains(err.Error(), "different Official Accounts") {
		t.Fatalf("the refusal does not name what disagreed: %v", err)
	}
	for _, sql := range rt.tx.statements {
		if strings.Contains(sql, "ON CONFLICT") {
			t.Fatalf("a row was written for a credential and a label that disagree: %s", sql)
		}
	}
}

// A RESUME DOES NOT OVERWRITE THE APP SECRET, because the secret that can renew
// the pair being resumed is the one already on deposit beside it.
//
// Writing the pasted one over it seals an app secret that cannot renew the
// credential in use: the connect answers 200, and the next scheduled rotation —
// up to a day later — parks a connection that was working, with recovery
// requiring an OA administrator in a browser.
func TestAResumeKeepsTheAppSecretThatCanActuallyRenewTheHeldPair(t *testing.T) {
	rt := newRuntime()
	// What an earlier connect left: a usable pair, and the app secret that renews it.
	seal(t, rt, renewedPair())
	if err := rt.secrets.PutUser(t.Context(), adminUserID, appSecretKey, []byte("the-secret-that-works")); err != nil {
		t.Fatalf("depositing the app secret: %v", err)
	}
	existing := connectionRow(statusConnected, nil, cursor{})
	existing[2] = "the-app-that-works"
	rt.tx.singleRows = [][]any{existing, connectionRow(statusConnected, nil, cursor{})}
	// The caller pastes a spent refresh token and an app that does not match.
	grants := &fakeGrants{rotateErr: errNoGrant}

	if _, err := connectVia(t.Context(), rt, connectArgs(), newZaloFake(t).dial(), grants, frozen(at(0))); err != nil {
		t.Fatalf("a resumable connect was refused: %v", err)
	}
	held := rt.secrets.stored["user/"+adminUserID+"/"+appSecretKey]
	if string(held) != "the-secret-that-works" {
		t.Fatalf("the app secret on deposit is %q; a resume must keep the one that can renew the pair it resumed", held)
	}
	_, args := rt.tx.statementMentioning(t, "ON CONFLICT")
	if args[1] != "the-app-that-works" {
		t.Fatalf("the row named app %v, want the one whose secret is on deposit", args[1])
	}
}

// The before-image is read FOR UPDATE, because what happens after this
// transaction depends on who the row named before it: two administrators
// connecting at once would otherwise both read the same pre-image, and the second
// withdrawal would take a credential nobody holds while leaving the one just
// superseded — a live credential, and standing ingest consent, that no disconnect
// ever reaches.
func TestTheBeforeImageIsReadUnderTheRowLock(t *testing.T) {
	rt := connectableRuntime(connectionRow(statusConnected, nil, cursor{}))
	if _, err := connectVia(t.Context(), rt, connectArgs(), newZaloFake(t).dial(),
		&fakeGrants{rotated: renewedPair()}, frozen(at(0))); err != nil {
		t.Fatalf("connect: %v", err)
	}
	sql, _ := rt.tx.statementMentioning(t, "FOR UPDATE")
	if !strings.Contains(sql, connectionTable) {
		t.Fatalf("the locked read does not name this unit's table: %s", sql)
	}
}

// The resume fallback is reached ONLY by the refusals that mean "this token is
// spent". A 503 at the token endpoint says nothing about the pasted pair, and
// resuming on one would quietly substitute a different credential for a caller
// who should simply try again.
func TestATransientTokenEndpointFailureDoesNotResume(t *testing.T) {
	rt := connectableRuntime()
	seal(t, rt, renewedPair())
	grants := &fakeGrants{rotateErr: errTransient}

	_, err := connectVia(t.Context(), rt, connectArgs(), newZaloFake(t).dial(), grants, frozen(at(0)))
	if err == nil {
		t.Fatal("an unreachable token endpoint completed the connect from a held pair")
	}
	for _, sql := range rt.tx.statements {
		if strings.Contains(sql, "ON CONFLICT") {
			t.Fatalf("a row was written on a transient renewal failure: %s", sql)
		}
	}
}
