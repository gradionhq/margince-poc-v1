// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The one send path (B-EP07.12): both transports — the HTTP handler
// and the MCP send_email tool — commit an outbound email through THIS
// method, so the ordering invariant (authorization refuses before the
// consent gate answers), the consent check itself, the RFC 8058
// deliverability derivation, and the threading chain cannot fork.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// SendProvider names the channel V1 transmits through. It is both the
// delivery's provider and the activity's source_system, deliberately the
// same literal: the provider files its own copy of every sent message back
// into the mailbox, and that copy is only recognised as this activity when
// the natural key it carries — (source_system, source_id) — is the one the
// send wrote.
//
// Exported because the composition root's mailbox pre-flight has to ask about
// the connection this path will actually transmit through; a second literal
// there could name a provider the send path never uses.
const SendProvider = "gmail"

// sourceManual is the provenance every send this system composes carries: a
// human typed it here, whichever transport carried it out. Spelled once because
// three write paths stamp it and `source` is read back as a vocabulary.
const sourceManual = "manual"

// unconfiguredMessageIDDomain is the right-hand side of a minted Message-ID
// on an installation that never configured its public base URL. RFC 2606
// reserves .invalid, so the identity stays syntactically valid and globally
// unique while being unmistakably not a domain this installation owns —
// preferable to borrowing one it does not.
const unconfiguredMessageIDDomain = "margince.invalid"

// errNoDeliveryStager refuses a send on a surface wired without delivery
// machinery. It carries no sentinel on purpose: this is a composition
// defect, not a client-correctable condition, so it must surface as the
// 500 it is rather than borrow a refusal (a 409 consent answer, say) that
// would tell the caller something untrue about their request.
var errNoDeliveryStager = errors.New("activities: send path has no delivery machinery wired")

// SendEmailInput is one consented outbound send anchored to an
// existing activity (the thread being replied to).
type SendEmailInput struct {
	// Recipients is the MERGED addressee list — every To AND Cc address —
	// because consent is owed to everyone who receives the message, however
	// they were addressed. Cc below is a SUBSET of it, by design and not by
	// accident: the delivery's To: line is what remains once the Cc
	// addresses come out.
	Recipients     []string
	Cc             []string
	Subject        string
	Body           string
	ConsentPurpose string
	// DraftRef names the voice draft this message came from, so the send can
	// close the learning signal that draft opened. Empty is the ordinary case:
	// mail the human composed independently resolves no draft.
	DraftRef string
}

// DeliveryStager records an outbound message for transmission. It is the
// seam between the governed send decision, which this module owns, and the
// delivery machinery, which it must not reach into directly.
//
// StageTx runs in the caller's transaction on purpose: the activity and the
// delivery are one fact. A crash between them would either promise a send
// that was never queued, or queue one with nothing on the timeline to show
// for it.
type DeliveryStager interface {
	StageTx(ctx context.Context, tx pgx.Tx, in DeliveryRequest) error
}

// DeliveryRequest is one message handed to the delivery machinery. Message
// identities are UNBRACKETED throughout — the connector adds brackets when
// it renders the header, and capture strips them when it reads one back.
//
// There is no sending-user field: whose mailbox transmits is derived from
// the authenticated principal at the far side of this seam, never named by
// a caller, exactly as captured_by is stamped everywhere else.
type DeliveryRequest struct {
	ActivityID     ids.ActivityID
	Provider       string
	MessageID      string
	Recipients     []string // To: only — the merged consent list minus Cc
	Cc             []string
	Subject        string
	Body           string // the unsubscribe footer, when there is one, is already applied
	ConsentPurpose string
	InReplyTo      string   // unbracketed; empty starts a conversation
	References     []string // unbracketed ancestry, oldest first
	ThreadKey      string
	// ListUnsubscribe is the RFC 8058 header VALUE (bracketed URL). The
	// companion List-Unsubscribe-Post value is fixed by the RFC at
	// "List-Unsubscribe=One-Click", so it is rendered at the wire from this
	// field being non-empty rather than carried alongside it — two fields
	// could drift apart, one cannot.
	ListUnsubscribe string
}

// MintMessageID generates the RFC822 message identity for one outbound
// message, UNBRACKETED. It is minted before transmission because it serves
// three purposes at once: the provider's retransmission-idempotency key,
// the natural key the provider's own copy of this message carries back into
// capture, and the identity the audit trail names.
func MintMessageID(domain string) string {
	return fmt.Sprintf("%s@%s", ids.NewV7(), domain)
}

