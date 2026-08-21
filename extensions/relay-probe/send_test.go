// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relayprobe

// The transport half: whose credential carries a message, where it lands, and
// what the core is told when it cannot leave.
//
// The provider is a loopback listener the production constructor would refuse
// to dial, so the factory is injected and the egress guard is left exactly as a
// deployment runs it — the same arrangement the poll's suite uses, and for the
// same reason.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// sendingProvider serves the two calls the transport makes: `me`, which is the
// liveness dry run, and the channel message post.
type sendingProvider struct {
	// unauthorized turns every call into a 401, which is how a revoked token
	// presents — the one refusal this unit treats as permanent.
	unauthorized bool
	// unavailable is the other kind: the provider is there and cannot answer.
	unavailable bool
	// sentID is the provider's own id for the accepted message, empty for a
	// provider that returns none.
	sentID string

	// seen records where the message went and what it said.
	seenPath string
	seenBody map[string]string
	seenAuth string
	// seenResolve is which account the send asked the provider to resolve a
	// conversation for.
	seenResolve struct {
		UserIDs []string `json:"user_ids"`
	}
}

func (p *sendingProvider) dial(t *testing.T) clientFactory {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(server.Close)
	return func(_, token string) (*client, error) {
		base, err := url.Parse(server.URL)
		if err != nil {
			return nil, err
		}
		return &client{base: base, token: token, http: server.Client()}, nil
	}
}

