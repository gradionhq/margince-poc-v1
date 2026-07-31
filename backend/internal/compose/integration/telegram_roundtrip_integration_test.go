// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The full Telegram round trip (telegram-oa design §1, §8, §12): a stranger's
// message arrives at the mounted webhook, becomes a conversation against a
// Person nobody had to create, a rep answers it from that conversation, and the
// answer reaches Telegram.
//
// It is the one test in this suite that crosses every seam at once, and it
// exists because each half can be correct while the join is not: the ingest
// resolves a recipient the send path has to be able to address, the send path
// stages a delivery the worker has to be able to resolve a bot for, and the bot
// it resolves has to be the one the ingress was authenticated against. Nothing
// short of the whole leg can catch a mismatch between those.
//
// The Telegram Bot API is the only fake. The router, the RLS-bound pool, the
// vault, River, the consent gate, the seat check and the dispatcher are the
// ones the api and worker roles run.

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// sendRegistry is the WORKER role's connector registry, carrying the fake Bot
// API in place of the real client compose.NewCaptureRegistry hard-wires. It is
// the seam the delivery path resolves the workspace's bot through, so a
// registry missing the connector reads as "this installation has no Telegram
// integration" and parks every reply.
func (c *telegramEnv) sendRegistry() *capture.Registry {
	registry := capture.NewRegistry(c.pool, capture.NewSink(c.pool), identity.NewService(c.pool), c.vault)
	registry.Register(telegram.New(c.api))
	return registry
}

// grantConsent records an active grant for one purpose against the person the
// ingest auto-created. The gate is per PURPOSE and default-deny, so this is
// exactly what a reply needs and nothing more.
func (c *telegramEnv) grantConsent(t *testing.T, personID, purposeKey string) {
	t.Helper()
	var purposes struct {
		Data []struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if status := c.call(t, "GET", "/v1/consent-purposes", nil, nil, &purposes); status != http.StatusOK {
		t.Fatalf("list consent purposes → %d", status)
	}
	var purposeID string
	for _, p := range purposes.Data {
		if p.Key == purposeKey {
			purposeID = p.ID
		}
	}
	if purposeID == "" {
		t.Fatalf("the bootstrap seeded no %q consent purpose", purposeKey)
	}
	if status := c.call(t, "POST", "/v1/people/"+personID+"/consent", anyMap{
		"purpose_id": purposeID, "new_state": "granted", "lawful_basis": "consent",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("record consent → %d", status)
	}
}

// TestInboundThenReplyRoundTrip is the whole loop, in the order a customer and
// a rep actually live it.
func TestInboundThenReplyRoundTrip(t *testing.T) {
	c := setupTelegramConnected(t)
	inbound := telegramUpdate{
		updateID: 5901, messageID: 91, senderID: 770901,
		username: "buyer", firstName: "Mara", text: "Is the blue one still available?",
	}
	const reply = "Yes — shipping Monday."

	// ONE worker role serves both legs, exactly as cmd/worker does: the ingest
	// job and the send job are worked by the same runner over the same registry.
	runner, sub := newTelegramWorker(t, c, compose.JobRunnerConfig{SendRegistry: c.sendRegistry()})
	startTelegramWorker(t, runner)

	// 1. The customer writes, and the worker's poll collects it.
	c.arrive(t, sub, inbound)
	awaitJobKind(t, sub, compose.TelegramIngestArgs{}.Kind())

	// 2. It became a conversation against a Person nobody created by hand.
	activityID, personID := c.capturedMessage(t, inbound)
	if n := c.count(t, `SELECT count(*) FROM person WHERE id = $1 AND owner_id IS NULL`, personID); n != 1 {
		t.Fatalf("the inbound message did not produce one ownerless counterparty")
	}

	// 3. The workspace records the lawful basis for answering.
	c.grantConsent(t, personID, "transactional")

	// 4. The rep answers from that conversation. Their own action IS the
	//    approval, so no token and no idempotency key ride the request.
	var sent struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		Direction string `json:"direction"`
		Body      string `json:"body"`
	}
	status := c.call(t, "POST", "/v1/activities/"+activityID+"/send-message", anyMap{
		"body": reply, "consent_purpose": "transactional",
	}, nil, &sent)
	if status != http.StatusAccepted {
		t.Fatalf("the rep's reply → %d, want 202", status)
	}
	if sent.Kind != "telegram" || sent.Direction != "outbound" || sent.Body != reply {
		t.Fatalf("the logged reply = %+v, want an outbound telegram activity carrying the rep's text", sent)
	}

	// 5. The delivery machinery carried it to Telegram.
	awaitJobKind(t, sub, compose.SendEmailArgs{}.Kind())
	c.assertTelegramReceived(t, inbound, reply)
	c.assertDeliveryRecorded(t, sent.ID, inbound, personID)

	// 6. And the customer can ask what was held about them. This subject has no
	//    address at all, so their whole correspondence hangs off the channel
	//    columns — the shape an address-shaped export cannot describe.
	c.assertSubjectAccessDescribesTheReply(t, personID, inbound, reply)
}

// assertSubjectAccessDescribesTheReply is Art. 15 over the message that just
// left: the export must say WHICH account this installation messaged, not only
// that some message existed.
//
// comms_outbound admits a mail-shaped row or a channel-shaped one and never
// half of each, so a channel delivery carries no subject, no recipients and no
// cc — its addressee lives in channel_user_id. An export projecting only the
// mail columns therefore hands a Telegram-only subject a message with no
// addressee, which both withholds the account id the row holds about them and
// misdescribes the send. Held here because the round trip is the one place a
// channel-only subject with a completed send actually exists.
func (c *telegramEnv) assertSubjectAccessDescribesTheReply(t *testing.T, personID string, inbound telegramUpdate, reply string) {
	t.Helper()
	person, err := ids.ParseAs[ids.PersonKind](personID)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := privacy.AssembleSAR(c.adminStoreCtx(t), c.pool, person)
	if err != nil {
		t.Fatalf("AssembleSAR: %v", err)
	}
	if len(pkg.SentMessages) != 1 {
		t.Fatalf("the export carried %d sent messages, want the one reply: %#v", len(pkg.SentMessages), pkg.SentMessages)
	}
	row := pkg.SentMessages[0]
	if row["body"] != reply {
		t.Errorf("exported body = %v, want the reply that was sent (%q)", row["body"], reply)
	}
	if row["channel_user_id"] != inbound.account() {
		t.Errorf("exported channel_user_id = %v, want the subject's own account %q — "+
			"a channel row's addressee is not in recipients, so a mail-only projection tells the subject a message went to nobody",
			row["channel_user_id"], inbound.account())
	}
	if row["provider"] != "telegram" {
		t.Errorf("exported provider = %v, want telegram — the export must say which channel carried the message", row["provider"])
	}
}

// assertTelegramReceived is the far end of the round trip: the bot transmitted
// the rep's words into the customer's own chat. The chat id is the assertion
// that matters most — a private chat's id IS the account id, so a reply sent to
// any other chat reached somebody else.
func (c *telegramEnv) assertTelegramReceived(t *testing.T, inbound telegramUpdate, reply string) {
	t.Helper()
	sent := c.api.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("Telegram received %d messages, want exactly 1", len(sent))
	}
	if sent[0].ChatID != inbound.senderID {
		t.Fatalf("the reply was sent to chat %d, want the customer's own chat %d", sent[0].ChatID, inbound.senderID)
	}
	if sent[0].Text != reply {
		t.Fatalf("Telegram received %q, want the rep's text %q", sent[0].Text, reply)
	}
	// Unanchored by design: the chat IS the conversation, and anchoring on one
	// message would mean this module guessing at the capture provider's own
	// natural-key format — a wrong anchor is refused outright, which would cost
	// the rep their message to buy some visual nesting.
	if sent[0].ReplyToMessageID != 0 {
		t.Errorf("the reply anchored on message %d; a channel reply is deliberately unanchored", sent[0].ReplyToMessageID)
	}
}