// SendEmail runs the governed send: anchor visibility → write grant →
// wiring guards → mailbox pre-flight → consent gate → deliverability → the
// outbound activity and its delivery, committed together in the write shape.
//
// The ORDER is the invariant, not a sequence of independent checks:
// AUTHORIZATION REFUSES BEFORE CONSENT ANSWERS. A caller with no rights over
// the anchor must get the row-scope answer and nothing else — a 500 that names
// the delivery wiring, or a consent verdict, both tell them something about a
// record and a person they may not read. Every guard below is fail-closed; only
// their order carries this rule.
func (s *Store) SendEmail(ctx context.Context, anchorID ids.ActivityID, in SendEmailInput, gate ConsentGate, stager DeliveryStager) (crmcontracts.Activity, error) {
	anchor, err := s.GetActivity(ctx, anchorID, storekit.LiveOnly)
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
		return crmcontracts.Activity{}, err
	}
	// A send with no addressee reaches nobody, and NOTHING below would have
	// said so: the consent gate answers "every recipient is granted" for an
	// empty list, because every member of an empty set satisfies anything. So
	// the send ran its whole governed path and handed the provider a message
	// with no To:. The contract says minItems 1 on both transports, but a
	// declared schema is documentation here, not a validator, and this is the
	// one place both transports pass through.
	//
	// It sits after authorization, with the other guards, for the reason
	// stated above: order carries the rule that a caller with no rights over
	// the anchor learns nothing else about it.
	if len(in.Recipients) == 0 {
		return crmcontracts.Activity{}, &NoRecipientsError{}
	}
	// The composition guards sit HERE, after authorization: they report a
	// deployment defect, and a caller who may not send has no business
	// learning which parts of this installation's send path are wired.
	if gate == nil {
		// Fail closed: a send surface without its suppression gate is a
		// wiring defect, not an implicit allow.
		return crmcontracts.Activity{}, fmt.Errorf("send path has no consent authority wired: %w", apperrors.ErrConsentNotGranted)
	}
	if stager == nil {
		// Same spirit: a send nothing will ever transmit must refuse, not
		// leave a timeline entry claiming a message went out.
		return crmcontracts.Activity{}, errNoDeliveryStager
	}
	// The mailbox pre-flight is the SENDER's own authority and precedes the
	// consent gate for the same reason authorization does: a user who holds no
	// send grant must get the refusal they can act on, not a verdict about the
	// recipients' consent state.
	capable, err := s.canSend(ctx, SendProvider)
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	if !capable {
		return crmcontracts.Activity{}, &MailboxNotSendCapableError{}
	}
	if err := gate.RequireGrantedForEmails(ctx, in.Recipients, in.ConsentPurpose); err != nil {
		return crmcontracts.Activity{}, err
	}

	// Deliverability is derived here, after the gates, so both transports
	// get it and neither can send marketing mail without it.
	derived, err := s.deliverability(ctx, in.Body, in.Recipients, in.ConsentPurpose)
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	messageID := MintMessageID(s.messageIDDomain())

	message := outboundMessage{
		in:              in,
		messageID:       messageID,
		body:            derived.transmitted,
		recordedBody:    derived.recorded,
		listUnsubscribe: derived.listUnsubscribe,
		to:              toRecipients(in.Recipients, in.Cc),
		links:           inheritedLinks(anchor),
	}

	var sent crmcontracts.Activity
	err = s.tx(ctx, func(tx pgx.Tx) error {
		chain, err := anchorThreading(ctx, tx, anchorID, messageID)
		if err != nil {
			return err
		}
		sent, _, err = logActivityInTx(ctx, tx, message.activity(chain))
		if err != nil {
			return err
		}
		if err := stager.StageTx(ctx, tx, message.delivery(ids.UUID(sent.Id), chain)); err != nil {
			return err
		}
		// in.Body, not message.body: the judgment is about the text the HUMAN
		// approved, and the two differ once a footer is applied. The reason
		// in.Body still holds that text is that deliverability() returns a NEW
		// local and never rewrites in — the transmitted body is that derived
		// local. So this is correct because in is immutable, NOT because of
		// where the footer is applied relative to this call, and moving the
		// transaction boundary does not make it wrong.
		return s.recordDraftOutcome(ctx, tx, in.DraftRef, in.Body)
	})
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	return sent, nil
}

