// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The channel reply over the real composition (telegram-oa design §8.1, §8.5):
// the 🟡 admission an agent caller meets, the human whose own click IS the
// approval, the default-deny consent gate, the unreachable person who is refused
// before anything is staged, and the outbound activity that keeps a sent message
// on the timeline across a reload.
//
// It boots the api role WITH the connect registry, which is what makes the
// REQUEST-TIME PRE-FLIGHT live. That is load-bearing for this suite rather than
// incidental: the pre-flight asks whether a credential can transmit, and a
// workspace bot lives in channel_connection while a mailbox grant lives in
// capture_connection — so a pre-flight that read the mailbox table would refuse
// every reply here with a 422 about a mailbox nobody asked about, and every other
// test in the tree would still pass.

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
)

const (
	// The customer's Telegram account and the chat the conversation was held in.
	// A private chat's id IS the account id, which is why one value serves both.
	channelSendAccountID = "770011"
	channelSendThreadKey = "telegram:8100:" + channelSendAccountID
	channelSendBaseURL   = "https://channel.example.test"
)

type channelSendEnv struct {
	*env
	activityID string
	personID   string
	ws, user   string
}

// setupChannelSend boots the api composition with the connect registry (so the
// send pre-flight is wired), then lays down what a captured Telegram
// conversation leaves behind: a person, their channel identity, the inbound
// activity filed under the chat, and the workspace's bot binding.
func setupChannelSend(t *testing.T) *channelSendEnv {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generating a test root key: %v", err)
	}
	vault, err := keyvault.New(keyvault.Config{RootKey: key, Pool: preflightAppPool(t)})
	if err != nil {
		t.Fatalf("building the local vault: %v", err)
	}
	e := setupWithOptions(t, compose.WithKeyvault(vault),
		// The Google app is configured only to make the connect registry — and
		// with it the pre-flight — exist. Nothing here sends mail.
		compose.WithGmailCapture(compose.GmailConfig{
			ClientID: "channel-id", ClientSecret: "channel-secret",
			StateKey: "0123456789abcdef0123456789abcdef", PublicBaseURL: channelSendBaseURL,
		}, compose.CaptureConfig{}),
		compose.WithPublicBaseURL(channelSendBaseURL))
	e.slug = "channel-send-e2e"
	bootstrapWorkspaceSession(t, e, "Channel Send E2E", "rep@fable.test", "Admin")

	c := &channelSendEnv{env: e}
	var person struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{"full_name": "Telegram Buyer"}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}
	c.personID = person.ID
	if err := e.inWorkspace(t, e.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT workspace_id, id FROM app_user WHERE email = $1`, "rep@fable.test").Scan(&c.ws, &c.user)
	}); err != nil {
		t.Fatalf("resolving the acting human: %v", err)
	}
	c.bindIdentity(t)
	c.seedInboundMessage(t)
	c.grantConsent(t, "transactional")
	c.connectBot(t)
	return c
}

// bindIdentity writes the person_channel_identity row an inbound message binds.
// Written as the table owner because the subject here is what the SEND path
// reads out of it, not how ingress puts it there.
func (c *channelSendEnv) bindIdentity(t *testing.T) {
	t.Helper()
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO person_channel_identity
				(workspace_id, person_id, provider, channel_user_id, username, source, captured_by)
			VALUES ($1, $2, 'telegram', $3, 'buyer', 'telegram', 'connector:telegram')`,
			c.ws, c.personID, channelSendAccountID)
		return err
	}); err != nil {
		t.Fatalf("binding the channel identity: %v", err)
	}
}

