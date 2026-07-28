// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The outbound path over the real composition, from the accepted HTTP send to
// the bytes on the wire — and, for the echo, back in again through capture.
// Two facts live here that nothing shorter can prove.
//
// THE ECHO COLLAPSES. Gmail files every sent message back into the mailbox, so
// capture re-reads this installation's own mail. If the identity written at
// send is not the identity capture derives from the transmitted bytes, every
// outbound email appears twice on the timeline. The key is therefore DERIVED
// here from the RFC822 the connector actually produced, through the same
// mailmap normalization the connector runs on a message it re-reads. Handing
// the sink the key the send already used would assert the assumption instead
// of the behaviour, and would pass against a broken derivation.
//
// THE ONE-CLICK PAIR ARRIVES TOGETHER. RFC 8058 fixes List-Unsubscribe-Post at
// one literal, so the send path stores only its partner and the connector
// derives the second line at the wire. Nothing but a real message can show
// that a single stored value renders both.
//
// Only the mailbox credential lookup is stubbed (there is no real Google
// here); the store, the dispatcher, the connector, the consent gate, the
// normalization and the sink are all the production objects.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gmail"
	"github.com/gradionhq/margince/backend/internal/modules/capture/mailmap"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// sendingMailbox is the address the stubbed Google reports as the connected
// mailbox owner. It is the From: line the connector stamps, so it is also the
// owner mailmap must be given when the same bytes come back — a different one
// would read the message as inbound.
const sendingMailbox = "sender@fable.test"

// sentMail holds the base64url RFC822 one transmission handed to Gmail.
type sentMail struct{ raw string }

// gmailSendStub answers the endpoints one connect-and-transmit touches and
// keeps the raw MIME it was asked to send.
func gmailSendStub(t *testing.T, captured *sentMail) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("decoding the token request form: %v", err)
			return
		}
		body := map[string]any{"access_token": "access-tok", "expires_in": 3599}
		if r.Form.Get("grant_type") == "authorization_code" {
			body["refresh_token"] = "refresh-tok"
		}
		writeJSON(w, body)
	})
	mux.HandleFunc("/profile", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"emailAddress": sendingMailbox, "historyId": "1001"})
	})
	mux.HandleFunc("/messages/send", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Raw string `json:"raw"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding the send body: %v", err)
			return
		}
		captured.raw = body.Raw
		writeJSON(w, map[string]any{"id": "gmail-msg-1", "threadId": "gmail-thread-1"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// stubMailbox stands in for the capture registry's credential resolution — the
// one seam that cannot run without a real Google. It hands back the REAL Gmail
// connector, so everything the message passes through after this point is
// production code.
type stubMailbox struct {
	sender connector.Sender
	auth   connector.Auth
}

var _ comms.ConnectionResolver = stubMailbox{}

func (m stubMailbox) Resolve(context.Context, ids.UserID, string) (connector.Sender, connector.Auth, []string, error) {
	return m.sender, m.auth, []string{gmailSendScope}, nil
}

// workspaceID is the fixture's workspace as the job-side code sees it: a bare
// id on a context, with no session behind it.
func (p *preflightEnv) workspaceID(t *testing.T) ids.UUID {
	t.Helper()
	ws, err := ids.Parse(p.ws)
	if err != nil {
		t.Fatalf("workspace id %q: %v", p.ws, err)
	}
	return ws
}

// sendExpectingAcceptance issues the authenticated send and returns the accepted
// activity's id — the timeline row the delivery reports on.
func (p *preflightEnv) sendExpectingAcceptance(t *testing.T, purpose, subject, body string) ids.UUID {
	t.Helper()
	var sent struct {
		ID string `json:"id"`
	}
	status := p.call(t, "POST", "/v1/activities/"+p.activityID+"/send-email", anyMap{
		"subject": subject, "body": body,
		"to": []string{"buyer@preflight.test"}, "consent_purpose": purpose,
	}, nil, &sent)
	if status != http.StatusAccepted {
		t.Fatalf("send-email under %q → %d, want 202", purpose, status)
	}
	id, err := ids.Parse(sent.ID)
	if err != nil {
		t.Fatalf("accepted send returned no activity id: %v", err)
	}
	return id
}

// deliveryFor reads what the accepted send staged: the delivery to
// transmit, and the message identity both it and the activity are keyed on.
func (p *preflightEnv) deliveryFor(t *testing.T, activityID ids.UUID) (ids.UUID, string) {
	t.Helper()
	var id ids.UUID
	var messageID string
	if err := p.inWorkspace(t, p.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT id, message_id FROM comms_outbound WHERE activity_id = $1`, activityID).Scan(&id, &messageID)
	}); err != nil {
		t.Fatalf("reading the staged delivery: %v", err)
	}
	return id, messageID
}

