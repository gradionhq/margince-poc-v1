// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The transport half. The two things worth proving here are that a message
// cannot go to the wrong human, and that a send whose outcome nobody knows is
// never retried — Zalo accepts no idempotency key, so a retry there messages a
// customer twice with nothing in the system able to detect it.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// outbound is a reply the core has staged, as the dispatcher hands it over.
func outbound(recipient, body string) extension.OutboundMessage {
	return extension.OutboundMessage{
		// The member the core names is the rep who staged it. This unit
		// deliberately does not use it to pick a credential: an Official Account
		// has one identity on the wire and every reply goes out as the account.
		Member:         "11111111-2222-3333-4444-555555555555",
		Recipient:      extension.ChannelIdentity{Provider: provider, ChannelUserID: recipient},
		Body:           body,
		IdempotencyKey: "delivery-1",
		Attempt:        1,
	}
}

// sendableRuntime is a live connection with a credential on deposit for the
// authorizing admin — never for the rep the core names.
func sendableRuntime(t *testing.T) *fakeRuntime {
	t.Helper()
	rt := newRuntime()
	if err := sealTokens(t.Context(), rt, adminUserID, livePair(at(20*time.Hour))); err != nil {
		t.Fatalf("sealing the credential: %v", err)
	}
	rt.secrets.stored["workspace//"+appSecretKey] = []byte("secret")
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, nil, cursor{})}
	return rt
}

// A reply leaves under the AUTHORIZING ADMIN's credential and as the Official
// Account, and the recipient on the wire is the bare account id with this
// installation's own namespace stripped off.
func TestAReplyLeavesAsTheAccountUnderTheAuthorizingAdminsCredential(t *testing.T) {
	rt := sendableRuntime(t)
	fake := newZaloFake(t)

	receipt, err := sendVia(t.Context(), rt, outbound(fixtureOAID+":user-1", "xin chào"),
		fake.dial(), &fakeGrants{}, frozen(at(0)))
	if err != nil {
		t.Fatalf("sendVia: %v", err)
	}
	if receipt.ProviderMessageID != "sent-1" {
		t.Fatalf("receipt = %q, want the provider's own message id", receipt.ProviderMessageID)
	}
	if len(fake.sent) != 1 {
		t.Fatalf("the provider saw %d sends, want 1", len(fake.sent))
	}
	recipient, ok := fake.sent[0]["recipient"].(map[string]any)
	if !ok || recipient["user_id"] != "user-1" {
		t.Fatalf("the send named %v, want the bare account id with the account namespace stripped", fake.sent[0])
	}
}

// A recipient carrying ANOTHER account's namespace is refused rather than
// trimmed. The bare id inside it is well-formed and names a different person at
// the account now connected, so delivering to it is a mistake nothing downstream
// could detect.
func TestAReplyToARecipientOfAnotherAccountIsRefusedBeforeAnythingIsSent(t *testing.T) {
	rt := sendableRuntime(t)
	fake := newZaloFake(t)

	_, err := sendVia(t.Context(), rt, outbound("9999999999:user-1", "xin chào"),
		fake.dial(), &fakeGrants{}, frozen(at(0)))
	if !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("error = %v, want the recipient refused", err)
	}
	if len(fake.sent) != 0 {
		t.Fatal("a message was sent to a recipient belonging to a different Official Account")
	}
}

// A POST whose ANSWER never came back is the one failure this unit must report as
// unknowable. The core retries every refusal it is not told is unanswerable, and
// this provider offers no way for a later attempt to discover that an earlier one
// arrived.
func TestAnUnansweredSendIsReportedAsAnUnknownOutcomeRatherThanRetried(t *testing.T) {
	rt := sendableRuntime(t)
	// A dial that reaches nothing: the request goes out and nothing comes back.
	dial := func(string) *client {
		api := newClient("token")
		api.base = "http://127.0.0.1:1"
		return api
	}

	_, err := sendVia(t.Context(), rt, outbound(fixtureOAID+":user-1", "xin chào"),
		dial, &fakeGrants{}, frozen(at(0)))
	if !errors.Is(err, extension.ErrSendOutcomeUnknown) {
		t.Fatalf("error = %v, want the unknown-outcome class — anything else has the core retry a message that may already have arrived", err)
	}
}