func (p *sendingProvider) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case p.unauthorized:
		w.WriteHeader(http.StatusUnauthorized)
	case p.unavailable:
		w.WriteHeader(http.StatusBadGateway)
	case r.URL.Path == "/api/dms/with-users":
		// The account the send was given, resolved to the conversation it
		// addresses. A group DM is served alongside on purpose: the resolve must
		// pick the 1:1, or a private reply lands in front of a room.
		if err := json.NewDecoder(r.Body).Decode(&p.seenResolve); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"channels":[
			{"slug":"G-group","type":"group_dm"},
			{"slug":"G-1c7u1r29","type":"dm"}]}`))
	case r.Method == http.MethodPost:
		p.seenPath = r.URL.Path
		p.seenAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&p.seenBody); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + p.sentID + `"}`))
	default:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"provider-member","workspace_id":"ws-7","email":"member@installation.test"}`))
	}
}

// connectedMember is a Runtime holding one member's connection and their
// deposited token — the state a send actually runs against.
func connectedMember(t *testing.T) *fakeRuntime {
	t.Helper()
	rt := newRuntime().unattended()
	rt.tx.singleRows = [][]any{
		connectionRow("11111111-1111-4111-8111-111111111111", callerUserID, testBaseURL, statusConnected, 0, 0),
	}
	if err := rt.secrets.PutUser(context.Background(), extension.UserID(callerUserID), tokenKey, []byte("pat_member")); err != nil {
		t.Fatalf("depositing the member's token: %v", err)
	}
	return rt
}

func anOutboundMessage() extension.OutboundMessage {
	return extension.OutboundMessage{
		Member: extension.UserID(callerUserID),
		Recipient: extension.ChannelIdentity{
			Provider: provider, ChannelUserID: "sender-1",
		},
		Body:           "the reply a rep wrote",
		IdempotencyKey: "delivery-1",
		Attempt:        1,
	}
}

// A message leaves on the MEMBER's own credential, into the conversation it was
// read from — and both halves are the whole point of this seam. A send under
// somebody else's token would transmit as a colleague the recipient never wrote
// to, and a send to a resolved-elsewhere destination would open a second
// conversation beside the one on the screen.
func TestASendLeavesOnTheMembersOwnCredentialIntoTheirConversation(t *testing.T) {
	remote := &sendingProvider{sentID: "provider-42"}
	rt := connectedMember(t)

	receipt, err := sendVia(context.Background(), rt, anOutboundMessage(), remote.dial(t))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	switch {
	case len(remote.seenResolve.UserIDs) != 1 || remote.seenResolve.UserIDs[0] != "sender-1":
		t.Errorf("resolved a conversation for %v, want the recipient's own account id — that binding is the person", remote.seenResolve.UserIDs)
	case remote.seenPath != "/api/channels/G-1c7u1r29/messages":
		t.Errorf("posted to %q, want the 1:1 conversation resolved from the account — the group DM served beside it would put a private reply in front of a room", remote.seenPath)
	case remote.seenBody["content"] != "the reply a rep wrote":
		t.Errorf("body = %q, want the rep's own words", remote.seenBody["content"])
	case remote.seenAuth != "Bearer pat_member":
		t.Errorf("Authorization = %q, want the member's own deposited token", remote.seenAuth)
	case receipt.ProviderMessageID != "provider-42":
		t.Errorf("receipt = %q, want the provider's id — it is what makes a later reply anchorable", receipt.ProviderMessageID)
	}
}

// A provider that accepts the message and returns no id is a SUCCESS with an
// empty receipt. The message is gone either way, and reporting a failure would
// have the core retry a delivery the recipient already has.
func TestAnAcceptedSendWithNoProviderIDIsStillASend(t *testing.T) {
	rt := connectedMember(t)

	receipt, err := sendVia(context.Background(), rt, anOutboundMessage(), (&sendingProvider{}).dial(t))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if receipt.ProviderMessageID != "" {
		t.Errorf("receipt = %q, want an empty one rather than an invented id that anchors nothing", receipt.ProviderMessageID)
	}
}

// A revoked credential is the ONE permanent refusal, and it is reported as
// ErrForbidden so the core can tell it from a provider that would not answer.
// Everything else stays transient: parking a message that would have sent is
// recoverable by a human, and re-sending one that already arrived is not.
func TestASendClassifiesARevokedCredentialAndNothingElse(t *testing.T) {
	for name, tc := range map[string]struct {
		remote    *sendingProvider
		permanent bool
	}{
		"a revoked token is permanent":         {remote: &sendingProvider{unauthorized: true}, permanent: true},
		"an unreachable provider is transient": {remote: &sendingProvider{unavailable: true}},
	} {
		t.Run(name, func(t *testing.T) {
			rt := connectedMember(t)

			_, err := sendVia(context.Background(), rt, anOutboundMessage(), tc.remote.dial(t))

			if err == nil {
				t.Fatal("the send reported success against a provider that refused it")
			}
			if got := errors.Is(err, extension.ErrForbidden); got != tc.permanent {
				t.Errorf("ErrForbidden = %v, want %v — got %v", got, tc.permanent, err)
			}
		})
	}
}

// A POST whose ANSWER never came back is reported as an unknown outcome, and
// this is the finding the whole class exists for.
//
// The core retries every refusal it is not told is unanswerable, and this
// provider offers no idempotency key and no prior-send lookup — so a lost
// response reported as an ordinary transport failure delivers the rep's message
// twice, with nothing in the system able to detect it. The delivery must stop
// with the uncertainty on the record instead.
//
// The connection is closed mid-request rather than refused, because that is the
// shape being tested: the request went OUT.
func TestALostAnswerToTheSendIsAnUnknownOutcome(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/channels/G-1c7u1r29/messages" {
			// The liveness dry run and the conversation resolve both answer
			// normally: what is under test is the POST that transmits, and a
			// pre-flight that failed instead would prove the opposite case.
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/api/dms/with-users" {
				_, _ = w.Write([]byte(`{"channels":[{"slug":"G-1c7u1r29","type":"dm"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"provider-member","workspace_id":"ws-7","email":"member@installation.test"}`))
			return
		}
		// The message post: hijack and drop the connection, so the request is
		// delivered and no answer ever is.
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijacking the connection: %v", err)
			return
		}
		//craft:ignore swallowed-errors dropping the connection IS the test; a close error here would mean the drop happened twice, which is the same fact
		_ = conn.Close()
	}))
	defer server.Close()
	dial := func(_, token string) (*client, error) {
		base, err := url.Parse(server.URL)
		if err != nil {
			return nil, err
		}
		return &client{base: base, token: token, http: server.Client()}, nil
	}

	_, err := sendVia(context.Background(), connectedMember(t), anOutboundMessage(), dial)

	if !errors.Is(err, extension.ErrSendOutcomeUnknown) {
		t.Fatalf("send → %v, want extension.ErrSendOutcomeUnknown; retried, this message arrives twice and nothing can tell", err)
	}
}

// The mirror, and it is what keeps the class from swallowing everything: a
// failure BEFORE the POST transmitted nothing, so it must stay retryable. The
// conversation resolve is the case in hand — it is a read.
func TestAFailureBeforeTheSendIsNotAnUnknownOutcome(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/dms/with-users" {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijacking the connection: %v", err)
				return
			}
			//craft:ignore swallowed-errors dropping the connection IS the test — the request went out and no answer comes back; a close error is that same fact reported twice
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"provider-member","workspace_id":"ws-7","email":"member@installation.test"}`))
	}))
	defer server.Close()
	dial := func(_, token string) (*client, error) {
		base, err := url.Parse(server.URL)
		if err != nil {
			return nil, err
		}
		return &client{base: base, token: token, http: server.Client()}, nil
	}

	_, err := sendVia(context.Background(), connectedMember(t), anOutboundMessage(), dial)

	if err == nil {
		t.Fatal("a send whose recipient could not be resolved reported success")
	}
	if errors.Is(err, extension.ErrSendOutcomeUnknown) {
		t.Errorf("a failure that transmitted nothing was reported as an unknown outcome (%v); the delivery would stop with a message a retry would have sent", err)
	}
}

