// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"
)

// raw_capture is jsonb, and a provider's original need not be JSON. The sink
// stores an RFC822 message as a JSON string and a calendar event as itself, so
// the replay has to read both spellings back — getting this wrong hands the
// mail parser a quoted, escaped copy of the message it cannot decompose.
func TestDecodeStoredOriginalReadsBothSpellings(t *testing.T) {
	message := "From: bob@target.com\r\nSubject: Hi\r\n\r\nBody."
	quoted := []byte(`"From: bob@target.com\r\nSubject: Hi\r\n\r\nBody."`)
	got, err := decodeStoredOriginal(quoted)
	if err != nil {
		t.Fatalf("decodeStoredOriginal on a JSON-string payload: %v", err)
	}
	if string(got) != message {
		t.Errorf("a JSON-string payload decoded to %q, want the message itself", got)
	}

	event := []byte(`{"id":"evt-1","status":"confirmed"}`)
	got, err = decodeStoredOriginal(event)
	if err != nil {
		t.Fatalf("decodeStoredOriginal on a JSON-object payload: %v", err)
	}
	if string(got) != string(event) {
		t.Errorf("a JSON-object payload was altered: %q", got)
	}
}

// An empty or malformed payload is a verdict the replay records, so it has to
// be reported rather than passed to a parser as an empty message.
func TestDecodeStoredOriginalRefusesWhatItCannotRead(t *testing.T) {
	if _, err := decodeStoredOriginal(nil); err == nil {
		t.Error("an empty payload was accepted")
	}
	if _, err := decodeStoredOriginal([]byte(`"unterminated`)); err == nil {
		t.Error("a malformed JSON string was accepted")
	}
}