// assertDeliveryRecorded holds the bookkeeping the rep's screen depends on: the
// delivery row closed as sent against the account the conversation was held
// with, and the outbound activity filed on that same conversation so the reply
// is still there after a reload.
func (c *telegramEnv) assertDeliveryRecorded(t *testing.T, sentActivityID string, inbound telegramUpdate, personID string) {
	t.Helper()
	var recipient, status, deliveryActivity string
	var providerMessageID *string
	var subject, messageID *string
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT channel_user_id, status, activity_id::text, provider_message_id, subject, message_id
			  FROM comms_outbound WHERE channel_user_id IS NOT NULL`).
			Scan(&recipient, &status, &deliveryActivity, &providerMessageID, &subject, &messageID)
	}); err != nil {
		t.Fatalf("reading the delivery: %v", err)
	}
	if status != "sent" {
		t.Fatalf("the delivery finished %q, want sent", status)
	}
	if recipient != inbound.account() {
		t.Fatalf("the delivery addressed %q, want the conversation's account %q", recipient, inbound.account())
	}
	if deliveryActivity != sentActivityID {
		t.Fatalf("the delivery anchors activity %s, want the reply just logged (%s)", deliveryActivity, sentActivityID)
	}
	if providerMessageID == nil || *providerMessageID == "" {
		t.Fatal("the delivery recorded no provider message id; the proof the message left is missing")
	}
	// Channel-shaped, not mail-shaped: the row admits one or the other and
	// never half of each.
	if subject != nil || messageID != nil {
		t.Fatalf("the delivery carries mail columns (subject=%v message_id=%v)", subject, messageID)
	}

	// The reply is filed on the SAME conversation and against the SAME person.
	// Capture joins inbound messages against outbound activities on thread_key,
	// so a reply filed anywhere else reads as a message out of nowhere.
	var threadKey, linkedPerson string
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
		ctx := context.Background()
		if err := tx.QueryRow(ctx,
			`SELECT coalesce(thread_key, '') FROM activity WHERE id = $1`, sentActivityID).Scan(&threadKey); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT person_id::text FROM activity_link WHERE activity_id = $1 AND entity_type = 'person'`,
			sentActivityID).Scan(&linkedPerson)
	}); err != nil {
		t.Fatalf("reading the reply's filing: %v", err)
	}
	if want := fmt.Sprintf("telegram:%d:%s", telegramBotID, inbound.account()); threadKey != want {
		t.Fatalf("the reply's thread_key = %q, want the conversation's %q", threadKey, want)
	}
	if linkedPerson != personID {
		t.Fatalf("the reply links person %s, want the conversation's %s", linkedPerson, personID)
	}
}
