// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package imap

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

func TestIMAPCursorRoundTripsAndRefusesCorruption(t *testing.T) {
	cur := imapCursor{UIDValidity: 7, LastUID: 4711, Email: "owner@ws.example"}
	parsed, err := parseIMAPCursor(marshalIMAPCursor(cur))
	if err != nil {
		t.Fatal(err)
	}
	if parsed != cur {
		t.Fatalf("round trip lost data: %+v != %+v", parsed, cur)
	}

	// Empty = fresh mailbox; garbage = corruption, never a silent re-anchor
	// (re-anchoring on a misread cursor would drop everything in between).
	if _, err := parseIMAPCursor(nil); err != nil {
		t.Fatalf("empty cursor must read as fresh: %v", err)
	}
	if _, err := parseIMAPCursor(connector.Cursor("not json")); err == nil {
		t.Fatal("an unreadable cursor must be an error, not a fresh mailbox")
	}
}

func TestNormalizeCredentials(t *testing.T) {
	t.Run("defaults fill in", func(t *testing.T) {
		got, err := normalizeCredentials(Credentials{Host: " imap.acme.test ", Email: " a@acme.test ", Password: "pw"})
		if err != nil {
			t.Fatal(err)
		}
		if got.Port != defaultPort || got.Mailbox != defaultMailbox || got.MaxMessages != defaultMaxMessages {
			t.Fatalf("defaults not applied: %+v", got)
		}
		if got.Host != "imap.acme.test" || got.Email != "a@acme.test" {
			t.Fatalf("whitespace not trimmed: %+v", got)
		}
	})
	t.Run("window is capped", func(t *testing.T) {
		got, err := normalizeCredentials(Credentials{Host: "h", Email: "e@x", Password: "pw", MaxMessages: 9999})
		if err != nil {
			t.Fatal(err)
		}
		if got.MaxMessages != maxMessagesCap {
			t.Fatalf("MaxMessages = %d, want capped at %d", got.MaxMessages, maxMessagesCap)
		}
	})
	t.Run("missing essentials park as auth, not retry", func(t *testing.T) {
		_, err := normalizeCredentials(Credentials{Host: "h", Email: "e@x"})
		if !errors.Is(err, connector.ErrAuthRejected) {
			t.Fatalf("missing password classified %v, want the auth class (a retry cannot fix it)", err)
		}
	})
}