// And ONLY that one. A refusal the provider actually sent is an ANSWER, so
// nothing was transmitted and reporting it as unknowable would stop a delivery
// that could simply be corrected.
func TestARefusalTheProviderActuallySentIsNotAnUnknownOutcome(t *testing.T) {
	rt := sendableRuntime(t)
	fake := newZaloFake(t)
	fake.errorCode = codeInvalidArgument

	_, err := sendVia(t.Context(), rt, outbound(fixtureOAID+":user-1", "xin chào"),
		fake.dial(), &fakeGrants{}, frozen(at(0)))
	if errors.Is(err, extension.ErrSendOutcomeUnknown) {
		t.Fatalf("a refusal the provider sent was reported as unknowable: %v", err)
	}
	if !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("error = %v, want a definite refusal of this delivery", err)
	}
	if !strings.Contains(err.Error(), "recipient") {
		t.Fatalf("the refusal does not name what a rep can act on: %v", err)
	}
}

// A credential that cannot be renewed is the one PERMANENT pre-flight failure:
// reporting it as transient would have the core retry a token that will never be
// accepted again.
func TestACredentialThatCannotBeRenewedRefusesRatherThanRetrying(t *testing.T) {
	for name, cause := range map[string]error{
		"a rejected credential": errUnauthorized,
		"an absent credential":  errCredentialGone,
		"a lost rotation":       errRotationLost,
	} {
		t.Run(name, func(t *testing.T) {
			if err := sendRefusal(cause); !errors.Is(err, extension.ErrForbidden) {
				t.Fatalf("error = %v, want a permanent refusal", err)
			}
		})
	}
	// And everything else stays transient, which is the conservative posture:
	// parking a message that would have sent is recoverable by a human.
	if errors.Is(sendRefusal(errTransient), extension.ErrForbidden) {
		t.Fatal("an unreachable provider was reported as a permanent refusal")
	}
	if errors.Is(sendRefusal(errRefreshInFlight), extension.ErrForbidden) {
		t.Fatal("another caller renewing the credential was reported as a permanent refusal")
	}
}

// Zalo caps a consultation message at 2000 characters and answers a longer one
// with an argument refusal, which the core would read as a permanent failure of a
// message the rep can see in their own timeline. Refusing here says what to do
// about it instead.
func TestAMessageOverTheProvidersCapIsRefusedWithWhatToDo(t *testing.T) {
	rt := sendableRuntime(t)
	fake := newZaloFake(t)

	_, err := sendVia(t.Context(), rt, outbound(fixtureOAID+":user-1", strings.Repeat("á", maxSendRunes+1)),
		fake.dial(), &fakeGrants{}, frozen(at(0)))
	if !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("error = %v, want the over-length refusal", err)
	}
	if !strings.Contains(err.Error(), "Shorten") && !strings.Contains(err.Error(), "shorten") {
		t.Fatalf("the refusal does not say what to do: %v", err)
	}
	if len(fake.sent) != 0 {
		t.Fatal("an over-length message was sent")
	}
	// The count is in CHARACTERS, not bytes: a Vietnamese message is multi-byte
	// throughout, and a byte count would refuse it at well under the cap the
	// provider publishes.
	if _, err := sendVia(t.Context(), rt, outbound(fixtureOAID+":user-1", strings.Repeat("á", maxSendRunes)),
		fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("a message exactly at the cap was refused: %v", err)
	}
}

// An empty message has nothing to deliver, and a provider refusal for it would be
// a definite failure of a delivery the rep would have to work out for themselves.
func TestAnEmptyMessageIsRefusedBeforeItReachesTheProvider(t *testing.T) {
	rt := sendableRuntime(t)
	fake := newZaloFake(t)

	_, err := sendVia(t.Context(), rt, outbound(fixtureOAID+":user-1", "   "),
		fake.dial(), &fakeGrants{}, frozen(at(0)))
	if !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("error = %v, want the empty message refused", err)
	}
	if len(fake.sent) != 0 {
		t.Fatal("an empty message was sent")
	}
}

