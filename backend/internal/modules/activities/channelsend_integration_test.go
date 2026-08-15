// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package activities

// The channel reply at the STORE, over a real migrated Postgres — the level both
// transports share, so what is proven here is proven for the HTTP handler and for
// any tool surface that reaches the same method.
//
// The seams a channel reply needs (the identity binding, the delivery machinery,
// the pre-flight) belong to other modules and are stubbed; what is real is the
// anchor, its links, the transaction, and the timeline row.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

const (
	testChannelAccount   = "550123"
	testChannelThreadKey = "telegram:8100:" + testChannelAccount
)

// recordingChannelStager captures what the reply path hands the delivery
// machinery, and can refuse, so a staging failure's effect on the timeline is
// provable.
type recordingChannelStager struct {
	staged []ChannelDeliveryRequest
	err    error
}

func (r *recordingChannelStager) StageChannelTx(_ context.Context, _ pgx.Tx, in ChannelDeliveryRequest) error {
	r.staged = append(r.staged, in)
	return r.err
}

// stubReachability answers the identity seam per person, so a test can compose a
// conversation that reaches nobody, one person, or two.
type stubReachability struct {
	byPerson map[ids.UUID][]connector.ChannelIdentity
	err      error
}

func (s stubReachability) ReachableChannelIdentities(_ context.Context, _ pgx.Tx, personID ids.UUID, _ string) ([]connector.ChannelIdentity, error) {
	return s.byPerson[personID], s.err
}

// reaches builds the seam for a conversation that reaches each of these people at
// one account apiece.
func reaches(accounts map[ids.UUID]string) stubReachability {
	byPerson := make(map[ids.UUID][]connector.ChannelIdentity, len(accounts))
	for person, account := range accounts {
		byPerson[person] = []connector.ChannelIdentity{{Provider: "telegram", ChannelUserID: account}}
	}
	return stubReachability{byPerson: byPerson}
}

// channelStore is the reply path as compose wires it: the identity seam on the
// store, where every transport reaches it.
func (e *sendEnv) channelStore(reach ChannelReachability) *Store {
	return NewStore(database.BindTo(e.pool, ids.From[ids.WorkspaceKind](e.ws))).WithChannelReachability(reach)
}

// seedChannelAnchor writes the captured conversation being answered: an inbound
// telegram activity filed under the chat, as the table owner — the shape ingress
// leaves behind, carrying BOTH the kind and the transport that brought it.
func (e *sendEnv) seedChannelAnchor(t *testing.T) ids.ActivityID {
	t.Helper()
	return e.seedAnchorWithTransport(t, KindMessage, "telegram")
}

// seedAnchorWithoutProvider writes an activity that never travelled on a
// messaging channel: a kind, and no transport. Mail is the honest example, and
// since ADR-0107/A158 it is the ONLY shape this helper can write — a message
// with no transport is refused by the schema, which
// TestTheDatabaseRefusesAKindAndTransportThatDisagree asserts directly.
func (e *sendEnv) seedAnchorWithoutProvider(t *testing.T, kind string) ids.ActivityID {
	t.Helper()
	return e.seedAnchorWithTransport(t, kind, "")
}

