// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// encodeMirrorCursor/decodeMirrorCursor's own unit-level proof: the
// List cursor's opaque base64 encoding round-trips, and a malformed
// cursor (never one a client is meant to construct by hand) is a clean
// error rather than a panic. The real-Postgres List paging behavior
// these feed is proven by mirrorstore_integration_test.go.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
)

func TestMirrorCursorRoundTrips(t *testing.T) {
	for _, externalID := range []string{"", "1", "100214862042"} {
		encoded := encodeMirrorCursor(externalID)
		got, err := decodeMirrorCursor(encoded)
		if err != nil {
			t.Fatalf("decodeMirrorCursor(%q): %v", encoded, err)
		}
		if got != externalID {
			t.Errorf("round trip: encodeMirrorCursor(%q) -> decodeMirrorCursor = %q", externalID, got)
		}
	}
}

func TestDecodeMirrorCursorEmptyStringIsStartOfPaging(t *testing.T) {
	got, err := decodeMirrorCursor("")
	if err != nil {
		t.Fatalf("decodeMirrorCursor(\"\"): %v", err)
	}
	if got != "" {
		t.Errorf("decodeMirrorCursor(\"\") = %q, want empty (the start-of-paging cursor)", got)
	}
}

// A malformed cursor is CLIENT input, so it has to arrive at the transport as
// the typed client fault httperr answers 422 for. A bare error wrap reaches
// the client as an opaque 500 — a self-inflicted mistake dressed as an outage.
func TestDecodeMirrorCursorRejectsMalformedInput(t *testing.T) {
	_, err := decodeMirrorCursor("not valid base64!!")
	var malformed *storekit.MalformedCursorError
	if !errors.As(err, &malformed) {
		t.Fatalf("decodeMirrorCursor: err = %v, want a storekit.MalformedCursorError", err)
	}
}

// Both surfaces that page this store decode the cursor before they touch the
// database, so a nil pool proves the refusal happens on the client's input
// rather than somewhere downstream. Every paging entry point is covered: the
// shared decoder is the fix, and a caller that re-wrapped it would hide that.
func TestPagingEntryPointsRejectAMalformedCursorAsClientInput(t *testing.T) {
	ctx := context.Background()
	store := &MirrorStore{}
	cases := []struct {
		name   string
		cursor string
		call   func(cursor string) error
	}{
		{
			name:   "List",
			cursor: "not valid base64!!",
			call: func(cursor string) error {
				_, _, err := store.List(ctx, "contact", cursor, 0)
				return err
			},
		},
		{
			name:   "ListUserMap",
			cursor: "not valid base64!!",
			call: func(cursor string) error {
				_, _, err := store.ListUserMap(ctx, "hubspot", cursor, 0)
				return err
			},
		},
		{
			// This cursor's payload IS a user id: a token that decodes to
			// anything else was never minted by ListUserMap.
			name:   "ListUserMap with a well-formed token carrying no user id",
			cursor: encodeMirrorCursor("definitely-not-a-uuid"),
			call: func(cursor string) error {
				_, _, err := store.ListUserMap(ctx, "hubspot", cursor, 0)
				return err
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.call(c.cursor)
			var malformed *storekit.MalformedCursorError
			if !errors.As(err, &malformed) {
				t.Fatalf("err = %v, want a storekit.MalformedCursorError", err)
			}
		})
	}
}