// transmit drives ONE real dispatch of a staged delivery against a stub Gmail
// and returns the RFC822 the connector produced, together with the connector
// itself — the echo test derives the captured message's source_system from its
// descriptor rather than restating the send path's own constant.
func (p *preflightEnv) transmit(t *testing.T, deliveryID ids.UUID) ([]byte, *gmail.Connector) {
	t.Helper()
	var captured sentMail
	stub := gmailSendStub(t, &captured)

	// The credential is built the way the OAuth callback builds it, so the
	// grant the dispatcher's authority gate reads is a real exchange result
	// rather than a hand-written bundle.
	oauth := gmail.NewOAuth(gmail.OAuthConfig{
		ClientID: "cid", ClientSecret: "sec", TokenURL: stub.URL + "/token",
		Scopes: []string{gmailReadonlyScope, gmailSendScope},
	})
	gmailConnector := gmail.New(oauth, gmail.NewAPI(stub.Client(), stub.URL))
	authReq, err := gmail.AuthRequestFrom("the-code", "https://app.test/v1/connectors/gmail/callback")
	if err != nil {
		t.Fatal(err)
	}
	auth, err := gmailConnector.Authenticate(context.Background(), authReq)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	// The dispatcher is assembled here rather than taken from the composition
	// because compose exposes no inline-dispatch seam: newSendWorker is
	// unexported and reachable only through a River worker, and driving one
	// would mean waiting on a queue in a lane that may not sleep. The store,
	// the gate and the connector are the production objects; what this
	// restates is the WIRING — so a send policy added to the composed chain
	// would not be exercised by these tests, and the pacing knobs below are
	// deliberately inert (no policies, a bound nothing here reaches).
	dispatcher := comms.NewDispatcher(
		comms.NewStore(p.pool, time.Now),
		stubMailbox{sender: gmailConnector, auth: auth},
		consent.NewGate(consent.NewStore(p.pool)),
		nil, time.Now, 24*time.Hour, 10,
	)
	// A job carries no session, only the workspace — the same context the
	// send worker builds.
	outcome, _, err := dispatcher.DispatchWithWait(
		principal.WithWorkspaceID(context.Background(), p.workspaceID(t)), deliveryID)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if outcome != comms.OutcomeSent {
		t.Fatalf("dispatch outcome = %q, want sent", outcome)
	}
	rfc822, err := base64.URLEncoding.DecodeString(captured.raw)
	if err != nil {
		t.Fatalf("the connector did not hand Gmail base64url: %v", err)
	}
	return rfc822, gmailConnector
}

// connectorCtx is the principal the capture registry builds for a sync: the
// connector identity acting under the granting human's permissions.
func (p *preflightEnv) connectorCtx(t *testing.T) context.Context {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), p.workspaceID(t))
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalConnector, ID: "connector:" + activities.SendProvider,
		Scopes: principal.NewScopeSet(principal.ScopeRead),
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"activity": {Create: true, Read: true}},
			RowScope: principal.RowScopeAll,
		},
	})
}

