// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The capture registry wiring as behavior: which connectors a
// configuration yields, and when the worker's sync registry exists at
// all — the composition rules the process roles rely on at boot.

import (
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

func TestWithKeyvaultWiresTheCredentialCustodian(t *testing.T) {
	s := &Server{}
	WithKeyvault(fakeVault{})(s, nil)
	if s.vault == nil {
		t.Fatal("the vault must be held for the connector-credential paths")
	}
	if s.imapConnectHandlers.registry == nil {
		t.Fatal("the transient IMAP pull must get a vault-carrying registry")
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
