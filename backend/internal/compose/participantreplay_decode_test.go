// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"strings"
	"testing"
)

// The replay marks an activity settled whatever it found, and the verdict is
// what decides which sets are ever revisited. `none` is settled forever;
// `unreadable` and `no_owner` name work a better parser or a re-authorized
// connection could still do.
func TestReplayVerdictsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range []string{replayWroteParticipants, replayFoundNone, replayUnreadable, replayNoOwner} {
		if strings.TrimSpace(v) == "" {
			t.Fatal("an empty verdict would violate the NOT NULL column")
		}
		if seen[v] {
			t.Errorf("verdict %q is used twice; two outcomes that read the same are one outcome", v)
		}
		seen[v] = true
	}
}

// The source systems this pass can re-read. One absent from the switch is
// recorded unreadable rather than skipped — skipping would re-select it on
// every pass and the run-until-zero loop would never terminate.
func TestReplaySourceSystemsAreTheParsersItHas(t *testing.T) {
	for _, s := range []string{sourceGmail, sourceIMAP, sourceGCal} {
		if strings.TrimSpace(s) == "" {
			t.Error("an empty source system would match every unkeyed row")
		}
	}
	if sourceGmail == sourceGCal {
		t.Error("mail and calendar share a source system; one parser would read the other's payloads")
	}
}

// A CRLF message body survives the round trip through jsonb. Getting this
// wrong hands the mail parser a quoted, escaped copy it cannot decompose, and
// every stored original reads as unreadable.
func TestDecodeStoredOriginalPreservesCRLF(t *testing.T) {
	got, err := decodeStoredOriginal([]byte(`"From: a@b.c\r\nTo: d@e.f\r\n\r\nBody."`))
	if err != nil {
		t.Fatalf("decodeStoredOriginal: %v", err)
	}
	if !strings.Contains(string(got), "\r\n\r\n") {
		t.Error("the header/body separator did not survive; the parser sees one long header")
	}
}
