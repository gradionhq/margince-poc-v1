// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dispact

// Which notifications become records, and what a record says.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// The allowlist, from both sides. A type nobody has taught this unit about is
// NOT captured, and a reaction — which an inbox is mostly made of — never is.
func TestOnlyDirectedNotificationsFromPeopleAreCaptured(t *testing.T) {
	for kind, want := range map[string]bool{
		"dm":               true,
		"dm_thread_reply":  true,
		"mention":          true,
		"channel_mention":  true,
		"thread_reply":     true,
		"reaction":         false,
		"scheduled_failed": false,
		"channel_joined":   false,
		"":                 false,
	} {
		item := dm(1)
		item.Type = kind
		if got := directed(item); got != want {
			t.Errorf("directed(%q) = %v, want %v", kind, got, want)
		}
	}
	// And a bot's message is not a customer interaction whatever its type. The
	// flag is read HERE because the provider's own `source=human` filter does
	// not filter: the same traffic comes back with or without it, measured.
	bot := dm(1)
	bot.Metadata.SenderIsBot = true
	if directed(bot) {
		t.Error("a bot's direct message was treated as an interaction with a counterparty")
	}
}

func aSender() providerUser {
	return providerUser{ID: "sender-1", Email: "outside@example.com", DisplayName: "A Sender"}
}

func aMember() providerUser {
	return providerUser{ID: "provider-member", WorkspaceID: "ws-7", Email: "member@installation.test"}
}

// What a landed record says, field by field — each one a decision the core acts
// on rather than a copy of the provider's document.
func TestARecordCarriesWhatTheCoreDecidesWith(t *testing.T) {
	item := dm(1042)
	item.Title = "  A Sender  "
	item.Body = "  the preview  "

	rec, err := recordFor(item, aSender(), aMember(), "ws-7")
	if err != nil {
		t.Fatalf("recordFor: %v", err)
	}
	switch {
	case rec.Key != "ws-7:1042":
		t.Errorf("key = %q — two deployments number their notifications independently, so the key is namespaced by workspace", rec.Key)
	case rec.Activity.Subject != "A Sender":
		t.Errorf("subject = %q, want it trimmed", rec.Activity.Subject)
	case rec.Activity.Body != "the preview":
		t.Errorf("body = %q, want it trimmed", rec.Activity.Body)
	case rec.Activity.Direction != extension.DirectionInbound:
		t.Errorf("direction = %q, want inbound — the member received this", rec.Activity.Direction)
	case !rec.Activity.OccurredAt.Equal(item.CreatedAt):
		t.Errorf("occurred_at = %v, want the provider's own time rather than when the poll noticed", rec.Activity.OccurredAt)
	case rec.Counterparty.Domain != "example.com":
		t.Errorf("domain = %q — the core's suppression gates key on it, and an empty one reads as 'keep' rather than as an opt-out", rec.Counterparty.Domain)
	case string(rec.Raw) != string(item.Raw):
		t.Errorf("raw = %s, want the provider's own document", rec.Raw)
	}
	// Both ends, and the reason is the internal-only gate: an EMPTY address set
	// reads as "this connector could not enumerate the parties", so a record
	// naming one end would silently disable a drop rather than pass it.
	if len(rec.Addresses) != 2 || rec.Addresses[0] != "outside@example.com" || rec.Addresses[1] != "member@installation.test" {
		t.Errorf("addresses = %v, want the sender and the connected member", rec.Addresses)
	}
	// The record must pass the published grammar, or the port refuses it and
	// the poll skips a message nobody sees again.
	if err := rec.Validate(); err != nil {
		t.Errorf("a record this unit builds is refused by the published grammar: %v", err)
	}
}

// A bare channel id in thread_key would share one column with every other
// source, where two of them can collide and join a stranger's conversation onto
// this one.
func TestTheThreadKeyIsNamespaced(t *testing.T) {
	rec, err := recordFor(dm(1), aSender(), aMember(), "ws-7")
	if err != nil {
		t.Fatalf("recordFor: %v", err)
	}
	if !strings.HasPrefix(rec.ThreadKey, "dispact:ws-7:") {
		t.Errorf("thread key = %q, want it namespaced by provider and deployment", rec.ThreadKey)
	}
}

// A sender this unit could not resolve has no address, so there is no
// counterparty and no record — reported as unrepresentable rather than landed
// with an empty party, which the core would read as a message with no other
// end.
func TestASenderWithNoAddressIsNotARecord(t *testing.T) {
	if _, err := recordFor(dm(1), providerUser{ID: "sender-1"}, aMember(), "ws-7"); err == nil {
		t.Fatal("a record was built for a sender with no address")
	}
}

// The display name falls back rather than rendering empty: a counterparty with
// no name at all is a blank row on a timeline.
func TestTheCounterpartyNameFallsBack(t *testing.T) {
	for name, sender := range map[string]providerUser{
		"display name": {Email: "outside@example.com", DisplayName: "A Sender", FullName: "Alexandra Sender"},
		"full name":    {Email: "outside@example.com", FullName: "Alexandra Sender"},
		"the address":  {Email: "outside@example.com"},
		"blank names":  {Email: "outside@example.com", DisplayName: "   "},
	} {
		t.Run(name, func(t *testing.T) {
			rec, err := recordFor(dm(1), sender, aMember(), "ws-7")
			if err != nil {
				t.Fatalf("recordFor: %v", err)
			}
			if strings.TrimSpace(rec.Counterparty.DisplayName) == "" {
				t.Error("the counterparty renders with no name at all")
			}
		})
	}
}

// The raw evidence is the provider's own document. A re-marshal of the fields
// this unit happens to read would store this unit's understanding of the
// message, which is not what evidence means.
func TestTheRawEvidenceIsTheProvidersOwnDocument(t *testing.T) {
	item := dm(1)
	item.Raw = json.RawMessage(`{"id":1,"type":"dm","a_field_this_unit_does_not_read":true}`)
	rec, err := recordFor(item, aSender(), aMember(), "ws-7")
	if err != nil {
		t.Fatalf("recordFor: %v", err)
	}
	if !strings.Contains(string(rec.Raw), "a_field_this_unit_does_not_read") {
		t.Errorf("raw = %s, want the provider's whole record", rec.Raw)
	}
}

// The declared source and the constant the records are built with are one
// string. Two spellings would land records under a provenance the manifest does
// not describe, and the port would refuse them — at run time, on a cadence.
func TestTheRecordsSourceIsTheDeclaredOne(t *testing.T) {
	declared := New().Ingress
	if len(declared) != 1 {
		t.Fatalf("the unit declares %d ingress source(s), want one", len(declared))
	}
	if declared[0].System != ingressSystem {
		t.Fatalf("declared %q, records built with %q — the port refuses a record naming an undeclared source", declared[0].System, ingressSystem)
	}
	if err := declared[0].Validate(); err != nil {
		t.Fatalf("the declaration does not pass the published grammar: %v", err)
	}
}

// The secret key the poll reads is the one the unit declares, so an operator
// reading the manifest sees the credential this connector actually asks for.
func TestTheTokenKeyIsTheDeclaredOne(t *testing.T) {
	for _, request := range New().Secrets {
		if request.Key == tokenKey && request.Scope == extension.SecretScopeUser {
			return
		}
	}
	t.Fatalf("no user-scoped %q secret is declared; the unit declares %+v", tokenKey, New().Secrets)
}
