// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// What the four operations must do, and — more of this file than usual — what
// they must never do. This unit's credential reads a human's personal life, so
// the properties asserted here are mostly negative: no operation acts for
// anybody but its caller, no operation returns a byte of a session, and no
// ordering leaves a live credential behind a screen that says otherwise.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// theOperations is every tool this unit publishes, so a rule that must hold for
// all of them is asserted over all of them — including one added later, which
// is the case a hand-written list of three quietly stops covering.
func theOperations() map[string]extension.ToolHandler {
	handlers := map[string]extension.ToolHandler{}
	for _, tool := range New().Tools {
		handlers[tool.Name] = tool.Handle
	}
	return handlers
}

func TestNoOperationActsForAnInvocationWithNobodyBehindIt(t *testing.T) {
	t.Parallel()
	for name, handle := range theOperations() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt := newRuntime().unattended()
			if _, err := handle(context.Background(), rt, json.RawMessage(`{}`)); !errors.Is(err, extension.ErrForbidden) {
				t.Fatalf("a job tick reached %s and was answered %v; it must be refused", name, err)
			}
			if len(rt.secrets.stored) != 0 || len(rt.tx.statements) != 0 {
				t.Fatalf("%s touched a secret or the database for an invocation with no principal", name)
			}
		})
	}
}

// The single most important property of this surface: an operation cannot be
// pointed at another member. The decoder refuses the argument, and this is the
// test that keeps it refused when somebody adds a field to a request struct.
func TestNoOperationAcceptsAMemberArgument(t *testing.T) {
	t.Parallel()
	for name, handle := range theOperations() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt := newRuntime()
			args := json.RawMessage(`{"user_id":"` + colleagueUserID + `"}`)
			if _, err := handle(context.Background(), rt, args); err == nil {
				t.Fatalf("%s accepted a document naming another member", name)
			}
			for key := range rt.secrets.stored {
				if strings.HasPrefix(key, colleagueUserID) {
					t.Fatalf("%s deposited something in %s's namespace", name, colleagueUserID)
				}
			}
		})
	}
}

func TestConnectStartSealsTheHandshakeAndReturnsTheProviderSOwnCode(t *testing.T) {
	t.Parallel()
	expires := time.Date(2026, time.August, 18, 10, 0, 0, 0, time.UTC)
	login := &fakeLogin{code: zaloQRCode{ImageDataURL: "data:image/png;base64,QUJD", ExpiresAt: expires}}
	rt := newRuntime()

	out, err := connectStartVia(context.Background(), rt, json.RawMessage(`{}`), login.handshake())
	if err != nil {
		t.Fatalf("starting a login: %v", err)
	}
	got := jsonOf[struct {
		QRImage   string `json:"qr_image"`
		ExpiresAt string `json:"expires_at"`
	}](t, out)
	if got.QRImage != "data:image/png;base64,QUJD" {
		t.Fatalf("the QR image was rewritten: %q", got.QRImage)
	}
	if got.ExpiresAt != expires.Format(time.RFC3339) {
		t.Fatalf("expiry rendered as %q, want RFC 3339", got.ExpiresAt)
	}
	if _, ok := rt.secrets.stored[callerUserID+"/"+pendingKey]; !ok {
		t.Fatal("the in-flight handshake was not sealed under the caller")
	}
	if len(rt.tx.statements) != 0 {
		t.Fatal("starting a login wrote a connection row; nothing is connected until a scan is confirmed")
	}
}

func TestConnectStartDepositsNothingWhenTheHandshakeFails(t *testing.T) {
	t.Parallel()
	login := &fakeLogin{startErr: errors.New("zalo would not issue a code")}
	rt := newRuntime()

	if _, err := connectStartVia(context.Background(), rt, json.RawMessage(`{}`), login.handshake()); err == nil {
		t.Fatal("a failed handshake was reported as a started login")
	}
	if len(rt.secrets.stored) != 0 {
		t.Fatal("a failed handshake still sealed something")
	}
}