func (e *sendEnv) seedAnchorWithTransport(t *testing.T, kind, provider string) ids.ActivityID {
	t.Helper()
	id := ids.New[ids.ActivityKind]()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO activity (id, workspace_id, kind, channel_provider, body, occurred_at, direction,
		                      source_system, source_id, source, captured_by, thread_key)
		VALUES ($1, $2, $3, NULLIF($4, ''), 'Is this still available?', now(), 'inbound',
		        'telegram', $5, 'telegram', 'connector:telegram', $6)`,
		id, e.ws, kind, provider, "8100:"+testChannelAccount+":"+id.String(), testChannelThreadKey); err != nil {
		t.Fatalf("seeding the anchor: %v", err)
	}
	return id
}

// linkPerson attaches one more person to the conversation and returns them.
func (e *sendEnv) linkPerson(t *testing.T, anchor ids.ActivityID, name string) ids.UUID {
	t.Helper()
	person := ids.NewV7()
	ctx := context.Background()
	if _, err := e.owner.Exec(ctx,
		`INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by)
		 VALUES ($1, $2, $3, $4, 'manual', 'human:x')`, person, e.ws, name, e.rep); err != nil {
		t.Fatalf("seeding the linked person: %v", err)
	}
	if _, err := e.owner.Exec(ctx,
		`INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
		 VALUES ($1, $2, 'person', $3)`, e.ws, anchor, person); err != nil {
		t.Fatalf("linking the person: %v", err)
	}
	return person
}

// channelInput is the reply every case here sends. The purpose is fixed at the
// one the fixtures grant: what varies between these cases is the conversation and
// the wiring, and a per-case purpose would only make the gate's own answer look
// like it came from somewhere else.
func channelInput() SendMessageInput {
	return SendMessageInput{Body: "Yes — shipping Monday.", ConsentPurpose: "transactional"}
}

// Mail has a send path of its own, and its anchors carry RFC822 identities rather
// than channel ones. Pointing the channel operation at one is a caller mistake
// and is refused before a recipient is resolved — accepting it would stage a
// message with no chat to deliver into.
func TestSendMessageRefusesAnAnchorThatIsNotAChannelConversation(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchorWithoutProvider(t, "email")
	person := e.linkPerson(t, anchor, "Mail Buyer")
	stager := &recordingChannelStager{}

	_, err := e.channelStore(reaches(map[ids.UUID]string{person: testChannelAccount})).SendMessage(
		e.as(principal.RowScopeAll), anchor, channelInput(), stubConsentGate{}, stager)

	var refusal *NotAChannelConversationError
	if !errors.As(err, &refusal) {
		t.Fatalf("reply on a mail anchor → %v, want a NotAChannelConversationError", err)
	}
	if len(stager.staged) != 0 || e.outboundCount(t) != 0 {
		t.Fatal("a refused reply still staged a delivery or logged an activity")
	}
}

// The transport is read from the anchor's own column and never recovered from
// its kind — and since ADR-0107/A158 the state that used to prove it cannot be
// created at all: a message with no provider violates activity_message_has_provider.
//
// So the gate moved DOWN, from behaviour to the schema, which is strictly
// stronger. 1a's version seeded kind='telegram' with a NULL provider and
// required the send path to refuse it; the narrowing makes that row unwritable,
// so what is asserted here is that the database itself refuses both directions.
// A test that kept asserting the old refusal would be asserting a code path no
// data can reach, which is the same as asserting nothing.
func TestTheDatabaseRefusesAKindAndTransportThatDisagree(t *testing.T) {
	e := setupSend(t)

	// A message that names no transport: unrepliable by construction, so the
	// column that would have to answer "what carried this" is never empty.
	_, err := e.owner.Exec(context.Background(), `
		INSERT INTO activity (id, workspace_id, kind, body, occurred_at, source, captured_by)
		VALUES ($1, $2, $3, 'orphan', now(), 'manual', 'human:test')`,
		ids.New[ids.ActivityKind](), e.ws, KindMessage)
	if err == nil {
		t.Fatal("a message with no transport was stored; the send path would then have to guess what carried it, which is the derivation this decision removed")
	}
	if !strings.Contains(err.Error(), "activity_message_has_provider") {
		t.Fatalf("storing a transportless message failed with %v, want the activity_message_has_provider CHECK", err)
	}

	// And the reverse: a kind that travelled on nothing must not acquire a
	// transport. Asserted because a one-directional CHECK would let an email
	// carry a provider and quietly become repliable on a channel.
	_, err = e.owner.Exec(context.Background(), `
		INSERT INTO activity (id, workspace_id, kind, channel_provider, body, occurred_at, source, captured_by)
		VALUES ($1, $2, 'email', 'telegram', 'orphan', now(), 'manual', 'human:test')`,
		ids.New[ids.ActivityKind](), e.ws)
	if err == nil {
		t.Fatal("an email acquired a messaging transport; the two axes must disagree in neither direction")
	}
	if !strings.Contains(err.Error(), "activity_message_has_provider") {
		t.Fatalf("storing an email with a transport failed with %v, want the activity_message_has_provider CHECK", err)
	}
}

// A channel reply addresses one person. When the conversation reaches two, the
// send path refuses rather than picking: the two accounts are two chats, and a
// reply delivered to the wrong one messages a customer somewhere they never
// wrote from.
func TestSendMessageRefusesWhenTheConversationReachesTwoPeople(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedChannelAnchor(t)
	first := e.linkPerson(t, anchor, "First Buyer")
	second := e.linkPerson(t, anchor, "Second Buyer")
	stager := &recordingChannelStager{}

	_, err := e.channelStore(reaches(map[ids.UUID]string{
		first: testChannelAccount, second: "990456",
	})).SendMessage(e.as(principal.RowScopeAll), anchor, channelInput(), stubConsentGate{}, stager)

	var refusal *ChannelRecipientError
	if !errors.As(err, &refusal) {
		t.Fatalf("reply on a two-person conversation → %v, want a ChannelRecipientError", err)
	}
	if refusal.Reachable != 2 || refusal.Code() != "ambiguous_channel_recipient" {
		t.Fatalf("refusal reports %d reachable as %q, want 2 as ambiguous_channel_recipient", refusal.Reachable, refusal.Code())
	}
	if !strings.Contains(refusal.Error(), "exactly one") {
		t.Fatalf("refusal %q does not tell the rep what to do about it", refusal.Error())
	}
	if len(stager.staged) != 0 || e.outboundCount(t) != 0 {
		t.Fatal("a refused reply still staged a delivery or logged an activity")
	}
}

// A conversation that reaches nobody is the ordinary block case, and it is the
// same refusal with the other code: the person is still on the record, the
// conversation is still readable, and the reply must not be accepted.
func TestSendMessageRefusesWhenTheConversationReachesNobody(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedChannelAnchor(t)
	e.linkPerson(t, anchor, "Blocked Buyer")
	stager := &recordingChannelStager{}

	_, err := e.channelStore(stubReachability{}).SendMessage(
		e.as(principal.RowScopeAll), anchor, channelInput(), stubConsentGate{}, stager)

	var refusal *ChannelRecipientError
	if !errors.As(err, &refusal) || refusal.Code() != "person_unreachable" {
		t.Fatalf("reply to an unreachable person → %v, want person_unreachable", err)
	}
	if len(stager.staged) != 0 || e.outboundCount(t) != 0 {
		t.Fatal("a refused reply still staged a delivery or logged an activity")
	}
}

// The consent gate must be asked about the RESOLVED account, not about an empty
// list: a default-deny gate asked about nobody refuses nobody, which is how
// suppression silently stops applying to a whole channel.
func TestSendMessageAsksTheConsentGateAboutTheResolvedRecipient(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedChannelAnchor(t)
	person := e.linkPerson(t, anchor, "Telegram Buyer")
	stager := &recordingChannelStager{}
	gate := &recordingConsentGate{}

	sent, err := e.channelStore(reaches(map[ids.UUID]string{person: testChannelAccount})).SendMessage(
		e.as(principal.RowScopeAll), anchor, channelInput(), gate, stager)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if len(gate.recipients) != 1 || gate.recipients[0].Channel == nil {
		t.Fatalf("the gate was asked about %+v, want exactly one channel recipient", gate.recipients)
	}
	if got := gate.recipients[0].Channel.ChannelUserID; got != testChannelAccount {
		t.Fatalf("the gate was asked about account %q, want the conversation's %q", got, testChannelAccount)
	}
	if gate.recipients[0].Email != "" {
		t.Fatal("the gate was handed a channel recipient carrying a mail address; Recipient names exactly one subject")
	}
	// What the gate answered about is what the delivery carries — a message
	// staged against a different account would have been consented for somebody
	// else.
	if len(stager.staged) != 1 || stager.staged[0].Recipient.ChannelUserID != testChannelAccount {
		t.Fatalf("staged %+v, want one delivery to the account the gate cleared", stager.staged)
	}
	if got := e.storedThreadKey(t, ids.UUID(sent.Id)); got != testChannelThreadKey {
		t.Fatalf("outbound activity thread_key = %q, want the conversation's %q", got, testChannelThreadKey)
	}
}

// The pre-flight has to ask about the credential THIS send transmits through. A
// channel reply is carried by the workspace's bot, so a pre-flight asked about
// the rep's mailbox would refuse every reply on this installation while every
// mail test kept passing.
func TestSendMessagePreFlightsTheChannelProviderRatherThanTheMailbox(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedChannelAnchor(t)
	person := e.linkPerson(t, anchor, "Telegram Buyer")
	authority := &stubSendAuthority{capable: true}
	stager := &recordingChannelStager{}

	store := e.channelStore(reaches(map[ids.UUID]string{person: testChannelAccount})).WithSendAuthority(authority)
	if _, err := store.SendMessage(
		e.as(principal.RowScopeAll), anchor, channelInput(), stubConsentGate{}, stager); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if len(authority.asked) != 1 || authority.asked[0] != "telegram" {
		t.Fatalf("the pre-flight was asked about %v, want the channel the conversation was held on", authority.asked)
	}
}

// …and when it answers no, the rep is told who fixes it instead of being handed a
// 202 for a message that can only park.
func TestSendMessageRefusesWhenNoBotIsBound(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedChannelAnchor(t)
	person := e.linkPerson(t, anchor, "Telegram Buyer")
	stager := &recordingChannelStager{}
	gate := &recordingConsentGate{}

	store := e.channelStore(reaches(map[ids.UUID]string{person: testChannelAccount})).
		WithSendAuthority(&stubSendAuthority{capable: false})
	_, err := store.SendMessage(e.as(principal.RowScopeAll), anchor, channelInput(), gate, stager)

	var refusal *ChannelNotSendCapableError
	if !errors.As(err, &refusal) {
		t.Fatalf("reply with no bot bound → %v, want a ChannelNotSendCapableError", err)
	}
	if !strings.Contains(refusal.Error(), "admin") {
		t.Fatalf("refusal %q does not say who has to fix it", refusal.Error())
	}
	// The sender's own authority answers before the recipient's consent state
	// does: a rep who cannot send at all has not earned a verdict about whether
	// this customer consented.
	if gate.consulted {
		t.Error("the consent gate answered for a reply this installation cannot transmit at all")
	}
	if len(stager.staged) != 0 || e.outboundCount(t) != 0 {
		t.Fatal("a refused reply still staged a delivery or logged an activity")
	}
}

// The ordering the whole send path rests on: AUTHORIZATION REFUSES BEFORE
// ANYTHING ELSE ANSWERS. A caller with no rights over the anchor must not learn
// how this installation's reply path is wired — that is a fact about a record
// they may not read.
func TestSendMessageAnswersAnUnauthorizedCallerBeforeTheWiringGuards(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedChannelAnchor(t)
	e.linkToPersonOwnedBy(t, anchor, e.other)

	// Composed with NO delivery machinery and no identity seam: either wiring
	// guard would fire on this call if it ran first.
	_, err := e.channelStore(nil).SendMessage(
		e.as(principal.RowScopeOwn), anchor, channelInput(), stubConsentGate{}, nil)

	if errors.Is(err, errNoDeliveryStager) || errors.Is(err, errNoChannelReachability) {
		t.Fatal("an unauthorized caller learned which parts of the reply path are wired")
	}
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("reply anchored outside the caller's row scope → %v, want ErrNotFound (existence-hiding)", err)
	}
}

// A reply nothing can resolve a recipient with must refuse, not send to nobody
// — the same fail-closed posture the consent gate and the delivery stager take.
func TestSendMessageRefusesWhenTheIdentitySeamIsUnwired(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedChannelAnchor(t)
	e.linkPerson(t, anchor, "Telegram Buyer")

	_, err := e.channelStore(nil).SendMessage(
		e.as(principal.RowScopeAll), anchor, channelInput(), stubConsentGate{}, &recordingChannelStager{})

	if !errors.Is(err, errNoChannelReachability) {
		t.Fatalf("reply with no identity seam → %v, want errNoChannelReachability", err)
	}
	if e.outboundCount(t) != 0 {
		t.Fatal("a reply refused at the wiring guard still logged an activity")
	}
}

// The activity and its delivery are one fact. A staging failure that still left
// the timeline row behind would promise the rep a message that was never queued,
// on a conversation they have no way to correct.
func TestSendMessageCommitsNoActivityWhenChannelStagingFails(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedChannelAnchor(t)
	person := e.linkPerson(t, anchor, "Telegram Buyer")
	stager := &recordingChannelStager{err: errors.New("delivery table unavailable")}

	_, err := e.channelStore(reaches(map[ids.UUID]string{person: testChannelAccount})).SendMessage(
		e.as(principal.RowScopeAll), anchor, channelInput(), stubConsentGate{}, stager)

	if err == nil {
		t.Fatal("SendMessage reported success though staging refused")
	}
	if n := e.outboundCount(t); n != 0 {
		t.Fatalf("%d outbound activities survived a failed staging, want 0 (one transaction, one fact)", n)
	}
}
