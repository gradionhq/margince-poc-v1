// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The channel reply (telegram-oa design §8.1, §8.5) — SendEmail's sibling, and
// the one path both transports commit through: the human's reply box on the
// timeline and the agent surface's send_message verb.
//
// What it SHARES with mail is what governs a send: the order of the gates
// (authorization refuses before consent answers), the default-deny consent
// check, and one transaction holding the timeline row and the delivery
// together. What differs is only the vocabulary of the transport:
//
//   - the recipient is RESOLVED from the conversation, never named by the
//     caller. A channel identity is an opaque third-party account id, so a
//     caller able to name one could message a human this conversation is not
//     with — and the reply surface has no legitimate use for that;
//   - reachability replaces address validity: a live identity that has not
//     blocked the workspace's bot;
//   - the outbound activity carries no subject and no natural key, because a
//     channel has no subject line and the provider files no echo of the sent
//     message for a key to collapse onto.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// channelProviders is the derived channel-kind vocabulary (DESIGN-SP4 §4):
// which activity kinds are messaging-channel conversations this installation
// can actually reply on. Set once at boot by compose's registry reconcile
// (internal/compose), from the composed core-connector and unit set — this
// module must not import either to compute it itself.
//
// A plain mutex-guarded package var, not a "register once, panic on
// duplicate" idiom: compose's own registry construction can legitimately run
// more than once in a process (a role-specific alternate wiring path, a
// one-shot backfill helper), and every one of those calls must be able to
// call SetChannelProviders again with the same or a narrower set — the last
// call in a boot sequence is authoritative, not the first.
var (
	channelProvidersMu sync.RWMutex
	channelProviders   = map[string]bool{"telegram": true} // the pre-registry default, replaced at first boot
)

// SetChannelProviders replaces the derived channel-kind vocabulary wholesale.
// Exported so compose — which knows the composed connector/unit set this
// module must not reach across to enumerate — can set it; nothing in this
// module calls it.
func SetChannelProviders(providers []string) {
	next := make(map[string]bool, len(providers))
	for _, p := range providers {
		next[p] = true
	}
	channelProvidersMu.Lock()
	channelProviders = next
	channelProvidersMu.Unlock()
}

// KindMessage is the one interaction kind that names a channel conversation
// (ADR-0107/A158). Which transport carried it is activity.channel_provider, a
// separate axis — reading one off the other is exactly what that decision
// retired.
const KindMessage = "message"

// IsChannelKind reports whether an activity kind names a messaging-channel
// conversation.
//
// Since the narrowing this is a comparison, not a set membership test: every
// channel message is `message` regardless of transport. Kept as a function
// rather than inlined at its callers because those callers ask a domain
// question — "is this conversation repliable in principle" — and the answer
// changing shape once already is the argument for it having one name.
func IsChannelKind(kind string) bool {
	return kind == KindMessage
}