// The ordering rule, asserted as an ordering rather than as two facts: the
// credential is on deposit BEFORE the row that advertises it exists. A row
// without a credential is a connection that looks live and is refused at first
// use; the reverse polls nothing and costs nothing.
func TestConfirmedScanSealsTheSessionBeforeWritingTheRow(t *testing.T) {
	t.Parallel()
	rt, login, live := confirmedScan()
	rt.tx.noRows = map[int]bool{1: true}
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, "u-42", false)}

	out, err := connectStatusVia(context.Background(), rt, json.RawMessage(`{}`), login.handshake(), live.resume())
	if err != nil {
		t.Fatalf("confirming a scan: %v", err)
	}
	rt.trace.before(t, "put "+callerUserID+"/"+sessionKey, "sql insert")
	rt.trace.before(t, "delete "+callerUserID+"/"+pendingKey, "sql insert")
	if got := jsonOf[struct {
		State       string `json:"state"`
		DisplayName string `json:"display_name"`
	}](t, out); got.State != string(zaloScanConfirmed) || got.DisplayName != "Tin Nguyen" {
		t.Fatalf("a confirmed scan answered %+v", got)
	}
	sql, args := rt.tx.statementMentioning(t, "ON CONFLICT")
	if !strings.Contains(sql, "capture_enabled") {
		t.Fatal("the upsert says nothing about capture_enabled, which decides whether anything is read")
	}
	if args[0] != callerUserID || args[1] != "u-42" {
		t.Fatalf("the row was written for %v with uid %v; both come from the caller and their credential", args[0], args[1])
	}
}

// A confirm that cannot be completed must leave NOTHING behind — no row, and no
// credential either.
//
// The second half is the one worth stating. Every arrangement below fails after
// the point where a session may already be on deposit, and a sealed session
// with no row is invisible to every screen this unit has: the member is told
// they are not connected, the disconnect button has nothing to act on, and a
// credential that reads their whole chat history sits there with no way to
// withdraw it. Asserting only "no row was written" would read like it covered
// this and would not.
func TestAConfirmThatCannotBeCompletedLeavesNoCredentialAndNoRow(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*fakeRuntime, *fakeLogin, *fakeSession){
		"the provider confirmed and returned no session": func(_ *fakeRuntime, login *fakeLogin, _ *fakeSession) {
			login.result.Sealed = nil
		},
		"the resumed session names no account": func(_ *fakeRuntime, _ *fakeLogin, resumed *fakeSession) {
			resumed.uid = ""
		},
		"the session could not be resumed": func(_ *fakeRuntime, _ *fakeLogin, resumed *fakeSession) {
			resumed.resumeErr = errors.New("the cookies were refused")
		},
		"the connection row could not be written": func(rt *fakeRuntime, _ *fakeLogin, _ *fakeSession) {
			rt.txErr = errors.New("the database was briefly unreachable")
		},
	}
	for name, arrange := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt, login, live := confirmedScan()
			arrange(rt, login, live)
			_, err := connectStatusVia(context.Background(), rt, json.RawMessage(`{}`), login.handshake(), live.resume())
			if err == nil {
				t.Fatal("the login was reported as connected")
			}
			for _, sql := range rt.tx.statements {
				// "ON CONFLICT" rather than the insert keyword, for the reason
				// fake_test.go's statementMentioning already gives: the SQL-scope
				// gate reads a statement's table out of the literal beside it, and
				// a bare insert keyword names none.
				if strings.Contains(sql, "ON CONFLICT") {
					t.Fatal("a connection row was written for a login that did not complete")
				}
			}
			if _, ok := rt.secrets.stored[callerUserID+"/"+sessionKey]; ok {
				t.Fatal("a live session was left on deposit with no row: the member is shown \"not connected\" and has no way to withdraw a credential that reads their whole chat history")
			}
		})
	}
}