// LIVENESS IS ANSWERED WITHOUT SPENDING THE CREDENTIAL. The obvious dry run —
// call the provider and see whether the token is accepted — spends it on every
// pre-flight, and the pre-flight runs three times per delivery.
func TestLivenessIsAnsweredWithoutReachingTheProvider(t *testing.T) {
	rt := newRuntime()
	expiresAt := at(20 * time.Hour)
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, &expiresAt, cursor{})}
	fake := newZaloFake(t)

	usable, err := liveVia(t.Context(), rt, frozen(at(0)))
	if err != nil {
		t.Fatalf("liveVia: %v", err)
	}
	if !usable {
		t.Fatal("a live connection reported itself unusable")
	}
	if len(fake.calls) != 0 {
		t.Fatalf("liveness reached the provider %v; it must answer without spending the credential", fake.calls)
	}
}

// A CONFIRMED no answers false and the delivery parks where a human can see it.
// No connection and a parked one are the same answer, because neither can carry a
// message and the remedy for both is an administrator on the setup screen.
func TestAConfirmedNoAnswersFalseSoTheDeliveryParks(t *testing.T) {
	for name, arm := range map[string]struct {
		status  string
		noRows  bool
		expired bool
	}{
		"no connection at all":  {noRows: true},
		"one awaiting reauth":   {status: statusReauth},
		"one whose tier lapsed": {status: statusTierLapse},
		"an expired credential": {status: statusConnected, expired: true},
	} {
		t.Run(name, func(t *testing.T) {
			rt := newRuntime()
			if arm.noRows {
				rt.tx.noRows = map[int]bool{1: true}
			} else {
				expiresAt := at(20 * time.Hour)
				if arm.expired {
					expiresAt = at(-time.Minute)
				}
				rt.tx.singleRows = [][]any{connectionRow(arm.status, &expiresAt, cursor{})}
			}
			usable, err := liveVia(t.Context(), rt, frozen(at(0)))
			if err != nil {
				t.Fatalf("liveVia: %v", err)
			}
			if usable {
				t.Fatal("an unusable connection reported itself live, so a message would be staged against it")
			}
		})
	}
}

// "I could not tell" returns an ERROR and the delivery is retried. Collapsing it
// into a false would park a message that is probably deliverable; collapsing it
// into a true would stage one against a credential nobody has confirmed.
func TestAnUntellableLivenessIsAnErrorRatherThanEitherAnswer(t *testing.T) {
	for name, expiry := range map[string]string{
		"no expiry recorded":   "",
		"an unreadable expiry": "yesterday afternoon",
	} {
		t.Run(name, func(t *testing.T) {
			conn := connectedConn()
			conn.AccessTokenExpiresAt = expiry
			usable, err := credentialStillWithinItsLife(conn, at(0))
			if err == nil {
				t.Fatal("a connection whose usability could not be established answered a definite yes or no")
			}
			if usable {
				t.Fatal("an untellable liveness answered true alongside its error")
			}
		})
	}
	// And a connection that DOES record one answers definitely, in both
	// directions, so the untellable answer stays reserved for what it is for.
	conn := connectedConn()
	conn.AccessTokenExpiresAt = at(time.Hour).Format(time.RFC3339)
	if usable, err := credentialStillWithinItsLife(conn, at(0)); err != nil || !usable {
		t.Fatalf("a credential inside its life answered %v, %v", usable, err)
	}
	conn.AccessTokenExpiresAt = at(-time.Hour).Format(time.RFC3339)
	if usable, err := credentialStillWithinItsLife(conn, at(0)); err != nil || usable {
		t.Fatalf("an expired credential answered %v, %v", usable, err)
	}
}