// SendableChannelProviders lists the transports this installation composed a
// sender for. Exported so the composition root can publish which registered
// transports can actually carry a message, without keeping a second copy of a
// set this package already owns — two copies is how the send path and the
// directory would come to disagree.
func SendableChannelProviders() []string {
	channelProvidersMu.RLock()
	defer channelProvidersMu.RUnlock()
	out := make([]string, 0, len(channelProviders))
	for p := range channelProviders {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// CanSendOnProvider reports whether this installation composed a transport
// that can carry a reply on the given provider.
//
// This is deliberately NOT the same question as IsChannelKind. A row may name a
// perfectly real transport this binary did not compose — whatsapp is registered
// but has no connector — and the honest answer there is "this is a message, and
// no reply can leave this installation for it". Answering both questions with
// one set is what let a kind stand in for a transport in the first place.
func CanSendOnProvider(provider string) bool {
	channelProvidersMu.RLock()
	defer channelProvidersMu.RUnlock()
	return channelProviders[provider]
}

// SendMessageInput is one consented channel reply. There is no recipient field
// and no subject: see the file comment.
type SendMessageInput struct {
	Body           string
	ConsentPurpose string
}

// ChannelDeliveryStager records an outbound CHANNEL message for transmission —
// the channel twin of DeliveryStager, and separate from it for the reason the
// delivery store keeps two staging shapes: one struct carrying both an RFC822
// subject and a channel recipient could describe a message that is half of each,
// and the only thing left to refuse it would be the database.
//
// StageChannelTx runs in the caller's transaction for DeliveryStager's reason:
// the activity and the delivery are one fact.
type ChannelDeliveryStager interface {
	StageChannelTx(ctx context.Context, tx pgx.Tx, in ChannelDeliveryRequest) error
}

// ChannelDeliveryRequest is one channel message handed to the delivery
// machinery.
//
// There is no sending-user field, exactly as on DeliveryRequest: whose act this
// is comes from the authenticated principal at the far side of the seam. The
// workspace's bot transmits, but the human is still the sender the seat gate
// re-reads at transmission.
type ChannelDeliveryRequest struct {
	ActivityID     ids.ActivityID
	Provider       string
	Recipient      connector.ChannelIdentity
	Body           string
	ConsentPurpose string
}

// ChannelReachability answers where a person can be reached on a messaging
// channel. The people module owns the identity binding and implements it; the
// composition root injects it, so this module never reaches across to read
// another's rows.
//
// It returns every reachable identity rather than one, because a person may hold
// two accounts on the same channel and NOTHING here may pick between them: the
// two accounts are two chats, and a reply delivered to the wrong one reaches the
// human somewhere they did not write from.
//
// An empty list is an ANSWER — unreachable — and not a fault. Only a failure to
// ask is an error.
type ChannelReachability interface {
	ReachableChannelIdentities(ctx context.Context, tx pgx.Tx, personID ids.UUID, provider string) ([]connector.ChannelIdentity, error)
}

// WithChannelReachability returns a store whose channel reply can resolve the
// conversation's counterparty. It lives on the STORE rather than on a transport
// because every send seam does: a tool surface reaches these methods directly,
// and a dependency wired into one transport is a dependency the others silently
// lack.
func (s *Store) WithChannelReachability(reach ChannelReachability) *Store {
	clone := *s
	clone.reachability = reach
	return &clone
}

// errNoChannelReachability refuses a channel send on a surface wired without the
// identity seam. It carries no sentinel for errNoDeliveryStager's reason: this
// is a composition defect, not a client-correctable condition, so it must
// surface as the 500 it is rather than borrow a refusal that would tell the
// caller something untrue about their request — here, that the person they are
// looking at cannot be reached.
var errNoChannelReachability = errors.New("activities: channel send path has no reachability authority wired")

// NotAChannelConversationError refuses a channel reply anchored to something
// that is not a channel conversation — mail, a note, a meeting. It maps to 422:
// the caller pointed the wrong operation at the record, and mail has a send
// path of its own.
type NotAChannelConversationError struct{ Kind string }

func (e *NotAChannelConversationError) Error() string {
	return "a " + e.Kind + " activity is not a messaging-channel conversation; reply on the channel the conversation was held on"
}

// FieldFault refuses a channel send against a conversation of another kind.
func (e *NotAChannelConversationError) FieldFault() (field, code, message string) {
	return "id", "not_a_channel_conversation", e.Error()
}

// errEmptyMessageBody refuses a reply with nothing in it. A messaging provider
// rejects a text-less message, so accepting one buys a timeline entry saying the
// rep answered plus a delivery that walks the entire retry ladder before parking
// under a transport reason that names nothing the operator can act on.
//
// Whitespace counts as nothing, because whitespace is what an accidental send
// leaves in the composer. The contract's minLength cannot carry this: nothing in
// this stack validates a request against the schema.
var errEmptyMessageBody = errors.New("a message needs something to say — type the reply, then send it")

// ChannelRecipientError refuses a reply whose ONE recipient cannot be
// determined: nobody on the conversation is reachable, or more than one is. Both
// are refused before anything is staged, and both are the caller's to resolve —
// so this maps to 422 and says which case it is.
//
// The two are one type because they are one question with one answer shape (how
// many people this conversation can reach), and because the alternative — a
// default that picks somebody — is the failure both exist to prevent.
type ChannelRecipientError struct {
	Provider  string
	Reachable int
}

func (e *ChannelRecipientError) Error() string {
	if e.Reachable == 0 {
		return "nobody on this conversation can be reached on " + e.Provider +
			" — the person has never messaged this workspace's bot, or they have blocked it"
	}
	return "this conversation reaches " + strconv.Itoa(e.Reachable) + " people on " + e.Provider +
		"; a channel reply addresses exactly one — send it from the person's own record"
}

// FieldFault carries the recipient code the caller must correct.
func (e *ChannelRecipientError) FieldFault() (field, code, message string) {
	return "recipient", e.Code(), e.Error()
}

// Code names the wire code this refusal answers with. The two cases are
// different problems for the caller — nobody to reach, versus a choice only they
// can make — so they are told apart on the wire even though one type carries
// both.
func (e *ChannelRecipientError) Code() string {
	if e.Reachable == 0 {
		return "person_unreachable"
	}
	return "ambiguous_channel_recipient"
}

// SendMessage runs the governed channel reply: anchor visibility → write grant →
// wiring guards → a message to send → channel pre-flight → recipient resolution →
// consent gate → the outbound activity and its delivery, committed together in
// the write shape.
//
// The ORDER is the invariant, and it is SendEmail's order for SendEmail's
// reasons: AUTHORIZATION REFUSES BEFORE ANYTHING ELSE ANSWERS. A caller with no
// rights over the anchor learns neither how this installation is wired nor
// whether the person behind the conversation consented — both are facts about a
// record they may not read.
//
// Recipient resolution precedes the consent gate because the gate is asked about
// the recipient: default-deny cannot answer about a subject nobody has named
// yet, and a gate handed an empty list refuses nobody.
func (s *Store) SendMessage(ctx context.Context, anchorID ids.ActivityID, in SendMessageInput, gate ConsentGate, stager ChannelDeliveryStager) (crmcontracts.Activity, error) {
	anchor, err := s.GetActivity(ctx, anchorID, storekit.LiveOnly)
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
		return crmcontracts.Activity{}, err
	}
	// The composition guards sit HERE, after authorization, for SendEmail's
	// reason: they report a deployment defect, and a caller who may not send has
	// no business learning which parts of this installation's send path are
	// wired.
	if gate == nil {
		// Fail closed: a send surface without its suppression gate is a wiring
		// defect, not an implicit allow.
		return crmcontracts.Activity{}, fmt.Errorf("channel send path has no consent authority wired: %w", apperrors.ErrConsentNotGranted)
	}
	if stager == nil {
		return crmcontracts.Activity{}, errNoDeliveryStager
	}
	if s.reachability == nil {
		return crmcontracts.Activity{}, errNoChannelReachability
	}
	if strings.TrimSpace(in.Body) == "" {
		return crmcontracts.Activity{}, errEmptyMessageBody
	}
	// The transport is READ, never recovered from the anchor's kind. Since
	// ADR-0107/A158 the kind names no transport at all, so there is nothing left
	// to derive from — and before it, the two vocabularies merely coincided for
	// the channels shipped so far, which was never a rule. An empty provider is
	// the anchor saying it never travelled on a channel, which the database now
	// guarantees is true exactly when the kind is not a message.
	provider, err := s.channelProviderOf(ctx, anchorID)
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	if provider == "" {
		// Reported by the anchor's KIND, which is what a rep recognises — being
		// told "a note is not a messaging-channel conversation" names the mistake,
		// where an empty provider would name the storage.
		return crmcontracts.Activity{}, &NotAChannelConversationError{Kind: string(anchor.Kind)}
	}
	// The bot binding is the workspace's, and its absence is knowable now: a
	// send accepted without one can only park where the rep never sees it.
	capable, err := s.canSend(ctx, provider)
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	if !capable {
		return crmcontracts.Activity{}, &ChannelNotSendCapableError{Provider: provider}
	}

	conversation, err := s.resolveConversation(ctx, anchorID, provider)
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	if err := gate.RequireGrantedForRecipients(ctx,
		[]connector.Recipient{{Channel: &conversation.recipient}}, in.ConsentPurpose); err != nil {
		return crmcontracts.Activity{}, err
	}

	message := outboundChannelMessage{in: in, provider: provider, conversation: conversation, links: inheritedLinks(anchor)}
	var sent crmcontracts.Activity
	err = s.tx(ctx, func(tx pgx.Tx) error {
		// The staged delivery names the anchor's conversation and the person it
		// is with, so this transaction reads records too — and anything that
		// reads a record carries the row-scope gate, whatever an earlier read
		// already answered.
		if err := auth.EnsureActivityVisible(ctx, tx, anchorID.UUID); err != nil {
			return err
		}
		var err error
		sent, _, err = logActivityInTx(ctx, tx, message.activity())
		if err != nil {
			return err
		}
		return stager.StageChannelTx(ctx, tx, message.delivery(ids.From[ids.ActivityKind](ids.UUID(sent.Id))))
	})
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	return sent, nil
}

// channelConversation is the anchor read the reply needs and the contract's
// Activity does not carry: which conversation it is filed under, and the one
// account the reply goes to.
type channelConversation struct {
	threadKey string
	recipient connector.ChannelIdentity
}

// channelProviderOf reads which messaging transport carried an activity, and
// returns empty when it carried none.
//
// It is a read of a record, so it carries the row-scope gate for the reason
// resolveConversation states below: an out-of-scope anchor reads as ErrNotFound,
// the same answer a missing one gives, so the send path cannot become an
// existence oracle for activities the caller may not see.
func (s *Store) channelProviderOf(ctx context.Context, anchorID ids.ActivityID) (string, error) {
	var provider string
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureActivityVisible(ctx, tx, anchorID.UUID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT coalesce(channel_provider, '') FROM activity WHERE id = $1`, anchorID).Scan(&provider); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperrors.ErrNotFound
			}
			return err
		}
		return nil
	})
	return provider, err
}

func (s *Store) resolveConversation(ctx context.Context, anchorID ids.ActivityID, provider string) (channelConversation, error) {
	var out channelConversation
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// The probe is repeated rather than inherited from the caller's earlier
		// read: what this returns is the conversation's own counterparty, and
		// anything that reads a record carries the row-scope gate — an
		// out-of-scope anchor reads as ErrNotFound, the answer a missing one
		// gives.
		if err := auth.EnsureActivityVisible(ctx, tx, anchorID.UUID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT coalesce(thread_key, '') FROM activity WHERE id = $1`, anchorID).Scan(&out.threadKey); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperrors.ErrNotFound
			}
			return err
		}
		reachable, err := s.reachableOnConversation(ctx, tx, anchorID, provider)
		if err != nil {
			return err
		}
		if len(reachable) != 1 {
			return &ChannelRecipientError{Provider: provider, Reachable: len(reachable)}
		}
		out.recipient = reachable[0]
		return nil
	})
	return out, err
}