// The final refusal in connectStatus, held in place. Zalo is an undocumented
// protocol that changes without notice, so a state this unit has never seen is
// a real arrival rather than a hypothetical — and the wrong answer to it is
// treating it as progress and sealing something.
func TestAScanStateThisUnitDoesNotRecogniseAdvancesNothing(t *testing.T) {
	t.Parallel()
	rt, login, resumed := confirmedScan()
	login.result = zaloPollResult{State: zaloScanState("reauthenticate_on_device")}

	_, err := connectStatusVia(context.Background(), rt, json.RawMessage(`{}`), login.handshake(), resumed.resume())
	if err == nil {
		t.Fatal("an unrecognised scan state was reported as progress")
	}
	if !strings.Contains(err.Error(), "reauthenticate_on_device") {
		t.Fatalf("the refusal does not say which state arrived: %v", err)
	}
	if _, ok := rt.secrets.stored[callerUserID+"/"+sessionKey]; ok {
		t.Fatal("a session was sealed for a state this unit cannot interpret")
	}
	if len(rt.tx.statements) != 0 {
		t.Fatal("an unrecognised state wrote a connection row")
	}
}

func TestADeclinedOrExpiredScanDropsTheHandshakeAndConnectsNothing(t *testing.T) {
	t.Parallel()
	for _, state := range []zaloScanState{zaloScanDeclined, zaloScanExpired} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			rt, login, live := confirmedScan()
			login.result = zaloPollResult{State: state}

			out, err := connectStatusVia(context.Background(), rt, json.RawMessage(`{}`), login.handshake(), live.resume())
			if err != nil {
				t.Fatalf("reporting a %s scan: %v", state, err)
			}
			if got := jsonOf[struct {
				State string `json:"state"`
			}](t, out); got.State != string(state) {
				t.Fatalf("the login answered %q for a %s scan", got.State, state)
			}
			if _, ok := rt.secrets.stored[callerUserID+"/"+pendingKey]; ok {
				t.Fatal("a handshake that can no longer be advanced was kept on deposit")
			}
			if _, ok := rt.secrets.stored[callerUserID+"/"+sessionKey]; ok {
				t.Fatal("a session was sealed for a login nobody completed")
			}
			if len(rt.tx.statements) != 0 {
				t.Fatal("a login that ended wrote a connection row")
			}
		})
	}
}

func TestAScanStillInFlightResealsTheAdvancedHandshake(t *testing.T) {
	t.Parallel()
	for _, state := range []zaloScanState{zaloScanWaiting, zaloScanScanned} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			rt, login, live := confirmedScan()
			login.result = zaloPollResult{State: state, DisplayName: "Tin Nguyen"}
			rt.secrets.stored[callerUserID+"/"+pendingKey] = []byte(`{"step":1}`)

			out, err := connectStatusVia(context.Background(), rt, json.RawMessage(`{}`), login.handshake(), live.resume())
			if err != nil {
				t.Fatalf("polling a %s scan: %v", state, err)
			}
			if got := jsonOf[struct {
				State string `json:"state"`
			}](t, out); got.State != string(state) {
				t.Fatalf("answered %q for a %s scan", got.State, state)
			}
			if login.budgeted != pollBudget {
				t.Fatalf("the poll was given %s rather than the bounded %s a request can afford", login.budgeted, pollBudget)
			}
			if _, ok := rt.secrets.stored[callerUserID+"/"+pendingKey]; !ok {
				t.Fatal("a login still in flight lost its handshake, which costs the member a re-scan")
			}
			if live.resumes != 0 {
				t.Fatal("an unconfirmed scan resumed a session")
			}
		})
	}
}

func TestConnectStatusWithNothingInFlightSaysSoWithoutFaulting(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	login, live := &fakeLogin{}, &fakeSession{}

	_, err := connectStatusVia(context.Background(), rt, json.RawMessage(`{}`), login.handshake(), live.resume())
	if !errors.Is(err, extension.ErrNotFound) {
		t.Fatalf("asking about a login nobody started answered %v, want ErrNotFound", err)
	}
	if login.polls != 0 {
		t.Fatal("the provider was polled for a login that does not exist")
	}
}

func TestAPollThatCouldNotReachTheProviderKeepsTheHandshake(t *testing.T) {
	t.Parallel()
	rt, login, live := confirmedScan()
	login.pollErr = errors.New("the provider timed out")

	if _, err := connectStatusVia(context.Background(), rt, json.RawMessage(`{}`), login.handshake(), live.resume()); err == nil {
		t.Fatal("a failed poll was reported as progress")
	}
	if _, ok := rt.secrets.stored[callerUserID+"/"+pendingKey]; !ok {
		t.Fatal("a network blip cost the member their in-flight login")
	}
}

