// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The capture registry wiring as behavior: which connectors a
// configuration yields, and when the worker's sync registry exists at
// all — the composition rules the process roles rely on at boot.

import (
	"slices"
	"testing"
)

func TestCaptureRegistryComposition(t *testing.T) {
	gmailApp := GmailConfig{ClientID: "id", ClientSecret: "secret"}

	t.Run("a configured gmail app registers the connector", func(t *testing.T) {
		reg := NewCaptureRegistryWithGmail(nil, nil, gmailApp, CaptureConfig{})
		names := map[string]bool{}
		for _, d := range reg.Connectors() {
			names[d.Name] = true
		}
		if !names["gmail"] {
			t.Fatalf("connectors = %v, want gmail registered", names)
		}
	})

	t.Run("an unconfigured app still carries standing imap, never gmail", func(t *testing.T) {
		reg := NewCaptureRegistryWithGmail(nil, nil, GmailConfig{}, CaptureConfig{})
		names := map[string]bool{}
		for _, d := range reg.Connectors() {
			names[d.Name] = true
		}
		if names["gmail"] {
			t.Fatal("gmail must be absent without its OAuth app")
		}
		if !names["imap"] {
			t.Fatal("the standing imap connector needs no app and must always register")
		}
	})

	t.Run("the poll registry exists only with a syncable app", func(t *testing.T) {
		if reg := GmailPollRegistry(nil, nil, GmailConfig{}, CaptureConfig{}); reg != nil {
			t.Fatal("no app configured must mean no poll registry (the job stays absent)")
		}
		if reg := GmailPollRegistry(nil, nil, gmailApp, CaptureConfig{}); reg == nil {
			t.Fatal("a syncable app must yield the poll registry")
		}
	})

	t.Run("connect needs more than sync", func(t *testing.T) {
		if gmailApp.Enabled() {
			t.Fatal("sync credentials alone must not enable the connect transport")
		}
		full := GmailConfig{
			ClientID: "id", ClientSecret: "secret",
			StateKey:      "0123456789abcdef0123456789abcdef",
			PublicBaseURL: "https://crm.example",
		}
		if !full.Enabled() {
			t.Fatal("a fully-configured app must enable the connect transport")
		}
	})
}

// CoreChannelProviders is the boot step's core half, and it enumerates the
// transports off a registry built with no pool and no vault. That is only
// honest while the vault-carrying and Google-app-carrying constructions add no
// channel-capable connector of their own — so the obligation is derived from
// the constructions themselves rather than restated as a list, and a connector
// that starts supplying a transport fails HERE instead of going unregistered on
// every install that has no Google app.
func TestCoreChannelProvidersEnumeratesEveryComposedCoreTransport(t *testing.T) {
	enumerated := CoreChannelProviders()
	if len(enumerated) == 0 {
		t.Fatal("this binary composed no core transport at all, so this gate would compare two empty sets")
	}
	// The richest core composition there is: both OAuth apps configured, so
	// every connector any role can register is on it.
	richest := CaptureSyncRegistry(nil, nil,
		GmailConfig{ClientID: "id", ClientSecret: "secret"},
		GraphConfig{ClientID: "id", ClientSecret: "secret"},
		CaptureConfig{}).ChannelProviders()
	if !slices.Equal(enumerated, richest) {
		t.Errorf("the fully-composed registry supplies transports %v, but the boot step would register %v — "+
			"a transport the boot step cannot see is one no captured message may name",
			richest, enumerated)
	}
}

func TestWithKeyvaultWiresTheCredentialCustodian(t *testing.T) {
	s := &Server{}
	WithKeyvault(fakeVault{})(s, nil)
	if s.vault == nil {
		t.Fatal("the vault must be held for the connector-credential paths")
	}
	if s.connectorHandlers.registry == nil {
		t.Fatal("the standing connect must get a registry when none is wired yet")
	}
	// A gmail-carrying registry wired earlier must NOT be replaced.
	marker := NewCaptureRegistry(nil, nil, CaptureConfig{})
	s2 := &Server{}
	s2.connectorHandlers.registry = marker
	WithKeyvault(fakeVault{})(s2, nil)
	if s2.connectorHandlers.registry != marker {
		t.Fatal("WithKeyvault must not displace an already-wired connector registry")
	}
}

// TestWithKeyvaultWiresTheSendPreflightWithNoGoogleApp pins the fix for the
// Telegram-only wiring gap: previously the outbound send pre-flight was
// installed ONLY by WithGmailCapture, so a role with no Google app configured
// composed no pre-flight at all — including for the channel branch, which
// never depended on Gmail in the first place (NewCaptureRegistry registers
// Telegram unconditionally). WithKeyvault must now install it over whichever
// registry it just ensured exists, with no Gmail/Graph option involved.
func TestWithKeyvaultWiresTheSendPreflightWithNoGoogleApp(t *testing.T) {
	s := &Server{}
	WithKeyvault(fakeVault{})(s, nil)
	if s.send.SendAuthority == nil {
		t.Fatal("WithKeyvault must install the send pre-flight even with no Google app configured")
	}
	authority, ok := s.send.SendAuthority.(mailboxAuthority)
	if !ok {
		t.Fatalf("send pre-flight = %T, want a mailboxAuthority", s.send.SendAuthority)
	}
	if authority.grants != s.connectorHandlers.registry {
		t.Fatal("the pre-flight must read the SAME registry the connect flow writes to, not a second construction")
	}
}