// reachableOnConversation collects every account the anchor's linked people can
// be reached at on provider.
//
// It reads the links rather than taking a person from the caller: the reply goes
// to the counterparty of THIS conversation, and the links are the record of who
// that is. They were visibility-checked when the anchor was read, and each one is
// re-checked when the reply's own links are inserted.
func (s *Store) reachableOnConversation(ctx context.Context, tx pgx.Tx, anchorID ids.ActivityID, provider string) ([]connector.ChannelIdentity, error) {
	rows, err := tx.Query(ctx,
		`SELECT person_id FROM activity_link
		  WHERE activity_id = $1 AND entity_type = 'person' AND person_id IS NOT NULL`, anchorID)
	if err != nil {
		return nil, err
	}
	people, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return nil, err
	}
	var reachable []connector.ChannelIdentity
	for _, personID := range people {
		identities, err := s.reachability.ReachableChannelIdentities(ctx, tx, personID, provider)
		if err != nil {
			return nil, err
		}
		reachable = append(reachable, identities...)
	}
	return reachable, nil
}

// outboundChannelMessage is one reply's derived facts. The timeline row and the
// delivery are two renderings of THIS value, built side by side for the reason
// outboundMessage is: a field that disagreed between them would be a message
// whose record and whose transmission say different things.
type outboundChannelMessage struct {
	in           SendMessageInput
	provider     string
	conversation channelConversation
	links        []ActivityLinkInput
}