// seedInboundMessage writes the conversation being answered: an inbound telegram
// activity filed under the chat's thread key and linked to the person, which is
// the shape capture leaves behind.
func (c *channelSendEnv) seedInboundMessage(t *testing.T) {
	t.Helper()
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
		ctx := context.Background()
		if err := tx.QueryRow(ctx, `
			INSERT INTO activity (workspace_id, kind, body, occurred_at, direction,
			                      source_system, source_id, source, captured_by, thread_key)
			VALUES ($1, 'telegram', 'Is this still available?', now(), 'inbound',
			        'telegram', '8100:770011:5', 'telegram:8100:770011:5', 'connector:telegram', $2)
			RETURNING id`, c.ws, channelSendThreadKey).Scan(&c.activityID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
			VALUES ($1, $2, 'person', $3)`, c.ws, c.activityID, c.personID)
		return err
	}); err != nil {
		t.Fatalf("seeding the inbound conversation: %v", err)
	}
}

// grantConsent records an active grant for one purpose. The gate is per PURPOSE,
// so this is what a send under that purpose needs and a send under any other
// still lacks.
func (c *channelSendEnv) grantConsent(t *testing.T, key string) {
	t.Helper()
	var purposes struct {
		Data []struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if status := c.call(t, "GET", "/v1/consent-purposes", nil, nil, &purposes); status != http.StatusOK {
		t.Fatalf("list purposes → %d", status)
	}
	var purposeID string
	for _, p := range purposes.Data {
		if p.Key == key {
			purposeID = p.ID
		}
	}
	if purposeID == "" {
		t.Fatalf("bootstrap seeded no %q purpose: %+v", key, purposes.Data)
	}
	if status := c.call(t, "POST", "/v1/people/"+c.personID+"/consent", anyMap{
		"purpose_id": purposeID, "new_state": "granted", "lawful_basis": "consent",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("record consent → %d", status)
	}
}

// connectBot writes the workspace's live bot binding. The credential ref is never
// resolved on this path — the pre-flight reads liveness and stops short of the
// vault — so its value is deliberately opaque here.
func (c *channelSendEnv) connectBot(t *testing.T) {
	t.Helper()
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO channel_connection
				(workspace_id, provider, channel_id, channel_label, credential_ref, status, connected_by)
			VALUES ($1, 'telegram', '8100', '@fablebot', 'vault:token', 'connected', $2)`,
			c.ws, c.user)
		return err
	}); err != nil {
		t.Fatalf("connecting the workspace bot: %v", err)
	}
}

// sendReply posts a reply with the given body and returns the status plus the
// problem's first validation code — the words the rep is shown, which is
// where the "what do I do about it" has to live.
func (c *channelSendEnv) sendReply(t *testing.T, purpose, body string, headers map[string]string) (status int, code, message string) {
	t.Helper()
	var answer struct {
		Code    string `json:"code"`
		Detail  string `json:"detail"`
		Details struct {
			Errors []struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"errors"`
		} `json:"details"`
	}
	status = c.call(t, "POST", "/v1/activities/"+c.activityID+"/send-message", anyMap{
		"body": body, "consent_purpose": purpose,
	}, headers, &answer)
	if errs := answer.Details.Errors; len(errs) > 0 {
		return status, errs[0].Code, errs[0].Message
	}
	return status, answer.Code, answer.Detail
}

// mintPassport issues an agent passport under the given scopes and returns its bearer token.
func (c *channelSendEnv) mintPassport(t *testing.T, scopes []string) string {
	t.Helper()
	var minted struct {
		Token string `json:"token"`
	}
	if status := c.call(t, "POST", "/v1/passports", anyMap{"label": "reply agent", "scopes": scopes}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport → %d", status)
	}
	return minted.Token
}

// sendReplyAs mints a passport under the given scopes and sends the reply as
// that agent — the two-step pattern both scope tests below share.
func (c *channelSendEnv) sendReplyAs(t *testing.T, scopes []string, purpose string) (status int, code, message string) {
	t.Helper()
	token := c.mintPassport(t, scopes)
	return c.sendReply(t, purpose, "Yes — shipping Monday.", map[string]string{"Authorization": "Bearer " + token})
}

// stagedChannelDeliveries counts channel-shaped comms_outbound rows — the fact a
// refusal must not leave behind, and the fact an acceptance must.
func (c *channelSendEnv) stagedChannelDeliveries(t *testing.T) int {
	t.Helper()
	var n int
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM comms_outbound WHERE channel_user_id IS NOT NULL`).Scan(&n)
	}); err != nil {
		t.Fatalf("counting staged channel deliveries: %v", err)
	}
	return n
}