// outboundMessage is one send's derived facts, computed before the
// transaction opens so the transaction holds writes only. The timeline row and
// the delivery are two renderings of THIS value, which is why they are built
// side by side: a field that disagreed between them would be a message whose
// record and whose transmission say different things.
type outboundMessage struct {
	in        SendEmailInput
	messageID string
	// body is what the recipient receives; recordedBody is what the
	// workspace keeps. They differ by exactly one thing — the live
	// preference token the footer carries — because the timeline row is
	// served back to any seat holding activity:read, and that token is a
	// bearer credential over the recipient's consent record (see
	// redactedToken). Only the delivery may read body.
	body            string
	recordedBody    string
	listUnsubscribe string
	to              []string
	links           []ActivityLinkInput
}

// activity is the timeline row the send commits.
func (m outboundMessage) activity(chain threading) LogActivityInput {
	direction, sourceSystem := "outbound", SendProvider
	return LogActivityInput{
		Kind:         "email",
		Subject:      &m.in.Subject,
		Body:         &m.recordedBody,
		Direction:    &direction,
		Links:        m.links,
		Source:       sourceManual,
		SourceSystem: &sourceSystem,
		SourceID:     &m.messageID,
		ThreadKey:    chain.threadKey,
		// This row IS the sent copy — its natural key is the one the provider's
		// echo carries, so the echo's upsert will find it and write nothing.
		// The correspondence evidence the echo used to bring therefore has to
		// be written here or it is never written at all (ADR-0072 §1: an
		// outbound activity to an address is what makes it
		// correspondence-positive).
		CounterpartyEmail:            primaryCounterparty(m.to, m.in.Recipients),
		CounterpartyOutboundAttested: true,
	}
}

// delivery is the same message as the delivery machinery receives it.
func (m outboundMessage) delivery(activityID ids.UUID, chain threading) DeliveryRequest {
	return DeliveryRequest{
		ActivityID:      ids.From[ids.ActivityKind](activityID),
		Provider:        SendProvider,
		MessageID:       m.messageID,
		Recipients:      m.to,
		Cc:              m.in.Cc,
		Subject:         m.in.Subject,
		Body:            m.body,
		ConsentPurpose:  m.in.ConsentPurpose,
		InReplyTo:       chain.inReplyTo,
		References:      chain.references,
		ThreadKey:       chain.threadKey,
		ListUnsubscribe: m.listUnsubscribe,
	}
}

// inheritedLinks carries the anchor's own links onto the reply, so the sent
// message lands on the same records' timelines as the conversation it
// answers. The links were already visibility-checked as part of reading the
// anchor, and each one is re-checked at insert.
func inheritedLinks(anchor crmcontracts.Activity) []ActivityLinkInput {
	if anchor.Links == nil {
		return nil
	}
	links := make([]ActivityLinkInput, 0, len(*anchor.Links))
	for _, l := range *anchor.Links {
		links = append(links, ActivityLinkInput{EntityType: string(l.EntityType), EntityID: ids.UUID(l.EntityId)})
	}
	return links
}

// messageIDDomain is the right-hand side of every minted Message-ID: the
// host of the installation's configured public base URL, the one identity
// this installation is boot-configured to own. A base URL that is unset or
// unparseable falls back to the reserved domain rather than failing the
// send — a Message-ID only has to be unique and well-formed, and a
// transactional send has no other reason to require the base URL.
func (s *Store) messageIDDomain() string {
	if s.publicBaseURL == "" {
		return unconfiguredMessageIDDomain
	}
	parsed, err := url.Parse(s.publicBaseURL)
	if err != nil || parsed.Hostname() == "" {
		return unconfiguredMessageIDDomain
	}
	return parsed.Hostname()
}

// toRecipients returns the To: addresses: the merged consent list with the
// Cc addresses taken out. SendEmailInput.Recipients is the merged superset
// (consent is owed to every addressee), so rendering it as To: would copy
// every cc'd person twice. Addresses are matched case- and space-
// insensitively, the way a mail server treats them.
func toRecipients(recipients, cc []string) []string {
	if len(cc) == 0 {
		return recipients
	}
	copied := make(map[string]bool, len(cc))
	for _, addr := range cc {
		copied[normalizeAddress(addr)] = true
	}
	to := make([]string, 0, len(recipients))
	for _, addr := range recipients {
		if !copied[normalizeAddress(addr)] {
			to = append(to, addr)
		}
	}
	return to
}