// status renders a connection and answers a yes-or-no about the credential. The
// assertion is on the WHOLE document rather than on named fields: a field added
// later that happens to carry session bytes is exactly what this must catch.
func TestStatusReturnsNoByteOfTheSession(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.secrets.stored[callerUserID+"/"+sessionKey] = []byte(`{"cookies":[{"name":"zpsid","value":"SECRETCOOKIEVALUE"}]}`)
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, "u-42", false)}

	out, err := status(context.Background(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("reading the connection: %v", err)
	}
	if strings.Contains(string(out), "SECRETCOOKIEVALUE") || strings.Contains(string(out), "zpsid") {
		t.Fatalf("the answer carries session material:\n%s", out)
	}
	got := jsonOf[struct {
		Connected        bool `json:"connected"`
		SessionDeposited bool `json:"session_deposited"`
		Connection       struct {
			ZaloUID        string `json:"zalo_uid"`
			CaptureEnabled bool   `json:"capture_enabled"`
			ConnectedAt    string `json:"connected_at"`
		} `json:"connection"`
	}](t, out)
	if !got.Connected || !got.SessionDeposited || got.Connection.ZaloUID != "u-42" {
		t.Fatalf("a connected member reads as %+v", got)
	}
	if got.Connection.CaptureEnabled {
		t.Fatal("capture reads as on for a member who has chosen nothing")
	}
	if got.Connection.ConnectedAt == "" {
		t.Fatal("connected_at was not rendered; the column is a timestamptz and must not scan as text")
	}
}

