// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// The mapping both transports decode through, and the link builder.
//
// These are pure functions carrying judgments a reader cannot recover from the
// types: which addresses are admissible, what a missing capability defaults to,
// and where a credential sits in the URL that carries it.

import (
	"errors"
	"strings"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestAnInviteRefusesAnAddressItCouldNeverReach(t *testing.T) {
	// An address that survives to the row takes the one live seat its room
	// allows for it, fails every send, and — once anyone has signed in — can no
	// longer be corrected. Refusing at the door is the only cheap moment.
	for _, tc := range []struct{ name, email string }{
		{"empty", ""},
		{"blank", "   "},
		{"no domain", "buyer"},
		{"no local part", "@example.com"},
		{"a display name, not a plain address", "Buyer <buyer@example.com>"},
		{"two addresses", "a@example.com, b@example.com"},
		{"a header smuggled in", "buyer@example.com\nBcc: someone@else.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := inviteInput(crmcontracts.InviteDealRoomParticipantRequest{
				FullName: "Buyer",
				Email:    openapi_types.Email(tc.email),
				Source:   "manual",
			})
			if err == nil {
				t.Fatalf("%q must be refused before it reaches a row", tc.email)
			}
		})
	}
}

func TestAnInviteNormalizesTheAddressItAccepts(t *testing.T) {
	// The schema CHECK requires email = lower(email). Normalizing in the mapping
	// rather than the store keeps one spelling for both transports, and means
	// the store never has to re-derive what the validator already settled.
	in, err := inviteInput(crmcontracts.InviteDealRoomParticipantRequest{
		FullName: "Buyer",
		Email:    openapi_types.Email("  Buyer@Example.COM  "),
		Source:   "manual",
	})
	if err != nil {
		t.Fatalf("a well-formed address must be accepted: %v", err)
	}
	if in.Email != "buyer@example.com" {
		t.Errorf("Email = %q, want it trimmed and lowercased", in.Email)
	}
}

func TestAnInviteWithoutACapabilityGetsTheLeastOne(t *testing.T) {
	// Defaulting up would hand a reviewer's authority — the only capability that
	// carries weight in a negotiation — to somebody nobody granted it to.
	in, err := inviteInput(crmcontracts.InviteDealRoomParticipantRequest{
		FullName: "Buyer",
		Email:    openapi_types.Email("buyer@example.com"),
		Source:   "manual",
	})
	if err != nil {
		t.Fatalf("invite must be accepted: %v", err)
	}
	if in.Capability != capabilityView {
		t.Errorf("Capability = %q, want %q — an omitted capability must default to the least", in.Capability, capabilityView)
	}
}

func TestACorrectionValidatesTheAddressToo(t *testing.T) {
	// The correction path is where a bad address does the most harm, because it
	// replaces one that may already have been delivered.
	bad := openapi_types.Email("not an address")
	if _, err := participantUpdateInput(crmcontracts.UpdateDealRoomParticipantRequest{
		Email: &bad,
	}); err == nil {
		t.Error("a malformed correction must be refused")
	}

	good := openapi_types.Email("Corrected@Example.com")
	in, err := participantUpdateInput(crmcontracts.UpdateDealRoomParticipantRequest{Email: &good})
	if err != nil {
		t.Fatalf("a well-formed correction must be accepted: %v", err)
	}
	if in.Email == nil || *in.Email != "corrected@example.com" {
		t.Errorf("Email = %v, want it lowercased", in.Email)
	}
}

func TestTheBuyerLinkCarriesTheCredentialInTheFragment(t *testing.T) {
	// A browser does not put a fragment on the wire, so the credential stays out
	// of access logs and out of the Referer a click sends onward. If this ever
	// moves into the path or the query, it starts appearing in both.
	h := Handlers{}.WithInviteLinkBase("https://example.test/")
	link := h.buyerLink("mdr_secret")

	fragment := strings.Index(link, "#")
	if fragment < 0 {
		t.Fatalf("the link carries no fragment: %q", link)
	}
	if strings.Contains(link[:fragment], "mdr_secret") {
		t.Errorf("the credential appears before the fragment, so a browser would send it: %q", link)
	}
	if !strings.HasPrefix(link, "https://example.test/#") {
		t.Errorf("the trailing slash must be trimmed exactly once: %q", link)
	}
}

func TestAnInviteRefusesAnUnboundedName(t *testing.T) {
	_, err := inviteInput(crmcontracts.InviteDealRoomParticipantRequest{
		FullName: strings.Repeat("n", nameLimit+1),
		Email:    openapi_types.Email("buyer@example.com"),
		Source:   "manual",
	})
	var fault interface {
		FieldFault() (string, string, string)
	}
	if !errors.As(err, &fault) {
		t.Fatalf("an overlong name must be refused as a field fault, got %v", err)
	}
	if field, _, _ := fault.FieldFault(); field != "full_name" {
		t.Errorf("the refusal must name full_name, got %q", field)
	}
}
