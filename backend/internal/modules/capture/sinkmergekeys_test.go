// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// A record carrying both a channel identity and an address is named by the
// IDENTITY. This is the precedence the whole change rests on: read the other way
// round the record would classify as mail, which binds nothing and — because
// every mail gate keys off the address — records no fault either.
func TestACorroboratingAddressDoesNotChangeWhoNamesTheHuman(t *testing.T) {
	both := connector.Counterparty{
		Email:           "tuyen@acme.example",
		ChannelIdentity: connector.ChannelIdentity{Provider: "dispact", ChannelUserID: "U0293"},
	}

	if got := counterpartyShapeOf(both); got != shapeChannel {
		t.Errorf("shape = %d, want shapeChannel (%d) — the identity names the human, the address only corroborates", got, shapeChannel)
	}
}

// Half an identity stays malformed however much else rides along: the missing
// half is what the advisory lock and the suppression key are built from, so an
// address cannot stand in for it.
func TestAnAddressDoesNotCompleteHalfAChannelIdentity(t *testing.T) {
	half := connector.Counterparty{
		Email:           "tuyen@acme.example",
		ChannelIdentity: connector.ChannelIdentity{Provider: "dispact"},
	}

	if got := counterpartyShapeOf(half); got != shapeHalfChannel {
		t.Errorf("shape = %d, want shapeHalfChannel (%d)", got, shapeHalfChannel)
	}
	if err := admitCounterpartyShape(counterpartyShapeOf(half)); !errors.Is(err, ErrChannelIdentityIncomplete) {
		t.Errorf("admission = %v, want ErrChannelIdentityIncomplete", err)
	}
}

func TestAdmitCounterpartyKeys(t *testing.T) {
	identity := connector.ChannelIdentity{Provider: "dispact", ChannelUserID: "U0293"}
	for _, tc := range []struct {
		name string
		cp   connector.Counterparty
		want error
	}{
		{
			// The case the gate exists for: matching evidence from a source
			// that vouched for none. Admitting it would feed the resolution
			// ladder a key nobody stands behind, which is how one human is
			// silently bound to another's record.
			name: "an undeclared corroborating address is refused",
			cp:   connector.Counterparty{Email: "tuyen@acme.example", ChannelIdentity: identity},
			want: ErrMergeKeyNotDeclared,
		},
		{
			name: "a declared corroborating address is admitted",
			cp: connector.Counterparty{Email: "tuyen@acme.example", ChannelIdentity: identity}.
				WithDeclaredEmailMerge(true),
		},
		{
			// A mail record's address IS its identity, not evidence about
			// somebody named another way, so it belongs to no declaration. This
			// is the case a rule phrased as "any undeclared key is refused"
			// would wrongly reject.
			name: "a mail-shaped record needs no declaration",
			cp:   connector.Counterparty{Email: "tuyen@acme.example"},
		},
		{
			name: "a channel record with no address needs no declaration",
			cp:   connector.Counterparty{ChannelIdentity: identity},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := admitCounterpartyKeys(tc.cp); !errors.Is(err, tc.want) {
				t.Errorf("admitCounterpartyKeys = %v, want %v", err, tc.want)
			}
		})
	}
}

// The stamp is the core's word about the SOURCE, so a record that has been
// copied, re-read or rebuilt must not arrive claiming a declaration nobody made.
// Unexported, the zero value is the refusing one at every boundary.
func TestTheDeclarationDefaultsToRefusing(t *testing.T) {
	var cp connector.Counterparty
	if cp.MayCorroborateByEmail() {
		t.Error("a zero Counterparty claims a declared merge key; the untrusting answer must be the default")
	}
	if cp.WithDeclaredEmailMerge(true).WithDeclaredEmailMerge(false).MayCorroborateByEmail() {
		t.Error("the declaration did not fall back to refusing when re-stamped as undeclared")
	}
}