func TestStatusForAMemberWhoNeverConnectedIsNotAnError(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.noRows = map[int]bool{1: true}

	out, err := status(context.Background(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("not having connected is the ordinary state, and it answered %v", err)
	}
	got := jsonOf[struct {
		Connected        bool `json:"connected"`
		SessionDeposited bool `json:"session_deposited"`
	}](t, out)
	if got.Connected || got.SessionDeposited {
		t.Fatalf("an unconnected member reads as %+v", got)
	}
}

// The withdrawal ordering, and the reason it is asserted as an ordering: the
// credential is what actually reads the member's messages, so it must be gone
// before anything says it is. BOTH secrets — a half-finished handshake is a
// credential too.
func TestDisconnectDeletesBothCredentialsBeforeTouchingTheRow(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.secrets.stored[callerUserID+"/"+sessionKey] = []byte(`{"cookies":[]}`)
	rt.secrets.stored[callerUserID+"/"+pendingKey] = []byte(`{"step":1}`)
	rt.tx.singleRows = [][]any{
		connectionRow(statusConnected, "u-42", true),
		connectionRow(statusDisconnected, "u-42", false),
	}

	out, err := disconnect(context.Background(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("disconnecting: %v", err)
	}
	rt.trace.before(t, "delete "+callerUserID+"/"+sessionKey, "sql select")
	rt.trace.before(t, "delete "+callerUserID+"/"+pendingKey, "sql select")
	if len(rt.secrets.stored) != 0 {
		t.Fatalf("a credential outlived the disconnect: %v", rt.secrets.stored)
	}
	if !jsonOf[struct {
		Disconnected bool `json:"disconnected"`
	}](t, out).Disconnected {
		t.Fatal("a connection that was withdrawn reported no withdrawal")
	}
	sql, _ := rt.tx.statementMentioning(t, "SET status")
	if !strings.Contains(sql, "capture_enabled = false") {
		t.Fatal("disconnecting left capture armed, so re-connecting would resume reading a list nobody re-approved")
	}
	if len(rt.tx.audited) != 1 || rt.tx.published[0].Verb != eventDisconnected {
		t.Fatalf("a withdrawal recorded %d ledger rows and published %v", len(rt.tx.audited), rt.tx.published)
	}
}

func TestDisconnectWithNoConnectionStillWithdrawsWhateverIsOnDeposit(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.secrets.stored[callerUserID+"/"+sessionKey] = []byte(`{"cookies":[]}`)
	rt.tx.noRows = map[int]bool{1: true}

	out, err := disconnect(context.Background(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("disconnecting without a row: %v", err)
	}
	if len(rt.secrets.stored) != 0 {
		t.Fatal("a sealed session survived a disconnect because no row named it")
	}
	if jsonOf[struct {
		Disconnected bool `json:"disconnected"`
	}](t, out).Disconnected {
		t.Fatal("a member with no connection was told one was withdrawn")
	}
}

func TestTheDeclarationMatchesTheConstantsTheHandlersUse(t *testing.T) {
	t.Parallel()
	unit := New()
	if len(unit.Channels) != 1 || unit.Channels[0].Provider != provider {
		t.Fatalf("the declared channel is %+v; provider must be %q", unit.Channels, provider)
	}
	if err := unit.Channels[0].Validate(); err != nil {
		t.Fatalf("the declared channel is refused at boot: %v", err)
	}
	declared := map[string]bool{}
	for _, request := range unit.Secrets {
		if request.Scope != extension.SecretScopeUser {
			t.Fatalf("secret %q is declared at %q scope; this unit holds no installation credential", request.Key, request.Scope)
		}
		if err := request.Validate(); err != nil {
			t.Fatalf("secret %q is refused at boot: %v", request.Key, err)
		}
		declared[request.Key] = true
	}
	if !declared[sessionKey] || !declared[pendingKey] {
		t.Fatalf("the handlers address keys the declaration does not request: %v", declared)
	}
	if len(unit.Ingress) != 0 || len(unit.Jobs) != 0 {
		t.Fatal("this change declares an ingress or a job it cannot yet perform")
	}
}

func TestAnUnreadableHandshakeTellsTheMemberToStartAgain(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.secrets.stored[callerUserID+"/"+pendingKey] = []byte(`not the shape this unit sealed`)
	login, live := &fakeLogin{}, &fakeSession{}

	_, err := connectStatusVia(context.Background(), rt, json.RawMessage(`{}`), login.handshake(), live.resume())
	if !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("an unreadable handshake answered %v, want ErrInvalid — nothing later decodes it either", err)
	}
	if login.polls != 0 {
		t.Fatal("the provider was polled with a handshake this unit could not read")
	}
}

// Re-scanning is an UPDATE, and recording it as a create would put a ledger row
// with no before-image over a state that existed — reading as "this connection
// appeared now" for a member who reconnected after a session died.
func TestReconnectingRecordsAnUpdateAndKeepsTheChoiceForTheSameAccount(t *testing.T) {
	t.Parallel()
	rt, login, live := confirmedScan()
	rt.tx.singleRows = [][]any{
		connectionRow(statusDisconnected, "u-42", false),
		connectionRow(statusConnected, "u-42", true),
	}

	if _, err := connectStatusVia(context.Background(), rt, json.RawMessage(`{}`), login.handshake(), live.resume()); err != nil {
		t.Fatalf("reconnecting: %v", err)
	}
	if len(rt.tx.audited) != 1 || rt.tx.audited[0].Action != extension.AuditUpdate {
		t.Fatalf("a reconnect recorded %+v", rt.tx.audited)
	}
	if rt.tx.published[0].Verb != eventConnected {
		t.Fatalf("a reconnect published %q", rt.tx.published[0].Verb)
	}
	if len(rt.tx.audited[0].Before) == 0 {
		t.Fatal("the ledger row has no before-image for a row that already existed")
	}
}

// confirmedScan is the arrangement most of this file starts from: a member with
// a login in flight, and a provider about to confirm it.
func confirmedScan() (*fakeRuntime, *fakeLogin, *fakeSession) {
	rt := newRuntime()
	rt.secrets.stored[callerUserID+"/"+pendingKey] = []byte(`{"step":1}`)
	sealed := zaloSealed{IMEI: "device-1", UserAgent: "a pinned browser", Language: "vi"}
	login := &fakeLogin{result: zaloPollResult{
		State: zaloScanConfirmed, DisplayName: "Tin Nguyen", Avatar: "https://zalo.example/avatar.png", Sealed: &sealed,
	}}
	return rt, login, &fakeSession{uid: "u-42"}
}