// The row could not be read at ALL — that is this installation's database rather
// than an answer about the provider, and it is retried.
func TestADatabaseThatCannotBeReadIsRetriedRatherThanParked(t *testing.T) {
	rt := newRuntime()
	rt.txErr = extension.ErrRuntimeExpired

	usable, err := liveVia(t.Context(), rt, frozen(at(0)))
	if !errors.Is(err, extension.ErrRuntimeExpired) {
		t.Fatalf("error = %v, want the read failure propagated", err)
	}
	if usable {
		t.Fatal("a failed read answered that the connection is live")
	}
}

// A send renews the credential on the way through, so a message staged just
// inside the renewal margin still leaves — and it leaves under the token that was
// just issued rather than the one about to expire.
func TestASendRenewsTheCredentialOnTheWayThrough(t *testing.T) {
	rt := newRuntime()
	if err := sealTokens(t.Context(), rt, adminUserID, livePair(at(-time.Hour))); err != nil {
		t.Fatalf("sealing the credential: %v", err)
	}
	rt.secrets.stored["workspace//"+appSecretKey] = []byte("secret")
	rt.tx.singleRows = [][]any{
		connectionRow(statusConnected, nil, cursor{}),
		connectionRow(statusConnected, nil, cursor{}),
		connectionRow(statusConnected, nil, cursor{}),
	}
	grants := &fakeGrants{rotated: tokenPair{AccessToken: "access-2", RefreshToken: "refresh-2", ExpiresAt: at(25 * time.Hour)}}
	fake := newZaloFake(t)

	if _, err := sendVia(t.Context(), rt, outbound(fixtureOAID+":user-1", "xin chào"),
		fake.dial(), grants, frozen(at(0))); err != nil {
		t.Fatalf("sendVia: %v", err)
	}
	if grants.rotations != 1 {
		t.Fatalf("the credential was renewed %d times on the way through, want once", grants.rotations)
	}
	onDeposit, err := unsealTokens(t.Context(), rt, adminUserID)
	if err != nil {
		t.Fatalf("reading back the credential: %v", err)
	}
	if onDeposit.AccessToken != "access-2" {
		t.Fatalf("the credential on deposit is %q; a send must keep what it renewed", onDeposit.AccessToken)
	}
}

// A SENT MESSAGE IS REMEMBERED, so the poll does not read it back as a second
// copy of one the core has already written.
//
// The provider's walk is global and includes the account's own outbound, so
// every reply staged through the timeline comes back on the next tick — and the
// core writes that reply with no provider id, so the two rows cannot meet on a
// natural key.
func TestASentMessageIsRememberedSoTheWalkDoesNotCaptureItBack(t *testing.T) {
	rt := sendableRuntime(t)
	fake := newZaloFake(t)

	if _, err := sendVia(t.Context(), rt, outbound(fixtureOAID+":user-1", "xin chào"),
		fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("sendVia: %v", err)
	}
	sql, args := rt.tx.statementMentioning(t, "DO NOTHING")
	if !strings.Contains(sql, sentTable) {
		t.Fatalf("the marker was not written to this unit's own table: %s", sql)
	}
	if args[0] != fixtureOAID || args[1] != "sent-1" {
		t.Fatalf("the marker recorded %v, want this account and the provider's own message id", args)
	}
}

// A send that returned NO id records nothing, and that is the honest outcome:
// there is no key to suppress on. Inventing one would suppress a real message
// that happened to be read at the same moment.
func TestASendWithNoProviderIdRemembersNothing(t *testing.T) {
	rt := sendableRuntime(t)
	fake := newZaloFake(t)
	fake.sentID = " "

	if _, err := sendVia(t.Context(), rt, outbound(fixtureOAID+":user-1", "xin chào"),
		fake.dial(), &fakeGrants{}, frozen(at(0))); err != nil {
		t.Fatalf("sendVia: %v", err)
	}
	for _, sql := range rt.tx.statements {
		if strings.Contains(sql, "DO NOTHING") {
			t.Fatalf("a marker was written for a send that returned no id: %s", sql)
		}
	}
}