// outboundActivities counts what the timeline gained. A refusal that logged one
// would show the rep a message that never left.
func (c *channelSendEnv) outboundActivities(t *testing.T) int {
	t.Helper()
	var n int
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM activity WHERE direction = 'outbound'`).Scan(&n)
	}); err != nil {
		t.Fatalf("counting outbound activities: %v", err)
	}
	return n
}

// assertNoOutboundEffect fails if a refusal left behind a staged delivery or a
// logged outbound activity — the two facts every refusal path in this suite
// must NOT produce, named by the caller's description of the refusal.
func (c *channelSendEnv) assertNoOutboundEffect(t *testing.T, refusal string) {
	t.Helper()
	if n := c.stagedChannelDeliveries(t); n != 0 {
		t.Fatalf("%d deliveries staged behind %s, want 0", n, refusal)
	}
	if n := c.outboundActivities(t); n != 0 {
		t.Fatalf("%d outbound activities logged behind %s, want 0", n, refusal)
	}
}

// A write-only passport may not reach an outbound verb at all: the scope cap
// the granting human set is checked before the tier question is asked.
func TestSendMessageRefusesAWriteOnlyPassport(t *testing.T) {
	c := setupChannelSend(t)

	status, code, _ := c.sendReplyAs(t, []string{"read", "write"}, "transactional")

	if status != http.StatusForbidden || code != "scope_exceeds_grantor" {
		t.Fatalf("write-only passport reply → %d %q, want 403 scope_exceeds_grantor", status, code)
	}
	c.assertNoOutboundEffect(t, "a scope-refused send")
}

// A send-scoped agent caller meets the 🟡 gate: it may propose the send and may
// not perform it. The refusal has to happen before the message is staged — an
// agent that could stage one has already sent it as far as the customer is
// concerned.
func TestSendMessageRequiresAnApprovalTokenForAnAgentCaller(t *testing.T) {
	c := setupChannelSend(t)

	status, code, detail := c.sendReplyAs(t, []string{"read", "send"}, "transactional")

	if status != http.StatusForbidden || code != "approval_required" {
		t.Fatalf("agent reply with no approval token → %d %q, want 403 approval_required", status, code)
	}
	// The refusal names the staged approval and how to redeem it; without that
	// the agent has been told "no" with no path to a yes.
	if !strings.Contains(detail, "X-Approval-Token") {
		t.Fatalf("refusal detail %q does not tell the agent how to redeem an approval", detail)
	}
	c.assertNoOutboundEffect(t, "a refused agent send")
}

// The human's own action IS the approval (ADR-0055), so their reply carries no
// token and no idempotency key and is accepted — and it is accepted THROUGH the
// live pre-flight, which is the half that would silently refuse every channel
// reply if it asked about the wrong credential.
func TestSendMessageAcceptsAHumanCallerWithoutAToken(t *testing.T) {
	c := setupChannelSend(t)

	status, code, detail := c.sendReply(t, "transactional", "Yes — shipping Monday.", nil)

	if status != http.StatusAccepted {
		t.Fatalf("human reply → %d %q (%s), want 202", status, code, detail)
	}
	if n := c.stagedChannelDeliveries(t); n != 1 {
		t.Fatalf("%d channel deliveries staged behind an accepted reply, want 1", n)
	}
	// The staged row is channel-shaped and addresses the account the
	// conversation was held with — resolved by the server, never named by the
	// caller.
	var recipient, body, provider string
	var subject, messageID *string
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT channel_user_id, body, provider, subject, message_id
			   FROM comms_outbound WHERE channel_user_id IS NOT NULL`).
			Scan(&recipient, &body, &provider, &subject, &messageID)
	}); err != nil {
		t.Fatalf("reading the staged delivery: %v", err)
	}
	if recipient != channelSendAccountID {
		t.Fatalf("staged recipient = %q, want the conversation's account %q", recipient, channelSendAccountID)
	}
	if provider != "telegram" || body != "Yes — shipping Monday." {
		t.Fatalf("staged provider/body = %q/%q, want telegram and the rep's text", provider, body)
	}
	if subject != nil || messageID != nil {
		t.Fatalf("staged row carries mail columns (subject=%v message_id=%v); it is not channel-shaped", subject, messageID)
	}
}

