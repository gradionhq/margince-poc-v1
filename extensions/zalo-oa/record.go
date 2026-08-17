// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// One Zalo message becomes one record the core can land.
//
// TWO DECISIONS RUN THROUGH THIS FILE, and both are about identity rather than
// formatting.
//
// FIRST, every id this unit writes is NAMESPACED BY THE OFFICIAL ACCOUNT.
// `connector.ChannelIdentity` deliberately omits an app id because Telegram user
// ids are global; Zalo's are not — the same human has a different id at every OA
// that talks to them. A bare id would mean that repointing this installation at
// another OA silently rebound every captured person to whoever happens to hold
// the same number there. It is not fixable later without a migration over live
// identity data, which is why it is done before the first row is written.
//
// SECOND, the counterparty is named by ACCOUNT and by nothing else. Zalo gives an
// OA no address for a human anywhere, so there is no address to carry, no domain
// to state, and no party list to enumerate — and the ingress declaration vouches
// for no merge key precisely because there is nothing here to vouch for.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// recordFor builds the record one message lands as.
//
// It refuses a message with no counterparty account or no provider id: the first
// is a record nothing can be replied to or bound by, and the second is a record
// with no stable natural key, which would land a second copy on every poll with
// nothing reporting an error.
func recordFor(msg chatMessage, oaID string) (extension.Record, error) {
	account, displayName := msg.counterparty()
	switch {
	case strings.TrimSpace(msg.MessageID) == "":
		return extension.Record{}, fmt.Errorf("zalo-oa: a message carries no message_id, so it has no key a replay could recognise")
	case strings.TrimSpace(account) == "":
		return extension.Record{}, fmt.Errorf("zalo-oa: message %s names no counterparty account", msg.MessageID)
	}
	scoped := scopedAccount(oaID, account)
	return extension.Record{
		System: ingressSystem,
		// The provider's own id for the message, namespaced by the account it
		// belongs to. It is proven identical on the send and on the read back,
		// which is what lets one number be both the receipt and the key that
		// makes a replay a no-op.
		Key: fmt.Sprintf("%s:%s", oaID, msg.MessageID),
		Activity: extension.ActivityFields{
			// A message, on this unit's own transport — the two axes stated
			// separately (ADR-0107/A158). The provider is a LITERAL rather than
			// the `provider` constant, for the reason the declaration's is: the
			// core holds a unit to the channels it DECLARED, and that
			// declaration is read statically from the AST without compiling the
			// unit. A test holds the two equal.
			Kind:            extension.ActivityKindMessage,
			ChannelProvider: "zalo_oa",
			Subject:         subjectFor(msg, displayName),
			Body:            bodyFor(msg),
			OccurredAt:      time.UnixMilli(msg.Time).UTC(),
			Direction:       directionOf(msg),
		},
		// Namespaced by provider AND account, per the core's own channel rule. A
		// bare conversation id would share activity.thread_key with every other
		// source, where two of them can collide and join a stranger's
		// conversation onto this one.
		ThreadKey: fmt.Sprintf("%s:%s:%s", provider, oaID, account),
		Counterparty: extension.Counterparty{
			DisplayName: boundedName(displayName),
			Direction:   directionOf(msg),
			ChannelIdentity: extension.ChannelIdentity{
				Provider:      "zalo_oa",
				ChannelUserID: scoped,
				DisplayName:   boundedName(displayName),
			},
		},
		// EMPTY, and legal precisely because the counterparty names no address:
		// the core's internal-message gate reads every party from this set, and
		// the partition it draws is "does the counterparty have an address",
		// which here it never does. A synthesized address would be this unit
		// inventing a party the provider never named.
		Addresses: nil,
		// The provider's record as received, kept as evidence. It is the original
		// document rather than a re-encoding of the fields above, so what the
		// installation stores is what the provider said — including
		// `user_id_by_app` where it appears, which nothing reads.
		Raw: msg.Raw,
	}, nil
}

// scopedAccount is the routing key the core binds a person on.
//
// The OA prefix is the whole point (see this file's header): Zalo account ids
// are per-OA, so the pair is what identifies a human and the bare id identifies
// nothing on its own.
func scopedAccount(oaID, account string) string {
	return oaID + ":" + account
}