func normalizeAddress(addr string) string {
	return strings.ToLower(strings.TrimSpace(addr))
}

// primaryCounterparty picks the one address `activity.counterparty_email`
// records for an outbound message: the first To, else the first addressee of
// any kind. One column holds one address, and this is the same choice the
// captured copy of this message would make — mailmap takes the first non-owner
// recipient — so a send and its echo name the same counterparty.
func primaryCounterparty(to, recipients []string) string {
	for _, addr := range append(append([]string{}, to...), recipients...) {
		if normalized := normalizeAddress(addr); normalized != "" {
			return normalized
		}
	}
	return ""
}

// messageIdentity returns value when it is a genuine RFC822 message identity
// carried by a mail activity, and "" otherwise.
//
// Both halves are load-bearing. KIND excludes the systems whose identifiers are
// opaque to mail — a Google Calendar event's iCalUID is spelled "…@google.com"
// and would pass a shape test alone while threading a reply onto nothing. SHAPE
// excludes an email activity whose source_id came from an importer rather than
// a mail header, and it is asked of the connector seam rather than spelled
// again here: the identity a send transmits under and the identity a header is
// derived from must agree on what counts.
func messageIdentity(kind, value string) string {
	if kind != "email" || !connector.ValidMessageID(value) {
		return ""
	}
	return value
}

// threading is the RFC 5322 conversation chain one outbound message
// carries, plus the key the timeline files it under.
type threading struct {
	inReplyTo  string
	references []string
	threadKey  string
}

// anchorThreading derives the conversation chain this send must carry from
// the activity it replies to, inside the staging transaction.
//
// The visibility probe is repeated here rather than inherited from the
// caller's earlier read: this reads an activity's own columns and the
// staged delivery then names that record, and anything that returns or
// references a record carries the row-scope gate — an out-of-scope anchor
// reads as ErrNotFound, the same answer a missing one gives.
//
// The chain is rooted at the anchor's thread_key because that is what the
// recipient's reply will root at: their mail client sets References to ours
// plus our Message-ID, and capture derives a thread key from the FIRST
// element of that chain. A chain whose root were not this message's stored
// thread_key would key the reply to a conversation this send is not part
// of, and the reply-detection join would miss the very mail it exists for.
// V1 reconstructs a two-element chain (root, parent) because an activity
// stores no References column; a deep thread's middle ancestors are lost,
// which costs nothing to the join and only some clients' visual nesting.
func anchorThreading(ctx context.Context, tx pgx.Tx, id ids.ActivityID, messageID string) (threading, error) {
	if err := auth.EnsureActivityVisible(ctx, tx, id.UUID); err != nil {
		return threading{}, err
	}
	var kind, parent, root string
	err := tx.QueryRow(ctx,
		`SELECT kind, coalesce(source_id, ''), coalesce(thread_key, '') FROM activity WHERE id = $1`,
		id).Scan(&kind, &parent, &root)
	if errors.Is(err, pgx.ErrNoRows) {
		return threading{}, apperrors.ErrNotFound
	}
	if err != nil {
		return threading{}, err
	}
	// Only a mail activity's identifiers are RFC822 message identities.
	// Nothing constrains an anchor to one — a send can be anchored to a
	// meeting captured from a calendar, or to a note — and emitting that
	// system's opaque id as In-Reply-To/References produces headers no mail
	// client can resolve to a message. An anchor that carries none simply
	// starts a conversation, which is the honest reading of a reply to
	// something that was never mail.
	parent, root = messageIdentity(kind, parent), messageIdentity(kind, root)

	chain := threading{inReplyTo: parent, threadKey: root}
	if root != "" && root != parent {
		chain.references = append(chain.references, root)
	}
	if parent != "" {
		chain.references = append(chain.references, parent)
	}
	if chain.threadKey == "" && len(chain.references) > 0 {
		chain.threadKey = chain.references[0]
	}
	if chain.threadKey == "" {
		// A message that answers nothing starts the conversation, and a
		// thread root is its own key — the same key capture derives when it
		// reads a root message back out of the mailbox.
		chain.threadKey = messageID
	}
	return chain, nil
}
