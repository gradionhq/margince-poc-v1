// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The four operations, and the tier gate that stands between the second one and
// a working connection.

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// authorizeArgs is what the screen sends to start the browser round trip.
func authorizeArgs() json.RawMessage {
	return json.RawMessage(`{"app_id":"app-1","app_secret":"the-secret","redirect_uri":"https://crm.example.com/zalo"}`)
}

// connectArgs is what the screen sends after the administrator comes back.
func connectArgs(state string) json.RawMessage {
	return json.RawMessage(`{"code":"the-code","oa_id":"` + fixtureOAID + `","state":"` + state + `"}`)
}

// Starting an authorization reaches nothing and spends nothing: it seals what the
// exchange will need and hands back a URL somebody else opens.
func TestStartingAnAuthorizationSealsTheMaterialAndReachesNoAccount(t *testing.T) {
	rt := newRuntime()
	rt.tx.noRows = map[int]bool{1: true}
	rt.tx.singleRows = [][]any{connectionRow(statusPending, nil, cursor{})}

	answer, err := authorize(t.Context(), rt, authorizeArgs())
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	started := jsonOf[struct {
		PermissionURL string     `json:"permission_url"`
		CodeChallenge string     `json:"code_challenge"`
		Connection    connection `json:"connection"`
	}](t, answer)

	if started.Connection.Status != statusPending {
		t.Fatalf("status = %q, want %q — nothing is connected until a code comes back", started.Connection.Status, statusPending)
	}
	verifier, ok := rt.secrets.stored["user/"+adminUserID+"/"+verifierKey]
	if !ok {
		t.Fatal("no code verifier was sealed; the code the administrator comes back with could never be redeemed")
	}
	if _, ok := rt.secrets.stored["workspace//"+appSecretKey]; !ok {
		t.Fatal("the app secret was not sealed at workspace scope")
	}
	// The challenge returned to the screen is the one the sealed verifier
	// produces: an administrator pastes it into the developer console, and a
	// challenge that did not match its verifier would fail the exchange at the
	// one point a ten-minute code cannot be retried.
	if started.CodeChallenge != challengeFor(string(verifier)) {
		t.Fatal("the challenge shown to the administrator is not the one the sealed verifier produces")
	}
	parsed, err := url.Parse(started.PermissionURL)
	if err != nil {
		t.Fatalf("the permission URL is not a URL: %v", err)
	}
	if parsed.Query().Get("state") != started.Connection.ID {
		t.Fatal("the permission URL's state is not the connection's own id, so the redirect could not be bound to this authorization")
	}
}

// The member is taken from the INVOCATION. A tick and a bus delivery both answer
// the zero caller, and neither can authorize an account because there is nobody
// whose grant it would be.
func TestAnInvocationWithNobodyBehindItCannotAuthorizeOrDisconnect(t *testing.T) {
	for name, call := range map[string]func(rt extension.Runtime) error{
		"authorize": func(rt extension.Runtime) error {
			_, err := authorize(t.Context(), rt, authorizeArgs())
			return err
		},
		"connect": func(rt extension.Runtime) error {
			_, err := connectVia(t.Context(), rt, connectArgs(fixtureConnectionID), nil, &fakeGrants{})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			rt := newRuntime().unattended()
			if err := call(rt); !errors.Is(err, extension.ErrForbidden) {
				t.Fatalf("error = %v, want a refusal naming that nobody is behind the call", err)
			}
			if len(rt.tx.statements) != 0 {
				t.Fatalf("an unattended call wrote to the database: %v", rt.tx.statements)
			}
		})
	}
}

