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
// normalization and the sink are all the production objects. The stub itself —
// including what a provider does to a message identity before storing its own
// copy — lives in comms_send_provider_test.go.

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
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

// stubMailbox stands in for the capture registry's credential resolution — the
// one seam that cannot run without a real Google. It hands back the REAL Gmail
// connector, so everything the message passes through after this point is
// production code.
type stubMailbox struct {
	sender connector.EmailSender
	auth   connector.Auth
}

var _ comms.ConnectionResolver = stubMailbox{}

func (m stubMailbox) Resolve(context.Context, ids.UserID, string) (connector.EmailSender, connector.Auth, []string, error) {
	return m.sender, m.auth, []string{gmailSendScope}, nil
}

// ResolveChannel is not this suite's transport: every delivery here is
// mail-shaped, so a channel resolve reaching this stub means the dispatcher's
// shape branch read the wrong row. It refuses rather than answering, so that
// mistake fails the run instead of quietly transmitting mail through a channel
// seam.
func (m stubMailbox) ResolveChannel(context.Context, string) (connector.MessageSender, connector.Auth, error) {
	return nil, nil, errors.New("stubMailbox: this suite stages mail deliveries only; a channel resolve here is a shape-branch defect")
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
	status := p.Call(t, "POST", "/v1/activities/"+p.activityID+"/send-email", apptest.AnyMap{
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
	if err := inWorkspace(p.AppEnv, t, p.Slug, func(tx pgx.Tx) error {
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
// descriptor rather than restating the send path's own constant. It insists on
// a completed send, so a case ABOUT a refusal calls dispatchOnce instead.
//
// stampAs names the identity the provider puts on its stored copy; empty is a
// provider that honoured the client's.
func (p *preflightEnv) transmit(t *testing.T, deliveryID ids.UUID, stampAs string) ([]byte, *gmail.Connector) {
	t.Helper()
	outcome, captured, gmailConnector := p.dispatchOnce(t, deliveryID, stampAs)
	if outcome != comms.OutcomeSent {
		t.Fatalf("dispatch outcome = %q, want sent", outcome)
	}
	rfc822, err := base64.URLEncoding.DecodeString(captured.raw)
	if err != nil {
		t.Fatalf("the connector did not hand Gmail base64url: %v", err)
	}
	return rfc822, gmailConnector
}

// dispatchOnce runs one real dispatch and reports its verdict plus whatever
// reached the stub provider — which is how a refusal is proven to have
// transmitted NOTHING rather than merely to have been recorded.
func (p *preflightEnv) dispatchOnce(t *testing.T, deliveryID ids.UUID, stampAs string) (comms.Outcome, sentMail, *gmail.Connector) {
	t.Helper()
	var captured sentMail
	stub := gmailSendStub(t, &captured, stampAs)

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
		comms.NewStore(p.Pool, time.Now, activities.NewStore(p.Pool)),
		stubMailbox{sender: gmailConnector, auth: auth},
		compose.NewSendSeatAuthority(p.Pool),
		consent.NewGate(consent.NewStore(p.Pool)),
		nil, time.Now, 24*time.Hour, 10,
	)
	// A job carries no session. The scope comes from the composition rather
	// than being rebuilt here: it binds the system actor and the correlation id
	// the identity reconcile's audit row and outbox event both require, and a
	// hand-rolled workspace-only context would drive a dispatch that silently
	// cannot record what it did.
	outcome, _, err := dispatcher.DispatchWithWait(
		compose.SendWorkerContext(context.Background(), p.workspaceID(t)), deliveryID,
	)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	return outcome, captured, gmailConnector
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
	rfc822, capturingConnector := p.transmit(t, deliveryID, "")

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

	if _, err := capture.NewSink(p.Pool).Upsert(p.connectorCtx(t), echo); err != nil {
		t.Fatalf("capturing the provider's own copy: %v", err)
	}

	var rows int
	if err := inWorkspace(p.AppEnv, t, p.Slug, func(tx pgx.Tx) error {
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
	if err := inWorkspace(p.AppEnv, t, p.Slug, func(tx pgx.Tx) error {
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
	rfc822, _ := p.transmit(t, deliveryID, "")
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
	if status := p.Call(t, "GET", "/v1/consent-purposes", nil, nil, &purposes); status != http.StatusOK {
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
	if status := p.Call(t, "POST", "/v1/people/"+p.personID+"/consent/double-opt-in", apptest.AnyMap{
		"purpose_id": marketing, "deliver": false,
	}, nil, &issued); status != http.StatusCreated {
		t.Fatalf("issue the double-opt-in token → %d", status)
	}
	if status := p.Call(t, "POST", "/v1/people/"+p.personID+"/consent", apptest.AnyMap{
		"purpose_id": marketing, "new_state": "granted",
		"lawful_basis": "consent", "double_opt_in_token": issued.Token,
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("confirm the marketing grant → %d", status)
	}
}

// Revocation binds mid-flight on the one lane that reaches a real external
// mailbox. Deactivating a user revokes their sessions and passports but leaves
// the mailbox connection standing, and a delivery staged before that moment
// carries no session of its own — so without a transmit-time seat check an
// off-boarded or compromised account keeps sending for the whole maximum age.
func TestDeactivatingTheSenderParksAStagedDelivery(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	sentActivity := p.sendExpectingAcceptance(t, "transactional", "Re: Inbound question", "As discussed.")
	deliveryID, _ := p.deliveryFor(t, sentActivity)
	p.deactivateSender(t)

	outcome, captured, _ := p.dispatchOnce(t, deliveryID, "")

	if outcome != comms.OutcomeParked {
		t.Fatalf("dispatch after the sender was deactivated → %q, want parked", outcome)
	}
	if captured.raw != "" {
		t.Error("a deactivated sender's staged message still reached the provider")
	}
	if reason := p.deliveryReason(t, deliveryID); !strings.Contains(reason, "no longer active") {
		t.Errorf("parked reason = %q; an operator must be able to read why the batch stopped", reason)
	}
}

// A downgrade binds mid-flight the same way a deactivation does: seat_type is
// the A62/ADR-0047 licensing ceiling, and nothing today mutates it after
// creation — but the transmit-time gate re-reads the live row rather than
// trusting whatever seat staged the delivery, exactly as it re-reads status.
// A sender still live but holding a read seat must park, not transmit, and
// the reason must say so rather than reporting an account that is not in
// fact deactivated.
func TestASenderDowngradedToAReadSeatParksAStagedDelivery(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	sentActivity := p.sendExpectingAcceptance(t, "transactional", "Re: Inbound question", "As discussed.")
	deliveryID, _ := p.deliveryFor(t, sentActivity)
	p.downgradeSenderToReadSeat(t)

	outcome, captured, _ := p.dispatchOnce(t, deliveryID, "")

	if outcome != comms.OutcomeParked {
		t.Fatalf("dispatch after the sender was downgraded to a read seat → %q, want parked", outcome)
	}
	if captured.raw != "" {
		t.Error("a read-seat sender's staged message still reached the provider")
	}
	reason := p.deliveryReason(t, deliveryID)
	if !strings.Contains(reason, "read-only seat") {
		t.Errorf("parked reason = %q; an operator must be able to read WHY the batch stopped", reason)
	}
	if strings.Contains(reason, "no longer active") {
		t.Errorf("parked reason = %q; a live read seat must not be reported as a deactivated account", reason)
	}
}

// deactivateSender flips the acting human's seat the way identity's
// DeactivateUser does. Written directly because the subject is what the SEND
// path reads out of the row, not how the admin endpoint puts it there.
func (p *preflightEnv) deactivateSender(t *testing.T) {
	t.Helper()
	if err := inWorkspace(p.AppEnv, t, p.Slug, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE app_user SET status = 'deactivated' WHERE id = $1`, p.user)
		return err
	}); err != nil {
		t.Fatalf("deactivating the sender: %v", err)
	}
}

// downgradeSenderToReadSeat flips the acting human's seat_type the way a
// licensing downgrade would, leaving status untouched — the sender is still a
// live, logged-in-capable account, just no longer a permitted one to send
// from.
func (p *preflightEnv) downgradeSenderToReadSeat(t *testing.T) {
	t.Helper()
	if err := inWorkspace(p.AppEnv, t, p.Slug, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE app_user SET seat_type = 'read' WHERE id = $1`, p.user)
		return err
	}); err != nil {
		t.Fatalf("downgrading the sender's seat: %v", err)
	}
}

// deliveryReason reads the operator sentence a terminal transition left behind.
func (p *preflightEnv) deliveryReason(t *testing.T, deliveryID ids.UUID) string {
	t.Helper()
	var reason *string
	if err := inWorkspace(p.AppEnv, t, p.Slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT reason FROM comms_outbound WHERE id = $1`, deliveryID).Scan(&reason)
	}); err != nil {
		t.Fatalf("reading the delivery reason: %v", err)
	}
	if reason == nil {
		return ""
	}
	return *reason
}
