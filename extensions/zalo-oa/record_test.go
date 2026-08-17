// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// What one Zalo message becomes, and the two identity rules that make it safe.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

func inboundMessage() chatMessage {
	raw := json.RawMessage(`{"src":1,"message_id":"66e35f59","time":1786689951020}`)
	return chatMessage{
		Src: srcUserToOA, Time: 1786689951020, MessageID: "66e35f59", Type: "text",
		Message: "Chào anh", FromID: "user-1", FromDisplayName: "Nguyễn An",
		ToID: fixtureOAID, ToDisplayName: "NFQ", Raw: raw,
	}
}

// The record the core is handed passes the core's own published grammar. That
// check is the point: a unit whose records are only validated by its own
// expectations is a unit whose suite agrees with it and whose production does
// not.
func TestAnInboundMessageBecomesARepliableChannelRecord(t *testing.T) {
	rec, err := recordFor(inboundMessage(), fixtureOAID)
	if err != nil {
		t.Fatalf("recordFor: %v", err)
	}
	if err := rec.Validate(); err != nil {
		t.Fatalf("the record the core would be handed is not valid: %v", err)
	}
	if rec.System != ingressSystem {
		t.Fatalf("System = %q, want %q", rec.System, ingressSystem)
	}
	if rec.Activity.Kind != extension.ActivityKindMessage {
		t.Fatalf("Kind = %q, want %q — a unit names the transport, never the kind", rec.Activity.Kind, extension.ActivityKindMessage)
	}
	if rec.Activity.ChannelProvider != provider {
		t.Fatalf("ChannelProvider = %q, want %q", rec.Activity.ChannelProvider, provider)
	}
	if rec.Activity.Direction != extension.DirectionInbound {
		t.Fatalf("Direction = %q, want inbound — src 1 is the customer writing to the account", rec.Activity.Direction)
	}
	if rec.Activity.Body != "Chào anh" {
		t.Fatalf("Body = %q, want the message text", rec.Activity.Body)
	}
	if got, want := rec.Activity.OccurredAt, time.UnixMilli(1786689951020).UTC(); !got.Equal(want) {
		t.Fatalf("OccurredAt = %s, want the provider's own timestamp %s", got, want)
	}
	// The counterparty must be repliable: a channel record with no identity lands
	// and reads fine on the timeline and answers "nobody on this conversation can
	// be reached".
	if rec.Counterparty.ChannelIdentity.ChannelUserID == "" {
		t.Fatal("the counterparty carries no channel identity, so the captured conversation could not be answered")
	}
	if string(rec.Raw) != `{"src":1,"message_id":"66e35f59","time":1786689951020}` {
		t.Fatalf("Raw = %s, want the provider's own document unaltered", rec.Raw)
	}
}

// Zalo account ids are OA-scoped rather than global, so everything keyed on one
// is prefixed with the account. Without it, repointing an installation at another
// Official Account silently rebinds every captured person to whoever holds the
// same number there.
func TestEveryIdentityKeyIsNamespacedByTheOfficialAccount(t *testing.T) {
	rec, err := recordFor(inboundMessage(), fixtureOAID)
	if err != nil {
		t.Fatalf("recordFor: %v", err)
	}
	if want := fixtureOAID + ":user-1"; rec.Counterparty.ChannelIdentity.ChannelUserID != want {
		t.Fatalf("ChannelUserID = %q, want %q — a bare Zalo id names a different human at another account",
			rec.Counterparty.ChannelIdentity.ChannelUserID, want)
	}
	if want := fixtureOAID + ":66e35f59"; rec.Key != want {
		t.Fatalf("Key = %q, want %q — two accounts number their messages independently", rec.Key, want)
	}
	if want := provider + ":" + fixtureOAID + ":user-1"; rec.ThreadKey != want {
		t.Fatalf("ThreadKey = %q, want %q — an unnamespaced thread key can join a stranger's conversation onto this one",
			rec.ThreadKey, want)
	}
}

// The counterparty is named by ACCOUNT and by nothing else, and the record names
// no addresses at all. Both follow from the provider: an Official Account is
// given no address for a human anywhere, and the core admits the empty set
// precisely because the counterparty has no address either.
func TestTheCounterpartyNamesAnAccountAndNoAddress(t *testing.T) {
	rec, err := recordFor(inboundMessage(), fixtureOAID)
	if err != nil {
		t.Fatalf("recordFor: %v", err)
	}
	if rec.Counterparty.Email != "" {
		t.Fatalf("the counterparty carries the address %q, which Zalo never supplies — it could only have been invented", rec.Counterparty.Email)
	}
	if rec.Counterparty.Domain != "" {
		t.Fatalf("the counterparty names the domain %q with no address to have taken it from", rec.Counterparty.Domain)
	}
	if len(rec.Addresses) != 0 {
		t.Fatalf("the record names addresses %v; there are none to name", rec.Addresses)
	}
	if err := rec.Validate(); err != nil {
		t.Fatalf("an address-less channel record must be legal, and this one is not: %v", err)
	}
}