// The arguments are decoded STRICTLY, because the contract declares
// additionalProperties: false and nothing between a client and this handler
// enforces it.
func TestArgumentsOutsideTheDeclaredShapeAreRefused(t *testing.T) {
	for name, args := range map[string]string{
		"an undeclared member":  `{"app_id":"a","app_secret":"s","redirect_uri":"https://x.example.com","user_id":"someone-else"}`,
		"a repeated member":     `{"app_id":"a","app_id":"b","app_secret":"s","redirect_uri":"https://x.example.com"}`,
		"a second document":     `{"app_id":"a","app_secret":"s","redirect_uri":"https://x.example.com"} {"app_id":"b"}`,
		"a case-shifted member": `{"APP_ID":"a","app_secret":"s","redirect_uri":"https://x.example.com"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rt := newRuntime()
			if _, err := authorize(t.Context(), rt, json.RawMessage(args)); err == nil {
				t.Fatal("the document was accepted; the published schema does not describe it")
			}
			if len(rt.secrets.stored) != 0 {
				t.Fatalf("a refused document still deposited a secret: %v", rt.secrets.stored)
			}
		})
	}
}

// The redirect address is checked for what a BROWSER and the provider will do
// with it: an authorization code travels back on it, and Zalo appends its own
// query — so a value carrying either would come back ambiguous.
func TestARedirectAddressThatWouldLoseTheCodeIsRefused(t *testing.T) {
	for name, redirect := range map[string]string{
		"not https":        "http://crm.example.com/zalo",
		"no host":          "https:///zalo",
		"with credentials": "https://user:pass@crm.example.com/zalo",
		"with a query":     "https://crm.example.com/zalo?next=1",
		"with a fragment":  "https://crm.example.com/zalo#done",
		"empty":            "",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := connectableRedirect(redirect); !errors.Is(err, extension.ErrInvalid) {
				t.Fatalf("%q was accepted as a redirect address", redirect)
			}
		})
	}
	if _, err := connectableRedirect(" https://crm.example.com/zalo "); err != nil {
		t.Fatalf("an ordinary https address was refused: %v", err)
	}
}

// THE TIER GATE IS A CAPABILITY PROBE. The package name Zalo returns is a
// localized display string it can rename or extend, so the gate asks the account
// whether it can do the thing this unit needs rather than reading what the
// account is called.
func TestTheTierGateAsksTheAccountRatherThanReadingItsPackageName(t *testing.T) {
	fake := newZaloFake(t)
	label, err := admitTier(t.Context(), fake.client("token"))
	if err != nil {
		t.Fatalf("admitTier: %v", err)
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
			fake := newZaloFake(t)
			fake.errorCode = arm.code
			_, err := admitTier(t.Context(), fake.client("token"))
			if !errors.Is(err, extension.ErrInvalid) {
				t.Fatalf("error = %v, want a refusal the caller is shown", err)
			}
			if !strings.Contains(err.Error(), arm.wantText) {
				t.Fatalf("the refusal does not say where to go: %v", err)
			}
			if strings.Contains(err.Error(), arm.notText) {
				t.Fatalf("the refusal sends an administrator to the wrong place: %v", err)
			}
		})
	}
}

// A code redeemed against a state that belongs to another authorization is
// REFUSED rather than warned about: it is either a stale browser tab or somebody
// else's redirect, and this side cannot tell which.
func TestARedirectCarryingAnotherAuthorizationsStateIsRefused(t *testing.T) {
	rt := newRuntime()
	rt.tx.singleRows = [][]any{connectionRow(statusPending, nil, cursor{})}
	grants := &fakeGrants{redeemed: livePair(at(25 * time.Hour))}

	_, err := connectVia(t.Context(), rt, connectArgs("some-other-authorization"), newZaloFake(t).dial(), grants)
	if !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("error = %v, want the state mismatch refused", err)
	}
	if grants.redemptions != 0 {
		t.Fatal("the ten-minute code was spent against a redirect that did not belong to this authorization")
	}
}

// A code cannot be redeemed without the verifier that minted its challenge, and
// the refusal says what to do about it — start again — rather than inviting a
// retry that cannot work.
func TestRedeemingWithNoVerifierOnDepositRefusesWithTheRemedy(t *testing.T) {
	rt := newRuntime()
	rt.tx.singleRows = [][]any{connectionRow(statusPending, nil, cursor{})}
	grants := &fakeGrants{redeemed: livePair(at(25 * time.Hour))}

	_, err := connectVia(t.Context(), rt, connectArgs(fixtureConnectionID), newZaloFake(t).dial(), grants)
	if !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("error = %v, want a refusal the caller is shown", err)
	}
	if !strings.Contains(err.Error(), "again") {
		t.Fatalf("the refusal does not say what to do: %v", err)
	}
	if grants.redemptions != 0 {
		t.Fatal("the code was spent with no verifier to present with it")
	}
}

// Completing an authorization seals the grant under WHOEVER COMPLETED IT and
// records them on the row. That is the connection's authority from then on: the
// poll spends their credential and the core resolves their live permissions per
// record.
func TestCompletingAnAuthorizationBindsTheGrantToTheCallerWhoFinishedIt(t *testing.T) {
	rt := newRuntime()
	if err := rt.secrets.PutUser(t.Context(), adminUserID, verifierKey, []byte("the-verifier")); err != nil {
		t.Fatalf("depositing the verifier: %v", err)
	}
	if err := rt.secrets.Put(t.Context(), appSecretKey, []byte("the-secret")); err != nil {
		t.Fatalf("depositing the app secret: %v", err)
	}
	rt.tx.singleRows = [][]any{
		connectionRow(statusPending, nil, cursor{}),
		connectionRow(statusConnected, nil, cursor{}),
	}
	expiresAt := at(25 * time.Hour)
	grants := &fakeGrants{redeemed: tokenPair{AccessToken: "a", RefreshToken: "r", ExpiresAt: expiresAt}}

	answer, err := connectVia(t.Context(), rt, connectArgs(fixtureConnectionID), newZaloFake(t).dial(), grants)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	stored := jsonOf[connection](t, answer)
	if stored.Status != statusConnected {
		t.Fatalf("status = %q, want connected", stored.Status)
	}
	if stored.AuthorizedBy != adminUserID {
		t.Fatalf("authorized_by = %q, want the caller who completed the authorization", stored.AuthorizedBy)
	}
	onDeposit, err := unsealTokens(t.Context(), rt, adminUserID)
	if err != nil {
		t.Fatalf("the grant was not sealed under the caller: %v", err)
	}
	if onDeposit.RefreshToken != "r" {
		t.Fatalf("the sealed pair is %+v, want the grant that was just issued", onDeposit)
	}
	// The verifier has done its work and the code it redeemed is dead, so leaving
	// PKCE material on deposit would be keeping a credential for an authorization
	// that has completed.
	if _, still := rt.secrets.stored["user/"+adminUserID+"/"+verifierKey]; still {
		t.Fatal("the code verifier is still on deposit after the authorization completed")
	}
	if _, args := rt.tx.statementMentioning(t, "access_token_expires_at = $7"); args[6] != expiresAt {
		t.Fatalf("the expiry mirrored onto the row is %v, want the grant's own %v", args[6], expiresAt)
	}
}

// A free-tier account is refused AFTER the grant is obtained and leaves NO
// credential sealed. A token on deposit for an account that is not connected
// would make the screen say it was.
func TestAnAccountTheGateRefusesLeavesNoCredentialBehind(t *testing.T) {
	rt := newRuntime()
	if err := rt.secrets.PutUser(t.Context(), adminUserID, verifierKey, []byte("the-verifier")); err != nil {
		t.Fatalf("depositing the verifier: %v", err)
	}
	if err := rt.secrets.Put(t.Context(), appSecretKey, []byte("the-secret")); err != nil {
		t.Fatalf("depositing the app secret: %v", err)
	}
	rt.tx.singleRows = [][]any{connectionRow(statusPending, nil, cursor{})}
	fake := newZaloFake(t)
	fake.errorCode = codeTierTooLow

	_, err := connectVia(t.Context(), rt, connectArgs(fixtureConnectionID), fake.dial(),
		&fakeGrants{redeemed: livePair(at(25 * time.Hour))})
	if !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("error = %v, want the free-tier refusal", err)
	}
	if _, sealed := rt.secrets.stored["user/"+adminUserID+"/"+tokenKey]; sealed {
		t.Fatal("a credential was sealed for an account the gate refused, which would present as a connection that works")
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

// A connection part way through a browser flow is NOT connected, and the screen
// has to be able to tell the two apart: one is somebody who has not finished, and
// the other is somebody who must repair something.
func TestAPendingAuthorizationIsReportedAsNotConnected(t *testing.T) {
	rt := newRuntime()
	rt.tx.singleRows = [][]any{connectionRow(statusPending, nil, cursor{})}

	answer, err := status(t.Context(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	reported := jsonOf[struct {
		Connected  bool        `json:"connected"`
		Connection *connection `json:"connection"`
	}](t, answer)
	if reported.Connected {
		t.Fatal("an unfinished authorization reported itself as connected")
	}
	if reported.Connection == nil || reported.Connection.Status != statusPending {
		t.Fatalf("the row was not reported, so the screen could not say what is unfinished: %+v", reported)
	}
}

// Disconnecting takes the row AND the credential, and the credential it takes is
// the AUTHORIZING ADMIN's rather than the caller's: any administrator may
// disconnect, and one who deleted only their own deposit would leave a live
// credential behind for a connection that no longer exists.
func TestDisconnectingRemovesTheAuthorizingAdminsCredentialWhoeverCallsIt(t *testing.T) {
	rt := newRuntime()
	rt.caller = extension.Caller{Type: extension.CallerHuman, UserID: "11111111-2222-3333-4444-555555555555"}
	seal(t, rt, livePair(at(20*time.Hour)))
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
	if _, still := rt.secrets.stored["user/"+adminUserID+"/"+tokenKey]; still {
		t.Fatal("the authorizing administrator's credential survived the disconnect, so the poll would keep spending it")
	}
	if _, args := rt.tx.statementMentioning(t, "RETURNING"); len(args) == 0 {
		t.Fatal("no statement carried the row being removed")
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

// Re-authorizing the SAME account keeps the cursor, and pointing at ANOTHER one
// resets it: a message timestamp from one account means nothing against another's
// log, and keeping it would put the floor above everything the new account has
// ever said.
func TestTheCursorSurvivesAReauthorizationAndIsResetByANewAccount(t *testing.T) {
	rt := newRuntime()
	if err := rt.secrets.PutUser(t.Context(), adminUserID, verifierKey, []byte("v")); err != nil {
		t.Fatalf("depositing the verifier: %v", err)
	}
	if err := rt.secrets.Put(t.Context(), appSecretKey, []byte("s")); err != nil {
		t.Fatalf("depositing the app secret: %v", err)
	}
	rt.tx.singleRows = [][]any{
		connectionRow(statusPending, nil, cursor{floor: 1000}),
		connectionRow(statusConnected, nil, cursor{floor: 1000}),
	}

	if _, err := connectVia(t.Context(), rt, connectArgs(fixtureConnectionID), newZaloFake(t).dial(),
		&fakeGrants{redeemed: livePair(at(25 * time.Hour))}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	sql, _ := rt.tx.statementMentioning(t, "high_water_mark = CASE")
	if !strings.Contains(sql, "coalesce(oa_id, '') = $2") {
		t.Fatalf("the cursor is not keyed on whether the account changed: %s", sql)
	}
	for _, column := range []string{"backfill_before = CASE", "pending_high_water_mark = CASE", "backfill_offset = CASE"} {
		if !strings.Contains(sql, column) {
			t.Fatalf("%q is not reset with the rest of the cursor, so half of it would survive a new account: %s", column, sql)
		}
	}
}

// An opaque value an administrator pasted is checked for presence and size and
// nothing else: these are the provider's identifiers and this unit has no grammar
// for them, so a refusal says what was expected rather than what it should look
// like.
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

// THE ACCOUNT ID IS THE TOKEN'S, NOT THE REQUEST'S. It namespaces every person
// binding, every thread key and every natural key this unit writes, and it is the
// whole of what stops a reply reaching a different human — so a caller able to
// name it could point an installation's existing bindings at an account their own
// credential speaks for, and nothing afterwards would show it.
func TestTheAccountIdStoredIsTheOneTheTokenAnswersForAndNotTheOneTheRequestCarried(t *testing.T) {
	rt := newRuntime()
	if err := rt.secrets.PutUser(t.Context(), adminUserID, verifierKey, []byte("v")); err != nil {
		t.Fatalf("depositing the verifier: %v", err)
	}
	if err := rt.secrets.Put(t.Context(), appSecretKey, []byte("s")); err != nil {
		t.Fatalf("depositing the app secret: %v", err)
	}
	rt.tx.singleRows = [][]any{connectionRow(statusPending, nil, cursor{floor: 1000})}
	// The redirect claims an account this credential does not speak for — the
	// installation's existing one, whose bindings the caller means to inherit.
	spoofed := json.RawMessage(`{"code":"the-code","oa_id":"9999999999","state":"` + fixtureConnectionID + `"}`)

	_, err := connectVia(t.Context(), rt, spoofed, newZaloFake(t).dial(),
		&fakeGrants{redeemed: livePair(at(25 * time.Hour))})
	if !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("error = %v, want the disagreement refused", err)
	}
	if _, sealed := rt.secrets.stored["user/"+adminUserID+"/"+tokenKey]; sealed {
		t.Fatal("a credential was sealed for an authorization whose account did not match the token")
	}
	for _, sql := range rt.tx.statements {
		if strings.Contains(sql, "oa_id = $2") {
			t.Fatalf("the row was written for a spoofed account: %s", sql)
		}
	}
}

// Re-pointing the row at a NEW administrator withdraws the previous one's sealed
// credential in the same act.
//
// What would be left behind is not a stray blob: the ingress port reads a deposit
// as that person's live consent to be acted for, and nothing else ever reaches it
// — a later disconnect withdraws the row's CURRENT administrator, who is by then
// somebody else.
func TestStartingAnAuthorizationAsSomebodyElseWithdrawsThePreviousAdminsCredential(t *testing.T) {
	rt := newRuntime()
	previous := "22222222-3333-4444-5555-666666666666"
	if err := sealTokens(t.Context(), rt, extension.UserID(previous), livePair(at(20*time.Hour))); err != nil {
		t.Fatalf("sealing the previous admin's credential: %v", err)
	}
	before := connectionRow(statusConnected, nil, cursor{})
	before[4] = previous
	rt.tx.singleRows = [][]any{before, connectionRow(statusPending, nil, cursor{})}

	if _, err := authorize(t.Context(), rt, authorizeArgs()); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if _, still := rt.secrets.stored["user/"+previous+"/"+tokenKey]; still {
		t.Fatal("the superseded administrator's credential is still on deposit, and the ingress port reads a deposit as live consent")
	}
}

// And re-authorizing as the SAME administrator does not withdraw their own
// credential, which they are about to replace.
func TestReauthorizingAsTheSameAdminKeepsTheirDepositUntilItIsReplaced(t *testing.T) {
	rt := newRuntime()
	seal(t, rt, livePair(at(20*time.Hour)))
	rt.tx.singleRows = [][]any{
		connectionRow(statusConnected, nil, cursor{}),
		connectionRow(statusPending, nil, cursor{}),
	}

	if _, err := authorize(t.Context(), rt, authorizeArgs()); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if _, still := rt.secrets.stored["user/"+adminUserID+"/"+tokenKey]; !still {
		t.Fatal("the caller's own credential was withdrawn by their own re-authorization")
	}
}
