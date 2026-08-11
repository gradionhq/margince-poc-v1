// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package channels

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
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// sendRegistry is the WORKER role's connector registry, carrying the fake Bot
// API in place of the real client compose.NewCaptureRegistry hard-wires. It is
// the seam the delivery path resolves the workspace's bot through, so a
// registry missing the connector reads as "this installation has no Telegram
// integration" and parks every reply.
func (c *telegramEnv) sendRegistry() *capture.Registry {
	registry := capture.NewRegistry(c.Pool, capture.NewSink(c.Pool), identity.NewService(c.Pool), c.vault)
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
	if status := c.Call(t, "GET", "/v1/consent-purposes", nil, nil, &purposes); status != http.StatusOK {
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
	if status := c.Call(t, "POST", "/v1/people/"+personID+"/consent", apptest.AnyMap{
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
	status := c.Call(t, "POST", "/v1/activities/"+activityID+"/send-message", apptest.AnyMap{
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

// TestCustomerReplyNamesTheChannelItArrivedOn continues the round trip one
// message further, to the leg CAP-FORMULA-1 is actually about: the customer
// answers the rep, and the reply fact that lands on the bus has to say it came
// over Telegram.
//
// The formula keys on thread_key and direction only, so it fires here exactly
// as it does for mail — activities/channelsend.go stamps the outbound bot
// message with the conversation's thread_key precisely so it does. What it must
// NOT do is describe the medium wrongly: `channel` is what an automation
// answering an inbound reply routes on, so "email" on a Telegram reply sends
// the answer to an address this subject does not have. The subject here has no
// address at all, which is what makes contact_id load-bearing too — resolved
// from the channel identity, it is nil for every mail-shaped lookup.
func TestCustomerReplyNamesTheChannelItArrivedOn(t *testing.T) {
	c := setupTelegramConnected(t)
	opening := telegramUpdate{
		updateID: 6101, messageID: 11, senderID: 771101,
		username: "buyer", firstName: "Nils", text: "Do you ship to Hamburg?",
	}
	answer := telegramUpdate{
		updateID: 6102, messageID: 12, senderID: 771101,
		username: "buyer", firstName: "Nils", text: "Perfect, I'll take two.",
	}
	const repReply = "We do — two days by courier."

	runner, sub := newTelegramWorker(t, c, compose.JobRunnerConfig{SendRegistry: c.sendRegistry()})
	startTelegramWorker(t, runner)

	// The opening message has no prior outbound above it, so it is not a reply.
	c.arrive(t, sub, opening)
	awaitJobKind(t, sub, compose.TelegramIngestArgs{}.Kind())
	activityID, personID := c.capturedMessage(t, opening)
	if n := c.count(t,
		`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'engagement.reply'`); n != 0 {
		t.Fatalf("%d engagement.reply events after the opening message, want 0 — "+
			"nothing outbound preceded it, so there is no thread to have replied into", n)
	}

	// The rep answers, which is what puts an outbound activity in this chat's
	// thread for the customer's next message to match against.
	c.grantConsent(t, personID, "transactional")
	if status := c.Call(t, "POST", "/v1/activities/"+activityID+"/send-message", apptest.AnyMap{
		"body": repReply, "consent_purpose": "transactional",
	}, nil, nil); status != http.StatusAccepted {
		t.Fatalf("the rep's reply → %d, want 202", status)
	}
	awaitJobKind(t, sub, compose.SendEmailArgs{}.Kind())

	// And the customer writes back. THIS is the reply.
	c.arrive(t, sub, answer)
	awaitJobKind(t, sub, compose.TelegramIngestArgs{}.Kind())

	// Counted before it is read: QueryRow over several rows scans the first and
	// reports no error, so reading alone would green a path that double-fired.
	if n := c.count(t,
		`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'engagement.reply'`); n != 1 {
		t.Fatalf("%d engagement.reply events, want exactly 1 — the formula emits once per inbound message", n)
	}
	var channel, contactID string
	if err := apptest.InWorkspace(c.AppEnv, t, c.Slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT envelope->'payload'->>'channel',
			       coalesce(envelope->'payload'->>'contact_id', '')
			FROM event_outbox WHERE envelope->>'type' = 'engagement.reply'`,
		).Scan(&channel, &contactID)
	}); err != nil {
		t.Fatalf("reading the reply fact: %v", err)
	}
	if channel != "telegram" {
		t.Errorf("engagement.reply channel = %q, want %q — an automation answering this reply "+
			"routes on this value, and a Telegram customer has no address to answer at", channel, "telegram")
	}
	if contactID != personID {
		t.Errorf("engagement.reply contact_id = %q, want the bound person %q — this sender resolves "+
			"through person_channel_identity, so an address-only lookup names nobody", contactID, personID)
	}
}

// TestAnArchivedOutboundIsNotAConversationToReplyInto holds the formula's
// prior-outbound scan to `archived_at is null`.
//
// Archiving is how a human takes a message off the timeline. A scan that still
// counted it would score engagement from a conversation the workspace has
// withdrawn — and would do it invisibly, since the matched activity the event
// names is one nobody can see any more. The assertion is a ZERO, so it is worth
// saying what makes it meaningful: the same sequence WITHOUT the archive is
// TestCustomerReplyNamesTheChannelItArrivedOn, which asserts exactly one event.
// Only the archive differs between them.
func TestAnArchivedOutboundIsNotAConversationToReplyInto(t *testing.T) {
	c := setupTelegramConnected(t)
	opening := telegramUpdate{
		updateID: 6201, messageID: 21, senderID: 771201,
		username: "browser", firstName: "Ida", text: "Are you at the fair next week?",
	}
	answer := telegramUpdate{
		updateID: 6202, messageID: 22, senderID: 771201,
		username: "browser", firstName: "Ida", text: "See you there.",
	}

	runner, sub := newTelegramWorker(t, c, compose.JobRunnerConfig{SendRegistry: c.sendRegistry()})
	startTelegramWorker(t, runner)

	c.arrive(t, sub, opening)
	awaitJobKind(t, sub, compose.TelegramIngestArgs{}.Kind())
	activityID, personID := c.capturedMessage(t, opening)

	c.grantConsent(t, personID, "transactional")
	var sent struct {
		ID string `json:"id"`
	}
	if status := c.Call(t, "POST", "/v1/activities/"+activityID+"/send-message", apptest.AnyMap{
		"body": "We are — hall 4.", "consent_purpose": "transactional",
	}, nil, &sent); status != http.StatusAccepted {
		t.Fatalf("the rep's reply → %d, want 202", status)
	}
	awaitJobKind(t, sub, compose.SendEmailArgs{}.Kind())

	// The rep takes their message back off the timeline, leaving the thread with
	// no LIVE outbound in it.
	if err := apptest.InWorkspace(c.AppEnv, t, c.Slug, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE activity SET archived_at = now() WHERE id = $1`, sent.ID)
		return err
	}); err != nil {
		t.Fatalf("archiving the outbound: %v", err)
	}

	c.arrive(t, sub, answer)
	awaitJobKind(t, sub, compose.TelegramIngestArgs{}.Kind())

	if n := c.count(t,
		`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'engagement.reply'`); n != 0 {
		t.Fatalf("%d engagement.reply events, want 0 — the only outbound in this thread was archived, "+
			"so there is no live conversation the customer replied into", n)
	}
}

// mailConnectorCtx is a MAIL connector principal in this workspace — the
// channel suite's one non-channel actor, so a message can arrive by the other
// medium without standing up a second harness.
func (c *telegramEnv) mailConnectorCtx(t *testing.T) context.Context {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), c.workspaceID(t))
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalConnector, ID: "connector:gmail",
		Permissions: principal.Permissions{
			RoleKeys: []string{"connector"},
			Objects:  map[string]principal.ObjectGrant{"activity": {Create: true}},
			RowScope: principal.RowScopeAll,
		},
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// TestAForgedThreadKeyCannotReplyIntoAnotherMediumsConversation holds the
// prior-outbound scan to one medium.
//
// thread_key is a single flat namespace carrying both a mail thread root and a
// channel's provider:bot:chat key, and the mail half is attacker-supplied — it
// is the message's own References root, copied verbatim. Both halves of a
// Telegram key are discoverable (a bot id is public, and a private chat's id IS
// the user's account id), so without a medium qualifier an outsider who can
// send mail manufactures an engagement.reply against a Telegram conversation
// they were never part of, and every consumer of that event — sequence
// exit-on-reply, warm-room scoring, an answering automation — acts on it.
func TestAForgedThreadKeyCannotReplyIntoAnotherMediumsConversation(t *testing.T) {
	c := setupTelegramConnected(t)
	opening := telegramUpdate{
		updateID: 6301, messageID: 31, senderID: 771301,
		username: "client", firstName: "Ora", text: "Can you quote the retrofit?",
	}

	runner, sub := newTelegramWorker(t, c, compose.JobRunnerConfig{SendRegistry: c.sendRegistry()})
	startTelegramWorker(t, runner)

	c.arrive(t, sub, opening)
	awaitJobKind(t, sub, compose.TelegramIngestArgs{}.Kind())
	activityID, personID := c.capturedMessage(t, opening)

	// A real outbound goes into the Telegram conversation.
	c.grantConsent(t, personID, "transactional")
	if status := c.Call(t, "POST", "/v1/activities/"+activityID+"/send-message", apptest.AnyMap{
		"body": "Sending it over today.", "consent_purpose": "transactional",
	}, nil, nil); status != http.StatusAccepted {
		t.Fatalf("the rep's reply → %d, want 202", status)
	}
	awaitJobKind(t, sub, compose.SendEmailArgs{}.Kind())

	// The thread key that outbound is filed under is what an attacker would
	// forge, so the test reads the real one rather than reconstructing it.
	var forged string
	if err := apptest.InWorkspace(c.AppEnv, t, c.Slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT thread_key FROM activity WHERE id = $1`, activityID).Scan(&forged)
	}); err != nil {
		t.Fatalf("reading the conversation's thread key: %v", err)
	}

	// A stranger's MAIL lands carrying that key as its References root. It is a
	// real inbound message that captures normally; only the thread it claims is
	// a lie, so what the assertion below reads is the match, not a refusal.
	mail := connector.NormalizedRecord{
		EntityType: datasource.EntityActivity,
		NaturalKey: connector.NaturalKey{SourceSystem: "gmail", SourceID: "forged-1@stranger.example"},
		Fields: capture.ActivityFields{
			Kind: "email", Subject: "Re: your conversation", Body: "let us talk",
			Direction: connector.DirectionInbound, OccurredAt: time.Now().UTC(),
		},
		Source:     "gmail:forged-1@stranger.example",
		CapturedBy: "connector:gmail",
		Counterparty: connector.Counterparty{
			Direction: connector.DirectionInbound,
			Email:     "stranger@elsewhere.example", Domain: "elsewhere.example",
			DisplayName: "A Stranger",
		},
		ThreadKey: forged,
	}
	if _, err := capture.NewSink(c.Pool).Upsert(c.mailConnectorCtx(t), mail); err != nil {
		t.Fatalf("capturing the stranger's mail: %v — it must land as an ordinary activity, "+
			"or this test proves a refusal rather than the thread scan", err)
	}

	if n := c.count(t,
		`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'engagement.reply'`); n != 0 {
		t.Fatalf("%d engagement.reply events, want 0 — a mail message claiming a Telegram "+
			"conversation's thread key must not match the outbound in it", n)
	}
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
	pkg, err := privacy.AssembleSAR(c.adminStoreCtx(t), c.Pool, person)
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
	if err := apptest.InWorkspace(c.AppEnv, t, c.Slug, func(tx pgx.Tx) error {
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
	if err := apptest.InWorkspace(c.AppEnv, t, c.Slug, func(tx pgx.Tx) error {
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
