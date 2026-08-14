// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// LogActivityInputFrom is the one mapping the HTTP handler, the SoR/MCP
// provider path, and the extension core-write seam all share (mapping.go's
// own doc comment) — normalizing a transcript here, rather than in each
// caller, is what makes "paste via the UI" and "log_activity via an agent"
// land the identical stored form.

import (
	"errors"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestActivityLogInputNormalizesATranscriptBody(t *testing.T) {
	transcript := "transcript"
	raw := "Anna: hello   \r\nBen: hi\r\n"
	in, err := LogActivityInputFrom(crmcontracts.CreateActivityRequest{
		Kind: "meeting", SourceSystem: &transcript, Body: &raw,
	})
	if err != nil {
		t.Fatalf("a transcript on a meeting must be accepted: %v", err)
	}
	if in.Body == nil || *in.Body != "Anna: hello\nBen: hi" {
		t.Errorf("Body = %v, want the normalized form", in.Body)
	}
}

func TestActivityLogInputRefusesATranscriptOnAKindThatIsNotCallOrMeeting(t *testing.T) {
	transcript := "transcript"
	body := "Anna: hello"
	_, err := LogActivityInputFrom(crmcontracts.CreateActivityRequest{
		Kind: "note", SourceSystem: &transcript, Body: &body,
	})
	var wrongKind *TranscriptKindError
	if !errors.As(err, &wrongKind) {
		t.Fatalf("err = %v, want *TranscriptKindError — a transcript is not a note", err)
	}
	// The refusal must never echo the caller's own kind value back — it fires
	// before the kind ever reaches the DB CHECK that would otherwise be its
	// only validation.
	if wrongKind.Error() != "only a call or meeting activity may carry a transcript" {
		t.Errorf("Error() = %q, echoes or otherwise varies with the caller's kind", wrongKind.Error())
	}
	field, code, _ := wrongKind.FieldFault()
	if field != "kind" || code != "invalid" {
		t.Errorf("FieldFault() = (%q, %q, _), want (kind, invalid, _)", field, code)
	}
}

func TestActivityLogInputRefusesABlankTranscript(t *testing.T) {
	transcript := "transcript"
	blank := "   \n\n"
	_, err := LogActivityInputFrom(crmcontracts.CreateActivityRequest{
		Kind: "call", SourceSystem: &transcript, Body: &blank,
	})
	if !errors.Is(err, ErrBlankTranscript) {
		t.Fatalf("err = %v, want ErrBlankTranscript", err)
	}
}
