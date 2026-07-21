// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// encodeMirrorCursor/decodeMirrorCursor's own unit-level proof: the
// List cursor's opaque base64 encoding round-trips, and a malformed
// cursor (never one a client is meant to construct by hand) is a clean
// error rather than a panic. The real-Postgres List paging behavior
// these feed is proven by mirrorstore_integration_test.go.

import "testing"

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

func TestDecodeMirrorCursorRejectsMalformedInput(t *testing.T) {
	if _, err := decodeMirrorCursor("not valid base64!!"); err == nil {
		t.Fatal("decodeMirrorCursor: want an error for a malformed cursor, got nil")
	}
}
