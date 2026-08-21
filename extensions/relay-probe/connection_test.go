// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relayprobe

// What the three operations do, and — the part worth the most — whose account
// they do it to.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

const testBaseURL = "https://workspace.example.com"

func connectArgs(base, token string) json.RawMessage {
	return json.RawMessage(`{"base_url":"` + base + `","token":"` + token + `"}`)
}

// The rule this unit exists to keep on its own surface: the member is the
// INVOCATION's, so the credential a colleague could deposit for somebody else
// — which the ingress port would then read as that somebody's consent — cannot
// be deposited at all.
func TestConnectBindsTheTokenToTheCallersOwnMember(t *testing.T) {
	rt := newRuntime()
	rt.tx.noRows = map[int]bool{1: true} // no connection yet
	rt.tx.singleRows = [][]any{connectionRow("11111111-1111-4111-8111-111111111111", callerUserID, testBaseURL, statusConnected, 0, 0)}

	answer, err := connect(context.Background(), rt, connectArgs(testBaseURL, "pat_abc"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	stored, ok := rt.secrets.stored[callerUserID+"/"+tokenKey]
	if !ok {
		t.Fatalf("the token was not deposited for the caller; the namespace holds %v", rt.secrets.stored)
	}
	if string(stored) != "pat_abc" {
		t.Errorf("deposited %q, want the token as given", stored)
	}
	_, args := rt.tx.statementMentioning(t, "ON CONFLICT")
	if args[0] != callerUserID {
		t.Errorf("the row names member %v, want the caller %s — a member a client can name is a member a client can forge", args[0], callerUserID)
	}
	if got := jsonOf[connection](t, answer); got.UserID != callerUserID {
		t.Errorf("the answer names member %q, want the caller's own", got.UserID)
	}
}

// The declared arguments are base_url and token, and the strict decoder refuses
// anything else — which is what stops `user_id` arriving as a member to act
// for, whatever a client believes it can send.
func TestConnectRefusesAnythingButItsDeclaredArguments(t *testing.T) {
	rt := newRuntime()
	_, err := connect(context.Background(), rt,
		json.RawMessage(`{"base_url":"`+testBaseURL+`","token":"pat_abc","user_id":"`+callerUserID+`"}`))
	if err == nil {
		t.Fatal("connect accepted an undeclared member argument")
	}
	if len(rt.secrets.stored) != 0 {
		t.Errorf("a refused connect still deposited a credential: %v", rt.secrets.stored)
	}
}

// A base URL this unit would refuse to DIAL must not be storable as a working
// connection: the two checks are one parser, so a member reads the refusal on
// the screen rather than watching a connection fail on a cadence.
func TestConnectRefusesABaseURLThePollCouldNotDial(t *testing.T) {
	for name, base := range map[string]string{
		"plain http":             "http://workspace.example.com",
		"an address literal":     "https://169.254.169.254",
		"credentials in the URL": "https://user:pass@workspace.example.com",
		"a query string":         "https://workspace.example.com?next=/api",
		"no host at all":         "https:///api",
	} {
		t.Run(name, func(t *testing.T) {
			rt := newRuntime()
			_, err := connect(context.Background(), rt, connectArgs(base, "pat_abc"))
			if !errors.Is(err, extension.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid for %s", err, name)
			}
			if len(rt.secrets.stored) != 0 {
				t.Errorf("a refused base URL still deposited a credential")
			}
		})
	}
}

// A tick has nobody behind it, so there is nobody whose credential this would
// be. It is refused rather than deposited under the zero member.
func TestConnectRefusesAnInvocationWithNobodyBehindIt(t *testing.T) {
	rt := newRuntime().unattended()
	_, err := connect(context.Background(), rt, connectArgs(testBaseURL, "pat_abc"))
	if !errors.Is(err, extension.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	if len(rt.secrets.stored) != 0 {
		t.Errorf("an unattended connect deposited a credential: %v", rt.secrets.stored)
	}
}

// Reconnecting replaces the credential and leaves the cursor alone: a member
// rotating a token has not asked to re-read their inbox.
func TestReconnectingKeepsTheCursorAndRecordsAnUpdate(t *testing.T) {
	rt := newRuntime()
	existing := connectionRow("11111111-1111-4111-8111-111111111111", callerUserID, testBaseURL, statusReauth, 900, 0)
	reconnected := connectionRow("11111111-1111-4111-8111-111111111111", callerUserID, testBaseURL, statusConnected, 900, 0)
	rt.tx.singleRows = [][]any{existing, reconnected}

	if _, err := connect(context.Background(), rt, connectArgs(testBaseURL, "pat_new")); err != nil {
		t.Fatalf("connect: %v", err)
	}
	upsert, _ := rt.tx.statementMentioning(t, "ON CONFLICT")
	// The cursor is written CONDITIONALLY — kept when the deployment is the
	// same, reset when it is not — so what this asserts is the condition, not
	// the absence of the column. An unconditional write here would re-walk a
	// member's whole feed on every token rotation; an unconditional KEEP would
	// leave a member who moved deployments with a cursor from another id
	// sequence, and their feed would stop dead while the row looked healthy.
	if !strings.Contains(upsert, "CASE WHEN "+connectionTable+".base_url = EXCLUDED.base_url") {
		t.Errorf("the upsert does not make the cursor conditional on the deployment:\n%s", upsert)
	}
	if !strings.Contains(upsert, "status = '"+statusConnected+"'") {
		t.Error("reconnecting does not clear a parked status, so a member who pastes a working token stays parked")
	}
	if len(rt.tx.audited) != 1 || rt.tx.audited[0].Action != extension.AuditUpdate {
		t.Fatalf("recorded %+v, want one update — a reconnect recorded as a create would read as a connection that appeared now", rt.tx.audited)
	}
}

// The other half of the same rule, at the level a test can see it here: the
// statement keeps the cursor for the SAME deployment and drops it for another.
// The SQL is the only place this decision exists, so the test reads the SQL —
// what it does against real rows is the integration lane's question.
func TestReconnectingToAnotherDeploymentDropsTheCursor(t *testing.T) {
	rt := newRuntime()
	existing := connectionRow("11111111-1111-4111-8111-111111111111", callerUserID, testBaseURL, statusConnected, 900, 0)
	moved := connectionRow("11111111-1111-4111-8111-111111111111", callerUserID, "https://other.example.com", statusConnected, 0, 0)
	rt.tx.singleRows = [][]any{existing, moved}

	if _, err := connect(context.Background(), rt, connectArgs("https://other.example.com", "pat_abc")); err != nil {
		t.Fatalf("connect: %v", err)
	}
	upsert, _ := rt.tx.statementMentioning(t, "ON CONFLICT")
	for _, column := range []string{"high_water_mark", "backfill_before", "pending_high_water_mark", "provider_workspace_id"} {
		if !strings.Contains(upsert, column+" = CASE WHEN") {
			t.Errorf("%s is not reset when the deployment changes — its ids belong to the deployment that issued them", column)
		}
	}
}

// A connection that appears is a create, and the ledger's own grammar refuses a
// create carrying a before-image — so getting this wrong is not cosmetic.
func TestAFirstConnectRecordsACreate(t *testing.T) {
	rt := newRuntime()
	rt.tx.noRows = map[int]bool{1: true}
	rt.tx.singleRows = [][]any{connectionRow("11111111-1111-4111-8111-111111111111", callerUserID, testBaseURL, statusConnected, 0, 0)}

	if _, err := connect(context.Background(), rt, connectArgs(testBaseURL, "pat_abc")); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if len(rt.tx.audited) != 1 || rt.tx.audited[0].Action != extension.AuditCreate {
		t.Fatalf("recorded %+v, want one create", rt.tx.audited)
	}
	if len(rt.tx.published) != 1 || rt.tx.published[0].Verb != eventConnected {
		t.Fatalf("published %+v, want one %q event", rt.tx.published, eventConnected)
	}
}

// The credential is sealed BEFORE the row exists, so a failure between them
// leaves a token nothing polls rather than a connection that looks live and
// cannot read anything.
func TestConnectDepositsTheCredentialBeforeTheRow(t *testing.T) {
	rt := newRuntime()
	rt.txErr = errors.New("the transaction could not open")
	if _, err := connect(context.Background(), rt, connectArgs(testBaseURL, "pat_abc")); err == nil {
		t.Fatal("connect answered success with no row written")
	}
	if len(rt.secrets.stored) != 1 {
		t.Errorf("the credential was not deposited first: %v", rt.secrets.stored)
	}
}

// Status is about the caller's own connection, and reports its absence as an
// ordinary state rather than an error — not having connected yet is what the
// screen shows most of the time.
func TestStatusReportsTheCallersOwnConnectionOrItsAbsence(t *testing.T) {
	rt := newRuntime()
	rt.tx.noRows = map[int]bool{1: true}
	answer, err := status(context.Background(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	absent := jsonOf[struct {
		Connected  bool        `json:"connected"`
		Connection *connection `json:"connection"`
	}](t, answer)
	if absent.Connected || absent.Connection != nil {
		t.Fatalf("an unconnected member reads as %+v, want connected:false and no connection", absent)
	}
	_, args := rt.tx.statementMentioning(t, "SELECT ")
	if args[0] != callerUserID {
		t.Errorf("status read member %v, want the caller's own — a connection says when somebody was last messaged", args[0])
	}
}

// Disconnect removes the credential AND the row: what ends the authority is the
// credential, and what stops the poll enumerating is the row.
func TestDisconnectRemovesTheCredentialAndTheRow(t *testing.T) {
	rt := newRuntime()
	rt.secrets.stored[callerUserID+"/"+tokenKey] = []byte("pat_abc")
	rt.tx.singleRows = [][]any{connectionRow("11111111-1111-4111-8111-111111111111", callerUserID, testBaseURL, statusConnected, 900, 0)}

	answer, err := disconnect(context.Background(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if len(rt.secrets.stored) != 0 {
		t.Errorf("the credential survived the disconnect: %v", rt.secrets.stored)
	}
	if sql, _ := rt.tx.statementMentioning(t, "DELETE"); !strings.Contains(sql, connectionTable) {
		t.Errorf("the delete does not name this unit's table: %s", sql)
	}
	if got := jsonOf[struct {
		Disconnected bool `json:"disconnected"`
	}](t, answer); !got.Disconnected {
		t.Error("a removed connection reported disconnected:false")
	}
	if len(rt.tx.audited) != 1 || rt.tx.audited[0].Action != extension.AuditErase {
		t.Fatalf("recorded %+v, want one erase", rt.tx.audited)
	}
}

// Disconnecting twice is not an error, and the second call must still take the
// credential away: a member clicking twice must not leave a token on deposit
// because the row had already gone.
func TestDisconnectingWithNoConnectionStillClearsTheCredential(t *testing.T) {
	rt := newRuntime()
	rt.secrets.stored[callerUserID+"/"+tokenKey] = []byte("pat_abc")
	rt.tx.noRows = map[int]bool{1: true}

	answer, err := disconnect(context.Background(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if got := jsonOf[struct {
		Disconnected bool `json:"disconnected"`
	}](t, answer); got.Disconnected {
		t.Error("removing nothing reported disconnected:true")
	}
	if len(rt.secrets.stored) != 0 {
		t.Errorf("the credential survived a disconnect with no row: %v", rt.secrets.stored)
	}
	if len(rt.tx.audited) != 0 {
		t.Errorf("recorded %+v for a connection that was not there", rt.tx.audited)
	}
}

// A token is opaque, so the only honest checks are that it is there and that it
// is not a paste of something else entirely.
func TestConnectRefusesATokenItCannotSeal(t *testing.T) {
	for name, token := range map[string]string{
		"nothing at all": "   ",
		"a whole page":   strings.Repeat("x", maxTokenBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			rt := newRuntime()
			if _, err := connect(context.Background(), rt, connectArgs(testBaseURL, token)); !errors.Is(err, extension.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}
