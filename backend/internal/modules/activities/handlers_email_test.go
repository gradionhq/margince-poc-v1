// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The wire→input mapping both mail surfaces share. It is worth its own test
// because the merge it performs is a RULE rather than a convenience: consent
// is owed to every addressee, so Recipients is To+Cc and Cc is a subset of
// it. A surface that merged differently would mail somebody the consent gate
// was never asked about.

import (
	"slices"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

func emails(addresses ...string) []openapi_types.Email {
	out := make([]openapi_types.Email, 0, len(addresses))
	for _, addr := range addresses {
		out = append(out, openapi_types.Email(addr))
	}
	return out
}

func TestSendInputMergesEveryAddresseeForTheConsentGate(t *testing.T) {
	cc := emails("boss@example.test")
	in := sendInputFrom(emails("buyer@example.test"), &cc, nil, "Pricing", "As discussed.", nil, nil, "transactional", nil)

	// The gate answers on Recipients, so a cc'd person must appear there or
	// they receive mail nobody asked consent for.
	want := []string{"buyer@example.test", "boss@example.test"}
	if len(in.Recipients) != len(want) {
		t.Fatalf("Recipients = %v, want the merged To+Cc list %v", in.Recipients, want)
	}
	for i, addr := range want {
		if in.Recipients[i] != addr {
			t.Fatalf("Recipients[%d] = %q, want %q", i, in.Recipients[i], addr)
		}
	}
	if len(in.Cc) != 1 || in.Cc[0] != "boss@example.test" {
		t.Fatalf("Cc = %v, want the cc list travelling separately so the delivery can render two lines", in.Cc)
	}
	// The To: line is what remains once the Cc addresses come out.
	if to := toRecipients(in.Recipients, in.Cc, in.Bcc); len(to) != 1 || to[0] != "buyer@example.test" {
		t.Fatalf("To: = %v, want the merged list minus the cc'd address", to)
	}
}

func TestSendInputWithoutCcCarriesOnlyTheAddressees(t *testing.T) {
	in := sendInputFrom(emails("buyer@example.test"), nil, nil, "Pricing", "As discussed.", nil, nil, "transactional", nil)

	if len(in.Recipients) != 1 || in.Recipients[0] != "buyer@example.test" {
		t.Fatalf("Recipients = %v, want just the one addressee", in.Recipients)
	}
	if len(in.Cc) != 0 {
		t.Fatalf("Cc = %v, want empty when the request carried none", in.Cc)
	}
}

// The contract field is nullable and its absence is the ordinary case: mail
// composed without a served draft resolves no learning signal, which the
// empty string is how the send path is told.
func TestSendInputResolvesNoDraftWhenNoneWasServed(t *testing.T) {
	in := sendInputFrom(emails("buyer@example.test"), nil, nil, "Pricing", "Body", nil, nil, "transactional", nil)
	if in.DraftRef != "" {
		t.Fatalf("DraftRef = %q, want empty so no voice outcome is inferred", in.DraftRef)
	}

	ref := "draft-42"
	withDraft := sendInputFrom(emails("buyer@example.test"), nil, nil, "Pricing", "Body", nil, nil, "transactional", &ref)
	if withDraft.DraftRef != ref {
		t.Fatalf("DraftRef = %q, want %q — the send closes the signal that draft opened", withDraft.DraftRef, ref)
	}
}

// A blind copy is blind to the RECIPIENTS and never to the consent gate. The
// gate answers on Recipients, so a bcc'd address missing from that list is a
// person who receives mail nobody asked consent for — which is the same defect
// as an unconsented To, with the added property that nobody can see it happened.
func TestSendInputMergesBlindCopiesForTheConsentGate(t *testing.T) {
	cc := emails("boss@example.test")
	bcc := emails("archive@example.test", "Quiet@Example.test")
	in := sendInputFrom(emails("buyer@example.test"), &cc, &bcc,
		"Pricing", "As discussed.", nil, nil, "transactional", nil)

	want := []string{
		"buyer@example.test", "boss@example.test",
		"archive@example.test", "Quiet@Example.test",
	}
	if len(in.Recipients) != len(want) {
		t.Fatalf("Recipients = %v, want every addressee including the blind ones", in.Recipients)
	}
	for _, addr := range want {
		if !slices.Contains(in.Recipients, addr) {
			t.Errorf("%q receives the message and never reaches the consent gate", addr)
		}
	}
	if len(in.Bcc) != 2 {
		t.Fatalf("Bcc = %v, want the blind list kept apart so the wire can omit it", in.Bcc)
	}
}
