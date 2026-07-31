// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

// The send-capability answer, and the recipient list the suppression gate is
// handed. Both are read by the dispatcher on every attempt, and both fail
// silently in the same direction if they are wrong: a delivery that parks with a
// reason describing a limitation that does not exist, or a default-deny gate
// asked about nobody.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A bot token holds no OAuth scope and still sends. Read through the old
// two-valued answer, "no scope" and "cannot send" were the same reply, so
// Telegram would be classified capture-only and every reply would park.
func TestSendScopeForReportsTelegramSendsWithoutAScope(t *testing.T) {
	scope, capability := SendScopeFor("telegram")
	if capability != SendsWithoutScope {
		t.Fatalf("SendScopeFor(\"telegram\") capability = %v, want SendsWithoutScope", capability)
	}
	if scope != "" {
		t.Errorf("SendScopeFor(\"telegram\") scope = %q, want none: a bot token has no grant to intersect", scope)
	}
}

// The other two states, kept beside the one above because the THREE-way split is
// the point: a provider nobody configured must still refuse, and Gmail must
// still be scope-checked.
func TestSendScopeForSeparatesScopedSendersFromProvidersThatCannotSend(t *testing.T) {
	scope, capability := SendScopeFor("gmail")
	if capability != SendsWithScope || scope == "" {
		t.Fatalf("SendScopeFor(\"gmail\") = (%q, %v), want a named scope with SendsWithScope", scope, capability)
	}
	if _, capability := SendScopeFor("carrier-pigeon"); capability != CannotSend {
		t.Fatalf("SendScopeFor on an unknown provider = %v, want CannotSend", capability)
	}
	// The zero value must be the refusal, so a capability nobody answered
	// cannot read as permission.
	var unanswered SendCapability
	if unanswered != CannotSend {
		t.Fatal("the zero SendCapability is not CannotSend; an unset capability would authorize a send")
	}
}

// The gate's subject list is derived from the delivery's SHAPE, and a channel
// delivery has to arrive as a channel recipient. Flattened to an address list it
// would arrive empty, and a default-deny gate asked about nobody refuses nobody.
func TestConsentRecipientsCarryTheDeliverysOwnShape(t *testing.T) {
	mail := consentRecipients(liveDelivery())
	if len(mail) != 2 || mail[0].Email != "buyer@example.com" || mail[1].Email != "cc@example.com" {
		t.Fatalf("mail recipients = %+v, want the To and Cc addresses as mail recipients", mail)
	}

	account := "7788"
	channel := Delivery{Provider: "telegram", ChannelUserID: &account, ConsentPurpose: "transactional"}
	got := consentRecipients(channel)
	if len(got) != 1 {
		t.Fatalf("channel recipients = %+v, want exactly one", got)
	}
	if got[0].Channel == nil || got[0].Channel.Provider != "telegram" || got[0].Channel.ChannelUserID != account {
		t.Fatalf("channel recipient = %+v, want the delivery's provider and account id", got[0])
	}
	if err := got[0].Validate(); err != nil {
		t.Fatalf("the recipient handed to the gate does not validate: %v", err)
	}
}

// The discriminator answers on the row's own NULL-ness, not on emptiness: a
// privacy scrub empties a channel recipient in place, and a delivery that then
// read as mail would be a channel row asking the mail gate about no addressees.
func TestIsChannelReadsTheRowsShapeNotItsEmptiness(t *testing.T) {
	if liveDelivery().IsChannel() {
		t.Error("a mail delivery reports IsChannel")
	}
	scrubbed := ""
	erased := Delivery{ID: ids.NewV7(), Provider: "telegram", ChannelUserID: &scrubbed}
	if !erased.IsChannel() {
		t.Error("a channel delivery whose recipient was scrubbed no longer reports IsChannel")
	}
	if erased.ChannelRecipient() != "" {
		t.Error("a scrubbed recipient reads back as an account id")
	}
	// And nothing may transmit to it: the gate refuses the recipient outright.
	if err := consentRecipients(erased)[0].Validate(); err == nil {
		t.Error("a scrubbed channel recipient validated; an erased subject could still be messaged")
	}
}