// A member with no connection cannot be sent for, and the refusal says which
// fact it is: ErrNotFound distinguishes "this member disconnected" from "the
// provider would not answer", which the core turns into a park rather than a
// retry.
func TestASendForAMemberWithNoConnectionIsNotFound(t *testing.T) {
	rt := newRuntime().unattended()
	rt.tx.noRows = map[int]bool{1: true}

	_, err := sendVia(context.Background(), rt, anOutboundMessage(), (&sendingProvider{}).dial(t))

	if !errors.Is(err, extension.ErrNotFound) {
		t.Fatalf("send → %v, want extension.ErrNotFound for a member who has no connection", err)
	}
}

// Liveness is the answer the core needs BEFORE it hands over a message, and the
// three cases are told apart on purpose: a confirmed no parks the delivery
// where a human can see it, and an "I could not tell" is retried. Collapsing
// them either strands a deliverable message or re-sends a refused one.
func TestLivenessAnswersConfirmedNoAndUnknownDifferently(t *testing.T) {
	t.Run("a usable connection", func(t *testing.T) {
		live, err := liveVia(context.Background(), connectedMember(t),
			extension.UserID(callerUserID), (&sendingProvider{}).dial(t))
		if err != nil || !live {
			t.Fatalf("live = %v, %v; want true with no error", live, err)
		}
	})
	t.Run("a revoked token is a confirmed no", func(t *testing.T) {
		live, err := liveVia(context.Background(), connectedMember(t),
			extension.UserID(callerUserID), (&sendingProvider{unauthorized: true}).dial(t))
		if err != nil {
			t.Fatalf("a revoked token answered an error (%v); it is a fact, and the delivery must park rather than retry", err)
		}
		if live {
			t.Error("a revoked token reported the connection as usable")
		}
	})
	t.Run("a member who disconnected is a confirmed no", func(t *testing.T) {
		rt := newRuntime().unattended()
		rt.tx.noRows = map[int]bool{1: true}
		live, err := liveVia(context.Background(), rt, extension.UserID(callerUserID), (&sendingProvider{}).dial(t))
		if err != nil {
			t.Fatalf("no connection answered an error (%v); there is nothing to retry into", err)
		}
		if live {
			t.Error("a member with no connection reported the connection as usable")
		}
	})
	t.Run("a provider that could not answer is not a no", func(t *testing.T) {
		live, err := liveVia(context.Background(), connectedMember(t),
			extension.UserID(callerUserID), (&sendingProvider{unavailable: true}).dial(t))
		if err == nil {
			t.Fatal("an unreachable provider answered a verdict; parking on one destroys a send nothing is wrong with")
		}
		if live {
			t.Error("an unreachable provider reported the connection as usable")
		}
	})
}
