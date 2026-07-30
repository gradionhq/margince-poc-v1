// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

// The seams one dispatch attempt runs against, and the facts derived from a
// delivery rather than asked of a collaborator. They live apart from the
// dispatch sequence itself so that the file next door reads as the sequence:
// what each gate asks, and of whom, is settled here.

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// deliveryStore is the persistence the dispatcher needs: one load that counts
// the attempt, and the four transitions that close or defer a delivery. It is
// private because Store is the only implementation the product ships — the
// interface exists so the dispatcher's branch table can be proven without a
// database, not to invite a second store.
type deliveryStore interface {
	Load(ctx context.Context, id ids.UUID) (Delivery, error)
	RecordSent(ctx context.Context, id ids.UUID, receipt connector.SendReceipt) error
	Park(ctx context.Context, id ids.UUID, reason string) error
	RecordFailure(ctx context.Context, id ids.UUID, reason string) error
	RecordDeferral(ctx context.Context, id ids.UUID, reason string) error
}

var _ deliveryStore = (*Store)(nil)

// MessageIdentityReconciler re-keys the timeline row for a message whose
// provider stamped an identity different from the one this system minted.
//
// It takes the caller's transaction so the delivery's own re-key and the
// timeline row commit together — but that transaction is NOT the receipt's.
// The ordering between the two is not symmetric: the receipt commits whenever
// the provider accepted the message, and the re-key is bookkeeping subordinate
// to it, run afterwards and best effort. A re-key that could roll the receipt
// back would return the delivery to a retry ladder whose prior-send lookup
// cannot see a rewritten identity, and the recipient would be mailed twice over
// a bookkeeping fault. So an error from here is recorded and dropped, never
// reported to the dispatcher, and an implementer may fail freely.
//
// previous is the identity the message was staged under, so the implementer
// can tell a conversation ROOT (thread_key == previous) from a reply, which
// must keep its anchor's root.
type MessageIdentityReconciler interface {
	ReconcileMessageIdentityTx(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, previous, stamped string) error
}

// ConsentGate answers whether these recipients may still be reached for this
// purpose. It is default-deny: a recipient who never granted the purpose, and
// one who withdrew it, are refused alike.
//
// It asks in RECIPIENTS rather than addresses because one delivery ladder
// carries both transports: a channel recipient has no address, and a gate that
// could only be handed addresses would be handed an empty list for every
// channel delivery — a default-deny gate asked about nobody refuses nobody, so
// the whole channel would pass a check that never ran.
//
// The dispatcher's call is THE AUTHORITATIVE CHECK. Consent is also verified
// when the send is requested, but transmission happens later and a recipient
// can withdraw in between; transmitting after a withdrawal is exactly the
// failure a default-deny gate exists to prevent. The request-time check exists
// to fail fast and keep the response ordering honest, not to stand in for this
// one.
//
// It must distinguish an ANSWER from a FAULT: apperrors.ErrConsentNotGranted
// says consent is absent, and every other error says the question could not be
// asked. The dispatcher parks on the first and retries on the second.
type ConsentGate interface {
	RequireGrantedForRecipients(ctx context.Context, recipients []connector.Recipient, purposeKey string) error
}

// SeatAuthority answers whether the human whose mailbox is about to transmit
// is still a live, mutation-capable seat, and if not, why. Deactivating a
// user revokes their sessions and passports, but a delivery staged before
// that moment carries no session of its own — so without this the off-boarded
// account's staged batch keeps leaving their mailbox for as long as the
// maximum age allows. A DOWNGRADE binds the same way: seat_type is the
// A62/ADR-0047 licensing ceiling every other seam enforces before it lets a
// principal mutate, and a delivery staged under a full seat must not outrun a
// downgrade to read that lands before it transmits — a read seat may read but
// never send, whatever staged it.
//
// It reports an ANSWER as (false, reason) and a FAULT as an error, the same
// split the consent gate makes and for the same reason: a deactivation or a
// downgrade is a decision the dispatcher must honour by parking with the
// reason named, while a database timeout is a failure to learn the decision
// and must not destroy a legitimate send.
type SeatAuthority interface {
	// ActiveSeat reports whether userID is a live, mutation-capable seat in
	// the workspace bound on ctx. reason is empty exactly when active is
	// true; when active is false, reason is the sentence the delivery parks
	// with, and it must say WHICH answer this is — an operator reading the
	// park record needs to tell a deactivated account from a live seat this
	// installation never let send.
	ActiveSeat(ctx context.Context, userID ids.UserID) (active bool, reason string, err error)
}

// ErrNoMailbox marks a user with no connection to the provider a delivery is
// staged against. There is nothing to retry against, so it parks.
var ErrNoMailbox = errors.New("comms: no mailbox is connected for this provider")

// ErrCannotSend marks a connected provider whose connector cannot transmit —
// it implements capture only. No retry turns a capture-only connector into a
// sender, so this parks too.
var ErrCannotSend = errors.New("comms: this connector cannot transmit messages")

// ErrProviderNotConfigured marks a provider this installation has no
// integration for: the delivery names it, the deployment configured no
// connector to reach it through, and nothing in the process will grow one.
//
// It PARKS rather than retries, and that is the whole reason it is a sentinel
// of its own. Read as a transient fault it is indistinguishable from a provider
// outage, so every attempt fails identically until the runner's ladder is spent
// — and the exhaustion guard runs after this point, so nothing else would ever
// move the row. It would stay pending forever, looking live and never sending.
// Parked, the row carries a reason an operator can act on, and a re-send after
// they configure the integration is one new delivery.
var ErrProviderNotConfigured = errors.New("comms: no integration for this provider is configured on this installation")