// Sending an email and then capturing the provider's own copy of it must yield
// ONE activity, and that activity must be the one the send wrote.
func TestCapturedCopyOfASentEmailCollapsesOntoTheSameActivity(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	sentActivity := p.sendExpectingAcceptance(t, "transactional", "Re: Inbound question", "As discussed.")
	deliveryID, messageID := p.deliveryFor(t, sentActivity)
	rfc822, capturingConnector := p.transmit(t, deliveryID)

	// BOTH halves of the natural key come from the capture side, never from the
	// send side. source_system is the connector's own declared name — two
	// independent constants (gmail.connectorName and activities.SendProvider)
	// spell it today, and nothing but this comparison holds them equal. Feeding
	// the send-side literal in here would assert the assumption and stay green
	// while every outbound email landed twice.
	sourceSystem := capturingConnector.Descriptor().Name

	// The key comes out of the bytes, through the connector's own mapping —
	// mailmap.Parse + ToRecord is precisely what the Gmail connector runs on
	// every message it reads back, sent ones included.
	msg, err := mailmap.Parse(rfc822, sendingMailbox)
	if err != nil {
		t.Fatalf("the message the connector produced does not parse:\n%s\n%v", rfc822, err)
	}
	echo := msg.AttestSentByOwner(true).ToRecord(sourceSystem, rfc822)
	if echo.NaturalKey.SourceSystem != activities.SendProvider {
		t.Fatalf("capture keys this message under source_system %q but the send path writes %q — the two constants have drifted apart",
			echo.NaturalKey.SourceSystem, activities.SendProvider)
	}
	if echo.NaturalKey.SourceID != messageID {
		t.Fatalf("the captured copy keys on %q but the send wrote %q — every outbound email would land twice",
			echo.NaturalKey.SourceID, messageID)
	}

	if _, err := capture.NewSink(p.pool).Upsert(p.connectorCtx(t), echo); err != nil {
		t.Fatalf("capturing the provider's own copy: %v", err)
	}

	var rows int
	if err := p.inWorkspace(t, p.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM activity WHERE source_system = $1 AND source_id = $2`,
			sourceSystem, messageID).Scan(&rows)
	}); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("%d activities carry the sent message's natural key, want exactly 1", rows)
	}

	// The echo's upsert is ON CONFLICT DO NOTHING, so anything the SEND did not
	// write is never written at all. The thread key is that kind of field: it
	// has to be stamped at send or the conversation has no identity.
	var id ids.UUID
	var threadKey *string
	if err := p.inWorkspace(t, p.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT id, thread_key FROM activity WHERE source_system = $1 AND source_id = $2`,
			sourceSystem, messageID).Scan(&id, &threadKey)
	}); err != nil {
		t.Fatal(err)
	}
	if id != sentActivity {
		t.Errorf("the surviving activity is %s, not the one the send created (%s)", id, sentActivity)
	}
	if threadKey == nil || *threadKey == "" {
		t.Errorf("thread_key = %v — the echo cannot supply it, so the send must", threadKey)
	}
}

// A marketing send carries the RFC 8058 one-click pair, and the pair is what
// is asserted: List-Unsubscribe-Post is derived from its partner rather than
// stored, so only a real message shows the two lines actually arriving
// together.
func TestAMarketingSendRendersBothOneClickUnsubscribeHeaders(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)
	p.grantMarketingConsent(t)

	sentActivity := p.sendExpectingAcceptance(t, "marketing_email", "Spring pricing", "Here is what changed.")
	deliveryID, _ := p.deliveryFor(t, sentActivity)
	rfc822, _ := p.transmit(t, deliveryID)
	mime := string(rfc822)

	if !strings.Contains(mime, "List-Unsubscribe: <"+preflightBaseURL+"/") {
		t.Fatalf("a marketing send left without a one-click unsubscribe target:\n%s", mime)
	}
	if !strings.Contains(mime, "List-Unsubscribe-Post: List-Unsubscribe=One-Click") {
		t.Errorf("the Post header is absent or not the RFC 8058 literal, so no client will honour the one-click:\n%s", mime)
	}
}

// grantMarketingConsent takes the recipient through the double-opt-in round
// trip marketing_email requires — the server mints the token, the confirming
// grant presents it — so the send under that purpose is lawful at both the
// request-time gate and the dispatcher's.
func (p *preflightEnv) grantMarketingConsent(t *testing.T) {
	t.Helper()
	var purposes struct {
		Data []struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if status := p.call(t, "GET", "/v1/consent-purposes", nil, nil, &purposes); status != http.StatusOK {
		t.Fatalf("list purposes → %d", status)
	}
	var marketing string
	for _, purpose := range purposes.Data {
		if purpose.Key == "marketing_email" {
			marketing = purpose.ID
		}
	}
	if marketing == "" {
		t.Fatalf("bootstrap seeded no marketing purpose: %+v", purposes.Data)
	}
	var issued struct {
		Token string `json:"token"`
	}
	if status := p.call(t, "POST", "/v1/people/"+p.personID+"/consent/double-opt-in", anyMap{
		"purpose_id": marketing, "deliver": false,
	}, nil, &issued); status != http.StatusCreated {
		t.Fatalf("issue the double-opt-in token → %d", status)
	}
	if status := p.call(t, "POST", "/v1/people/"+p.personID+"/consent", anyMap{
		"purpose_id": marketing, "new_state": "granted",
		"lawful_basis": "consent", "double_opt_in_token": issued.Token,
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("confirm the marketing grant → %d", status)
	}
}