// The pre-flight's other half, and the reason the acceptance above proves
// something: with no bot bound, the same reply is refused at request time with a
// sentence naming who fixes it — instead of a 202 and a delivery that can only
// park where the rep never looks.
func TestSendMessageRefusesWhenNoBotIsBoundForTheChannel(t *testing.T) {
	c := setupChannelSend(t)
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE channel_connection SET status = 'disconnected'`)
		return err
	}); err != nil {
		t.Fatalf("disconnecting the bot: %v", err)
	}

	status, code, detail := c.sendReply(t, "transactional", "Yes — shipping Monday.", nil)

	if status != http.StatusUnprocessableEntity || code != "channel_not_send_capable" {
		t.Fatalf("reply with no bot bound → %d %q, want 422 channel_not_send_capable", status, code)
	}
	if !strings.Contains(detail, "admin") {
		t.Fatalf("refusal detail %q does not say who has to fix it", detail)
	}
	if n := c.stagedChannelDeliveries(t); n != 0 {
		t.Fatalf("%d deliveries staged behind a refused reply, want 0", n)
	}
}

// An empty or whitespace-only body has nothing to transmit: Telegram refuses it
// outright, so accepting one buys a timeline entry claiming the rep answered and
// a delivery that can only walk the whole retry ladder before parking under a
// reason naming nothing the operator can act on. The contract's minLength is
// documentation — httperr.Decode performs no schema validation — so the refusal
// has to be a Go guard, and it has to land before anything is written.
func TestSendMessageRefusesAnEmptyBody(t *testing.T) {
	c := setupChannelSend(t)

	for _, body := range []string{"", "   \n\t "} {
		status, code, message := c.sendReply(t, "transactional", body, nil)

		if status != http.StatusUnprocessableEntity {
			t.Fatalf("reply with body %q → %d, want 422", body, status)
		}
		if code != "empty_message_body" {
			t.Fatalf("reply with body %q answered code %q, want an empty_message_body validation error", body, code)
		}
		if !strings.Contains(message, "type") {
			t.Fatalf("refusal detail %q does not tell the rep what to do", message)
		}
		c.assertNoOutboundEffect(t, fmt.Sprintf("an empty body %q", body))
	}
}

// Consent is default-deny PER PURPOSE: the person granted `transactional` and
// nothing else, so a reply sent under another purpose is suppressed with the
// same 409 the mail path answers — the shape the composer relies on to keep the
// rep's drafted text on screen.
func TestSendMessageRefusesWithoutConsentForThePurpose(t *testing.T) {
	c := setupChannelSend(t)

	status, code, _ := c.sendReply(t, "marketing_email", "Yes — shipping Monday.", nil)

	if status != http.StatusConflict || code != "consent_not_granted" {
		t.Fatalf("reply under an ungranted purpose → %d %q, want 409 consent_not_granted", status, code)
	}
	c.assertNoOutboundEffect(t, "a suppressed reply")
}

// A person who blocked the bot cannot be reached, and blocking does NOT archive
// the identity (D9) — so the conversation is still there, still readable, and the
// reply must be refused on reachability rather than accepted and parked.
func TestSendMessageRefusesAnUnreachablePerson(t *testing.T) {
	c := setupChannelSend(t)
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE person_channel_identity SET blocked_at = now() WHERE channel_user_id = $1`, channelSendAccountID)
		return err
	}); err != nil {
		t.Fatalf("blocking the identity: %v", err)
	}

	status, code, detail := c.sendReply(t, "transactional", "Yes — shipping Monday.", nil)

	if status != http.StatusUnprocessableEntity || code != "person_unreachable" {
		t.Fatalf("reply to a blocked person → %d %q, want 422 person_unreachable", status, code)
	}
	if !strings.Contains(detail, "blocked") {
		t.Fatalf("refusal detail %q does not say why the person cannot be reached", detail)
	}
	c.assertNoOutboundEffect(t, "an unreachable recipient")
}

// Without the outbound activity the UI's optimistic append vanishes the moment
// the rep reloads, which reads as "my message was lost". So the row is the
// feature, not bookkeeping: same conversation, same person, the text that was
// sent.
func TestSentMessageLandsAsAnOutboundActivity(t *testing.T) {
	c := setupChannelSend(t)
	var sent struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		Direction string `json:"direction"`
		Body      string `json:"body"`
	}
	if status := c.call(t, "POST", "/v1/activities/"+c.activityID+"/send-message", anyMap{
		"body": "On its way.", "consent_purpose": "transactional",
	}, nil, &sent); status != http.StatusAccepted {
		t.Fatalf("human reply → %d", status)
	}
	if sent.Kind != "telegram" || sent.Direction != "outbound" || sent.Body != "On its way." {
		t.Fatalf("logged activity = %+v, want an outbound telegram activity carrying the sent text", sent)
	}

	var threadKey, linkedPerson string
	var deliveryActivity string
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
		ctx := context.Background()
		if err := tx.QueryRow(ctx,
			`SELECT coalesce(thread_key, '') FROM activity WHERE id = $1`, sent.ID).Scan(&threadKey); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT person_id::text FROM activity_link WHERE activity_id = $1 AND entity_type = 'person'`,
			sent.ID).Scan(&linkedPerson); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT activity_id::text FROM comms_outbound WHERE channel_user_id IS NOT NULL`).Scan(&deliveryActivity)
	}); err != nil {
		t.Fatalf("reading what the reply wrote: %v", err)
	}
	// Capture joins an inbound reply against outbound activities on thread_key,
	// so a reply filed under any other key is a reply this conversation never
	// had — and the timeline would show it as a message out of nowhere.
	if threadKey != channelSendThreadKey {
		t.Fatalf("outbound activity thread_key = %q, want the conversation's %q", threadKey, channelSendThreadKey)
	}
	if linkedPerson != c.personID {
		t.Fatalf("outbound activity links person %s, want the conversation's %s", linkedPerson, c.personID)
	}
	// The delivery names THIS activity: one transaction, one fact, so a receipt
	// recorded later lands on the row the rep is looking at.
	if deliveryActivity != sent.ID {
		t.Fatalf("the staged delivery anchors activity %s, want the one just logged (%s)", deliveryActivity, sent.ID)
	}
}