// activity is the timeline row the reply commits — the row that makes the sent
// message survive a page reload, which is the whole reason the rep can trust the
// surface.
//
// It carries NO natural key, and that is a statement rather than an omission: a
// bot has no companion app filing an echo of the sent message back into capture
// (design §6.4), so there is no second copy for a (source_system, source_id) key
// to collapse onto. The provider's own message id lands on the delivery row,
// where the receipt is recorded.
func (m outboundChannelMessage) activity() LogActivityInput {
	direction := "outbound"
	return LogActivityInput{
		// The two axes, stated separately (ADR-0107/A158). The reply IS a
		// message; what carries it is the anchor's own transport. Capture's
		// reply-match reads the PAIR — kind to separate mail from channel,
		// provider to separate one transport from another — so an outbound leg
		// filed with either half wrong is a conversation the next inbound
		// message cannot match into.
		Kind:            KindMessage,
		ChannelProvider: m.provider,
		Body:            &m.in.Body,
		Direction:       &direction,
		Links:           m.links,
		Source:          sourceManual,
		// The same conversation the anchor is filed under: capture's
		// reply-detection joins an inbound message against outbound activities
		// on this key, so a reply filed anywhere else is a reply that
		// conversation never had.
		ThreadKey: m.conversation.threadKey,
	}
}

// delivery is the same message as the delivery machinery receives it.
//
// It anchors on nothing. A channel reply lands in the customer's chat because the
// chat IS the conversation, while anchoring it to one MESSAGE would mean parsing
// the provider's own natural-key format out of the anchor's source_id — a format
// that belongs to the capture provider, and which this module would be guessing
// at. A wrong anchor is refused by the provider outright, so guessing costs the
// rep their message to buy some visual nesting.
func (m outboundChannelMessage) delivery(activityID ids.ActivityID) ChannelDeliveryRequest {
	return ChannelDeliveryRequest{
		ActivityID:     activityID,
		Provider:       m.provider,
		Recipient:      m.conversation.recipient,
		Body:           m.in.Body,
		ConsentPurpose: m.in.ConsentPurpose,
	}
}