// The direction is read from `src` rather than inferred from which id matched the
// account, so an outbound message names the customer it went TO.
func TestAnOutboundMessageNamesTheCustomerItWentTo(t *testing.T) {
	msg := inboundMessage()
	msg.Src = srcOAToUser
	msg.FromID, msg.FromDisplayName = fixtureOAID, "NFQ"
	msg.ToID, msg.ToDisplayName = "user-1", "Nguyễn An"

	rec, err := recordFor(msg, fixtureOAID)
	if err != nil {
		t.Fatalf("recordFor: %v", err)
	}
	if rec.Activity.Direction != extension.DirectionOutbound {
		t.Fatalf("Direction = %q, want outbound", rec.Activity.Direction)
	}
	if want := fixtureOAID + ":user-1"; rec.Counterparty.ChannelIdentity.ChannelUserID != want {
		t.Fatalf("ChannelUserID = %q, want the customer %q rather than the account itself",
			rec.Counterparty.ChannelIdentity.ChannelUserID, want)
	}
	if !strings.Contains(rec.Activity.Subject, "to Nguyễn An") {
		t.Fatalf("Subject = %q, want it to name who the message went to", rec.Activity.Subject)
	}
}

// A message with no id has no key a replay could recognise, and one with no
// counterparty account cannot be bound or answered. Both are refused rather than
// landed, because a record that lands and cannot be identified is worse than one
// that never lands.
func TestAMessageThatCannotBeIdentifiedIsRefused(t *testing.T) {
	for name, mutate := range map[string]func(*chatMessage){
		"no message id":   func(m *chatMessage) { m.MessageID = "  " },
		"no counterparty": func(m *chatMessage) { m.FromID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			msg := inboundMessage()
			mutate(&msg)
			if _, err := recordFor(msg, fixtureOAID); err == nil {
				t.Fatal("the message was accepted; it names nothing a later read could match it by")
			}
		})
	}
}

// A non-text message reads as its type plus whatever locator the provider gave,
// and the URL is passed through rather than fetched — resolving a
// customer-supplied URL would have this installation make a request a stranger
// chose.
func TestANonTextMessageNamesItsTypeAndCarriesTheProvidersOwnLocator(t *testing.T) {
	msg := inboundMessage()
	msg.Type, msg.Message = "photo", ""
	msg.URL, msg.Description = "https://cdn.example.com/a.jpg", "trước cửa hàng"

	rec, err := recordFor(msg, fixtureOAID)
	if err != nil {
		t.Fatalf("recordFor: %v", err)
	}
	for _, want := range []string{"[photo]", "trước cửa hàng", "https://cdn.example.com/a.jpg"} {
		if !strings.Contains(rec.Activity.Body, want) {
			t.Fatalf("Body = %q, want it to carry %q", rec.Activity.Body, want)
		}
	}
}

// A shared location arrives as a STRING carrying a longitude and a latitude, not
// as an object. It is passed through as written: decoding it into a pair of
// numbers yields zeroes when it is not the shape expected, which reads on a
// timeline as a customer having shared the Gulf of Guinea.
func TestASharedLocationIsCarriedAsTheProviderWroteIt(t *testing.T) {
	msg := inboundMessage()
	msg.Type, msg.Message, msg.Location = "location", "", "106.6297,10.8231"

	rec, err := recordFor(msg, fixtureOAID)
	if err != nil {
		t.Fatalf("recordFor: %v", err)
	}
	if !strings.Contains(rec.Activity.Body, "106.6297,10.8231") {
		t.Fatalf("Body = %q, want the provider's own location string", rec.Activity.Body)
	}
}

// A display name over the core's cap is shortened rather than making the whole
// record a refusal the poll would then have to classify.
func TestAnOverlongDisplayNameIsShortenedRatherThanRefused(t *testing.T) {
	msg := inboundMessage()
	msg.FromDisplayName = strings.Repeat("á", extension.MaxDisplayNameRunes+50)

	rec, err := recordFor(msg, fixtureOAID)
	if err != nil {
		t.Fatalf("recordFor: %v", err)
	}
	if err := rec.Validate(); err != nil {
		t.Fatalf("the record is not valid after shortening: %v", err)
	}
	if got := len([]rune(rec.Counterparty.DisplayName)); got != extension.MaxDisplayNameRunes {
		t.Fatalf("the display name is %d runes, want it capped at %d", got, extension.MaxDisplayNameRunes)
	}
}

// A recipient carrying another account's prefix is REFUSED rather than trimmed.
// The bare id inside it is perfectly well-formed and names a different person at
// the account now connected, so sending to it is a mistake nothing downstream
// could detect.
func TestARecipientFromAnotherOfficialAccountIsRefused(t *testing.T) {
	account, err := accountWithinOA(fixtureOAID, fixtureOAID+":user-1")
	if err != nil {
		t.Fatalf("a recipient from this account must resolve: %v", err)
	}
	if account != "user-1" {
		t.Fatalf("account = %q, want user-1", account)
	}
	for name, scoped := range map[string]string{
		"another account": "9999999999:user-1",
		"no account":      "user-1",
		"account only":    fixtureOAID + ":",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := accountWithinOA(fixtureOAID, scoped); err == nil {
				t.Fatalf("%q was accepted as a recipient of this account", scoped)
			}
		})
	}
}
