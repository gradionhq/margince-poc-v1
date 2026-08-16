// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// The unit-facing half of the merge-key gate. It is the ATTRIBUTABLE refusal —
// a unit author reads their own grammar rather than an unattributable "the core
// could not land this record" — and it is deliberately not the only one: capture
// holds the same invariant for every caller of Upsert.
func TestRefuseUndeclaredMergeKey(t *testing.T) {
	declared := extension.IngressSource{System: "inbox", Merges: []extension.MergeKey{extension.MergeKeyEmail}}
	silent := extension.IngressSource{System: "inbox"}
	identity := extension.ChannelIdentity{Provider: "dispact", ChannelUserID: "U0293"}

	for _, tc := range []struct {
		name   string
		source extension.IngressSource
		cp     extension.Counterparty
		refuse bool
	}{
		{
			name:   "an address to match on from a source that vouched for none",
			source: silent,
			cp:     extension.Counterparty{Email: "tuyen@acme.example", ChannelIdentity: identity},
			refuse: true,
		},
		{
			name:   "the same address from a source that declared the key",
			source: declared,
			cp:     extension.Counterparty{Email: "tuyen@acme.example", ChannelIdentity: identity},
		},
		{
			// A mention in a shared room: the record is NAMED by the address,
			// which is identity rather than corroboration and belongs to no
			// declaration. Refusing this would break every mail-shaped record a
			// unit lands today.
			name:   "a mail-shaped record from a source that declared nothing",
			source: silent,
			cp:     extension.Counterparty{Email: "tuyen@acme.example"},
		},
		{
			name:   "a channel record carrying no address at all",
			source: silent,
			cp:     extension.Counterparty{ChannelIdentity: identity},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseUndeclaredMergeKey(tc.source, tc.cp)
			if tc.refuse && !errors.Is(err, extension.ErrInvalid) {
				t.Fatalf("got %v, want an ErrInvalid-class refusal", err)
			}
			if !tc.refuse && err != nil {
				t.Fatalf("got %v, want the record admitted", err)
			}
		})
	}
}

// The declaration is the SOURCE's, resolved by the core from the manifest. A
// unit that could state it per record could widen its own trust, which is the
// one thing the declaration exists to bound — so the mapper must read the
// source and never the record.
func TestTheDeclarationIsStampedFromTheSourceNotTheRecord(t *testing.T) {
	cp := extension.Counterparty{
		Email:           "tuyen@acme.example",
		ChannelIdentity: extension.ChannelIdentity{Provider: "dispact", ChannelUserID: "U0293"},
	}

	silent := counterpartyOf(cp, extension.IngressSource{System: "inbox"})
	if silent.MayCorroborateByEmail() {
		t.Error("a record from a source declaring nothing arrived able to corroborate by address")
	}

	vouched := counterpartyOf(cp, extension.IngressSource{
		System: "inbox", Merges: []extension.MergeKey{extension.MergeKeyEmail},
	})
	if !vouched.MayCorroborateByEmail() {
		t.Error("a record from a source declaring the email key arrived unable to corroborate by address")
	}
}