// accountWithinOA is scopedAccount reversed, and it REFUSES an id that belongs
// to another Official Account.
//
// The refusal is the point rather than the parsing. A recipient carrying another
// OA's prefix reaches this installation when a connection has been repointed and
// a person binding survives from the old account — and the bare id inside it is
// perfectly well-formed, so sending it would deliver a rep's message to whoever
// holds that number at the CURRENT account. There is no way to detect that
// afterwards, which is why it is refused before.
func accountWithinOA(oaID, scoped string) (string, error) {
	prefix := oaID + ":"
	if oaID == "" || !strings.HasPrefix(scoped, prefix) {
		return "", fmt.Errorf("zalo-oa: this recipient belongs to a different Official Account than the one connected, so the account id in it names a different person here")
	}
	account := strings.TrimPrefix(scoped, prefix)
	if account == "" {
		return "", fmt.Errorf("zalo-oa: this recipient names an Official Account and no person within it")
	}
	return account, nil
}

// directionOf reads the provider's own `src`, which states the direction rather
// than implying it: 1 is the customer writing to the OA, 0 is the OA writing
// back. A connector inferring it from which id matched the account would invert
// every message the day an OA wrote to itself.
func directionOf(msg chatMessage) string {
	if msg.inbound() {
		return extension.DirectionInbound
	}
	return extension.DirectionOutbound
}

// subjectFor is the one-line "who did what" a timeline row shows.
//
// Zalo messages have no subject, so this unit supplies the only honest one: the
// human's own name at the account. It is the counterparty's name in both
// directions, because a timeline row is read from the CRM's side and the other
// party is who the conversation is with.
func subjectFor(msg chatMessage, displayName string) string {
	name := strings.TrimSpace(displayName)
	if name == "" {
		return "Zalo message"
	}
	if msg.inbound() {
		return "Zalo message from " + boundedName(name)
	}
	return "Zalo message to " + boundedName(name)
}

// bodyFor is the message as a human reads it.
//
// A text message is its text. Everything else is named by TYPE plus whatever
// locator the provider gave, and the URL is shown as it arrived and never
// fetched: resolving a customer-supplied URL would have this installation's own
// worker make a request somebody else chose, which is a request-forgery surface
// on a body a stranger controls. A link therefore reads as a bare address rather
// than as an unfurled preview, which is a real loss against the webhook this
// design does not use, and it is a loss taken deliberately.
func bodyFor(msg chatMessage) string {
	text := strings.TrimSpace(msg.Message)
	kind := strings.TrimSpace(msg.Type)
	if kind == "" || kind == typeText {
		return text
	}
	parts := []string{"[" + kind + "]"}
	for _, extra := range []string{text, strings.TrimSpace(msg.Description), strings.TrimSpace(msg.URL), locationOf(msg)} {
		if extra != "" {
			parts = append(parts, extra)
		}
	}
	for _, link := range msg.Links {
		if address := linkURL(link); address != "" {
			parts = append(parts, address)
		}
	}
	return strings.Join(parts, " ")
}

// linkURL reads one entry of a `link` or `links` message.
//
// It accepts BOTH shapes the provider might be sending — a bare URL string, or
// an object carrying one — because which of the two arrives here has never been
// measured: the documentation says bare URLs and the webhook this design does
// not use wrapped each in an object. Reading it as one concrete type would have
// dropped the whole body of every link message the day it turned out to be the
// other, silently, on a field that IS the message.
//
// Anything it cannot read answers empty rather than guessing, and the provider's
// original row is kept as evidence either way.
func linkURL(raw json.RawMessage) string {
	var bare string
	if err := json.Unmarshal(raw, &bare); err == nil {
		return strings.TrimSpace(bare)
	}
	var wrapped struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil {
		return strings.TrimSpace(wrapped.URL)
	}
	return ""
}

// typeText is the only message type whose body is simply its text.
const typeText = "text"

// locationOf renders a shared location.
//
// `location` arrives as a STRING carrying a longitude and a latitude, not as an
// object — decoding it into a struct silently yields a zero pair, which reads on
// a timeline as a customer having shared the Gulf of Guinea. It is therefore
// passed through as the provider wrote it.
func locationOf(msg chatMessage) string {
	if strings.TrimSpace(msg.Location) == "" {
		return ""
	}
	return "location " + strings.TrimSpace(msg.Location)
}

// boundedName trims untrusted display text to the core's own cap.
//
// The core bounds and strips it too; doing it here as well means a name over the
// cap is shortened rather than making the whole record a refusal the poll would
// then have to classify.
func boundedName(name string) string {
	trimmed := strings.TrimSpace(name)
	if len([]rune(trimmed)) <= extension.MaxDisplayNameRunes {
		return trimmed
	}
	return string([]rune(trimmed)[:extension.MaxDisplayNameRunes])
}