// ConnectionResolver resolves the transmitting mailbox: the connector's send
// seam, its unsealed credential, and the scopes the provider says the grant
// actually holds.
//
// ErrNoMailbox, ErrCannotSend and ErrProviderNotConfigured are the only facts
// about the deployment; EVERY OTHER ERROR IS TRANSIENT. A keyvault blip or a
// database timeout here is a failure to get an answer, and parking on one would
// permanently destroy a legitimate send that nothing is wrong with.
type ConnectionResolver interface {
	Resolve(ctx context.Context, userID ids.UserID, provider string) (connector.EmailSender, connector.Auth, []string, error)
}

// consentRecipients is every subject this delivery reaches, in the vocabulary
// the suppression gate answers about. It is the ONE place a delivery's shape is
// read on the way to that gate, which is what lets the dispatcher ask the same
// question once for both transports instead of branching before a default-deny
// check.
//
// A channel delivery reaches exactly one subject: the channel has no Cc, and
// the provider plus the recipient's account id ARE the resolution key
// (connector.ChannelIdentity). The username is deliberately absent — a handle
// can be released and re-claimed, so nothing may resolve or authorize on it.
func consentRecipients(del Delivery) []connector.Recipient {
	if del.IsChannel() {
		return []connector.Recipient{{Channel: &connector.ChannelIdentity{
			Provider:      del.Provider,
			ChannelUserID: del.ChannelRecipient(),
		}}}
	}
	return connector.EmailRecipients(addressees(del))
}

// addressees is every person this delivery reaches — To and Cc together, in
// To-then-Cc order, deduplicated case- and space-insensitively the way a mail
// server treats an address.
//
// The delivery stores the two lists apart because the wire needs them apart,
// and consent is owed to EVERY addressee however they were addressed. Gating on
// the To list alone would leave a Cc'd person no suppression at all: their
// one-click unsubscribe, and an erasure of their record, would both land
// between staging and transmit and change nothing about the message they
// receive.
//
// It fills a slice of its own and never appends onto the delivery's, because
// the wire rendering downstream reads Recipients and Cc as the separate lists
// they are.
//
// What it appends is the NORMALIZED address, not the stored spelling: the key
// it dedupes on and the value it hands the gate are then one string. Handing on
// the padded spelling would make two addresses equivalent here and then ask
// about one the gate cannot resolve — a legitimate send parked as "consent not
// granted", which reads as a recipient who opted out.
func addressees(del Delivery) []string {
	all := make([]string, 0, len(del.Recipients)+len(del.Cc))
	seen := make(map[string]bool, len(del.Recipients)+len(del.Cc))
	for _, list := range [][]string{del.Recipients, del.Cc} {
		for _, addr := range list {
			key := strings.ToLower(strings.TrimSpace(addr))
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			all = append(all, key)
		}
	}
	return all
}

// SendCapability is what this installation knows about a provider's ability to
// transmit, and it has THREE states because two cannot express a bot token: the
// token holds no OAuth scope, yet it sends. Collapsing "sends without a scope"
// into "cannot send" would read a channel provider as capture-only and park
// every message it was ever handed, with a reason naming a connector limitation
// that does not exist.
type SendCapability int

const (
	// CannotSend means nothing on this installation transmits for the provider.
	// It is the zero value, so a capability nobody answered refuses rather than
	// sends.
	CannotSend SendCapability = iota
	// SendsWithScope means the provider's grant must hold a named OAuth scope
	// before its credential may transmit, and SendScopeFor's first return names
	// that scope.
	SendsWithScope
	// SendsWithoutScope means the credential IS the whole authority and there is
	// no scope to intersect — a Telegram bot token authorizes sendMessage by
	// itself. SendScopeFor returns an empty scope, and that emptiness is not a
	// refusal.
	SendsWithoutScope
)

// SendScopeFor answers whether a provider can transmit and, when its grant must
// carry an OAuth scope to do so, which scope. A switch rather than a registry:
// two providers is not a registry, and one with two entries is an abstraction
// with no third caller.
//
// It is exported so the request-time pre-flight — which refuses a send this
// installation already knows cannot leave — asks the SAME question as the
// authority gate. Two spellings of "may this grant send" could disagree, and a
// pre-flight that accepted what the gate then parks is worse than none.
//
// Both literals below are SECOND spellings of strings a capture provider
// already declares — Gmail's OAuth consent requests that same scope constant
// rather than a copy, and capture.ProviderTelegram names that provider once —
// and they have to be: this module must not import a capture provider. compose
// imports both sides and holds them against each other
// (compose/sendscope_test.go), because drift here is silent: a misspelled scope
// parks every send as ungranted, which reads as a user who declined consent,
// and a misspelled provider reads a live channel as capture-only.
func SendScopeFor(provider string) (string, SendCapability) {
	switch provider {
	case "gmail":
		return "https://www.googleapis.com/auth/gmail.send", SendsWithScope
	case "telegram":
		return "", SendsWithoutScope
	default:
		return "", CannotSend
	}
}

// rfc8058Post derives the List-Unsubscribe-Post header from its partner. RFC
// 8058 fixes the value, so it is derived rather than stored and the pair
// cannot drift apart — a Post header without a target instructs a mail client
// to POST nowhere.
func rfc8058Post(listUnsubscribe string) string {
	if listUnsubscribe == "" {
		return ""
	}
	return "List-Unsubscribe=One-Click"
}
